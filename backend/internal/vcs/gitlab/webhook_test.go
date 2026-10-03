package gitlab

// Tests del webhook de GitLab contra el Postgres de desarrollo (mapa:
// pruebas de backend.vcs — payloads firmados de fixtures, sin salida a
// internet: la API de GitLab es un stub httptest al que apunta base_url).
// Requieren DATABASE_URL (just test lo exporta del .env).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/migrations"
)

// Credenciales de test: el signing token (whsec_ + base64 de la clave cruda,
// §9.3), el secret token legacy y la master key que cifra las columnas.
var (
	testSigningKey   = []byte("codeowl-gitlab-signing-test-key")
	testSigningToken = "whsec_" + base64.StdEncoding.EncodeToString(testSigningKey)
	testLegacyToken  = "legacy-secret-de-test"
	testMasterKey    = bytes.Repeat([]byte{0x42}, 32)
	testBotUsername  = "codeowl-bot"
)

// recordingQueue es el stub de jobs.JobQueue (§4.5: stub de 10 líneas, sin
// mocks): registra kinds, args encolados y MRs cuyo descarte se pidió.
type recordingQueue struct {
	mu      sync.Mutex
	kinds   []string
	args    []json.RawMessage
	cancels []int64
}

func (q *recordingQueue) Enqueue(_ context.Context, kind string, args json.RawMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.kinds = append(q.kinds, kind)
	q.args = append(q.args, args)
	return nil
}

func (q *recordingQueue) CancelPendingByPR(_ context.Context, prID int64) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancels = append(q.cancels, prID)
	return 1, nil
}

func (q *recordingQueue) Register(...jobs.Worker) error { return nil }
func (q *recordingQueue) Start(context.Context) error   { return nil }
func (q *recordingQueue) Stop(context.Context) error    { return nil }

func (q *recordingQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.kinds)
}

// last devuelve el último kind + args encolados (cero valores si vacío).
func (q *recordingQueue) last() (string, json.RawMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.kinds) == 0 {
		return "", nil
	}
	return q.kinds[len(q.kinds)-1], q.args[len(q.args)-1]
}

// lastCancel devuelve el último MR cuyo descarte se pidió (0 si ninguno).
func (q *recordingQueue) lastCancel() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.cancels) == 0 {
		return 0
	}
	return q.cancels[len(q.cancels)-1]
}

// webhookEnv es el entorno de un test: adapter + cola registradora + store +
// stub de la API de GitLab (el endpoint de branches, §3.6.1.1).
type webhookEnv struct {
	ad  *Adapter
	q   *recordingQueue
	st  *store.Store
	api *httptest.Server
}

