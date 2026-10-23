//go:build integration

// Test de integración F6 e2e (mapa: backend.internal/integration — F6): la
// corrida completa de revisión con los cuatro gaps de CodeRabbit activos:
// SlopDetector (rol cheap) publica findings categoría `slop`, el Summarizer
// agrega release_notes como sección del resumen, y el triage extendido
// persiste effort_score (siempre) e impact_score/change_stack (null sin
// proveedor de grafo — degradación visible, §9.6). LLM stub por markers,
// VCS y analyzer stubs, store real. Requiere DATABASE_URL — sin ella el
// test se salta, igual que el resto.
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

	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// f6PRNumber es el número del PR de F6 (repo propio: sin colisión).
const f6PRNumber = int64(601)

// Espejos de los markers de review/agents.go (mismo criterio que
// f5PremergeMarker): los stubs enrutan por el prefijo del prompt de usuario.
const (
	f6VerifierMarker = "Verificá los siguientes hallazgos."
	f6SlopMarker     = "Detectá código de relleno (slop) en el siguiente diff."
)

// Salidas del stub LLM para la corrida F6: UN hallazgo del Reviewer
// (main.go:3, con sugerencia — anclable al diff fixture), UN hallazgo slop
// del SlopDetector (main.go:6, con sugerencia) y el veredicto del Verifier
// para el lote del único archivo (2 hallazgos, ambos corroborados).
const (
	f6ReviewerOut = `[{"line":3,"severity":"medium","category":"logic","body":"la constante del saludo queda sin declarar.","suggestion":"const saludo = \"hola mundo\""}]`

	f6SlopOut = `[{"file":"main.go","line":6,"severity":"low","body":"wrapper que solo delega en fmt.Println sin agregar valor.","suggestion":"fmt.Println(\"hola mundo\")"}]`

	f6VerifierOut = `[
		{"index": 0, "verified": true, "reason": "el hunk muestra la constante sin declarar"},
		{"index": 1, "verified": true, "reason": "el hunk muestra la delegación directa"}
	]`

	f6SummarizerOut = `{"summary":"Stub: agrega el import fmt y ajusta el saludo.",` +
		`"release_notes":"- El saludo ahora es configurable vía constante."}`
)

// f6StubLLM es el stub OpenAI-compatible de F6: routing por markers para
// Reviewer, Summarizer, Verifier (rol cheap) y SlopDetector (rol cheap).
type f6StubLLM struct {
	srv *httptest.Server
}

