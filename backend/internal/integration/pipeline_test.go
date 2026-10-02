//go:build integration

// Package integration prueba el pipeline completo de punta a punta (mapa:
// "backend/internal/api/api_test.go (webhook firmado simulado)" — esta es la
// versión comprehensiva): webhook firmado → validación → filtro → upsert →
// enqueue → pipeline de review → estado en BD. Nada toca internet: el LLM es
// un stub OpenAI-compatible (httptest), el VCS de publicación y el analyzer
// son stubs. Requiere DATABASE_URL (Postgres de desarrollo) — sin ella el
// test se salta, igual que el resto de los tests de integración.
package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/api"
	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
	vcsgh "github.com/Andres39128/codeowl/backend/internal/vcs/github"
	"github.com/Andres39128/codeowl/backend/migrations"
)

// Secret de firma del webhook de test (§9.3: en el handler vive en
// cfg.Stage2.GitHubWebhookSecret).
const testWebhookSecret = "secreto-integration-del-webhook"

// Marcadores del prompt de usuario que enrutan la respuesta del stub LLM
// (mismos que review/agents.go usa: el Reviewer y el Summarizer se
// distinguen por el prefijo del prompt).
const (
	reviewerMarker   = "Archivo: "
	summarizerMarker = "Resumí el siguiente pull request."
)

// Identidades de la corrida: SHAs realistas (hex de 40) — la base es fake y
// el merge-base del clon cae a base_sha por diseño (§3.6.1).
const (
	prNumber   = int64(101)
	fakeBase   = "1111111111111111111111111111111111111111"
	syncHead   = "89abcdef0123456789abcdef0123456789abcdef"
	summaryTxt = "Stub: agrega el import fmt y ajusta el saludo."
)

// diffFixture es el diff que el VCS stub le sirve al pipeline: main.go con
// la línea 3 nueva del lado RIGHT (import) — el finding del LLM ancla ahí.
const diffFixture = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,5 +1,6 @@
 package main

+import "fmt"
+
 func main() {
-	fmt.Println("hola")
+	fmt.Println("hola mundo")
 }
`

// reviewerOut es la salida JSON válida del Reviewer (§9.8: conjuntos
// cerrados respetados) con sugerencia aplicable.
const reviewerOut = `[{"line":3,"severity":"medium","category":"logic","body":"main no valida sus argumentos antes de usarlos.","suggestion":"func main() { /* validar */ }"}]`

// summarizerOut es la salida JSON válida del Summarizer.
const summarizerOut = `{"summary":"` + summaryTxt + `","walkthrough":"Un único archivo tocado.","mermaid":"sequenceDiagram\n  Dev->>Bot: abre PR\n  Bot-->>Dev: resumen"}`

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// enqueuedJob es un job capturado por la cola de test.
type enqueuedJob struct {
	Kind string
	Args json.RawMessage
}

// capturedQueue es el stub de jobs.JobQueue basado en canal: el handler
// encola (buffered — el handler jamás bloquea) y el test drena y verifica.
type capturedQueue struct {
	ch chan enqueuedJob
}

func newCapturedQueue() *capturedQueue { return &capturedQueue{ch: make(chan enqueuedJob, 16)} }

func (q *capturedQueue) Enqueue(_ context.Context, kind string, args json.RawMessage) error {
	q.ch <- enqueuedJob{Kind: kind, Args: args}
	return nil
}
func (q *capturedQueue) Register(...jobs.Worker) error { return nil }
func (q *capturedQueue) Start(context.Context) error   { return nil }
func (q *capturedQueue) Stop(context.Context) error    { return nil }

// pending drena el canal sin bloquear: los jobs encolados hasta ahora.
func (q *capturedQueue) pending() []enqueuedJob {
	var out []enqueuedJob
	for {
		select {
		case j := <-q.ch:
			out = append(out, j)
		default:
			return out
		}
	}
}

// stubLLM arma el servidor OpenAI-compatible: responde chat completions en
// {base}/v1/chat/completions y enruta por el marcador del prompt de usuario.
func stubLLM(t *testing.T, reviewer, summarizer string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			http.Error(w, "request inválido", http.StatusBadRequest)
			return
		}
		var content string
		switch last := req.Messages[len(req.Messages)-1].Content; {
		case strings.HasPrefix(last, reviewerMarker):
			content = reviewer
		case strings.HasPrefix(last, summarizerMarker):
			content = summarizer
		default:
			http.Error(w, "prompt de agente desconocido", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`, content)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stubVCS sirve el diff fixture al pipeline y graba las publicaciones: el