func newWebhookEnv(t *testing.T) *webhookEnv {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL no está seteado: saltando tests de integración")
	}
	st, err := store.Open(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	if err := store.Migrate(dbURL, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Stub del endpoint de branches: el tip de cada rama es determinístico
	// ("tip-<rama>") — los tests lo asertan como BaseSha del upsert.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/")
		parts := strings.Split(rest, "/")
		if len(parts) == 4 && parts[1] == "repository" && parts[2] == "branches" {
			branch, err := url.PathUnescape(parts[3])
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":   branch,
				"commit": map[string]any{"id": "tip-" + branch},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(api.Close)

	q := &recordingQueue{}
	cfg := &config.Config{
		MasterKey: testMasterKey,
		Stage2:    config.Stage2Config{GitLabBotUsername: testBotUsername},
	}
	return &webhookEnv{ad: New(st, cfg, q), q: q, st: st, api: api}
}

// newRepo conecta un repo GitLab efímero (identidad única por corrida) con
// signing token + secret token + api token cifrados y base_url apuntando al
// stub.
func (e *webhookEnv) newRepo(t *testing.T, enabled bool) store.Repository {
	t.Helper()
	encSigning, err := store.Encrypt(testMasterKey, []byte(testSigningToken))
	if err != nil {
		t.Fatalf("Encrypt(signing): %v", err)
	}
	encLegacy, err := store.Encrypt(testMasterKey, []byte(testLegacyToken))
	if err != nil {
		t.Fatalf("Encrypt(legacy): %v", err)
	}
	encAPI, err := store.Encrypt(testMasterKey, []byte("project-access-token-de-test"))
	if err != nil {
		t.Fatalf("Encrypt(api): %v", err)
	}
	repo, err := e.st.CreateRepository(context.Background(), store.CreateRepositoryParams{
		Vcs:           "gitlab",
		ExternalID:    time.Now().UnixNano(),
		Owner:         "codeowl-tests",
		Name:          fmt.Sprintf("gitlab-webhook-%d", time.Now().UnixNano()),
		WebhookSecret: pgtype.Text{String: encSigning, Valid: true},
		SecretToken:   pgtype.Text{String: encLegacy, Valid: true},
		ApiToken:      pgtype.Text{String: encAPI, Valid: true},
		BaseUrl:       pgtype.Text{String: e.api.URL, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if !enabled {
		if _, err := e.st.SetRepositoryEnabled(context.Background(), store.SetRepositoryEnabledParams{ID: repo.ID, Enabled: false}); err != nil {
			t.Fatalf("SetRepositoryEnabled(false): %v", err)
		}
	}
	return repo
}

// deliverySeq garantiza webhook IDs únicos entre fixtures.
var (
	deliveryMu sync.Mutex
	deliveryN  int
)

func nextDelivery() string {
	deliveryMu.Lock()
	defer deliveryMu.Unlock()
	deliveryN++
	return fmt.Sprintf("whid-%d-%d", time.Now().UnixNano(), deliveryN)
}

// signedRequest firma el payload con el signing token (Standard Webhooks,
// §9.3: HMAC-SHA256 sobre id.timestamp.body, firma v1,{base64}).
func signedRequest(t *testing.T, payload map[string]any, whID string) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("serializando payload: %v", err)
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, testSigningKey)
	mac.Write([]byte(whID))
	mac.Write([]byte("."))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerWebhookID, whID)
	req.Header.Set(headerWebhookTimestamp, ts)
	req.Header.Set(headerWebhookSignature, "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return req
}

func createSignedPayload(t *testing.T, payload map[string]any) *http.Request {
	t.Helper()
	return signedRequest(t, payload, nextDelivery())
}

// legacyRequest firma con el secret token plano (X-Gitlab-Token, §9.3).
func legacyRequest(t *testing.T, payload map[string]any, token string) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("serializando payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerLegacyToken, token)
	return req
}

// mrPayload arma el payload de un evento merge_request (§3.5).
func mrPayload(projectID, iid int64, action, head, target string, mutators ...func(map[string]any)) map[string]any {
	payload := map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": projectID, "name": "repo"},
		"user":        map[string]any{"username": "autora"},
		"object_attributes": map[string]any{
			"action":        action,
			"iid":           iid,
			"draft":         false,
			"source_branch": "feature",
			"target_branch": target,
			"created_at":    "2026-01-01T00:00:00Z",
			"updated_at":    "2026-01-02T03:04:05Z",
			"last_commit":   map[string]any{"id": head, "message": "cambios"},
		},
	}
	for _, m := range mutators {
		m(payload)
	}
	return payload
}

// notePayload arma el payload de un evento note (id fijo de nota: los
// asserts de parent_comment_id lo usan).
func notePayload(projectID int64, noteableType, body, author string, mrIID int64) map[string]any {
	p := map[string]any{
		"object_kind": "note",
		"project":     map[string]any{"id": projectID, "name": "repo"},
		"user":        map[string]any{"username": author},
		"object_attributes": map[string]any{
			"id":            555,
			"noteable_type": noteableType,
			"note":          body,
		},
	}
	if noteableType == "MergeRequest" {
		p["merge_request"] = map[string]any{"iid": mrIID}
	}
	return p
}

// serve ejecuta el request y verifica el status esperado.
func serve(t *testing.T, e *webhookEnv, req *http.Request, wantStatus int) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ad.HandleWebhook(context.Background(), rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("status: got %d want %d (body: %s)", rec.Code, wantStatus, rec.Body.String())
	}
}

// getPR resuelve la fila del MR por repo+iid (fila si existe).
func (e *webhookEnv) getPR(t *testing.T, repoID, number int64) (store.PullRequest, error) {
	t.Helper()
	return e.st.GetPullRequestByRepoNumber(context.Background(), store.GetPullRequestByRepoNumberParams{
		RepositoryID: repoID, Number: number,
	})
}

func TestWebhookFirmaValidaPasaEInvalidaRechaza(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 1, "open", "h1", "main")), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("firma válida debe procesar: jobs=%d", e.q.count())
	}

	// Entrega nueva con firma corrupta: 401, sin cambios.
	req := createSignedPayload(t, mrPayload(repo.ExternalID, 1, "open", "h2", "main"))
	sig := req.Header.Get(headerWebhookSignature)
	corrupted := "v1," + strings.Repeat("A", len(sig)-len("v1,"))
	req.Header.Set(headerWebhookSignature, corrupted)
	serve(t, e, req, http.StatusUnauthorized)
	if e.q.count() != 1 {
		t.Errorf("firma inválida no debe encolar: jobs=%d", e.q.count())
	}

	// Replay: misma firma válida re-entregada fuera de la ventana de
	// frescura (§9.3) → 401.
	replayed := signedRequest(t, mrPayload(repo.ExternalID, 1, "open", "h3", "main"), nextDelivery())
	replayed.Header.Set(headerWebhookTimestamp, fmt.Sprintf("%d", time.Now().Add(-2*config.DefaultWebhookTimestampTolerance).Unix()))
	serve(t, e, replayed, http.StatusUnauthorized)
	if e.q.count() != 1 {
		t.Errorf("replay fuera de tolerancia no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookTokenLegacyValidoEInvalido(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	// Legacy válido: pasa y procesa.
	serve(t, e, legacyRequest(t, mrPayload(repo.ExternalID, 2, "open", "h1", "main"), testLegacyToken), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("token legacy válido debe procesar: jobs=%d", e.q.count())
	}

	// Legacy inválido: 401.
	serve(t, e, legacyRequest(t, mrPayload(repo.ExternalID, 2, "open", "h2", "main"), "otro-token"), http.StatusUnauthorized)
	if e.q.count() != 1 {
		t.Errorf("token legacy inválido no debe encolar: jobs=%d", e.q.count())
	}

	// Sin firma ni token: 401 — aceptar sin firma sería aceptar spoofing.
	body, _ := json.Marshal(mrPayload(repo.ExternalID, 2, "open", "h3", "main"))
	serve(t, e, httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader(body)), http.StatusUnauthorized)
	if e.q.count() != 1 {
		t.Errorf("entrega sin firma no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookMROpenUpsertYEncolaReview(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 3, "open", "head-aaa", "main")), http.StatusOK)

	pr, err := e.getPR(t, repo.ID, 3)
	if err != nil {
		t.Fatalf("el MR debe upsertearse: %v", err)
	}
	// El SHA de la base no viene en el payload: lo resuelve el adapter por
	// API al tip de la rama target (§3.6.1.1) — "tip-main" en el stub.
	if pr.HeadSha != "head-aaa" || pr.BaseSha != "tip-main" || pr.BaseRef != "main" || pr.State != "open" || pr.Author != "autora" {
		t.Errorf("upsert del MR: got %+v", pr)
	}
	if e.q.count() != 1 {
		t.Fatalf("open debe encolar 1 job: jobs=%d", e.q.count())
	}
	kind, args := e.q.last()
	if kind != jobs.KindReview {
		t.Errorf("kind: got %q want %q", kind, jobs.KindReview)
	}
	var jobArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(args, &jobArgs); err != nil {
		t.Fatalf("args del job: %v", err)
	}
	if jobArgs.PullRequestID != pr.ID || jobArgs.HeadSha != "head-aaa" || jobArgs.BaseSha != "tip-main" {
		t.Errorf("args del ReviewJob: got %+v", jobArgs)
	}
}

func TestWebhookMRUpdatePushEncola(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 4, "open", "head-v1", "main")), http.StatusOK)

	// update por push: oldrev presente y last_commit con el SHA nuevo (§3.5).
	push := mrPayload(repo.ExternalID, 4, "update", "head-v2", "main", func(p map[string]any) {
		p["object_attributes"].(map[string]any)["oldrev"] = "head-v1"
	})
	serve(t, e, createSignedPayload(t, push), http.StatusOK)

	if e.q.count() != 2 {
		t.Fatalf("el push debe encolar otra corrida: jobs=%d", e.q.count())
	}
	_, args := e.q.last()
	var jobArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(args, &jobArgs); err != nil {
		t.Fatalf("args: %v", err)
	}
	if jobArgs.HeadSha != "head-v2" {
		t.Errorf("el push debe traer el head nuevo: got %q", jobArgs.HeadSha)
	}
	pr, err := e.getPR(t, repo.ID, 4)
	if err != nil || pr.HeadSha != "head-v2" {
		t.Errorf("la fila del MR debe reflejar el head nuevo: %+v err=%v", pr, err)
	}
}

