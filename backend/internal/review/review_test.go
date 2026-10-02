// Tests de contrato del pipeline con stubs (mapa: backend.review —
// "contract con stub del gateway en CI; evals con proveedor real fuera de
// CI"). Sin BD: la store se reemplaza por un stub en memoria (§4.5: stubs de
// pocas líneas sobre mocks). El mapeo finding→posición usa el
// MapFindingToPosition real de internal/vcs contra un diff fixture.
package review

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// diffFixture es un diff unificado chico: main.go con 4 líneas cambiadas.
// Línea 3 del lado nuevo (import) y línea 5 (contexto de func main) anclan
// al RIGHT; la línea eliminada 4 del lado viejo ancla al LEFT.
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

// Identidades de la corrida de prueba (§3.6.1).
const (
	testRepoID = 1
	testPRID   = 7
	testHead   = "headaaa"
	testBase   = "basebbb"
)

// testInput es la entrada canónica que coincide con el PR del stub.
func testInput() ReviewInput {
	return ReviewInput{
		PullRequestID: testPRID,
		RepositoryID:  testRepoID,
		HeadSHA:       testHead,
		BaseSHA:       testBase,
		Workdir:       "/nonexistent-codeowl-test", // merge-base cae a base_sha
	}
}

// reviewerFindings es la salida JSON válida del Reviewer para main.go.
func reviewerFindings(line int, severity, category, body, suggestion string) string {
	s := ""
	if suggestion != "" {
		s = fmt.Sprintf(`,"suggestion":%q`, suggestion)
	}
	return fmt.Sprintf(`[{"line":%d,"severity":%q,"category":%q,"body":%q%s}]`,
		line, severity, category, body, s)
}

// summarizerJSON es la salida JSON válida del Summarizer.
const summarizerJSON = `{"summary":"Agrega un import y cambia un saludo.","walkthrough":"","mermaid":"sequenceDiagram\n  A->>B: ping"}`

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// stubStore es la store en memoria: las 9 operaciones que consume Run.
type stubStore struct {
	mu       sync.Mutex
	repo     store.Repository
	pr       store.PullRequest
	reviews  map[int64]*store.Review
	nextRev  int64
	findings []store.CreateFindingParams
	comments map[string][]store.CommentsSent // indexadas por type
	nextCmt  int64
}

func newStubStore() *stubStore {
	return &stubStore{
		repo: store.Repository{ID: testRepoID, Vcs: "github", Owner: "acme", Name: "demo", Enabled: true},
		pr: store.PullRequest{ID: testPRID, RepositoryID: testRepoID, Number: 7,
			State: "open", HeadSha: testHead, BaseRef: "main", BaseSha: testBase},
		reviews:  map[int64]*store.Review{},
		comments: map[string][]store.CommentsSent{},
	}
}

func (s *stubStore) GetRepository(_ context.Context, id int64) (store.Repository, error) {
	if id != s.repo.ID {
		return store.Repository{}, fmt.Errorf("repo %d no existe", id)
	}
	return s.repo, nil
}

func (s *stubStore) GetPullRequest(_ context.Context, id int64) (store.PullRequest, error) {
	if id != s.pr.ID {
		return store.PullRequest{}, fmt.Errorf("PR %d no existe", id)
	}
	return s.pr, nil
}

func (s *stubStore) CreateReview(_ context.Context, arg store.CreateReviewParams) (store.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextRev++
	rev := store.Review{ID: s.nextRev, PullRequestID: arg.PullRequestID,
		HeadSha: arg.HeadSha, BaseSha: arg.BaseSha, Status: StatusRunning}
	s.reviews[rev.ID] = &rev
	return rev, nil
}

func (s *stubStore) GetLatestReviewByPR(_ context.Context, prID int64) (store.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest *store.Review
	for _, r := range s.reviews {
		if r.PullRequestID == prID && (latest == nil || r.ID > latest.ID) {
			latest = r
		}
	}
	if latest == nil {
		return store.Review{}, pgx.ErrNoRows
	}
	return *latest, nil
}

func (s *stubStore) UpdateReviewStatus(_ context.Context, arg store.UpdateReviewStatusParams) (store.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reviews[arg.ID]
	if !ok {
		return store.Review{}, fmt.Errorf("review %d no existe", arg.ID)
	}
	r.Status = arg.Status
	return *r, nil
}