func newF6StubLLM(t *testing.T, reviewer, slop, verifier, summarizer string) *f6StubLLM {
	t.Helper()
	s := &f6StubLLM{}
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
		case strings.HasPrefix(last, f6VerifierMarker):
			content = verifier
		case strings.HasPrefix(last, f6SlopMarker):
			content = slop
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

// f6SeedCheapProvider agrega un proveedor del rol cheap sobre el mismo stub
// (el Verifier y el SlopDetector lo consumen) con su propia limpieza.
func f6SeedCheapProvider(t *testing.T, st *store.Store, stubURL string) {
	t.Helper()
	encKey, err := store.Encrypt(make([]byte, 32), []byte("stub-cheap-key"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	provider, err := st.CreateLlmProvider(context.Background(), store.CreateLlmProviderParams{
		BaseUrl: stubURL, Model: "stub-model", ApiKey: encKey,
		Role: "cheap", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider(cheap): %v", err)
	}
	t.Cleanup(func() {
		if err := st.DeleteLlmProvider(context.Background(), provider.ID); err != nil {
			t.Logf("limpiando el proveedor cheap: %v", err)
		}
	})
}

// TestF6GapsEnLaCorrida (§6 F6 e2e): la corrida completa publica el hallazgo
// slop como inline (con sugerencia), trae la sección de release notes en el
// resumen, persiste effort_score y deja impact_score/change_stack null sin
// proveedor de grafo — degradación visible, nunca silenciosa (§9.6).
func TestF6GapsEnLaCorrida(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	stub := newF6StubLLM(t, f6ReviewerOut, f6SlopOut, f6VerifierOut, f6SummarizerOut)
	repo, _, _, _ := f5SeedRun(t, st, stub.srv.URL)
	f6SeedCheapProvider(t, st, stub.srv.URL)

	workdir, headSHA := newTempClone(t)
	pr, err := st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: f6PRNumber,
		Author: "dev-integration", State: "open",
		HeadSha: headSHA, BaseRef: "main", BaseSha: fakeBase,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}

	gateway := llm.New(st, make([]byte, 32), llm.Limits{
		MaxPerReview: 8, MaxGlobal: 8, Timeout: 10 * time.Second, MaxRetries: 1,
	})
	vcsStub := &stubVCS{diff: diffFixture}
	res, err := review.Run(ctx, review.DefaultConfig(), st, gateway, stubAnalyzer{}, vcsStub, nil, nil,
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

	// Slop publicado como inline con sugerencia (mismo pipeline que el LLM).
	_, inlines := vcsStub.snapshot()
	if len(inlines) != 2 {
		t.Errorf("inlines publicados = %d, querés 2 (reviewer + slop): %+v", len(inlines), inlines)
	}

	// Release notes en el resumen final (§6 F6): sección del Summarizer.
	summaries, _ := vcsStub.snapshot()
	if len(summaries) != 2 {
		t.Fatalf("PostSummary debe correr 2 veces (dos fases), corrió %d", len(summaries))
	}
	final := summaries[len(summaries)-1]
	if !strings.Contains(final, "### Notas de lanzamiento (borrador)") ||
		!strings.Contains(final, "El saludo ahora es configurable") {
		t.Errorf("el resumen final no trae las release notes:\n%s", final)
	}
	// Con proveedor cheap presente, NINGUNA omisión se declara.
	if strings.Contains(final, "Detección de slop omitida") ||
		strings.Contains(final, "Verificación de hallazgos omitida") {
		t.Errorf("no debe declararse omisión con proveedor cheap presente:\n%s", final)
	}

	// El finding slop queda persistido, verificado por el Verifier (§3.3).
	findings, err := st.ListFindingsByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("ListFindingsByPR: %v", err)
	}
	var slop *store.Finding
	for i := range findings {
		if findings[i].Category == "slop" {
			slop = &findings[i]
		}
	}
	if slop == nil {
		t.Fatalf("no hay finding categoría slop: %+v", findings)
	}
	if slop.Source != "llm" || slop.Severity != "low" || !slop.Verified.Valid || !slop.Verified.Bool {
		t.Errorf("finding slop = source=%s severity=%s verified=%v, querés llm/low/true",
			slop.Source, slop.Severity, slop.Verified)
	}

	// Triage extendido (§6 F6): effort siempre; impact y change_stack null
	// sin proveedor de grafo — la columna NULL es la degradación visible.
	pr, err = st.GetPullRequest(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if !pr.EffortScore.Valid || pr.EffortScore.Int32 < 0 || pr.EffortScore.Int32 > 100 {
		t.Errorf("effort_score debe quedar persistido en 0-100: %+v", pr.EffortScore)
	}
	if pr.ImpactScore.Valid {
		t.Errorf("impact_score debe ser NULL sin proveedor de grafo: %+v", pr.ImpactScore)
	}
	if len(pr.ChangeStack) > 0 {
		t.Errorf("change_stack debe ser NULL sin proveedor de grafo: %q", pr.ChangeStack)
	}
}

// TestF6SlopOmitidoSinCheap (§6 F6/§9.6 e2e): sin proveedor del rol cheap el
// SlopDetector no corre y la re-edición declara la omisión — nunca ausencia
// silenciosa. El resto de la corrida sigue success.
func TestF6SlopOmitidoSinCheap(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	stub := newF6StubLLM(t, f6ReviewerOut, f6SlopOut, f6VerifierOut, f6SummarizerOut)
	repo, _, _, _ := f5SeedRun(t, st, stub.srv.URL)

	workdir, headSHA := newTempClone(t)
	pr, err := st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: f6PRNumber + 1,
		Author: "dev-integration", State: "open",
		HeadSha: headSHA, BaseRef: "main", BaseSha: fakeBase,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}

	gateway := llm.New(st, make([]byte, 32), llm.Limits{
		MaxPerReview: 8, MaxGlobal: 8, Timeout: 10 * time.Second, MaxRetries: 1,
	})
	vcsStub := &stubVCS{diff: diffFixture}
	res, err := review.Run(ctx, review.DefaultConfig(), st, gateway, stubAnalyzer{}, vcsStub, nil, nil,
		review.ReviewInput{
			PullRequestID: pr.ID, RepositoryID: repo.ID,
			HeadSHA: headSHA, BaseSHA: fakeBase, Workdir: workdir,
		})
	if err != nil {
		t.Fatalf("review.Run: %v", err)
	}
	if res.Status != review.StatusSuccess {
		t.Fatalf("corrida: querés success (la omisión no degrada, §9.6), fue %+v", res)
	}

	summaries, inlines := vcsStub.snapshot()
	if len(inlines) != 1 {
		t.Errorf("sin slop, inline publicado = %d, querés 1 (solo reviewer)", len(inlines))
	}
	final := summaries[len(summaries)-1]
	if !strings.Contains(final, "Detección de slop omitida (sin proveedor del rol cheap)") {
		t.Errorf("la re-edición final debe declarar la omisión del slop:\n%s", final)
	}
	if strings.Contains(final, "Notas de lanzamiento") {
		t.Errorf("sin proveedor cheap el summarizer igual corrió: las release notes deben estar:\n%s", final)
	}

	findings, err := st.ListFindingsByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("ListFindingsByPR: %v", err)
	}
	for _, f := range findings {
		if f.Category == "slop" {
			t.Errorf("no debe persistirse ningún finding slop sin proveedor cheap: %+v", f)
		}
	}
}