func TestWebhookMRUpdateTituloSeDescarta(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 5, "open", "h1", "main")), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("open debe encolar: jobs=%d", e.q.count())
	}

	// update con solo título nuevo (changes.title): ni push ni retarget ni
	// draft — descarte (§3.5).
	edit := mrPayload(repo.ExternalID, 5, "update", "h1", "main", func(p map[string]any) {
		p["changes"] = map[string]any{"title": map[string]any{"previous": "viejo", "current": "nuevo"}}
	})
	serve(t, e, createSignedPayload(t, edit), http.StatusOK)

	if e.q.count() != 1 {
		t.Errorf("update de título no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookMRUpdateRetargetEncola(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 6, "open", "h1", "main")), http.StatusOK)

	// Retarget: changes.target_branch presente — la review contra la base
	// vieja quedó stale (§3.5).
	retarget := mrPayload(repo.ExternalID, 6, "update", "h1", "release-1", func(p map[string]any) {
		p["changes"] = map[string]any{"target_branch": map[string]any{"previous": "main", "current": "release-1"}}
	})
	serve(t, e, createSignedPayload(t, retarget), http.StatusOK)

	if e.q.count() != 2 {
		t.Fatalf("el retarget debe disparar re-review: jobs=%d", e.q.count())
	}
	_, args := e.q.last()
	var jobArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(args, &jobArgs); err != nil {
		t.Fatalf("args: %v", err)
	}
	if jobArgs.BaseSha != "tip-release-1" {
		t.Errorf("el job debe anclarse a la base nueva: got %q", jobArgs.BaseSha)
	}
	pr, err := e.getPR(t, repo.ID, 6)
	if err != nil || pr.BaseSha != "tip-release-1" || pr.BaseRef != "release-1" {
		t.Errorf("la fila debe reflejar la base nueva: %+v err=%v", pr, err)
	}
}

func TestWebhookMRDraftTransicion(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true) // review_drafts=false default (§3.5)

	// MR abierto como draft: upsert sin review.
	draftOpen := mrPayload(repo.ExternalID, 7, "open", "h1", "main", func(p map[string]any) {
		p["object_attributes"].(map[string]any)["draft"] = true
	})
	serve(t, e, createSignedPayload(t, draftOpen), http.StatusOK)
	if e.q.count() != 0 {
		t.Fatalf("un draft no se revisa por default: jobs=%d", e.q.count())
	}
	if _, err := e.getPR(t, repo.ID, 7); err != nil {
		t.Fatalf("el draft debe upsertearse: %v", err)
	}

	// update por push de un draft sigue sin revisar (sigue draft).
	push := mrPayload(repo.ExternalID, 7, "update", "h2", "main", func(p map[string]any) {
		attrs := p["object_attributes"].(map[string]any)
		attrs["draft"] = true
		attrs["oldrev"] = "h1"
		attrs["last_commit"].(map[string]any)["id"] = "h2"
	})
	serve(t, e, createSignedPayload(t, push), http.StatusOK)
	if e.q.count() != 0 {
		t.Errorf("push de un draft no debe encolar: jobs=%d", e.q.count())
	}

	// Transición draft→ready (changes.draft previous=true current=false):
	// pasa el filtro — el equivalente de ready_for_review (§3.5).
	ready := mrPayload(repo.ExternalID, 7, "update", "h2", "main", func(p map[string]any) {
		attrs := p["object_attributes"].(map[string]any)
		attrs["draft"] = false
		p["changes"] = map[string]any{"draft": map[string]any{"previous": true, "current": false}}
	})
	serve(t, e, createSignedPayload(t, ready), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("draft→ready debe encolar la review: jobs=%d", e.q.count())
	}

	// Vuelta a draft (previous=false current=true): se descarta, mismo
	// tratamiento que converted_to_draft en GitHub (§3.5).
	back := mrPayload(repo.ExternalID, 7, "update", "h2", "main", func(p map[string]any) {
		attrs := p["object_attributes"].(map[string]any)
		attrs["draft"] = true
		p["changes"] = map[string]any{"draft": map[string]any{"previous": false, "current": true}}
	})
	serve(t, e, createSignedPayload(t, back), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("ready→draft no debe encolar: jobs=%d", e.q.count())
	}
}

