package github

// Tests del webhook de GitHub contra el Postgres de desarrollo (mapa:
// pruebas de backend.vcs — payloads firmados de fixtures, sin salida a
// internet). Requieren DATABASE_URL (just test lo exporta del .env).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/migrations"
)

// testWebhookSecret es el secret de firma de los fixtures (§9.3: en el
// handler vive en cfg.Stage2.GitHubWebhookSecret).
const testWebhookSecret = "secreto-de-test-del-webhook"

// recordingQueue es el stub de jobs.JobQueue (§4.5: stub de 10 líneas, sin
// mocks): registra kinds y args encolados.
type recordingQueue struct {
	mu    sync.Mutex
	kinds []string
	args  []json.RawMessage
}

func (q *recordingQueue) Enqueue(_ context.Context, kind string, args json.RawMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.kinds = append(q.kinds, kind)
	q.args = append(q.args, args)
	return nil
}

func (q *recordingQueue) Start(context.Context) error { return nil }
func (q *recordingQueue) Stop(context.Context) error  { return nil }

func (q *recordingQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.kinds)
}

// lastKind devuelve el último kind + args encolados (cero valores si vacío).
func (q *recordingQueue) last() (string, json.RawMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.kinds) == 0 {
		return "", nil
	}
	return q.kinds[len(q.kinds)-1], q.args[len(q.args)-1]
}

// deliverySeq garantiza delivery IDs únicos entre fixtures.
var deliverySeq atomic.Int64

func nextDelivery() string {
	return fmt.Sprintf("delivery-%d-%d", time.Now().UnixNano(), deliverySeq.Add(1))
}

// webhookEnv es el entorno de un test: adapter + cola registradora + store.
type webhookEnv struct {
	ad *Adapter
	q  *recordingQueue
	st *store.Store
}

func newWebhookEnv(t *testing.T) *webhookEnv {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no está seteado: saltando tests de integración")
	}
	st, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	if err := store.Migrate(url, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	q := &recordingQueue{}
	cfg := &config.Config{Stage2: config.Stage2Config{GitHubWebhookSecret: testWebhookSecret}}
	return &webhookEnv{ad: New(st, cfg, q), q: q, st: st}
}

