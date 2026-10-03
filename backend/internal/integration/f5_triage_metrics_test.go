//go:build integration

// Test de integración F5 e2e (mapa: backend.internal/integration — F5): tres
// cadenas contra la store real. 1) Cierre del PR: webhook firmado `closed`
// (merge) por el Server real → MetricsJob encolado → el worker corre directo
// contra el VCS stub (threads + patches con fechas) y escribe
// resolved/applied/accepted sobre las filas existentes, sin LLM. 2) Veredicto
// pre-merge: el stub LLM responde el marcador del agente → la re-edición
// final del resumen trae la sección y pull_requests.risk_score queda
// persistido. 3) GET /api/metrics con sesión member: las tasas reflejan
// exactamente el delta sembrado (patrón baseline-delta contra BD compartida).
// Nada toca internet: LLM y VCS son stubs, analyzer es stub. Requiere
// DATABASE_URL — sin ella el test se salta, igual que el resto.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Andres39128/codeowl/backend/internal/api"
	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
	vcsgh "github.com/Andres39128/codeowl/backend/internal/vcs/github"
)

// f5PRNumber es el número del PR de F5 (repo propio: sin colisión con los
// PRs de los demás tests de integración).
const f5PRNumber = int64(501)

// f5PremergeMarker es el prefijo del prompt de usuario del agente Pre-merge
// (espejo de review/agents.go: los stubs enrutan por él, mismo criterio que
// reviewerMarker/summarizerMarker).
const f5PremergeMarker = "Evaluá el veredicto pre-merge del siguiente pull request."

// Salidas del stub LLM para la corrida F5: DOS hallazgos con sugerencia
// (main.go:3 logic y main.go:5 style — ambas líneas anclables al diff
// fixture) y el veredicto del Pre-merge en el conjunto cerrado (§9.8).
const f5ReviewerOut = `[` +
	`{"line":3,"severity":"medium","category":"logic","body":"la constante del saludo queda sin declarar.","suggestion":"const saludo = \"hola mundo aplicado\""},` +
	`{"line":5,"severity":"low","category":"style","body":"main no despide al usuario.","suggestion":"fmt.Println(\"chau\")"}]`

// f5PremergeOut es la salida JSON válida del Pre-merge: veredicto del
// conjunto cerrado + resumen no vacío + checklist dentro del tope.
const f5PremergeOut = `{"verdict":"apto_con_observaciones",` +
	`"checklist":[{"item":"Sin hallazgos de alta severidad","ok":true},{"item":"Diff pequeño sin rutas sensibles","ok":true}],` +
	`"resumen":"El cambio es acotado y con hallazgos menores: apto con observaciones."}`

// Sugerencias como constantes para armar los patches del timeline con el
// MISMO texto exacto que publicó el pipeline (la heurística compara
// contenido normalizado, §6 F5).
const (
	f5SugerenciaAplicada   = `const saludo = "hola mundo aplicado"`
	f5SugerenciaNoAplicada = `fmt.Println("chau")`
)

// f5StubLLM arma el stub OpenAI-compatible de F5: enruta por los tres
// marcadores de agente del rol review (Reviewer/Summarizer/Pre-merge). Sin
// proveedor del rol cheap el Verifier no corre (degrada, §9.6).
type f5StubLLM struct {
	srv *httptest.Server
}