// crearEmbeddingProvider inserta un proveedor embedding enabled efímero:
// la guarda del IndexJob es de EXISTENCIA (§9.6) y el estado heredado de la
// BD compartida no es determinista.
func crearEmbeddingProvider(t *testing.T, st *store.Store) {
	t.Helper()
	clave := bytes.Repeat([]byte{0xC5}, 32)
	apiKey, err := store.Encrypt(clave, []byte("sk-embed-test"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateLlmProvider(context.Background(), store.CreateLlmProviderParams{
		BaseUrl: "https://embed.test/v1", Model: fmt.Sprintf("embed-%d", time.Now().UnixNano()),
		ApiKey: apiKey, Role: "embedding", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DeleteLlmProvider(context.Background(), p.ID) })
}

func TestWebhookMRCloseSinJobYMergeEncolaIndice(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)
	crearEmbeddingProvider(t, e.st) // con rol embedding, el merge encola el índice (§6 F4)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 8, "open", "h1", "main")), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("open debe encolar: jobs=%d", e.q.count())
	}

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 8, "close", "h1", "main")), http.StatusOK)
	if e.q.count() != 2 {
		t.Fatalf("el close debe encolar el MetricsJob (§6 F5): jobs=%d", e.q.count())
	}
	kind, args := e.q.last()
	if kind != jobs.KindMetrics {
		t.Errorf("el close debe encolar el MetricsJob: got %q", kind)
	}
	pr, err := e.getPR(t, repo.ID, 8)
	if err != nil || pr.State != "closed" {
		t.Fatalf("el close debe actualizar el estado: %+v err=%v", pr, err)
	}
	var metricsArgs jobs.MetricsJobArgs
	if err := json.Unmarshal(args, &metricsArgs); err != nil {
		t.Fatalf("args del MetricsJob: %v", err)
	}
	if metricsArgs.RepositoryID != repo.ID || metricsArgs.PullRequestID != pr.ID {
		t.Errorf("el MetricsJob debe apuntar al repo y MR cerrados: %+v", metricsArgs)
	}
	if got := e.q.lastCancel(); got != pr.ID {
		t.Errorf("el close debe pedir el descarte de jobs del MR %d: pidió %d", pr.ID, got)
	}
	if pr.MergedAt.Valid {
		t.Errorf("close simple no debe registrar merged_at: %+v", pr.MergedAt)
	}

	// Merge: close con merged_at (el updated_at del evento, §3.5) + IndexJob
	// del repo (§6 F4 — la rama default avanzó). El MetricsJob sale en TODO
	// cierre, merge incluido: [review, metrics(close), metrics(merge), index].
	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 8, "merge", "h1", "main")), http.StatusOK)
	if e.q.count() != 4 {
		t.Fatalf("el merge encola MetricsJob (todo cierre) + IndexJob: jobs=%d", e.q.count())
	}
	kind, args = e.q.last()
	if kind != jobs.KindIndex {
		t.Errorf("kind: got %q want %q", kind, jobs.KindIndex)
	}
	var indexArgs jobs.IndexJobArgs
	if err := json.Unmarshal(args, &indexArgs); err != nil {
		t.Fatalf("args del IndexJob: %v", err)
	}
	if indexArgs.RepositoryID != repo.ID {
		t.Errorf("el IndexJob debe apuntar al repo %d: %+v", repo.ID, indexArgs)
	}
	pr, err = e.getPR(t, repo.ID, 8)
	if err != nil {
		t.Fatalf("MR: %v", err)
	}
	if !pr.MergedAt.Valid || pr.MergedAt.Time.UTC().Hour() != 3 {
		t.Errorf("merge debe registrar merged_at del evento: %+v", pr.MergedAt)
	}
}