// newRepo conecta un repo GitHub efímero (identidad única por corrida).
func (e *webhookEnv) newRepo(t *testing.T, enabled bool) store.Repository {
	t.Helper()
	repo, err := e.st.CreateRepository(context.Background(), store.CreateRepositoryParams{
		Vcs:        "github",
		ExternalID: time.Now().UnixNano(),
		Owner:      "codeowl-tests",
		Name:       fmt.Sprintf("webhook-%d", time.Now().UnixNano()),
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

// createSignedPayload firma un payload de fixture con el secret de test
// (§9.3: HMAC-SHA256 sobre el body crudo, hex con prefijo sha256=).
func createSignedPayload(t *testing.T, event string, payload map[string]any) *http.Request {
	t.Helper()
	return signedPayload(t, event, nextDelivery(), payload)
}

// signedPayload firma con un delivery ID explícito: permite simular la
// re-entrega del mismo delivery con requests frescas.
func signedPayload(t *testing.T, event, delivery string, payload map[string]any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("serializando payload: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(testWebhookSecret))
	mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerEvent, event)
	req.Header.Set(headerDelivery, delivery)
	req.Header.Set(headerSignature, signaturePrefix+fmt.Sprintf("%x", mac.Sum(nil)))
	return req
}

// prPayload arma el payload de un evento pull_request.
func prPayload(repoID int64, number int64, action, head, base, baseRef string, mutators ...func(map[string]any)) map[string]any {
	payload := map[string]any{
		"action": action,
		"number": number,
		"pull_request": map[string]any{
			"number":     number,
			"state":      "open",
			"draft":      false,
			"created_at": "2026-01-01T00:00:00Z",
			"user":       map[string]any{"login": "autora", "type": "User"},
			"head":       map[string]any{"ref": "feature", "sha": head},
			"base":       map[string]any{"ref": baseRef, "sha": base},
		},
		"repository": map[string]any{"id": repoID, "name": "repo"},
	}
	for _, m := range mutators {
		m(payload)
	}
	return payload
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

// getPR resuelve la fila del PR por repo+number (fila si existe).
func (e *webhookEnv) getPR(t *testing.T, repoID, number int64) (store.PullRequest, error) {
	t.Helper()
	return e.st.GetPullRequestByRepoNumber(context.Background(), store.GetPullRequestByRepoNumberParams{
		RepositoryID: repoID, Number: number,
	})
}

func TestWebhookFirmaValidaPasaEInvalidaRechaza(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request", prPayload(repo.ExternalID, 1, "opened", "h1", "b1", "main")), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("firma válida debe procesar: jobs=%d", e.q.count())
	}

	// Entrega nueva con firma corrupta (primer dígito hex cambiado): 401,
	// sin cambios.
	req := createSignedPayload(t, "pull_request", prPayload(repo.ExternalID, 1, "opened", "h2", "b1", "main"))
	sig := req.Header.Get(headerSignature)
	first := sig[len(signaturePrefix)]
	flipped := byte('0')
	if first == '0' {
		flipped = '1'
	}
	req.Header.Set(headerSignature, signaturePrefix+string(flipped)+sig[len(signaturePrefix)+1:])
	serve(t, e, req, http.StatusUnauthorized)
	if e.q.count() != 1 {
		t.Errorf("firma inválida no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookDeliveryDuplicadaSkip(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	// Dos requests frescas, MISMO delivery ID: la re-entrega del mismo
	// delivery corta por dedup (§9.3).
	delivery := nextDelivery()
	payload := prPayload(repo.ExternalID, 2, "opened", "h1", "b1", "main")
	serve(t, e, signedPayload(t, "pull_request", delivery, payload), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("primera entrega debe encolar: jobs=%d", e.q.count())
	}
	serve(t, e, signedPayload(t, "pull_request", delivery, payload), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("la re-entrega no debe encolar: jobs=%d", e.q.count())
	}
	pr, err := e.getPR(t, repo.ID, 2)
	if err != nil || pr.HeadSha != "h1" {
		t.Errorf("el PR debe existir una sola vez con h1: %+v err=%v", pr, err)
	}
}

func TestWebhookPROpenedUpsertYEncolaReview(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 3, "opened", "head-aaa", "base-aaa", "main")), http.StatusOK)

	pr, err := e.getPR(t, repo.ID, 3)
	if err != nil {
		t.Fatalf("el PR debe upsertearse: %v", err)
	}
	if pr.HeadSha != "head-aaa" || pr.BaseSha != "base-aaa" || pr.BaseRef != "main" || pr.State != "open" || pr.Author != "autora" {
		t.Errorf("upsert del PR: got %+v", pr)
	}
	if e.q.count() != 1 {
		t.Fatalf("opened debe encolar 1 job: jobs=%d", e.q.count())
	}
	kind, args := e.q.last()
	if kind != jobs.KindReview {
		t.Errorf("kind: got %q want %q", kind, jobs.KindReview)
	}
	var jobArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(args, &jobArgs); err != nil {
		t.Fatalf("args del job: %v", err)
	}
	if jobArgs.PullRequestID != pr.ID || jobArgs.HeadSha != "head-aaa" || jobArgs.BaseSha != "base-aaa" {
		t.Errorf("args del ReviewJob: got %+v", jobArgs)
	}
}

func TestWebhookPRSynchronizeNuevaCabeza(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 4, "opened", "head-v1", "b1", "main")), http.StatusOK)
	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 4, "synchronize", "head-v2", "b1", "main")), http.StatusOK)

	if e.q.count() != 2 {
		t.Fatalf("synchronize debe encolar otra corrida: jobs=%d", e.q.count())
	}
	kind, args := e.q.last()
	if kind != jobs.KindReview {
		t.Fatalf("kind: got %q", kind)
	}
	var jobArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(args, &jobArgs); err != nil {
		t.Fatalf("args: %v", err)
	}
	if jobArgs.HeadSha != "head-v2" {
		t.Errorf("el synchronize debe traer el head nuevo: got %q", jobArgs.HeadSha)
	}
	pr, err := e.getPR(t, repo.ID, 4)
	if err != nil || pr.HeadSha != "head-v2" {
		t.Errorf("la fila del PR debe reflejar el head nuevo: %+v err=%v", pr, err)
	}
}

func TestWebhookPRClosedSinJob(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 5, "opened", "h1", "b1", "main")), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("opened debe encolar: jobs=%d", e.q.count())
	}

	// Cierre como merge: state closed + merged_at (§3.5), sin job nuevo.
	merged := prPayload(repo.ExternalID, 5, "closed", "h1", "b1", "main", func(p map[string]any) {
		pr := p["pull_request"].(map[string]any)
		pr["state"] = "closed"
		pr["merged"] = true
		pr["merged_at"] = "2026-02-03T04:05:06Z"
	})
	serve(t, e, createSignedPayload(t, "pull_request", merged), http.StatusOK)

	if e.q.count() != 1 {
		t.Errorf("closed no debe encolar (MetricsJob es F5): jobs=%d", e.q.count())
	}
	pr, err := e.getPR(t, repo.ID, 5)
	if err != nil {
		t.Fatalf("PR: %v", err)
	}
	if pr.State != "closed" {
		t.Errorf("state: got %q want closed", pr.State)
	}
	if !pr.MergedAt.Valid || pr.MergedAt.Time.UTC().Hour() != 4 {
		t.Errorf("merged_at debe registrarse en el merge: %+v", pr.MergedAt)
	}
}