func (s *stubStore) UpdateReviewSummary(_ context.Context, arg store.UpdateReviewSummaryParams) (store.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reviews[arg.ID]
	if !ok {
		return store.Review{}, fmt.Errorf("review %d no existe", arg.ID)
	}
	r.Summary, r.Walkthrough, r.Mermaid = arg.Summary, arg.Walkthrough, arg.Mermaid
	return *r, nil
}

func (s *stubStore) CreateFinding(_ context.Context, arg store.CreateFindingParams) (store.Finding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.findings = append(s.findings, arg)
	return store.Finding{ID: int64(len(s.findings))}, nil
}

func (s *stubStore) GetCommentsSentByPRAndType(_ context.Context, arg store.GetCommentsSentByPRAndTypeParams) ([]store.CommentsSent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.CommentsSent(nil), s.comments[arg.Type]...), nil
}

func (s *stubStore) CreateCommentSent(_ context.Context, arg store.CreateCommentSentParams) (store.CommentsSent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextCmt++
	row := store.CommentsSent{ID: s.nextCmt, PullRequestID: arg.PullRequestID,
		ReviewID: arg.ReviewID, CommentID: arg.CommentID, Type: arg.Type,
		File: arg.File, Category: arg.Category, Anchor: arg.Anchor}
	s.comments[arg.Type] = append(s.comments[arg.Type], row)
	return row, nil
}

func (s *stubStore) UpdateCommentSentCommentID(_ context.Context, arg store.UpdateCommentSentCommentIDParams) (store.CommentsSent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for t, rows := range s.comments {
		for i := range rows {
			if rows[i].ID == arg.ID {
				rows[i].CommentID = arg.CommentID
				s.comments[t] = rows
				return rows[i], nil
			}
		}
	}
	return store.CommentsSent{}, fmt.Errorf("comment_sent %d no existe", arg.ID)
}

// stubGateway responde de colas por agente: reviewer por archivo,
// summarizer global. Cuenta las llamadas.
type stubGateway struct {
	mu         sync.Mutex
	calls      int
	reviewer   map[string][]string
	summarizer []string
}

func newStubGateway() *stubGateway {
	return &stubGateway{reviewer: map[string][]string{}}
}

func (g *stubGateway) Complete(_ context.Context, _, system, user string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if strings.HasPrefix(user, summarizerMarker) {
		if len(g.summarizer) == 0 {
			return "", fmt.Errorf("sin respuesta de summarizer encolada (llamada %d)", g.calls)
		}
		r := g.summarizer[0]
		g.summarizer = g.summarizer[1:]
		return r, nil
	}
	first := strings.SplitN(user, "\n", 2)[0]
	file := strings.TrimPrefix(first, userFilePrefix)
	q := g.reviewer[file]
	if len(q) == 0 {
		return "", fmt.Errorf("sin respuesta de reviewer para %s", file)
	}
	r := q[0]
	g.reviewer[file] = q[1:]
	return r, nil
}

// stubAnalyzer devuelve un resultado fijo (o error).
type stubAnalyzer struct {
	result *analyze.AnalysisResult
	err    error
}

func (a *stubAnalyzer) Run(_ context.Context, _ string) (*analyze.AnalysisResult, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.result, nil
}

// stubVCS graba las publicaciones y sirve el diff fixture.
type stubVCS struct {
	diff        string
	failInline  bool
	mu          sync.Mutex
	summaryBods []string
	summaryID   string
	inlineReqs  []vcs.CommentPosition
	inlineBods  []string
}

func newStubVCS() *stubVCS { return &stubVCS{diff: diffFixture, summaryID: "sum-1"} }

func (s *stubVCS) HandleWebhook(_ context.Context, _ http.ResponseWriter, _ *http.Request) {}
func (s *stubVCS) FetchPR(context.Context, *store.Repository, *store.PullRequest, string) error {
	return nil
}
func (s *stubVCS) FetchDefaultBranch(context.Context, *store.Repository, string) error { return nil }
func (s *stubVCS) FetchPRTimeline(context.Context, *store.Repository, *store.PullRequest) (*vcs.PRTimeline, error) {
	return &vcs.PRTimeline{}, nil
}
func (s *stubVCS) GetDiff(context.Context, *store.Repository, *store.PullRequest) (string, error) {
	return s.diff, nil
}
func (s *stubVCS) ListOpenPRs(context.Context, *store.Repository) ([]vcs.OpenPR, error) {
	return nil, nil
}
func (s *stubVCS) PostSuggestion(context.Context, *store.Repository, *store.PullRequest, vcs.CommentPosition, string) (string, error) {
	return "", nil
}