// contrato vcs.VCSProvider completo, sin HTTP real.
type stubVCS struct {
	diff string

	mu        sync.Mutex
	summaries []string // cuerpos publicados (dos fases: provisional + final)
	inlines   []vcs.CommentPosition
}

func (s *stubVCS) HandleWebhook(context.Context, http.ResponseWriter, *http.Request) {}
func (s *stubVCS) FetchPR(context.Context, *store.Repository, *store.PullRequest, string) error {
	return nil
}
func (s *stubVCS) FetchDefaultBranch(context.Context, *store.Repository, string) error { return nil }
func (s *stubVCS) FetchPRTimeline(context.Context, *store.Repository, *store.PullRequest) (*vcs.PRTimeline, error) {
	return &vcs.PRTimeline{}, nil
}
func (s *stubVCS) ListOpenPRs(context.Context, *store.Repository) ([]vcs.OpenPR, error) {
	return nil, nil
}
func (s *stubVCS) GetDiff(context.Context, *store.Repository, *store.PullRequest) (string, error) {
	return s.diff, nil
}
func (s *stubVCS) PostSuggestion(_ context.Context, _ *store.Repository, _ *store.PullRequest, pos vcs.CommentPosition, _ string) (string, error) {
	return s.recordInline(pos), nil
}
func (s *stubVCS) PostInlineComment(_ context.Context, _ *store.Repository, _ *store.PullRequest, pos vcs.CommentPosition, _ string) (string, error) {
	return s.recordInline(pos), nil
}
func (s *stubVCS) PostSummary(_ context.Context, _ *store.Repository, _ *store.PullRequest, body, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summaries = append(s.summaries, body)
	return fmt.Sprintf("summary-%d", len(s.summaries)), nil
}

func (s *stubVCS) recordInline(pos vcs.CommentPosition) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inlines = append(s.inlines, pos)
	return fmt.Sprintf("inline-%d", len(s.inlines))
}

func (s *stubVCS) snapshot() (summaries []string, inlines []vcs.CommentPosition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.summaries...), append([]vcs.CommentPosition(nil), s.inlines...)
}

// stubAnalyzer reemplaza al sandbox de podman: SAST sin hallazgos y con
// declaración de que el linter corrió (§9.4 — la corrida queda success).
type stubAnalyzer struct{}