func TestWebhookPRConvertedToDraftSinJob(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 6, "opened", "h1", "b1", "main")), http.StatusOK)
	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 6, "converted_to_draft", "h1", "b1", "main")), http.StatusOK)

	if e.q.count() != 1 {
		t.Errorf("converted_to_draft no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookPREditedSinCambioDeBaseSeDescarta(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 7, "opened", "h1", "b1", "main")), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("opened debe encolar: jobs=%d", e.q.count())
	}

	// edited con título nuevo (changes.title): no es retarget — descarte.
	edit := prPayload(repo.ExternalID, 7, "edited", "h1", "b1", "main", func(p map[string]any) {
		p["changes"] = map[string]any{"title": map[string]any{"from": "viejo"}}
	})
	serve(t, e, createSignedPayload(t, "pull_request", edit), http.StatusOK)

	if e.q.count() != 1 {
		t.Errorf("edited sin cambio de base no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookPRRetargetEncola(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 8, "opened", "h1", "b-vieja", "main")), http.StatusOK)

	// Retarget: changes.base presente — la review contra la base vieja
	// quedó vigente para un diff que ya no existe (§3.5).
	retarget := prPayload(repo.ExternalID, 8, "edited", "h1", "b-nueva", "release-1", func(p map[string]any) {
		p["changes"] = map[string]any{"base": map[string]any{
			"ref": map[string]any{"from": "main"},
			"sha": map[string]any{"from": "b-vieja"},
		}}
	})
	serve(t, e, createSignedPayload(t, "pull_request", retarget), http.StatusOK)

	if e.q.count() != 2 {
		t.Fatalf("el retarget debe disparar re-review: jobs=%d", e.q.count())
	}
	_, args := e.q.last()
	var jobArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(args, &jobArgs); err != nil {
		t.Fatalf("args: %v", err)
	}
	if jobArgs.BaseSha != "b-nueva" {
		t.Errorf("el job debe anclarse a la base nueva: got %q", jobArgs.BaseSha)
	}
	pr, err := e.getPR(t, repo.ID, 8)
	if err != nil || pr.BaseSha != "b-nueva" || pr.BaseRef != "release-1" {
		t.Errorf("la fila debe reflejar la base nueva: %+v err=%v", pr, err)
	}
}

func TestWebhookRepoDesconocidoSinEstado(t *testing.T) {
	e := newWebhookEnv(t)

	req := createSignedPayload(t, "pull_request", prPayload(time.Now().UnixNano(), 9, "opened", "h1", "b1", "main"))
	serve(t, e, req, http.StatusOK)

	if e.q.count() != 0 {
		t.Errorf("repo desconocido no debe encolar: jobs=%d", e.q.count())
	}
	// §3.5: sin fila en webhook_deliveries — solo repos conectados generan
	// estado.
	exists, err := e.st.WebhookDeliveryExists(context.Background(), store.WebhookDeliveryExistsParams{
		Vcs: "github", DeliveryID: req.Header.Get(headerDelivery),
	})
	if err != nil || exists {
		t.Errorf("repo desconocido no debe dejar fila de delivery: exists=%v err=%v", exists, err)
	}
}

func TestWebhookRepoDeshabilitadoSinEstado(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, false) // desconectado: flag enabled (§3.5)

	req := createSignedPayload(t, "pull_request", prPayload(repo.ExternalID, 10, "opened", "h1", "b1", "main"))
	serve(t, e, req, http.StatusOK)

	if e.q.count() != 0 {
		t.Errorf("repo deshabilitado no debe encolar: jobs=%d", e.q.count())
	}
	exists, err := e.st.WebhookDeliveryExists(context.Background(), store.WebhookDeliveryExistsParams{
		Vcs: "github", DeliveryID: req.Header.Get(headerDelivery),
	})
	if err != nil || exists {
		t.Errorf("repo deshabilitado no debe dejar fila de delivery: exists=%v err=%v", exists, err)
	}
	if _, err := e.getPR(t, repo.ID, 10); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("repo deshabilitado no debe upsertear el PR, got %v", err)
	}
}