func (s *stubVCS) PostInlineComment(_ context.Context, _ *store.Repository, _ *store.PullRequest, pos vcs.CommentPosition, body string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failInline {
		return "", fmt.Errorf("VCS caído")
	}
	s.inlineReqs = append(s.inlineReqs, pos)
	s.inlineBods = append(s.inlineBods, body)
	return fmt.Sprintf("inline-%d", len(s.inlineReqs)), nil
}

func (s *stubVCS) PostSummary(_ context.Context, _ *store.Repository, _ *store.PullRequest, body, existing string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summaryBods = append(s.summaryBods, body)
	return s.summaryID, nil
}

// resetCache limpia la cache compartida entre tests: cada test arranca frío.
func resetCache() {
	results.mu.Lock()
	defer results.mu.Unlock()
	results.entries = map[string]cacheEntry{}
}

// run es el harness: arma los deps por defecto y corre el pipeline.
type deps struct {
	st  *stubStore
	gw  *stubGateway
	az  *stubAnalyzer
	vcs *stubVCS
	cfg Config
}

func newDeps() *deps {
	resetCache()
	az := &stubAnalyzer{result: &analyze.AnalysisResult{
		FilesAnalyzed: 1,
		LintersRun:    []analyze.LinterStatus{{Name: "revive", Status: "ok"}},
		Findings:      []analyze.Finding{},
	}}
	return &deps{st: newStubStore(), gw: newStubGateway(), az: az, vcs: newStubVCS(), cfg: DefaultConfig()}
}

// run ejecuta Run con los stubs armados (el VCS y el analyzer son el mismo).
func (d *deps) run(t *testing.T) (*ReviewResult, error) {
	t.Helper()
	return Run(context.Background(), d.cfg, d.st, d.gw, d.az, d.vcs, testInput())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Happy path: review creada, SAST + LLM publicados como inline, resumen en
// dos fases, comments_sent poblado y fila success con el resumen final.
func TestRunHappyPath(t *testing.T) {
	d := newDeps()
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "inyección SQL", "db.Query(q, arg)")}
	d.gw.summarizer = []string{summarizerJSON}
	d.az.result.Findings = []analyze.Finding{{
		File: "main.go", Line: 5, Severity: "low", Category: "style",
		Body: "nombre corto", Source: "sast", Linter: "revive", Rule: "var-naming",
	}}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés %q", res.Status, StatusSuccess)
	}
	if res.FindingsCount != 2 {
		t.Errorf("FindingsCount = %d, querés 2", res.FindingsCount)
	}
	// Publicación en dos fases: provisional + re-edición final.
	if len(d.vcs.summaryBods) != 2 {
		t.Fatalf("PostSummary llamado %d veces, querés 2", len(d.vcs.summaryBods))
	}
	final := d.vcs.summaryBods[1]
	if !strings.Contains(final, "### Hallazgos: 1 alta · 0 media · 1 baja") {
		t.Errorf("el resumen final no trae el recuento por severidad:\n%s", final)
	}
	if !strings.Contains(final, "Cobertura completa") {
		t.Errorf("el resumen final no declara cobertura completa:\n%s", final)
	}
	// Fase 1 provisional: sin recuento.
	if strings.Contains(d.vcs.summaryBods[0], "### Hallazgos:") {
		t.Errorf("el resumen provisional no debería traer recuento final:\n%s", d.vcs.summaryBods[0])
	}
	// Inline: LLM línea 3 y SAST línea 5 anclan al RIGHT del fixture.
	if len(d.vcs.inlineReqs) != 2 {
		t.Fatalf("inline publicados = %d, querés 2", len(d.vcs.inlineReqs))
	}
	if d.vcs.inlineReqs[0].Line != 3 || d.vcs.inlineReqs[0].Side != vcs.SideRight {
		t.Errorf("inline[0] = %+v, querés main.go:3 RIGHT", d.vcs.inlineReqs[0])
	}
	if d.vcs.inlineReqs[1].Line != 5 || d.vcs.inlineReqs[1].Side != vcs.SideRight {
		t.Errorf("inline[1] = %+v, querés main.go:5 RIGHT", d.vcs.inlineReqs[1])
	}
	if !strings.Contains(d.vcs.inlineBods[0], "```suggestion") {
		t.Errorf("la sugerencia del LLM no quedó como bloque aplicable:\n%s", d.vcs.inlineBods[0])
	}
	// Persistencia.
	if len(d.st.findings) != 2 {
		t.Errorf("findings persistidos = %d, querés 2", len(d.st.findings))
	}
	if n := len(d.st.comments["inline"]); n != 2 {
		t.Errorf("comments_sent inline = %d, querés 2", n)
	}
	if n := len(d.st.comments["summary"]); n != 1 {
		t.Errorf("comments_sent summary = %d, querés 1", n)
	}
	// Fila de la corrida.
	rev := d.st.reviews[1]
	if rev.Status != StatusSuccess {
		t.Errorf("review status = %q, querés success", rev.Status)
	}
	if rev.Summary != "Agrega un import y cambia un saludo." {
		t.Errorf("review summary = %q", rev.Summary)
	}
	if rev.Mermaid == "" {
		t.Errorf("review mermaid vacío, el fixture trae diagrama válido")
	}
}