func TestWebhookMRReopenEncola(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 9, "open", "h1", "main")), http.StatusOK)
	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 9, "close", "h1", "main")), http.StatusOK)
	if e.q.count() != 2 {
		t.Fatalf("el close encola el MetricsJob (§6 F5): jobs=%d", e.q.count())
	}

	// reopen se trata como open: state vuelve a open y dispara review (§3.5).
	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 9, "reopen", "h2", "main")), http.StatusOK)
	if e.q.count() != 3 {
		t.Fatalf("reopen debe encolar la review: jobs=%d", e.q.count())
	}
	pr, err := e.getPR(t, repo.ID, 9)
	if err != nil || pr.State != "open" {
		t.Errorf("reopen debe devolver el estado a open: %+v err=%v", pr, err)
	}
}

func TestWebhookNoteConMencionEncolaChat(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 10, "open", "h1", "main")), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("open debe encolar: jobs=%d", e.q.count())
	}
	pr, err := e.getPR(t, repo.ID, 10)
	if err != nil {
		t.Fatalf("MR: %v", err)
	}

	// note en MR con mención: encola el ChatJob (F2).
	serve(t, e, createSignedPayload(t,
		notePayload(repo.ExternalID, "MergeRequest", "@"+testBotUsername+" revisá esto", "humano", 10)), http.StatusOK)
	if e.q.count() != 2 {
		t.Fatalf("la mención debe encolar el ChatJob: jobs=%d", e.q.count())
	}
	kind, args := e.q.last()
	if kind != jobs.KindChat {
		t.Fatalf("kind: got %q want %q", kind, jobs.KindChat)
	}
	var chatArgs jobs.ChatJobArgs
	if err := json.Unmarshal(args, &chatArgs); err != nil {
		t.Fatalf("args del ChatJob: %v", err)
	}
	if chatArgs.PullRequestID != pr.ID || chatArgs.ParentCommentID != "555" ||
		chatArgs.CommentAuthor != "humano" || chatArgs.HeadSha != "h1" ||
		chatArgs.BaseSha != "tip-main" || chatArgs.Language != repo.Language {
		t.Errorf("args del ChatJob: got %+v", chatArgs)
	}
}