func TestWebhookAccionNoSoportadaSeDescarta(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 11, "labeled", "h1", "b1", "main")), http.StatusOK)

	if e.q.count() != 0 {
		t.Errorf("labeled no debe encolar: jobs=%d", e.q.count())
	}
	if _, err := e.getPR(t, repo.ID, 11); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("labeled no debe upsertear el PR, got %v", err)
	}
}

func TestWebhookDraftSinReviewSalvoConfig(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true) // review_drafts=false default (§3.5)

	draft := prPayload(repo.ExternalID, 12, "opened", "h1", "b1", "main", func(p map[string]any) {
		p["pull_request"].(map[string]any)["draft"] = true
	})
	serve(t, e, createSignedPayload(t, "pull_request", draft), http.StatusOK)

	if e.q.count() != 0 {
		t.Fatalf("un draft no se revisa por default: jobs=%d", e.q.count())
	}
	// La fila igual queda al día (el chat de F2 opera sobre drafts).
	if _, err := e.getPR(t, repo.ID, 12); err != nil {
		t.Fatalf("el draft debe upsertearse: %v", err)
	}

	// Config por repo: con review_drafts la review arranca igual.
	if _, err := e.st.UpdateRepository(context.Background(), store.UpdateRepositoryParams{
		ID: repo.ID, Owner: repo.Owner, Name: repo.Name,
		ReviewDrafts: true, Language: repo.Language, ChatOrgOnly: repo.ChatOrgOnly,
	}); err != nil {
		t.Fatalf("habilitando review_drafts: %v", err)
	}
	draft2 := prPayload(repo.ExternalID, 13, "opened", "h2", "b1", "main", func(p map[string]any) {
		p["pull_request"].(map[string]any)["draft"] = true
	})
	serve(t, e, createSignedPayload(t, "pull_request", draft2), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("con review_drafts el draft se revisa: jobs=%d", e.q.count())
	}
}

func TestWebhookComentarioEnPRSeFiltraSinJob(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	// El PR debe existir para que el comentario pase el filtro (§3.5).
	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 14, "opened", "h1", "b1", "main")), http.StatusOK)
	if e.q.count() != 1 {
		t.Fatalf("opened debe encolar: jobs=%d", e.q.count())
	}

	// issue_comment created sobre el PR: pasa el filtro (ChatJob es F2) y
	// por ahora solo se registra y descarta — sin job, sin estado.
	comment := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       14,
			"pull_request": map[string]any{},
		},
		"comment":    map[string]any{"body": "@codeowl revisá esto", "user": map[string]any{"login": "humano", "type": "User"}},
		"repository": map[string]any{"id": repo.ExternalID},
	}
	serve(t, e, createSignedPayload(t, "issue_comment", comment), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("los comentarios no encolan en F1 (ChatJob es F2): jobs=%d", e.q.count())
	}

	// pull_request_review_comment created sobre el PR: mismo tratamiento.
	reviewComment := map[string]any{
		"action":       "created",
		"pull_request": map[string]any{"number": 14},
		"comment":      map[string]any{"body": "nit", "user": map[string]any{"login": "humano", "type": "User"}},
		"repository":   map[string]any{"id": repo.ExternalID},
	}
	serve(t, e, createSignedPayload(t, "pull_request_review_comment", reviewComment), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("los comentarios inline tampoco encolan en F1: jobs=%d", e.q.count())
	}
}

func TestWebhookComentarioDeBotIgnorado(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "pull_request",
		prPayload(repo.ExternalID, 15, "opened", "h1", "b1", "main")), http.StatusOK)

	// Anti-bucle (§3.5): el comentario del propio bot jamás dispara nada.
	bot := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       15,
			"pull_request": map[string]any{},
		},
		"comment":    map[string]any{"body": "@codeowl", "user": map[string]any{"login": "codeowl[bot]", "type": "Bot"}},
		"repository": map[string]any{"id": repo.ExternalID},
	}
	serve(t, e, createSignedPayload(t, "issue_comment", bot), http.StatusOK)
	if e.q.count() != 1 {
		t.Errorf("el comentario del bot no debe encolar: jobs=%d", e.q.count())
	}
}

func TestWebhookEventoNoSuscritoSeDescarta(t *testing.T) {
	e := newWebhookEnv(t)
	repo := e.newRepo(t, true)

	serve(t, e, createSignedPayload(t, "push", map[string]any{
		"repository": map[string]any{"id": repo.ExternalID},
	}), http.StatusOK)
	serve(t, e, createSignedPayload(t, "ping", map[string]any{
		"repository": map[string]any{"id": repo.ExternalID},
	}), http.StatusOK)

	if e.q.count() != 0 {
		t.Errorf("eventos no suscritos no encolan: jobs=%d", e.q.count())
	}
}