// Stale (§3.6.1.3): identidad distinta → status stale, cero LLM, cero
// publicación.
func TestRunStale(t *testing.T) {
	d := newDeps()
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.summarizer = []string{summarizerJSON}
	d.st.pr.HeadSha = "headnuevo" // push entre encolado e inicio

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusStale {
		t.Errorf("status = %q, querés stale", res.Status)
	}
	if d.gw.calls != 0 {
		t.Errorf("el gateway fue llamado %d veces, querés 0 (stale no gasta LLM)", d.gw.calls)
	}
	if len(d.vcs.summaryBods) != 0 || len(d.vcs.inlineReqs) != 0 {
		t.Errorf("hubo publicación en una corrida stale")
	}
	if rev := d.st.reviews[1]; rev.Status != StatusStale {
		t.Errorf("review status = %q, querés stale", rev.Status)
	}
}

// Dedup (§3.6.3): huella file+categoría+ancla contra comments_sent con drift
// ±3 — el hallazgo ya comentado no se republica; el otro sí.
func TestRunDedup(t *testing.T) {
	d := newDeps()
	// Corrida anterior comentó security en la línea 4; el nuevo finding de
	// security está en la línea 3: |3-4| ≤ 3 → duplicado.
	d.st.comments["inline"] = []store.CommentsSent{{
		ID:            99,
		PullRequestID: testPRID,
		CommentID:     "viejo-1",
		Type:          "inline",
		File:          pgtype.Text{String: "main.go", Valid: true},
		Category:      pgtype.Text{String: "security", Valid: true},
		Anchor:        pgtype.Text{String: "4", Valid: true},
	}}
	d.gw.reviewer["main.go"] = []string{
		"[" + strings.Trim(reviewerFindings(3, "high", "security", "x", ""), "[]") + "," +
			strings.Trim(reviewerFindings(5, "medium", "logic", "chequeo faltante", ""), "[]") + "]",
	}
	d.gw.summarizer = []string{summarizerJSON}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FindingsCount != 1 {
		t.Errorf("FindingsCount = %d, querés 1 (el duplicado no se publica)", res.FindingsCount)
	}
	if len(d.vcs.inlineReqs) != 1 {
		t.Fatalf("inline publicados = %d, querés 1", len(d.vcs.inlineReqs))
	}
	if d.vcs.inlineReqs[0].Line != 5 {
		t.Errorf("se publicó la línea %d, querés la 5 (la 3 está deduplicada)", d.vcs.inlineReqs[0].Line)
	}
}