func newF5StubLLM(t *testing.T, reviewer, summarizer, premerge string) *f5StubLLM {
	t.Helper()
	s := &f5StubLLM{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		case strings.HasPrefix(last, f5PremergeMarker):
			content = premerge
		default:
			http.Error(w, "prompt de agente desconocido", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`, content)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// f5TimelineVCS envuelve al stubVCS del pipeline con una FetchPRTimeline que
// devuelve el fixture de la corrida: el MetricsJob consume ESTE contrato.
type f5TimelineVCS struct {
	*stubVCS
	timeline vcs.PRTimeline
}

func (s *f5TimelineVCS) FetchPRTimeline(context.Context, *store.Repository, *store.PullRequest) (*vcs.PRTimeline, error) {
	tl := s.timeline
	return &tl, nil
}

// f5SeedRun arma el entorno común de las corridas F5 (repo conectado +
// proveedor del rol review sobre el stub + cola capturadora + Server real
// con el adapter GitHub montado). Devuelve también el puntero de deliveries
// que hay que pasarle a postWebhook: el cleanup lo lee al final, ya lleno.
func f5SeedRun(t *testing.T, st *store.Store, stubURL string) (store.Repository, *capturedQueue, string, *[]string) {
	t.Helper()
	ctx := context.Background()
	masterKey := make([]byte, 32) // clave AES-256 de test
	cfg := &config.Config{
		MasterKey: masterKey,
		Stage2:    config.Stage2Config{GitHubWebhookSecret: testWebhookSecret},
	}

	now := time.Now().UnixNano()
	repo, err := st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "github", ExternalID: now, Owner: "test", Name: fmt.Sprintf("repo-f5-%d", now),
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	encKey, err := store.Encrypt(masterKey, []byte("sk-stub-integration"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	provider, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl: stubURL, Model: "stub-model", ApiKey: encKey,
		Role: "review", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider: %v", err)
	}

	// La limpieza lee deliveries al FINAL (closure sobre el puntero): las
	// entregas registradas después del Cleanup ya están incluidas.
	deliveries := &[]string{}
	t.Cleanup(func() { cleanupRows(t, st, repo.ID, provider.ID, stubURL, *deliveries) })

	// Cadena completa con la cola capturadora (la de pipeline_test: graba los
	// encolados; su CancelPendingByPR es no-op — el descarte real vive en el
	// RiverQueue y tiene tests propios).
	q := newCapturedQueue()
	ghAdapter := vcsgh.New(st, cfg, q)
	apiSrv := httptest.NewServer(api.New(st, cfg, ghAdapter, nil, q, nil).Routes())
	t.Cleanup(apiSrv.Close)
	return repo, q, apiSrv.URL, deliveries
}

// f5MetricsView es el espejo local de api.metricsView para decodificar la
// respuesta de GET /api/metrics (los json tags son el contrato público).
type f5MetricsView struct {
	MergedPrs           int64    `json:"merged_prs"`
	AvgCycleTimeHours   *float64 `json:"avg_cycle_time_hours"`
	FindingsWithOutcome int64    `json:"findings_with_outcome"`
	Accepted            int64    `json:"accepted"`
	AcceptedRate        *float64 `json:"accepted_rate"`
	ResolvedComments    int64    `json:"resolved_comments"`
	FalsePositives      int64    `json:"false_positives"`
	FalsePositiveRate   *float64 `json:"false_positive_rate"`
	ReviewsWithCost     int64    `json:"reviews_with_cost"`
	AvgTokensPerReview  *float64 `json:"avg_tokens_per_review"`
}

// f5GetMetrics pide GET /api/metrics con la cookie de sesión dada (vacía →
// sin cookie: request anónimo). Devuelve el status y el cuerpo decodificado.
func f5GetMetrics(t *testing.T, serverURL, sessionToken string) (int, f5MetricsView) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, serverURL+"/api/metrics", nil)
	if err != nil {
		t.Fatalf("armado del GET /api/metrics: %v", err)
	}
	if sessionToken != "" {
		req.AddCookie(&http.Cookie{Name: "codeowl_session", Value: sessionToken})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var view f5MetricsView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decodificando /api/metrics (status %d): %v", resp.StatusCode, err)
	}
	return resp.StatusCode, view
}

// TestF5CierreMetricsOutcome (cierre e2e, §6 F5): apertura + corrida de
// revisión real (2 hallazgos con sugerencia publicados inline) → webhook
// `closed` (merge) por el Server real encola SOLO el MetricsJob con los args
// del PR y deja state=closed+merged_at → el worker corre directo contra el
// VCS stub y escribe el outcome: resolved por comment_id (hilo ajeno
// ignorado), applied por contenido en commit POSTERIOR (el commit anterior
// al comentario no cuenta) y accepted como espejo en el finding.
func TestF5CierreMetricsOutcome(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	llmStub := newF5StubLLM(t, f5ReviewerOut, summarizerOut, f5PremergeOut)
	repo, q, apiURL, deliveries := f5SeedRun(t, st, llmStub.srv.URL)

	// El clon existe antes del webhook: el head del payload es su SHA real.
	workdir, headSHA := newTempClone(t)

	// -- 1. Apertura + corrida de revisión: 2 hallazgos publicados inline ---
	resp := postWebhook(t, apiURL, "pull_request",
		prPayload(repo.ExternalID, f5PRNumber, "opened", headSHA, fakeBase, "open"), deliveries)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("opened: la API debe responder 2xx, fue %d", resp.StatusCode)
	}
	pr, err := st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: f5PRNumber,
	})
	if err != nil {
		t.Fatalf("el PR debe upsertearse con el webhook: %v", err)
	}

	// La corrida usa el review.Run real (la cola capturadora grabó el job de
	// apertura; drenar para que el conteo del cierre sea exacto).
	_ = q.pending()
	gateway := llm.New(st, make([]byte, 32), llm.Limits{
		// 3 llamadas por review (reviewer + summarizer + premerge): el tope
		// default de 2 dejaría el pre-merge sin slot (§9.6).
		MaxPerReview: 6, MaxGlobal: 6, Timeout: 10 * time.Second, MaxRetries: 1,
	})
	vcsStub := &stubVCS{diff: diffFixture}
	res, err := review.Run(ctx, review.DefaultConfig(), st, gateway, stubAnalyzer{}, vcsStub, nil,
		review.ReviewInput{
			PullRequestID: pr.ID, RepositoryID: repo.ID,
			HeadSHA: headSHA, BaseSHA: fakeBase, Workdir: workdir,
		})
	if err != nil {
		t.Fatalf("review.Run: %v", err)
	}
	if res.Status != review.StatusSuccess || res.FindingsCount != 2 {
		summaries, _ := vcsStub.snapshot()
		t.Fatalf("corrida: querés success con 2 hallazgos, fue %+v (resúmenes: %q)", res, summaries)
	}
	rev, err := st.GetLatestReviewByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("la review debe existir: %v", err)
	}
	if findings, err := st.ListFindingsByReview(ctx, rev.ID); err != nil || len(findings) != 2 {
		t.Fatalf("querés 2 findings persistidos, hay %d (err=%v)", len(findings), err)
	}

	// Inlines publicados en orden del conjunto (main.go:3 logic → inline-1,
	// main.go:5 style → inline-2). Antes del cierre: outcome TODO null.
	inlines, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "inline",
	})
	if err != nil || len(inlines) != 2 {
		t.Fatalf("querés 2 comentarios inline, hay %d (err=%v)", len(inlines), err)
	}
	porCommentID := map[string]bool{}
	for _, cs := range inlines {
		if cs.Resolved.Valid || cs.Applied.Valid {
			t.Errorf("antes del cierre el outcome debe ser null: %+v", cs)
		}
		porCommentID[cs.CommentID] = true
	}
	if !porCommentID["inline-1"] || !porCommentID["inline-2"] {
		t.Fatalf("los comment_ids del stub deben ser inline-1 e inline-2: %v", inlines)
	}

	// -- 2. Webhook closed (merge): state + merged_at + SOLO el MetricsJob --
	payload := prPayload(repo.ExternalID, f5PRNumber, "closed", headSHA, fakeBase, "closed")
	payload["pull_request"].(map[string]any)["merged"] = true
	payload["pull_request"].(map[string]any)["merged_at"] = time.Now().UTC().Format(time.RFC3339)
	resp = postWebhook(t, apiURL, "pull_request", payload, deliveries)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("closed: la API debe responder 2xx, fue %d", resp.StatusCode)
	}
	cierre := q.pending()
	if len(cierre) != 1 || cierre[0].Kind != jobs.KindMetrics {
		t.Fatalf("el cierre debe encolar solo el MetricsJob (sin proveedor embedding no va IndexJob): got %+v", cierre)
	}
	var metricsArgs jobs.MetricsJobArgs
	if err := json.Unmarshal(cierre[0].Args, &metricsArgs); err != nil {
		t.Fatalf("args del MetricsJob: %v", err)
	}
	if metricsArgs.RepositoryID != repo.ID || metricsArgs.PullRequestID != pr.ID {
		t.Errorf("el MetricsJob debe apuntar al repo y PR cerrados: %+v", metricsArgs)
	}
	pr, err = st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: f5PRNumber,
	})
	if err != nil || pr.State != "closed" || !pr.MergedAt.Valid {
		t.Errorf("el cierre merge debe dejar state=closed y merged_at: %+v err=%v", pr, err)
	}

	// -- 3. MetricsJobWorker directo contra el VCS stub con el fixture -------
	// Threads: inline-1 resuelto, inline-2 sin resolver y un hilo AJENO (no
	// es comentario del bot: se ignora, §6 F5). Commits: uno ANTERIOR al
	// comentario con la sugerencia del finding 2 (no cuenta: la fecha manda)
	// y uno POSTERIOR con la sugerencia del finding 1 (aplicada).
	vcsMetrics := &f5TimelineVCS{
		stubVCS: vcsStub,
		timeline: vcs.PRTimeline{
			Threads: []vcs.PRThread{
				{CommentID: "inline-1", Resolved: true},
				{CommentID: "inline-2", Resolved: false},
				{CommentID: "thread-ajeno-999", Resolved: true},
			},
			Commits: []vcs.PRCommit{
				{
					SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Author: "dev",
					AuthoredAt: inlines[0].CreatedAt.Time.Add(-time.Hour), // anterior al comentario
					Patch:      "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -6,2 +6,3@\n }\n+" + f5SugerenciaNoAplicada + "\n",
				},
				{
					SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Author: "dev",
					AuthoredAt: inlines[0].CreatedAt.Time.Add(time.Minute), // posterior al comentario
					Patch:      "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -3,2 +3,3@\n import \"fmt\"\n+" + f5SugerenciaAplicada + "\n",
				},
			},
		},
	}
	worker := &jobs.MetricsJobWorker{Store: st, Provider: vcsMetrics}
	if err := worker.Work(ctx, &river.Job[jobs.MetricsJobArgs]{Args: metricsArgs}); err != nil {
		t.Fatalf("MetricsJobWorker.Work: %v", err)
	}

	// -- 4. Outcome en BD ----------------------------------------------------
	inlines, err = st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "inline",
	})
	if err != nil || len(inlines) != 2 {
		t.Fatalf("post-worker: querés 2 inline, hay %d (err=%v)", len(inlines), err)
	}
	quedo := map[string]store.CommentsSent{}
	for _, cs := range inlines {
		quedo[cs.CommentID] = cs
	}
	cs1, ok := quedo["inline-1"]
	if !ok || !cs1.Resolved.Valid || !cs1.Resolved.Bool || !cs1.Applied.Valid || !cs1.Applied.Bool {
		t.Errorf("inline-1: querés resolved=true y applied=true, got %+v", cs1)
	}
	cs2, ok := quedo["inline-2"]
	if !ok || !cs2.Resolved.Valid || cs2.Resolved.Bool || !cs2.Applied.Valid || cs2.Applied.Bool {
		t.Errorf("inline-2: querés resolved=false y applied=false, got %+v", cs2)
	}

	// accepted como espejo de applied, POR finding (línea 3 → aplicada;
	// línea 5 → no: su sugerencia solo está en el commit ANTERIOR al
	// comentario, y sin fecha posterior no hay applied, §6 F5).
	finales, err := st.ListFindingsByReview(ctx, rev.ID)
	if err != nil || len(finales) != 2 {
		t.Fatalf("ListFindingsByReview post-worker: %d findings (err=%v)", len(finales), err)
	}
	aceptadoPorLinea := map[int32]bool{}
	for _, f := range finales {
		if !f.Accepted.Valid {
			t.Errorf("finding %s:%d debe quedar evaluado (accepted no null), got %+v", f.File, f.Line, f)
		}
		aceptadoPorLinea[f.Line] = f.Accepted.Bool
	}
	if !aceptadoPorLinea[3] {
		t.Errorf("finding línea 3: querés accepted=true, got %v", aceptadoPorLinea[3])
	}
	if aceptadoPorLinea[5] {
		t.Errorf("finding línea 5: querés accepted=false (su sugerencia solo está en un commit anterior), got %v", aceptadoPorLinea[5])
	}

	// El hilo ajeno no escribe filas: siguen siendo exactamente 2 inline y
	// el resumen (type summary) queda sin outcome (no tiene thread).
	summaries, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "summary",
	})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("querés 1 resumen, hay %d (err=%v)", len(summaries), err)
	}
	if summaries[0].Resolved.Valid || summaries[0].Applied.Valid {
		t.Errorf("el resumen no participa del outcome de threads: %+v", summaries[0])
	}
}

// TestF5VeredictoPreMerge (§6 F5, T4 e2e): el stub LLM responde el marcador
// del Pre-merge → la re-edición FINAL del resumen trae la sección con el
// veredicto del conjunto cerrado + su resumen, y el risk score computado por
// el pipeline queda persistido en pull_requests.risk_score (no null).
func TestF5VeredictoPreMerge(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	veredicto := "apto"
	premergeOut := `{"verdict":"` + veredicto + `",` +
		`"checklist":[{"item":"Un solo archivo, diff de 4 líneas","ok":true}],` +
		`"resumen":"Cambio mínimo con hallazgos menores: listo para fusionar."}`
	llmStub := newF5StubLLM(t, f5ReviewerOut, summarizerOut, premergeOut)
	repo, _, _, _ := f5SeedRun(t, st, llmStub.srv.URL)

	workdir, headSHA := newTempClone(t)
	pr, err := st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: f5PRNumber + 1,
		Author: "dev-integration", State: "open",
		HeadSha: headSHA, BaseRef: "main", BaseSha: fakeBase,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}

	gateway := llm.New(st, make([]byte, 32), llm.Limits{
		MaxPerReview: 6, MaxGlobal: 6, Timeout: 10 * time.Second, MaxRetries: 1,
	})
	vcsStub := &stubVCS{diff: diffFixture}
	res, err := review.Run(ctx, review.DefaultConfig(), st, gateway, stubAnalyzer{}, vcsStub, nil,
		review.ReviewInput{
			PullRequestID: pr.ID, RepositoryID: repo.ID,
			HeadSHA: headSHA, BaseSHA: fakeBase, Workdir: workdir,
		})
	if err != nil {
		t.Fatalf("review.Run: %v", err)
	}
	if res.Status != review.StatusSuccess {
		t.Fatalf("corrida: querés success, fue %+v", res)
	}

	// Publicación en dos fases: el veredicto vive en la re-edición FINAL
	// (edit in place, §3.6) — nunca en la provisional ni como action del VCS.
	summaries, inlines := vcsStub.snapshot()
	if len(summaries) != 2 {
		t.Fatalf("PostSummary debe correr 2 veces (dos fases), corrió %d", len(summaries))
	}
	final := summaries[len(summaries)-1]
	seccion := "### Veredicto pre-merge: " + veredicto
	if !strings.Contains(final, seccion) {
		t.Errorf("el resumen final debe traer la sección %q: %q", seccion, final)
	}
	if !strings.Contains(final, "listo para fusionar") {
		t.Errorf("el resumen final debe traer el resumen del veredicto: %q", final)
	}
	if len(inlines) != 2 {
		t.Errorf("el veredicto no cambia la publicación inline: querés 2, hay %d", len(inlines))
	}

	// Risk score persistido (§6 F5, T3): no null y dentro del rango 0-100
	// (la fórmula exacta vive en review/risk_test.go — acá importa el e2e).
	pr, err = st.GetPullRequest(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if !pr.RiskScore.Valid {
		t.Fatalf("pull_requests.risk_score debe quedar persistido tras la corrida")
	}
	if pr.RiskScore.Int32 < 0 || pr.RiskScore.Int32 > 100 {
		t.Errorf("risk_score fuera del rango 0-100: %d", pr.RiskScore.Int32)
	}
}

// TestF5MetricsEndpoint (§6 F5, T7 e2e): con sesión member, GET /api/metrics
// refleja EXACTAMENTE el delta sembrado (baseline-delta: la BD es compartida
// entre tests y corridas) — un PR merged con cycle time de 1h, dos findings
// evaluados (1 aceptado), un inline resuelto y NO aplicado (falso positivo)
// y un usage con review_id (costo por review).
func TestF5MetricsEndpoint(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	// API real sin adapters (solo lectura de métricas: no monta webhooks).
	apiSrv := httptest.NewServer(api.New(st, &config.Config{}, nil, nil, nil, nil).Routes())
	t.Cleanup(apiSrv.Close)

	// Sesión member directa en BD (§3.4): token → hash → fila + cookie.
	passHash, err := store.HashPassword("clave-integration-f5")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user, err := st.CreateUser(ctx, store.CreateUserParams{
		Username: fmt.Sprintf("member-f5-%d", time.Now().UnixNano()),
		Role:     "member", PasswordHash: passHash,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token, err := store.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if _, err := st.CreateSession(ctx, store.CreateSessionParams{
		TokenHash: store.HashSessionToken(token), UserID: user.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() {
		for _, paso := range []struct {
			sql  string
			args []any
		}{
			{"DELETE FROM sessions WHERE user_id = $1", []any{user.ID}},
			{"DELETE FROM users WHERE id = $1", []any{user.ID}},
		} {
			if _, err := st.Pool.Exec(ctx, paso.sql, paso.args...); err != nil {
				t.Errorf("limpieza del usuario de métricas: %v", err)
			}
		}
	})

	// Sin cookie el endpoint es 401 (auth §3.4) — sanity del guard.
	if status, _ := f5GetMetrics(t, apiSrv.URL, ""); status != http.StatusUnauthorized {
		t.Errorf("sin sesión: querés 401, fue %d", status)
	}

	status, before := f5GetMetrics(t, apiSrv.URL, token)
	if status != http.StatusOK {
		t.Fatalf("con sesión member: querés 200, fue %d", status)
	}

	// -- Delta sembrado (una unidad exacta por métrica) ----------------------
	now := time.Now().UnixNano()
	repo, err := st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "github", ExternalID: now, Owner: "test", Name: fmt.Sprintf("repo-f5-metrics-%d", now),
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	pr, err := st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: f5PRNumber + 2,
		Author: "dev-integration", State: "closed",
		HeadSha: "f5metrics0000000000000000000000000000000a", BaseRef: "main", BaseSha: fakeBase,
		// Cycle time exacto de 1 h: created_at → merged_at.
		CreatedAt: pgtype.Timestamptz{Time: time.Now().Add(-2 * time.Hour), Valid: true},
		MergedAt:  pgtype.Timestamptz{Time: time.Now().Add(-1 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}
	rev, err := st.CreateReview(ctx, store.CreateReviewParams{
		PullRequestID: pr.ID,
		HeadSha:       "f5metrics0000000000000000000000000000000a", BaseSha: fakeBase,
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	for i, aceptado := range []bool{true, false} {
		f, err := st.CreateFinding(ctx, store.CreateFindingParams{
			ReviewID: rev.ID, File: "main.go", Line: int32(i + 1),
			Severity: "medium", Category: "logic",
			Body: "hallazgo de métricas F5", Source: "llm",
		})
		if err != nil {
			t.Fatalf("CreateFinding %d: %v", i, err)
		}
		if err := st.UpdateFindingAccepted(ctx, store.UpdateFindingAcceptedParams{
			ID: f.ID, Accepted: pgtype.Bool{Bool: aceptado, Valid: true},
		}); err != nil {
			t.Fatalf("UpdateFindingAccepted %d: %v", i, err)
		}
	}
	cs, err := st.CreateCommentSent(ctx, store.CreateCommentSentParams{
		PullRequestID: pr.ID, ReviewID: pgtype.Int8{Int64: rev.ID, Valid: true},
		CommentID: fmt.Sprintf("f5-metrics-%d", now), Type: "inline",
		File:     pgtype.Text{String: "main.go", Valid: true},
		Category: pgtype.Text{String: "logic", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateCommentSent: %v", err)
	}
	// Falso positivo (§6 F5): resuelto pero NO aplicado.
	if err := st.UpdateCommentsSentResolved(ctx, store.UpdateCommentsSentResolvedParams{
		ID: cs.ID, Resolved: pgtype.Bool{Bool: true, Valid: true},
	}); err != nil {
		t.Fatalf("UpdateCommentsSentResolved: %v", err)
	}
	if err := st.UpdateCommentsSentApplied(ctx, store.UpdateCommentsSentAppliedParams{
		ID: cs.ID, Applied: pgtype.Bool{Bool: false, Valid: true},
	}); err != nil {
		t.Fatalf("UpdateCommentsSentApplied: %v", err)
	}
	if _, err := st.CreateLlmUsage(ctx, store.CreateLlmUsageParams{
		ReviewID: pgtype.Int8{Int64: rev.ID, Valid: true},
		Role:     "review", Provider: "stub-f5-metrics", Model: "stub-model",
		TokensIn: 100, TokensOut: 50,
	}); err != nil {
		t.Fatalf("CreateLlmUsage: %v", err)
	}
	t.Cleanup(func() {
		for _, paso := range []struct {
			sql  string
			args []any
		}{
			{"DELETE FROM llm_usage WHERE review_id = $1", []any{rev.ID}},
			{`DELETE FROM comments_sent WHERE pull_request_id = $1`, []any{pr.ID}},
			{"DELETE FROM findings WHERE review_id = $1", []any{rev.ID}},
			{"DELETE FROM reviews WHERE id = $1", []any{rev.ID}},
			{"DELETE FROM pull_requests WHERE id = $1", []any{pr.ID}},
			{"DELETE FROM repositories WHERE id = $1", []any{repo.ID}},
		} {
			if _, err := st.Pool.Exec(ctx, paso.sql, paso.args...); err != nil {
				t.Errorf("limpieza del delta de métricas: %v", err)
			}
		}
	})

	// -- El delta exacto sobre el baseline -----------------------------------
	status, after := f5GetMetrics(t, apiSrv.URL, token)
	if status != http.StatusOK {
		t.Fatalf("segundo GET: querés 200, fue %d", status)
	}
	esperado := map[string][2]int64{ // [baseline+delta, hay]
		"merged_prs":            {before.MergedPrs + 1, after.MergedPrs},
		"findings_with_outcome": {before.FindingsWithOutcome + 2, after.FindingsWithOutcome},
		"accepted":              {before.Accepted + 1, after.Accepted},
		"resolved_comments":     {before.ResolvedComments + 1, after.ResolvedComments},
		"false_positives":       {before.FalsePositives + 1, after.FalsePositives},
		"reviews_with_cost":     {before.ReviewsWithCost + 1, after.ReviewsWithCost},
	}
	for campo, v := range esperado {
		if v[1] != v[0] {
			t.Errorf("%s: querés %d (baseline + delta sembrado), hay %d", campo, v[0], v[1])
		}
	}

	// Tasas EXACTAS para el delta sembrado: los numeradores del baseline más
	// la unidad sembrada, sobre los denominadores igual agrandados.
	denAceptados := before.FindingsWithOutcome + 2
	if denAceptados > 0 {
		rate := float64(before.Accepted+1) / float64(denAceptados)
		if after.AcceptedRate == nil || *after.AcceptedRate != rate {
			t.Errorf("accepted_rate: querés exactamente %v (delta sembrado), hay %v", rate, after.AcceptedRate)
		}
	}
	denFP := before.ResolvedComments + 1
	if denFP > 0 {
		rate := float64(before.FalsePositives+1) / float64(denFP)
		if after.FalsePositiveRate == nil || *after.FalsePositiveRate != rate {
			t.Errorf("false_positive_rate: querés exactamente %v (delta sembrado), hay %v", rate, after.FalsePositiveRate)
		}
	}
	if after.AvgCycleTimeHours == nil {
		t.Errorf("avg_cycle_time_hours debe ser no-null con un PR merged en la ventana")
	}
	if after.AvgTokensPerReview == nil {
		t.Errorf("avg_tokens_per_review debe ser no-null con un usage con review_id en la ventana")
	}
}