func (stubAnalyzer) Run(context.Context, string) (*analyze.AnalysisResult, error) {
	return &analyze.AnalysisResult{
		FilesAnalyzed: 1,
		LintersRun:    []analyze.LinterStatus{{Name: "stub", Status: "ok"}},
		Findings:      []analyze.Finding{},
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers de entorno
// ---------------------------------------------------------------------------

// testStore abre la store contra DATABASE_URL y aplica migraciones si faltan.
func testStore(t *testing.T) *store.Store {
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
	return st
}

// newTempClone arma el "clon shallow" del job: repo git mínimo con un
// commit; devuelve el workdir y el SHA real de head.
func newTempClone(t *testing.T) (dir, headSHA string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// -c commit.gpgsign=false: la config global del desarrollador no
		// puede romper el commit del test.
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("init")
	file := filepath.Join(dir, "main.go")
	contenido := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hola\")\n}\n"
	if err := os.WriteFile(file, []byte(contenido), 0o644); err != nil {
		t.Fatalf("escribiendo main.go del clon: %v", err)
	}
	run("add", ".")
	run("-c", "commit.gpgsign=false", "-c", "user.name=integration", "-c", "user.email=integration@test",
		"commit", "-m", "commit inicial del clon de test")
	return dir, strings.TrimSpace(run("rev-parse", "HEAD"))
}

// deliverySeq garantiza delivery IDs únicos entre entregas y corridas.
var deliverySeq atomic.Int64

// prPayload arma el payload realista de un evento pull_request de GitHub.
func prPayload(repoExternalID, number int64, action, head, base, state string) map[string]any {
	return map[string]any{
		"action": action,
		"number": number,
		"pull_request": map[string]any{
			"number":     number,
			"state":      state,
			"draft":      false,
			"created_at": "2026-01-15T10:00:00Z",
			"user":       map[string]any{"login": "dev-integration", "type": "User"},
			"head":       map[string]any{"ref": "feature/integration", "sha": head},
			"base":       map[string]any{"ref": "main", "sha": base},
		},
		"repository": map[string]any{
			"id":        repoExternalID,
			"name":      "repo",
			"full_name": "test/repo",
			"owner":     map[string]any{"login": "test", "type": "Organization"},
			"private":   true,
		},
		"sender": map[string]any{"login": "dev-integration", "type": "User"},
	}
}

// postWebhook firma el payload con HMAC-SHA256 del body crudo (§9.3) y lo
// manda por HTTP real al server de la API. Registra el delivery ID para la
// limpieza.
func postWebhook(t *testing.T, serverURL, event string, payload map[string]any, deliveries *[]string) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("serializando payload: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(testWebhookSecret))
	mac.Write(body)
	delivery := fmt.Sprintf("integration-%d-%d", time.Now().UnixNano(), deliverySeq.Add(1))
	*deliveries = append(*deliveries, delivery)

	req, err := http.NewRequest(http.MethodPost, serverURL+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("armado del POST: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/github: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// cleanupRows borra las filas del test en orden hoja → raíz (las FKs del
// schema no tienen ON DELETE CASCADE).
func cleanupRows(t *testing.T, st *store.Store, repoID, providerID int64, stubURL string, deliveries []string) {
	t.Helper()
	ctx := context.Background()
	for _, paso := range []struct {
		sql  string
		args []any
	}{
		{"DELETE FROM llm_usage WHERE provider = $1", []any{stubURL}},
		{"DELETE FROM llm_providers WHERE id = $1", []any{providerID}},
		{`DELETE FROM findings WHERE review_id IN (
			SELECT id FROM reviews WHERE pull_request_id IN
			(SELECT id FROM pull_requests WHERE repository_id = $1))`, []any{repoID}},
		{"DELETE FROM comments_sent WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE repository_id = $1)", []any{repoID}},
		{"DELETE FROM reviews WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE repository_id = $1)", []any{repoID}},
		{"DELETE FROM pull_requests WHERE repository_id = $1", []any{repoID}},
		{"DELETE FROM webhook_deliveries WHERE vcs = 'github' AND delivery_id = ANY($1)", []any{deliveries}},
		{"DELETE FROM repositories WHERE id = $1", []any{repoID}},
	} {
		if _, err := st.Pool.Exec(ctx, paso.sql, paso.args...); err != nil {
			t.Errorf("limpieza: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Test E2E
// ---------------------------------------------------------------------------

// TestGitHubWebhookToReview recorre el pipeline completo: webhook firmado de
// apertura → upsert + enqueue; ejecución del pipeline de review contra el
// LLM stub; push (synchronize) con re-encolado; cierre sin job nuevo.
func TestGitHubWebhookToReview(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	// -- Setup: repo conectado + proveedor LLM stub + cadena de dependencias.
	masterKey := make([]byte, 32) // clave AES-256 de test
	cfg := &config.Config{
		MasterKey: masterKey,
		Stage2:    config.Stage2Config{GitHubWebhookSecret: testWebhookSecret},
	}

	now := time.Now().UnixNano()
	repo, err := st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs:        "github",
		ExternalID: now,
		Owner:      "test",
		Name:       fmt.Sprintf("repo-%d", now),
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	llmSrv := stubLLM(t, reviewerOut, summarizerOut)
	encKey, err := store.Encrypt(masterKey, []byte("sk-stub-integration"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	provider, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl:  llmSrv.URL,
		Model:    "stub-model",
		ApiKey:   encKey,
		Role:     "review",
		Priority: 1,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider: %v", err)
	}

	// Cadena completa: cola capturadora → adapter GitHub → API con el mux
	// real (logging + recover + POST /webhooks/github). El wrapper es el
	// mismo que arma cmd/api: HandleWebhook cuelga del adapter, no de
	// http.Handler.
	q := newCapturedQueue()
	ghAdapter := vcsgh.New(st, cfg, q)
	githubWebhook := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghAdapter.HandleWebhook(r.Context(), w, r)
	})
	apiSrv := httptest.NewServer(api.New(st, cfg, githubWebhook, q, nil).Routes())
	t.Cleanup(apiSrv.Close)

	var deliveries []string
	t.Cleanup(func() { cleanupRows(t, st, repo.ID, provider.ID, llmSrv.URL, deliveries) })

	// El "clon" del job existe antes del webhook: el head del payload es su
	// SHA real — la identidad de la corrida (§3.6.1) queda coherente.
	workdir, headSHA := newTempClone(t)

	// -- 1. Webhook pull_request opened --------------------------------------
	resp := postWebhook(t, apiSrv.URL, "pull_request",
		prPayload(repo.ExternalID, prNumber, "opened", headSHA, fakeBase, "open"), &deliveries)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("opened: la API debe responder 2xx, fue %d", resp.StatusCode)
	}

	// Fila del PR upsertada con los datos del payload.
	pr, err := st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: prNumber,
	})
	if err != nil {
		t.Fatalf("el PR debe upsertearse con el webhook: %v", err)
	}
	if pr.Author != "dev-integration" || pr.State != "open" ||
		pr.HeadSha != headSHA || pr.BaseRef != "main" || pr.BaseSha != fakeBase {
		t.Errorf("fila del PR incorrecta: %+v", pr)
	}

	// Delivery registrada (dedup de re-entregas, §9.3).
	exists, err := st.WebhookDeliveryExists(ctx, store.WebhookDeliveryExistsParams{
		Vcs: "github", DeliveryID: deliveries[0],
	})
	if err != nil || !exists {
		t.Errorf("la delivery debe quedar registrada: exists=%v err=%v", exists, err)
	}

	// ReviewJob encolado con la identidad del evento.
	jobs1 := q.pending()
	if len(jobs1) != 1 {
		t.Fatalf("opened debe encolar 1 job, encoló %d", len(jobs1))
	}
	if jobs1[0].Kind != jobs.KindReview {
		t.Fatalf("kind: got %q want %q", jobs1[0].Kind, jobs.KindReview)
	}
	var jobArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(jobs1[0].Args, &jobArgs); err != nil {
		t.Fatalf("args del ReviewJob: %v", err)
	}
	if jobArgs.PullRequestID != pr.ID || jobArgs.RepositoryID != repo.ID ||
		jobArgs.HeadSha != headSHA || jobArgs.BaseSha != fakeBase {
		t.Errorf("args del ReviewJob incorrectos: %+v", jobArgs)
	}

	// -- 2. Ejecución del job: el pipeline corre directo (sin worker) --------
	gateway := llm.New(st, masterKey, llm.Limits{
		MaxPerReview: 2, MaxGlobal: 2, Timeout: 5 * time.Second, MaxRetries: 1,
	})
	vcsStub := &stubVCS{diff: diffFixture}

	res, err := review.Run(ctx, review.DefaultConfig(), st, gateway, stubAnalyzer{}, vcsStub,
		review.ReviewInput{
			PullRequestID: pr.ID,
			RepositoryID:  repo.ID,
			HeadSHA:       headSHA,
			BaseSHA:       fakeBase,
			Workdir:       workdir,
		})
	if err != nil {
		t.Fatalf("review.Run: %v", err)
	}

	// -- 3. Estado del pipeline en BD -----------------------------------------
	if res.Status != review.StatusSuccess {
		summaries, _ := vcsStub.snapshot()
		t.Fatalf("status = %q, querés %q (resúmenes publicados: %q)", res.Status, review.StatusSuccess, summaries)
	}
	if res.FindingsCount != 1 {
		t.Errorf("FindingsCount = %d, querés 1 (1 LLM + 0 SAST)", res.FindingsCount)
	}

	rev, err := st.GetLatestReviewByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("la review debe existir: %v", err)
	}
	if rev.Status != review.StatusSuccess {
		t.Errorf("reviews.status = %q, querés success", rev.Status)
	}
	if rev.Summary != summaryTxt {
		t.Errorf("reviews.summary = %q, querés %q", rev.Summary, summaryTxt)
	}
	if rev.Mermaid == "" {
		t.Error("reviews.mermaid vacío: el stub del summarizer trae diagrama válido")
	}

	findings, err := st.ListFindingsByReview(ctx, rev.ID)
	if err != nil {
		t.Fatalf("ListFindingsByReview: %v", err)
	}
	if len(findings) != 1 || findings[0].Source != review.SourceLLM ||
		findings[0].File != "main.go" || findings[0].Line != 3 {
		t.Errorf("findings de la corrida incorrectos: %+v", findings)
	}

	comments, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "summary",
	})
	if err != nil {
		t.Fatalf("GetCommentsSentByPRAndType: %v", err)
	}
	if len(comments) != 1 || comments[0].CommentID == "" {
		t.Errorf("comments_sent debe tener 1 fila summary con comment_id: %+v", comments)
	}

	// Publicación en dos fases (§3.6.2): provisional + re-edición final, y
	// el inline ancla a main.go:3 RIGHT.
	summaries, inlines := vcsStub.snapshot()
	if len(summaries) != 2 {
		t.Errorf("PostSummary llamado %d veces, querés 2 (dos fases)", len(summaries))
	}
	if len(inlines) != 1 || inlines[0].File != "main.go" || inlines[0].Line != 3 || inlines[0].Side != vcs.SideRight {
		t.Errorf("inline incorrecto: %+v, querés main.go:3 RIGHT", inlines)
	}

	// -- 4. Webhook synchronize (push): misma fila, head nuevo, re-encolado --
	resp = postWebhook(t, apiSrv.URL, "pull_request",
		prPayload(repo.ExternalID, prNumber, "synchronize", syncHead, fakeBase, "open"), &deliveries)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("synchronize: la API debe responder 2xx, fue %d", resp.StatusCode)
	}

	jobs2 := q.pending() // pending() drena: el conteo es incremental por etapa
	if len(jobs2) != 1 {
		t.Fatalf("synchronize debe encolar la corrida nueva: encoló %d", len(jobs2))
	}
	var syncArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(jobs2[0].Args, &syncArgs); err != nil {
		t.Fatalf("args del segundo ReviewJob: %v", err)
	}
	if syncArgs.HeadSha != syncHead || syncArgs.PullRequestID != pr.ID {
		t.Errorf("el synchronize debe traer el head nuevo y el mismo PR: %+v", syncArgs)
	}
	pr, err = st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: prNumber,
	})
	if err != nil || pr.HeadSha != syncHead {
		t.Errorf("la fila del PR debe reflejar el head nuevo: %+v err=%v", pr, err)
	}

	// -- 5. Webhook closed: estado actualizado, SIN job nuevo -----------------
	resp = postWebhook(t, apiSrv.URL, "pull_request",
		prPayload(repo.ExternalID, prNumber, "closed", syncHead, fakeBase, "closed"), &deliveries)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("closed: la API debe responder 2xx, fue %d", resp.StatusCode)
	}

	if jobs := q.pending(); len(jobs) != 0 {
		t.Errorf("closed no debe encolar (el MetricsJob llega en F5): encoló %d", len(jobs))
	}
	pr, err = st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: prNumber,
	})
	if err != nil || pr.State != "closed" {
		t.Errorf("el cierre debe dejar state=closed: %+v err=%v", pr, err)
	}
	for _, d := range deliveries {
		exists, err := st.WebhookDeliveryExists(ctx, store.WebhookDeliveryExistsParams{
			Vcs: "github", DeliveryID: d,
		})
		if err != nil || !exists {
			t.Errorf("delivery %q debe estar registrada: exists=%v err=%v", d, exists, err)
		}
	}
}