// Tope de diff (§9.6): diff sobre el tope → sin LLM de reviewer ni inline,
// solo SAST + resumen, status partial con declaración.
func TestRunDiffOverCap(t *testing.T) {
	d := newDeps()
	d.cfg.DiffMaxLines = 2 // el fixture tiene 4 líneas cambiadas
	d.gw.summarizer = []string{summarizerJSON}
	d.az.result.Findings = []analyze.Finding{{
		File: "main.go", Line: 3, Severity: "high", Category: "security",
		Body: "inyección", Source: "sast", Linter: "gosec", Rule: "G201",
	}}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPartial {
		t.Errorf("status = %q, querés partial", res.Status)
	}
	if res.FindingsCount != 1 {
		t.Errorf("FindingsCount = %d, querés 1 (solo SAST)", res.FindingsCount)
	}
	if d.gw.calls != 1 {
		t.Errorf("gateway llamado %d veces, querés 1 (solo summarizer)", d.gw.calls)
	}
	if len(d.vcs.inlineReqs) != 0 {
		t.Errorf("hubo inline con diff sobre el tope — §9.6 dice solo resumen")
	}
	final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if !strings.Contains(final, "Hallazgos fuera del diff") || !strings.Contains(final, "inyección") {
		t.Errorf("el SAST no bajó al resumen:\n%s", final)
	}
	if !strings.Contains(final, "solo resumen") {
		t.Errorf("el resumen no declara el motivo de la cobertura parcial:\n%s", final)
	}
}

// Salida malformada del Reviewer (§9.8): se reintenta; si se recupera, la
// corrida queda success; si se agota, el archivo se descarta con declaración
// y el job sigue (nunca crashea).
func TestRunMalformedOutput(t *testing.T) {
	t.Run("recupera en el reintento", func(t *testing.T) {
		d := newDeps()
		d.cfg.AgentRetries = 1
		d.gw.reviewer["main.go"] = []string{"esto no es json", reviewerFindings(3, "high", "security", "x", "")}
		d.gw.summarizer = []string{summarizerJSON}

		res, err := d.run(t)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Status != StatusSuccess {
			t.Errorf("status = %q, querés success (el reintento recuperó)", res.Status)
		}
		if d.gw.calls != 3 { // 2 del reviewer + 1 del summarizer
			t.Errorf("gateway llamado %d veces, querés 3", d.gw.calls)
		}
		if len(d.vcs.inlineReqs) != 1 {
			t.Errorf("inline publicados = %d, querés 1", len(d.vcs.inlineReqs))
		}
	})

	t.Run("agota y descarta el archivo", func(t *testing.T) {
		d := newDeps()
		d.cfg.AgentRetries = 1
		d.gw.reviewer["main.go"] = []string{"mal 1", `{"tampoco": "json"}`}
		d.gw.summarizer = []string{summarizerJSON}
		d.az.result.Findings = []analyze.Finding{{
			File: "main.go", Line: 3, Severity: "low", Category: "style",
			Body: "estilo", Source: "sast", Linter: "revive", Rule: "naming",
		}}

		res, err := d.run(t)
		if err != nil {
			t.Fatalf("Run: %v (un descarte no debe fallar el job, §9.8)", err)
		}
		if res.Status != StatusPartial {
			t.Errorf("status = %q, querés partial", res.Status)
		}
		if res.FindingsCount != 1 {
			t.Errorf("FindingsCount = %d, querés 1 (solo el SAST)", res.FindingsCount)
		}
		final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
		if !strings.Contains(final, "análisis LLM omitido") {
			t.Errorf("el resumen no declara el descarte:\n%s", final)
		}
	})
}

// Enmascarado de secrets (§9.4): el valor jamás sale en un comentario, ni
// inline ni en el resumen, venga del LLM o de un linter.
func TestRunSecretsMasked(t *testing.T) {
	const token = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"
	d := newDeps()
	// La línea 999 no está en el diff: el hallazgo baja al resumen.
	d.gw.reviewer["main.go"] = []string{
		reviewerFindings(999, "high", "security", "hardcodeá el token "+token+" acá", ""),
	}
	d.gw.summarizer = []string{summarizerJSON}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success", res.Status)
	}
	final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if strings.Contains(final, token) {
		t.Errorf("el token quedó en claro en el resumen:\n%s", final)
	}
	if !strings.Contains(final, redacted) {
		t.Errorf("el resumen debería traer el valor enmascarado:\n%s", final)
	}
	// El enmascarado también cubre asignaciones con nombre de clave.
	got := maskSecrets("reemplazá password: SuperSecret99 por vault")
	if strings.Contains(got, "SuperSecret99") {
		t.Errorf("asignación de credencial en claro: %q", got)
	}
	if !strings.Contains(got, "password:") {
		t.Errorf("el nombre de la clave debería conservarse: %q", got)
	}
}