func TestWebhookNoteEnIssueSeDescarta(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t,
		notePayload(repo.ExternalID, "Issue", "@"+testBotUsername+" revisá esto", "humano", 99)), http.StatusOK)
	if e.q.count() != 0 {
		t.Errorf("nota fuera de un MR no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookNoteDelBotIgnorada(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 11, "open", "h1", "main")), http.StatusOK)

	// Anti-bucle (§3.5): la nota del propio bot jamás dispara nada, aunque
	// mencione al bot o cite una mención previa.
	serve(t, e, createSignedPayload(t,
		notePayload(repo.ExternalID, "MergeRequest", "@"+testBotUsername+", listo", testBotUsername, 11)), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("la nota del bot no debe encolar: jobs=%d", e.q.count())
	}

	// Sin mención: también se descarta.
	serve(t, e, createSignedPayload(t,
		notePayload(repo.ExternalID, "MergeRequest", "esto está bien", "humano", 11)), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("la nota sin mención no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookRepoDesconocidoSinEstado(t *testing.T) {
	e := newWebhookEnv(t)

	req := createSignedPayload(t, mrPayload(time.Now().UnixNano(), 12, "open", "h1", "main"))
	serve(t, e, req, http.StatusOK)

	if e.q.count() != 0 {
		t.Errorf("repo desconocido no debe encolar: jobs=%d", e.q.count())
	}
	// §3.5: sin fila en webhook_deliveries — solo repos conectados generan
	// estado. Y sin verificar firma: no hay secret contra el cual hacerlo.
	exists, err := e.st.WebhookDeliveryExists(context.Background(), store.WebhookDeliveryExistsParams{
		Vcs: "gitlab", DeliveryID: req.Header.Get(headerWebhookID),
	})
	if err != nil || exists {
		t.Errorf("repo desconocido no debe dejar fila de delivery: exists=%v err=%v", exists, err)
	}
}

func TestWebhookRepoDeshabilitadoSinEstado(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, false) // desconectado: flag enabled (§3.5)

	req := createSignedPayload(t, mrPayload(repo.ExternalID, 13, "open", "h1", "main"))
	serve(t, e, req, http.StatusOK)

	if e.q.count() != 0 {
		t.Errorf("repo deshabilitado no debe encolar: jobs=%d", e.q.count())
	}
	exists, err := e.st.WebhookDeliveryExists(context.Background(), store.WebhookDeliveryExistsParams{
		Vcs: "gitlab", DeliveryID: req.Header.Get(headerWebhookID),
	})
	if err != nil || exists {
		t.Errorf("repo deshabilitado no debe dejar fila de delivery: exists=%v err=%v", exists, err)
	}
	if _, err := e.getPR(t, repo.ID, 13); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("repo deshabilitado no debe upsertear el MR, got %v", err)
	}
}

func TestWebhookDeliveryDuplicadaSkip(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	// Dos requests frescas, MISMO webhook-id: la re-entrega del mismo
	// delivery corta por dedup (§9.3).
	whID := nextDelivery()
	payload := mrPayload(repo.ExternalID, 14, "open", "h1", "main")
	serve(t, e, signedRequest(t, payload, whID), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("primera entrega debe encolar: jobs=%d", e.q.count())
	}
	serve(t, e, signedRequest(t, payload, whID), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("la re-entrega no debe encolar: jobs=%d", e.q.count())
	}
	pr, err := e.getPR(t, repo.ID, 14)
	if err != nil || pr.HeadSha != "h1" {
		t.Errorf("el MR debe existir una sola vez con h1: %+v err=%v", pr, err)
	}
}

func TestWebhookEventoNoSuscritoSeDescarta(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	// push y pipeline no están suscritos (§3.5: merge_request y note, y nada más).
	serve(t, e, createSignedPayload(t, map[string]any{
		"object_kind": "push",
		"project":     map[string]any{"id": repo.ExternalID},
	}), http.StatusOK)
	serve(t, e, createSignedPayload(t, map[string]any{
		"object_kind": "pipeline",
		"project":     map[string]any{"id": repo.ExternalID},
	}), http.StatusOK)
	// merge_request con acción fuera del set (approved): descarte.
	serve(t, e, createSignedPayload(t, mrPayload(repo.ExternalID, 15, "approved", "h1", "main")), http.StatusOK)

	if e.q.count() != 0 {
		t.Errorf("eventos no suscritos no encolan: jobs=%d", e.q.count())
	}
}
