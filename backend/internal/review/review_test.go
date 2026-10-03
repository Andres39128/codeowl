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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/repoconfig"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
	"github.com/Andres39128/codeowl/backend/prompts"
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

// stubStore es la store en memoria: las operaciones que consume Run.
type stubStore struct {
	mu             sync.Mutex
	repo           store.Repository
	pr             store.PullRequest
	reviews        map[int64]*store.Review
	nextRev        int64
	findings       []store.CreateFindingParams
	comments       map[string][]store.CommentsSent // indexadas por type
	nextCmt        int64
	cheapProviders []store.LlmProvider // cola del rol cheap (verifier)
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

func (s *stubStore) ListEnabledLlmProvidersByRole(_ context.Context, role string) ([]store.LlmProvider, error) {
	if role != roleCheap {
		return nil, nil
	}
	return s.cheapProviders, nil
}

func (s *stubStore) GetCommentsSentByPRAndType(_ context.Context, arg store.GetCommentsSentByPRAndTypeParams) ([]store.CommentsSent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.CommentsSent(nil), s.comments[arg.Type]...), nil
}

func (s *stubStore) ListFindingsByReview(_ context.Context, reviewID int64) ([]store.Finding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Finding
	for i, arg := range s.findings {
		if arg.ReviewID != reviewID {
			continue
		}
		out = append(out, store.Finding{
			ID: int64(i + 1), ReviewID: arg.ReviewID, File: arg.File, Line: arg.Line,
			Severity: arg.Severity, Category: arg.Category, Body: arg.Body,
			Suggestion: arg.Suggestion, Source: arg.Source,
		})
	}
	return out, nil
}