// Cache (§9.6): segunda corrida con la misma clave (head + merge-base +
// prompts + config) reusa findings sin llamar al LLM; el dedup contra los
// comentarios ya publicados evita republicar.
func TestRunCacheHit(t *testing.T) {
	d := newDeps()
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.summarizer = []string{summarizerJSON}

	if _, err := d.run(t); err != nil {
		t.Fatalf("primera corrida: %v", err)
	}
	firstCalls := d.gw.calls
	if firstCalls == 0 {
		t.Fatal("la primera corrida debería haber llamado al LLM")
	}

	res, err := d.run(t) // misma identidad: cache hit
	if err != nil {
		t.Fatalf("segunda corrida: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success", res.Status)
	}
	if d.gw.calls != firstCalls {
		t.Errorf("el gateway fue llamado %d veces en el cache hit (antes: %d)", d.gw.calls, firstCalls)
	}
	if res.FindingsCount != 0 {
		t.Errorf("FindingsCount = %d, querés 0 (todo deduplica contra la corrida 1)", res.FindingsCount)
	}
}

// Fallo del analyzer (§9.4): la ausencia de SAST se declara, la corrida
// queda partial — jamás ausencia silenciosa de hallazgos.
func TestRunAnalyzerFails(t *testing.T) {
	d := newDeps()
	d.az.err = errors.New("podman murió")
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.summarizer = []string{summarizerJSON}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPartial {
		t.Errorf("status = %q, querés partial", res.Status)
	}
	final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if !strings.Contains(final, "SAST omitido") {
		t.Errorf("el resumen no declara el fallo del analyzer:\n%s", final)
	}
}

// Fallo de publicación inline (§9.7): la corrida queda failed y el resumen
// se re-edita mejor esfuerzo con el estado final.
func TestRunPublishFailsMarksFailed(t *testing.T) {
	d := newDeps()
	d.vcs.failInline = true
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.summarizer = []string{summarizerJSON}

	res, err := d.run(t)
	if err == nil {
		t.Fatal("Run debería devolver el error de publicación para que el job reintente")
	}
	if res.Status != StatusFailed {
		t.Errorf("status = %q, querés failed", res.Status)
	}
	last := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if !strings.Contains(last, "La revisión falló") {
		t.Errorf("el resumen no fue re-editado con el estado failed (§9.7):\n%s", last)
	}
	if rev := d.st.reviews[1]; rev.Status != StatusFailed {
		t.Errorf("review status = %q, querés failed", rev.Status)
	}
}

// Unitarias de dedup y prompts.
func TestFingerprintFormat(t *testing.T) {
	got := Fingerprint("main.go", "security", 42)
	if got != "main.go|security|42" {
		t.Errorf("Fingerprint = %q", got)
	}
}

func TestIsDuplicateDriftBoundaries(t *testing.T) {
	existing := []store.CommentsSent{{
		File:     pgtype.Text{String: "a.go", Valid: true},
		Category: pgtype.Text{String: "logic", Valid: true},
		Anchor:   pgtype.Text{String: "10", Valid: true},
	}}
	f := func(line int32) Finding {
		return Finding{File: "a.go", Category: "logic", Line: line}
	}
	cases := []struct {
		line  int32
		drift int
		want  bool
	}{
		{10, 3, true},  // exacto
		{13, 3, true},  // borde del drift
		{14, 3, false}, // fuera del drift
		{7, 3, true},   // drift hacia abajo
		{6, 3, false},
		{10, 0, true}, // drift 0: solo exacto
		{11, 0, false},
	}
	for _, c := range cases {
		if got := IsDuplicate(existing, f(c.line), c.drift); got != c.want {
			t.Errorf("IsDuplicate(line=%d, drift=%d) = %v, querés %v", c.line, c.drift, got, c.want)
		}
	}
}

func TestMermaidValidation(t *testing.T) {
	if !validMermaid("sequenceDiagram\n  A->>B: hola") {
		t.Error("sequenceDiagram debería validar")
	}
	if !validMermaid("graph TD\n A-->B") {
		t.Error("graph debería validar")
	}
	if validMermaid("") || validMermaid("hola mundo") {
		t.Error("texto sin encabezado de diagrama no debería validar")
	}
}