func (s *stubStore) CreateCommentSent(_ context.Context, arg store.CreateCommentSentParams) (store.CommentsSent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextCmt++
	row := store.CommentsSent{ID: s.nextCmt, PullRequestID: arg.PullRequestID,
		ReviewID: arg.ReviewID, CommentID: arg.CommentID, Type: arg.Type,
		File: arg.File, Category: arg.Category, Anchor: arg.Anchor,
		ParentCommentID: arg.ParentCommentID}
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
// summarizer, chat y testgen por marcador. Cuenta las llamadas y graba los
// system prompts recibidos (para afirmar qué le llegó a cada agente).
type stubGateway struct {
	mu           sync.Mutex
	calls        int
	systemSeen   []string
	reviewer     map[string][]string
	reviewerSeen []string // prompts de usuario del reviewer recibidos (contexto §6 F4)
	summarizer   []string
	chat         []string
	chatSeen     []string // prompts de usuario de chat recibidos
	testgen      []string // respuestas encoladas para /tests
	testgenSeen  []string // prompts de usuario de testgen recibidos
	verifier     []string // respuestas encoladas para el verifier (rol cheap)
	verifierSeen []string // prompts de usuario del verifier recibidos
}

func newStubGateway() *stubGateway {
	return &stubGateway{reviewer: map[string][]string{}}
}

func (g *stubGateway) Complete(_ context.Context, _, system, user string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	g.systemSeen = append(g.systemSeen, system)
	if strings.HasPrefix(user, summarizerMarker) {
		if len(g.summarizer) == 0 {
			return "", fmt.Errorf("sin respuesta de summarizer encolada (llamada %d)", g.calls)
		}
		r := g.summarizer[0]
		g.summarizer = g.summarizer[1:]
		return r, nil
	}
	if strings.HasPrefix(user, chatUserMarker) {
		g.chatSeen = append(g.chatSeen, user)
		if len(g.chat) == 0 {
			return "", fmt.Errorf("sin respuesta de chat encolada (llamada %d)", g.calls)
		}
		r := g.chat[0]
		g.chat = g.chat[1:]
		return r, nil
	}
	if strings.HasPrefix(user, testgenUserMarker) {
		g.testgenSeen = append(g.testgenSeen, user)
		if len(g.testgen) == 0 {
			return "", fmt.Errorf("sin respuesta de testgen encolada (llamada %d)", g.calls)
		}
		r := g.testgen[0]
		g.testgen = g.testgen[1:]
		return r, nil
	}
	if strings.HasPrefix(user, verifierUserMarker) {
		g.verifierSeen = append(g.verifierSeen, user)
		if len(g.verifier) == 0 {
			return "", fmt.Errorf("sin respuesta de verifier encolada (llamada %d)", g.calls)
		}
		r := g.verifier[0]
		g.verifier = g.verifier[1:]
		return r, nil
	}
	first := strings.SplitN(user, "\n", 2)[0]
	file := strings.TrimPrefix(first, userFilePrefix)
	g.reviewerSeen = append(g.reviewerSeen, user)
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
	diff         string
	failInline   bool
	mu           sync.Mutex
	summaryBods  []string
	summaryID    string
	inlineReqs   []vcs.CommentPosition
	inlineBods   []string
	suggestReqs  []vcs.CommentPosition // publicaciones vía PostSuggestion
	suggestBods  []string
	replyParents []string // padres de las respuestas de chat
	replyBods    []string // cuerpos de las respuestas de chat
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

func (s *stubVCS) PostSuggestion(_ context.Context, _ *store.Repository, _ *store.PullRequest, pos vcs.CommentPosition, body string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failInline {
		return "", fmt.Errorf("VCS caído")
	}
	s.suggestReqs = append(s.suggestReqs, pos)
	s.suggestBods = append(s.suggestBods, body)
	return fmt.Sprintf("suggestion-%d", len(s.suggestReqs)), nil
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

func (s *stubVCS) PostReply(_ context.Context, _ *store.Repository, _ *store.PullRequest, parentID, body string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replyParents = append(s.replyParents, parentID)
	s.replyBods = append(s.replyBods, body)
	return fmt.Sprintf("chat-%d", len(s.replyBods)), nil
}

// resetCache limpia la cache compartida entre tests: cada test arranca frío.
func resetCache() {
	results.mu.Lock()
	defer results.mu.Unlock()
	results.entries = map[string]cacheEntry{}
}

// run es el harness: arma los deps por defecto y corre el pipeline.
type deps struct {
	st   *stubStore
	gw   *stubGateway
	az   *stubAnalyzer
	vcs  *stubVCS
	retr Retriever // contexto simbólico (§6 F4); nil por defecto
	cfg  Config
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
	return Run(context.Background(), d.cfg, d.st, d.gw, d.az, d.vcs, d.retr, testInput())
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
	// Inline: el LLM (con sugerencia) publica vía PostSuggestion; el SAST
	// (sin sugerencia) vía PostInlineComment. Ambos anclan al RIGHT.
	if len(d.vcs.suggestReqs) != 1 || d.vcs.suggestReqs[0].Line != 3 ||
		d.vcs.suggestReqs[0].Side != vcs.SideRight {
		t.Fatalf("PostSuggestion = %+v, querés main.go:3 RIGHT", d.vcs.suggestReqs)
	}
	// El bloque aplicable trae SOLO el código corregido — sin doble envoltura
	// y con la explicación fuera del fence.
	if !strings.Contains(d.vcs.suggestBods[0], "```suggestion\ndb.Query(q, arg)\n```") {
		t.Errorf("la sugerencia no quedó como bloque aplicable con solo el código:\n%s", d.vcs.suggestBods[0])
	}
	if !strings.Contains(d.vcs.suggestBods[0], "inyección SQL") {
		t.Errorf("el comentario de sugerencia debe traer la explicación:\n%s", d.vcs.suggestBods[0])
	}
	if strings.Contains(d.vcs.suggestBods[0], "```suggestion\n```suggestion") {
		t.Errorf("la sugerencia quedó envuelta dos veces:\n%s", d.vcs.suggestBods[0])
	}
	if len(d.vcs.inlineReqs) != 1 || d.vcs.inlineReqs[0].Line != 5 || d.vcs.inlineReqs[0].Side != vcs.SideRight {
		t.Fatalf("inline publicados = %+v, querés solo el SAST en main.go:5 RIGHT", d.vcs.inlineReqs)
	}
	if strings.Contains(d.vcs.inlineBods[0], "```") {
		t.Errorf("el inline sin sugerencia no debe traer bloque de código:\n%s", d.vcs.inlineBods[0])
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

// Contexto simbólico del Reviewer (§6 F4): stub Retriever + bloque en el
// prompt, sin degradación ante fallo, tope de chars y prompts intactos sin
// retriever (§9.6: la revisión jamás se degrada por indexar).

// stubRetriever devuelve símbolos fijos o error, y graba las queries
// recibidas (archivo, código) para afirmar el contrato de llamada.
type stubRetriever struct {
	syms    []RelatedSymbol
	err     error
	calls   []string // "file|code" por cada llamada recibida
	callsN  int
	repoIDs []int64
}

func (r *stubRetriever) RetrieveRelated(_ context.Context, repoID int64, file, code string) ([]RelatedSymbol, error) {
	r.calls = append(r.calls, file+"|"+code)
	r.callsN++
	r.repoIDs = append(r.repoIDs, repoID)
	if r.err != nil {
		return nil, r.err
	}
	return r.syms, nil
}

// runReviewerHarness arma el happy path con un solo archivo y devuelve los
// deps ya preparados; setup ajusta lo que el test necesite antes de correr.
func runReviewerHarness(t *testing.T, setup func(d *deps)) (*ReviewResult, *deps) {
	t.Helper()
	d := newDeps()
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "inyección SQL", "")}
	d.gw.summarizer = []string{summarizerJSON}
	if setup != nil {
		setup(d)
	}
	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, d
}

// Bloque presente: el prompt del reviewer trae el encabezado, una línea por
// símbolo con su vía, y queda después del fence del diff.
func TestReviewerContextBlock(t *testing.T) {
	_, d := runReviewerHarness(t, func(d *deps) {
		d.retr = &stubRetriever{syms: []RelatedSymbol{
			{File: "util/query.go", Symbol: "Query", Kind: "func", StartLine: 10, EndLine: 20, Via: "similar"},
			{File: "util/db.go", Symbol: "Conn", Kind: "type", StartLine: 5, EndLine: 9, Via: "import"},
		}}
	})
	if d.retr.(*stubRetriever).callsN != 1 {
		t.Errorf("retrieval llamado %d veces, querés 1 por archivo", d.retr.(*stubRetriever).callsN)
	}
	if got := d.retr.(*stubRetriever).repoIDs[0]; got != testRepoID {
		t.Errorf("repoID = %d, querés %d", got, testRepoID)
	}
	// La query es el inicio de los hunks del diff del archivo.
	if q := d.retr.(*stubRetriever).calls[0]; !strings.HasPrefix(strings.SplitN(q, "|", 2)[1], "+++ b/main.go") {
		t.Errorf("la query no son los hunks del archivo: %q", q)
	}
	if len(d.gw.reviewerSeen) != 1 {
		t.Fatalf("llamadas al reviewer = %d, querés 1", len(d.gw.reviewerSeen))
	}
	prompt := d.gw.reviewerSeen[0]
	if !strings.Contains(prompt, relatedContextHeader) {
		t.Errorf("el prompt no trae el bloque de símbolos:\n%s", prompt)
	}
	if !strings.Contains(prompt, "util/query.go:10 func Query (vía similitud)") {
		t.Errorf("falta la línea del símbolo por similitud:\n%s", prompt)
	}
	if !strings.Contains(prompt, "util/db.go:5 type Conn (vía import)") {
		t.Errorf("falta la línea del símbolo por import:\n%s", prompt)
	}
	// El bloque va DESPUÉS del fence del diff (§6 F4, decisión 6).
	if idx := strings.Index(prompt, relatedContextHeader); !strings.HasSuffix(prompt[:idx], "```") {
		t.Errorf("el bloque no queda después del fence del diff:\n%s", prompt)
	}
}

// Fallo del retriever: la corrida sigue y publica sus hallazgos; sin bloque
// en el prompt y sin degradar el estado (§9.6: jamás se degrada por indexar).
func TestReviewerContextRetrievalError(t *testing.T) {
	res, d := runReviewerHarness(t, func(d *deps) {
		d.retr = &stubRetriever{err: errors.New("índice caído")}
	})
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success: un fallo de retrieval no degrada la corrida (§9.6)", res.Status)
	}
	if res.FindingsCount != 1 {
		t.Errorf("FindingsCount = %d, querés 1: los hallazgos siguen saliendo", res.FindingsCount)
	}
	if len(d.gw.reviewerSeen) != 1 || strings.Contains(d.gw.reviewerSeen[0], "Símbolos relacionados") {
		t.Errorf("con retrieval fallida el prompt no debe traer bloque:\n%s", d.gw.reviewerSeen)
	}
}

// Sin retriever: el prompt del reviewer es byte-idéntico al de siempre
// (regresión de compatibilidad con repos sin indexar).
func TestReviewerContextNil(t *testing.T) {
	_, d := runReviewerHarness(t, nil)
	if len(d.gw.reviewerSeen) != 1 {
		t.Fatalf("llamadas al reviewer = %d, querés 1", len(d.gw.reviewerSeen))
	}
	want := reviewerUser("main.go", mainGoHunks(), "")
	if d.gw.reviewerSeen[0] != want {
		t.Errorf("sin retriever el prompt cambió:\n got %q\nwant %q", d.gw.reviewerSeen[0], want)
	}
}

// Tope de chars: muchas entradas → el bloque respeta cfg.ReviewContextMaxChars,
// descarta símbolos ENTEROS (nunca un corte a mitad de línea) y declara la
// omisión al pie.
func TestReviewerContextCap(t *testing.T) {
	var syms []RelatedSymbol
	for i := 0; i < 40; i++ {
		syms = append(syms, RelatedSymbol{
			File:   fmt.Sprintf("pkg/archivo%02d_con_nombre_largo.go", i),
			Symbol: fmt.Sprintf("SimboloNumero%02d", i), Kind: "func",
			StartLine: int32(100 + i), Via: "similar",
		})
	}
	d := newDeps()
	d.cfg.ReviewContextMaxChars = 500 // sobre el mínimo: no lo normaliza
	d.retr = &stubRetriever{syms: syms}
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "inyección SQL", "")}
	d.gw.summarizer = []string{summarizerJSON}
	if _, err := d.run(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	prompt := d.gw.reviewerSeen[0]
	idx := strings.Index(prompt, relatedContextHeader)
	if idx < 0 {
		t.Fatalf("el prompt no trae bloque pese a tener símbolos:\n%s", prompt)
	}
	block := prompt[idx:]
	if len(block) > d.cfg.ReviewContextMaxChars {
		t.Errorf("bloque de %d chars sobre el tope de %d", len(block), d.cfg.ReviewContextMaxChars)
	}
	if !strings.Contains(block, "… (") || !strings.Contains(block, "más omitidos)") {
		t.Errorf("el bloque recortado no declara la omisión:\n%s", block)
	}
	// Sin cortes a mitad de línea: cada línea es completa — header, símbolo
	// con su vía, o la línea de omisión.
	for _, l := range strings.Split(strings.TrimPrefix(block, relatedContextHeader), "\n") {
		switch {
		case l == "":
			continue
		case strings.HasPrefix(l, "… (") && strings.HasSuffix(l, "más omitidos)"):
		case !strings.Contains(l, "(vía ") || !strings.HasSuffix(l, ")"):
			t.Errorf("línea cortada a mitad o malformada: %q", l)
		}
	}
}

// Sin símbolos (índice vacío): el prompt queda byte-idéntico al de siempre
// — el bloque simplemente no existe.
func TestReviewerContextEmpty(t *testing.T) {
	_, d := runReviewerHarness(t, func(d *deps) {
		d.retr = &stubRetriever{syms: nil}
	})
	if len(d.gw.reviewerSeen) != 1 {
		t.Fatalf("llamadas al reviewer = %d, querés 1", len(d.gw.reviewerSeen))
	}
	if strings.Contains(d.gw.reviewerSeen[0], "Símbolos relacionados") {
		t.Errorf("con símbolos vacíos el prompt no debe traer bloque:\n%s", d.gw.reviewerSeen[0])
	}
	if d.gw.reviewerSeen[0] != reviewerUser("main.go", mainGoHunks(), "") {
		t.Errorf("con índice vacío el prompt debe quedar como siempre:\n%s", d.gw.reviewerSeen[0])
	}
}

// mainGoHunks devuelve los hunks del diff fixture para main.go (la query y
// el prompt del reviewer lo usan como referencia canónica).
func mainGoHunks() string {
	hunks, _, _ := splitDiffByFile(diffFixture)
	return hunks["main.go"]
}

// Config efectiva en la corrida (§9.5/§6 F3): los tests de acá abajo corren
// contra un repo git real (el review.yaml se lee del merge-base) usando el
// helper gitRepo de este archivo.

// runConConfig arma un repo temporal cuyo commit base trae review.yaml y
// corre el pipeline contra él (merge-base = base: historia lineal).
func runConConfig(t *testing.T, cfgYAML string, setup func(d *deps)) (*ReviewResult, *deps) {
	t.Helper()
	baseFiles := map[string]string{}
	if cfgYAML != "" {
		baseFiles["review.yaml"] = cfgYAML
	}
	wd, base, head := gitRepo(t, baseFiles, map[string]string{"y.go": "package y"})
	d := newDeps()
	d.st.pr.HeadSha, d.st.pr.BaseSha = head, base
	input := testInput()
	input.HeadSHA, input.BaseSHA, input.Workdir = head, base, wd
	if setup != nil {
		setup(d)
	}
	res, err := Run(context.Background(), d.cfg, d.st, d.gw, d.az, d.vcs, d.retr, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, d
}

// twoFileDiff es un diff de dos archivos: main.go (fuera de los filtros de
// los tests de path_filters) y src/util.go (dentro).
const twoFileDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
 
+import "fmt"
+
diff --git a/src/util.go b/src/util.go
index 3333333..4444444 100644
--- a/src/util.go
+++ b/src/util.go
@@ -1,3 +1,4 @@
 package util
 
+func Sum(a, b int) int { return a + b }
+
`

// path_filters (§9.5): los archivos fuera de los filtros no llegan al LLM ni
// generan hallazgos de SAST — el analyzer corre una vez y el filtro opera en
// el mismo punto de selección.
func TestRunPathFilters(t *testing.T) {
	res, d := runConConfig(t, "path_filters:\n  - src/**\n", func(d *deps) {
		d.vcs.diff = twoFileDiff
		d.gw.reviewer["src/util.go"] = []string{reviewerFindings(3, "high", "security", "inyección", "")}
		d.gw.summarizer = []string{summarizerJSON}
		d.az.result.Findings = []analyze.Finding{
			{File: "main.go", Line: 2, Severity: "medium", Category: "logic", Body: "fuera de filtros", Source: "sast"},
			{File: "src/util.go", Line: 4, Severity: "low", Category: "style", Body: "estilo", Source: "sast"},
		}
	})

	// Si main.go hubiera llegado al reviewer, el stub habría fallado por no
	// tener respuesta encolada → descarte → partial. Success + 2 llamadas
	// (reviewer de util.go + summarizer) prueba que main.go jamás se envió.
	if d.gw.calls != 2 {
		t.Errorf("gateway llamado %d veces, querés 2 (solo util.go + summarizer)", d.gw.calls)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success", res.Status)
	}
	if res.FindingsCount != 2 {
		t.Errorf("FindingsCount = %d, querés 2 (LLM + SAST de util.go)", res.FindingsCount)
	}
	if len(d.st.findings) != 2 {
		t.Fatalf("findings persistidos = %d, querés 2", len(d.st.findings))
	}
	for _, f := range d.st.findings {
		if f.File == "main.go" {
			t.Errorf("el hallazgo de un archivo filtrado no debe persistirse: %+v", f)
		}
	}
}

// Todos los archivos fuera de los filtros: la corrida completa normal con
// cero hallazgos — filtrar todo no es error.
func TestRunPathFiltersAllFiltered(t *testing.T) {
	res, d := runConConfig(t, "path_filters:\n  - \"!**\"\n", func(d *deps) {
		d.gw.summarizer = []string{summarizerJSON}
		d.az.result.Findings = []analyze.Finding{
			{File: "main.go", Line: 2, Severity: "high", Category: "security", Body: "x", Source: "sast"},
		}
	})

	if d.gw.calls != 1 {
		t.Errorf("gateway llamado %d veces, querés 1 (solo summarizer)", d.gw.calls)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success", res.Status)
	}
	if res.FindingsCount != 0 || len(d.st.findings) != 0 {
		t.Errorf("hallazgos = %d (res) / %d (persistidos), querés 0/0", res.FindingsCount, len(d.st.findings))
	}
	if len(d.vcs.inlineReqs)+len(d.vcs.suggestReqs) != 0 {
		t.Error("no debería haber comentarios inline con todo filtrado")
	}
}

// Perfil chill (§6 F3): publica solo high|medium sin style — todo hallazgo
// persiste (auditoría), pero los filtrados no salen ni inline ni en el
// resumen, y el recuento final refleja solo lo publicado. El perfil jamás
// cambia el estado de la corrida (§1.1).
func TestRunChillProfile(t *testing.T) {
	res, d := runConConfig(t, "profile: chill\n", func(d *deps) {
		d.gw.reviewer["main.go"] = []string{
			"[" + strings.Trim(reviewerFindings(3, "high", "security", "inyección SQL", "db.Query(q, arg)"), "[]") + "," +
				strings.Trim(reviewerFindings(5, "low", "style", "espaciado", ""), "[]") + "]",
		}
		d.gw.summarizer = []string{summarizerJSON}
		// Fuera del diff visible: con el filtro caería del resumen también.
		d.az.result.Findings = []analyze.Finding{
			{File: "main.go", Line: 999, Severity: "low", Category: "style", Body: "nombre corto", Source: "sast", Linter: "revive", Rule: "naming"},
		}
	})

	if res.FindingsCount != 1 {
		t.Errorf("FindingsCount = %d, querés 1 (solo el high/security se publica)", res.FindingsCount)
	}
	// Auditoría: TODO se persiste, incluido lo no publicado.
	if len(d.st.findings) != 3 {
		t.Fatalf("findings persistidos = %d, querés 3", len(d.st.findings))
	}
	// Solo el high/security sale inline (con sugerencia → PostSuggestion).
	if len(d.vcs.suggestReqs) != 1 || len(d.vcs.inlineReqs) != 0 {
		t.Fatalf("publicación inline = %d sugerencias + %d planos, querés 1+0",
			len(d.vcs.suggestReqs), len(d.vcs.inlineReqs))
	}
	final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if !strings.Contains(final, "### Hallazgos: 1 alta · 0 media · 0 baja") {
		t.Errorf("el recuento debe reflejar lo publicado:\n%s", final)
	}
	for _, filtrado := range []string{"espaciado", "nombre corto"} {
		if strings.Contains(final, filtrado) {
			t.Errorf("lo filtrado por el perfil no debería aparecer en el resumen: %q", filtrado)
		}
	}
	if rev := d.st.reviews[1]; rev.Status != StatusSuccess {
		t.Errorf("status = %q, querés success (el perfil jamás bloquea, §1.1)", rev.Status)
	}
}

// Perfil strict + instructions (§9.5/§6 F3): el system prompt del reviewer
// llega con la regla de nits y el bloque de reglas del repo llenos, sin
// residuo de placeholders; con el default (sin review.yaml) no trae ninguno
// de los dos.
func TestRunReviewerPromptProfileAndInstructions(t *testing.T) {
	_, d := runConConfig(t, "profile: strict\ninstructions: \"Cuidá los panics silenciosos.\"\n", func(d *deps) {
		d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
		d.gw.summarizer = []string{summarizerJSON}
	})

	var system string
	for _, s := range d.gw.systemSeen {
		if strings.Contains(s, "nits") || strings.Contains(s, "panics") {
			system = s
			break
		}
	}
	if system == "" {
		t.Fatal("ningún system prompt traía la config del perfil: el reviewer no recibió strict/instructions")
	}
	if !strings.Contains(system, "reportá también nits") {
		t.Errorf("con strict el prompt debe pedir nits:\n%s", system)
	}
	if !strings.Contains(system, "Reglas de revisión de este repositorio") ||
		!strings.Contains(system, "Cuidá los panics silenciosos.") {
		t.Errorf("el bloque de instrucciones del repo no llegó delimitado:\n%s", system)
	}
	if strings.Contains(system, nitsPlaceholder) || strings.Contains(system, instructionsPlaceholder) {
		t.Errorf("residuo de placeholder en el prompt:\n%s", system)
	}

	// Default sin review.yaml: ni nits ni bloque ni residuo.
	d2 := newDeps()
	d2.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d2.gw.summarizer = []string{summarizerJSON}
	if _, err := d2.run(t); err != nil {
		t.Fatalf("Run default: %v", err)
	}
	def := d2.gw.systemSeen[0] // la primera llamada es la del reviewer
	if strings.Contains(def, "nits") || strings.Contains(def, "Reglas de revisión") ||
		strings.Contains(def, nitsPlaceholder) || strings.Contains(def, instructionsPlaceholder) {
		t.Errorf("el prompt default no debe traer nits ni reglas del repo:\n%s", def)
	}
}

// Tabla del filtro de publicación por perfil (§6 F3): chill publica solo
// high|medium excluyendo style; assertive y strict pasan todo — los nits de
// strict llegan como findings low/style normales.
func TestIsPublishable(t *testing.T) {
	cases := []struct {
		profile  string
		severity string
		category string
		want     bool
	}{
		{"chill", "high", "style", false},
		{"chill", "high", "logic", true},
		{"chill", "medium", "style", false},
		{"chill", "medium", "logic", true},
		{"chill", "low", "style", false},
		{"chill", "low", "logic", false},
		{"assertive", "high", "security", true},
		{"assertive", "low", "style", true},
		{"strict", "low", "style", true},
		{"strict", "medium", "other", true},
		{"", "low", "style", true},           // vacío → default assertive
		{"aggressive", "low", "style", true}, // inválido → default assertive
	}
	for _, c := range cases {
		f := Finding{Severity: c.severity, Category: c.category}
		if got := isPublishable(c.profile, f); got != c.want {
			t.Errorf("isPublishable(%q, %s/%s) = %v, querés %v",
				c.profile, c.severity, c.category, got, c.want)
		}
	}
}

// El generador de pruebas (F4): pasa el hallazgo, el framework y los hunks
// al LLM y devuelve la prueba; salida vacía es error (el caller la salta).
func TestRunTestGenerator(t *testing.T) {
	d := newDeps()
	d.gw.testgen = []string{"```go\nfunc TestX(t *testing.T) {}\n```"}
	f := Finding{File: "main.go", Line: 3, Severity: "high", Category: "security", Body: "inyección SQL"}

	out, err := runTestGenerator(context.Background(), d.gw, f, "hunks de main.go", "go test")
	if err != nil {
		t.Fatalf("runTestGenerator: %v", err)
	}
	if !strings.Contains(out, "func TestX") {
		t.Errorf("salida: %q", out)
	}
	prompt := d.gw.testgenSeen[0]
	for _, want := range []string{"main.go:3", "inyección SQL", "go test", "```diff", "hunks de main.go"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("el prompt debe traer %q:\n%s", want, prompt)
		}
	}

	// La corrección sugerida, si existe, viaja en el prompt.
	d.gw.testgen = []string{"```go\nfunc TestY(t *testing.T) {}\n```"}
	f.Suggestion = "db.Query(q, arg)"
	if _, err := runTestGenerator(context.Background(), d.gw, f, "", "go test"); err != nil {
		t.Fatalf("runTestGenerator con sugerencia: %v", err)
	}
	if !strings.Contains(d.gw.testgenSeen[1], "db.Query(q, arg)") {
		t.Errorf("el prompt debe traer la corrección sugerida:\n%s", d.gw.testgenSeen[1])
	}

	// Salida vacía → error: el caller la salta sin publicar basura.
	d.gw.testgen = []string{"   "}
	if _, err := runTestGenerator(context.Background(), d.gw, f, "", "go test"); err == nil {
		t.Error("la salida vacía debe fallar")
	}
}

// gitRepo arma un repo temporal con historia lineal (base → head: el
// merge-base es el commit base) y devuelve el clon y ambos SHAs.
func gitRepo(t *testing.T, baseFiles, headFiles map[string]string) (workdir, baseSHA, headSHA string) {
	t.Helper()
	workdir = t.TempDir()
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", workdir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(files map[string]string) {
		for p, c := range files {
			full := filepath.Join(workdir, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
			run("add", p)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@codeowl")
	run("config", "user.name", "codeowl-test")
	write(baseFiles)
	run("commit", "-qm", "base")
	baseSHA = run("rev-parse", "HEAD")
	write(headFiles)
	run("commit", "-qm", "head")
	headSHA = run("rev-parse", "HEAD")
	return workdir, baseSHA, headSHA
}

// La config efectiva entra al hash de la clave de cache (§9.6: dos configs
// distintas son corridas distintas) — cada campo, por separado.
func TestCacheKeyEffectiveConfig(t *testing.T) {
	input := ReviewInput{HeadSHA: "h", BaseSHA: "b", Workdir: "w"}
	base := repoconfig.RepoConfig{Language: "es", Profile: "assertive"}

	k := cacheKey(input, "mb", DefaultConfig(), base)
	if again := cacheKey(input, "mb", DefaultConfig(), base); again != k {
		t.Fatal("la misma config efectiva debe dar la misma clave (estable)")
	}

	mutaciones := []repoconfig.RepoConfig{
		{Language: "en", Profile: "assertive"},
		{Language: "es", Profile: "strict"},
		{Language: "es", Profile: "assertive", PathFilters: []string{"backend/**"}},
		{Language: "es", Profile: "assertive", Instructions: "cuidá los panics"},
	}
	for i, m := range mutaciones {
		if other := cacheKey(input, "mb", DefaultConfig(), m); other == k {
			t.Errorf("mutación %d (%+v) no cambió la clave de cache", i, m)
		}
	}
}

// Los tres prompts con idioma traen el hueco y fillLanguage lo llena todos.
func TestFillLanguage(t *testing.T) {
	for name, p := range map[string]string{
		"reviewer":   prompts.Reviewer(),
		"summarizer": prompts.Summarizer(),
		"chat":       prompts.Chat(),
	} {
		if !strings.Contains(p, languagePlaceholder) {
			t.Errorf("el prompt %s debería traer el hueco %s", name, languagePlaceholder)
		}
		filled := fillLanguage(p, "pt")
		if strings.Contains(filled, languagePlaceholder) {
			t.Errorf("fillLanguage dejó el hueco sin llenar en %s", name)
		}
		if !strings.Contains(filled, "pt") {
			t.Errorf("el prompt %s lleno debería mencionar el idioma pt", name)
		}
	}
}

// fillInstructions llena el hueco de reglas del repo con el bloque
// delimitado, o lo vacía sin dejar bloque ni residuo (§9.5).
func TestFillInstructions(t *testing.T) {
	p := prompts.Reviewer()
	if !strings.Contains(p, instructionsPlaceholder) {
		t.Fatalf("el prompt del reviewer debería traer el hueco %s", instructionsPlaceholder)
	}

	empty := fillInstructions(p, "   \n")
	if strings.Contains(empty, instructionsPlaceholder) ||
		strings.Contains(empty, "Reglas de revisión de este repositorio") {
		t.Errorf("instructions vacío no debe dejar bloque ni residuo:\n%s", empty)
	}

	filled := fillInstructions(p, "Cuidá los panics silenciosos.")
	if !strings.Contains(filled, "## Reglas de revisión de este repositorio") ||
		!strings.Contains(filled, "Cuidá los panics silenciosos.") {
		t.Errorf("el bloque de reglas del repo no quedó delimitado con el texto:\n%s", filled)
	}
	if strings.Contains(filled, instructionsPlaceholder) {
		t.Errorf("residuo de %s tras llenar", instructionsPlaceholder)
	}
}

// fillNits agrega la regla de nits solo con perfil strict (§6 F3): con otro
// perfil, ni regla ni residuo.
func TestFillNits(t *testing.T) {
	p := prompts.Reviewer()
	if !strings.Contains(p, nitsPlaceholder) {
		t.Fatalf("el prompt del reviewer debería traer el hueco %s", nitsPlaceholder)
	}

	got := fillNits(p, "strict")
	if !strings.Contains(got, "reportá también nits") {
		t.Errorf("con strict el prompt debe pedir nits")
	}
	if strings.Contains(got, nitsPlaceholder) {
		t.Errorf("residuo de %s tras llenar", nitsPlaceholder)
	}
	for _, perfil := range []string{"chill", "assertive", ""} {
		got := fillNits(p, perfil)
		if strings.Contains(got, "reportá también nits") || strings.Contains(got, nitsPlaceholder) {
			t.Errorf("con perfil %q no debe haber regla de nits ni residuo", perfil)
		}
	}
}
