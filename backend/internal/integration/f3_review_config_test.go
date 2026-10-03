//go:build integration

// Test de integración F3 (mapa: backend.internal/integration — F3): el
// pipeline de review respeta la config efectiva de review.yaml del
// merge-base y el Verifier de punta a punta, contra la store real. El repo
// fixture es git de verdad: la base trae review.yaml (perfil chill,
// path_filters e instructions) y el head toca un archivo excluido y uno
// revisado. Nada toca internet: el LLM es un stub OpenAI-compatible que
// enruta por marcador (Reviewer/Verifier/Summarizer) y graba los prompts,
// el VCS de publicación y el analyzer son stubs. Requiere DATABASE_URL —
// sin ella el test se salta, igual que el resto de los tests de integración.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// f3PRNumber es el número del PR de la corrida F3 (repo propio del test:
// sin colisión con los PRs de los demás tests de integración).
const f3PRNumber = int64(301)

// verifierMarkerF3 enruta la respuesta del stub para el Verifier: prefijo
// del prompt de usuario (verifierUserMarker en review/agents.go — no
// exportado, misma constante literal).
const verifierMarkerF3 = "Verificá los siguientes hallazgos."

// reviewYAMLF3 es el review.yaml del commit base del repo fixture: idioma y
// perfil que pisan los defaults (§9.5), path_filters que excluyen los
// *_test.go e instructions reconocibles — el system prompt del Reviewer debe
// contenerlas y main_test.go jamás debe llegarle (§9.5/§6 F3).
const reviewYAMLF3 = `language: es
profile: chill
path_filters:
  - "!*_test.go"
instructions: |
  Prestá atención especial al manejo de errores de los handlers HTTP.
`

// Contenido de los archivos del fixture: la base trae main.go, main_test.go
// y review.yaml; el head cambia ambos archivos (solo main.go pasa los
// path_filters). El diff del stub refleja exactamente base...head.
const (
	f3MainBase = `package main

func main() {
	fmt.Println("hola")
}
`
	f3MainHead = `package main

import "fmt"

func main() {
	fmt.Println("hola mundo")
}
`
	f3TestBase = `package main

func TestHola(t *testing.T) {
	_ = t
}
`
	f3TestHead = `package main

import "testing"

func TestHola(t *testing.T) {
	_ = t
}
`
)

// diffF3 es el diff base...head que sirve el VCS stub: main.go (revisado) y
// main_test.go (excluido por path_filters). Los anchors de los hallazgos del
// Reviewer caen en líneas visibles del lado RIGHT de main.go.
const diffF3 = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,5 +1,7 @@
 package main
 
+import "fmt"
+
 func main() {
-	fmt.Println("hola")
+	fmt.Println("hola mundo")
 }
diff --git a/main_test.go b/main_test.go
index 3333333..4444444 100644
--- a/main_test.go
+++ b/main_test.go
@@ -1,5 +1,7 @@
 package main
 
+import "testing"
+
 func TestHola(t *testing.T) {
 	_ = t
 }
`

// reviewerF3Out es la salida JSON del Reviewer para main.go: dos high logic
// (uno declara en el body que es un falso positivo — el marcador que el stub
// del Verifier usa para confirmarlo), una low (chill la filtra por
// severidad) y una medium style (chill la filtra por categoría, §6 F3).
// Todas persisten — auditoría — pero solo las publicables llegan al inline.
const reviewerF3Out = `[
 {"line":3,"severity":"high","category":"logic","body":"Hallazgo que el verifier confirma como falso positivo: la línea no introduce ningún riesgo real.","suggestion":"// sin cambio necesario"},
 {"line":6,"severity":"high","category":"logic","body":"fmt.Println descarta el error devuelto: la corrida sigue en silencio ante un fallo de escritura.","suggestion":"if _, err := fmt.Println(\"hola mundo\"); err != nil { return err }"},
 {"line":4,"severity":"low","category":"logic","body":"El import podría moverse dentro de la función para reducir el alcance.","suggestion":""},
 {"line":5,"severity":"medium","category":"style","body":"El nombre de la función no sigue la convención del repo.","suggestion":""}
]`

// ---------------------------------------------------------------------------
// Stub LLM con captura de prompts
// ---------------------------------------------------------------------------

// f3Prompts es una llamada capturada del stub: system + user tal como los
// vio el gateway (§9.5: el sistema lleva idioma/instructions/nits llenos).
type f3Prompts struct{ system, user string }

// f3StubLLM arma el servidor OpenAI-compatible de F3: enruta por el
// prefijo del prompt de usuario (Reviewer "Archivo: ", Verifier
// verifierMarkerF3, Summarizer) y graba los prompts del Reviewer y del
// Verifier para las aserciones de config efectiva.
type f3StubLLM struct {
	srv      *httptest.Server
	mu       sync.Mutex
	reviewer []f3Prompts
	verifier []f3Prompts
}

func newF3StubLLM(t *testing.T) *f3StubLLM {
	t.Helper()
	s := &f3StubLLM{}
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) < 2 {
			http.Error(w, "request inválido", http.StatusBadRequest)
			return
		}
		var content string
		user := req.Messages[len(req.Messages)-1].Content
		switch {
		case strings.HasPrefix(user, reviewerMarker):
			s.mu.Lock()
			s.reviewer = append(s.reviewer, f3Prompts{system: req.Messages[0].Content, user: user})
			s.mu.Unlock()
			content = reviewerF3Out
		case strings.HasPrefix(user, verifierMarkerF3):
			s.mu.Lock()
			s.verifier = append(s.verifier, f3Prompts{system: req.Messages[0].Content, user: user})
			s.mu.Unlock()
			content = f3Verdicts(user)
		case strings.HasPrefix(user, summarizerMarker):
			content = summarizerOut
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

func (s *f3StubLLM) url() string { return s.srv.URL }

func (s *f3StubLLM) reviewerCalls() []f3Prompts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]f3Prompts(nil), s.reviewer...)
}

func (s *f3StubLLM) verifierCalls() []f3Prompts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]f3Prompts(nil), s.verifier...)
}

// f3Verdict arma la salida del Verifier para el lote del prompt: confirma
// como falso positivo todo hallazgo cuyo body lo declara (marcador "falso
// positivo") y verifica el resto. Parsear el lote real mantiene la salida
// válida ante cualquier orden (§9.8: un veredicto por hallazgo, mismo index).
func f3Verdicts(userPrompt string) string {
	const lote = "Hallazgos a verificar:\n```json\n"
	start := strings.Index(userPrompt, lote)
	if start < 0 {
		return "[]"
	}
	rest := userPrompt[start+len(lote):]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		return "[]"
	}
	var fs []struct {
		Index int    `json:"index"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(rest[:end]), &fs); err != nil {
		return "[]"
	}
	type veredicto struct {
		Index    int    `json:"index"`
		Verified bool   `json:"verified"`
		Reason   string `json:"reason"`
	}
	out := make([]veredicto, 0, len(fs))
	for _, f := range fs {
		fp := strings.Contains(strings.ToLower(f.Body), "falso positivo")
		reason := "verificado contra el diff: el problema es real."
		if fp {
			reason = "la línea no introduce riesgo: falso positivo confirmado."
		}
		out = append(out, veredicto{Index: f.Index, Verified: !fp, Reason: reason})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Repo fixture
// ---------------------------------------------------------------------------

// newF3FixtureRepo arma el repo git del test: commit base con main.go,
// main_test.go y review.yaml (el merge-base del que el pipeline lee la
// config, §9.5) + commit head que toca ambos archivos. Devuelve el workdir,
// el SHA de la base y el del head — la identidad de la corrida.
func newF3FixtureRepo(t *testing.T) (dir, baseSHA, headSHA string) {
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
	commit := func(msg string) string {
		run("add", ".")
		run("-c", "commit.gpgsign=false", "-c", "user.name=integration",
			"-c", "user.email=integration@test", "commit", "-m", msg)
		return strings.TrimSpace(run("rev-parse", "HEAD"))
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("escribiendo %s del fixture: %v", name, err)
		}
	}
	run("init")
	write("main.go", f3MainBase)
	write("main_test.go", f3TestBase)
	write("review.yaml", reviewYAMLF3)
	baseSHA = commit("base: main.go, main_test.go y review.yaml")
	write("main.go", f3MainHead)
	write("main_test.go", f3TestHead)
	headSHA = commit("head: toca main.go y main_test.go")
	return dir, baseSHA, headSHA
}

// ---------------------------------------------------------------------------
// Test E2E F3
// ---------------------------------------------------------------------------

// TestF3ReviewConfigPipeline recorre el pipeline F3 contra la store real:
// review.yaml del merge-base (perfil chill + path_filters + instructions,
// §9.5/§6 F3) gobierna qué mira el Reviewer y qué se publica; el Verifier
// (rol cheap, §3.3/§6 F3) marca los hallazgos verificados y los falsos
// positivos confirmados no se publican — pero todo persiste (auditoría).
func TestF3ReviewConfigPipeline(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	// -- Setup: repo + PR + proveedores LLM (review y cheap) sobre el stub --
	masterKey := make([]byte, 32) // clave AES-256 de test

	now := time.Now().UnixNano()
	repo, err := st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs:        "github",
		ExternalID: now,
		Owner:      "test",
		Name:       fmt.Sprintf("repo-f3-%d", now),
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	// El PR existe antes de la corrida con la identidad del fixture: el
	// chequeo stale (§3.6.1.3) compara contra esta fila.
	workdir, baseSHA, headSHA := newF3FixtureRepo(t)
	pr, err := st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID,
		Number:       f3PRNumber,
		Author:       "dev-integration",
		State:        "open",
		HeadSha:      headSHA,
		BaseRef:      "main",
		BaseSha:      baseSHA,
		// created_at es NOT NULL sin default: el webhook lo trae del payload
		// (§3.3) — el upsert directo necesita el mismo dato.
		CreatedAt: pgtype.Timestamptz{Time: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}

	llmStub := newF3StubLLM(t)
	encKey, err := store.Encrypt(masterKey, []byte("sk-stub-integration"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	providerReview, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl: llmStub.url(), Model: "stub-model", ApiKey: encKey,
		Role: "review", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider (review): %v", err)
	}
	// El Verifier corre contra el rol cheap (§9.6): mismo stub, otra fila.
	providerCheap, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl: llmStub.url(), Model: "stub-model", ApiKey: encKey,
		Role: "cheap", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider (cheap): %v", err)
	}
	t.Cleanup(func() {
		cleanupRows(t, st, repo.ID, providerReview.ID, llmStub.url(), nil)
		if _, err := st.Pool.Exec(ctx, "DELETE FROM llm_providers WHERE id = $1", providerCheap.ID); err != nil {
			t.Errorf("limpieza del proveedor cheap: %v", err)
		}
	})

	gateway := llm.New(st, masterKey, llm.Limits{
		MaxPerReview: 2, MaxGlobal: 2, Timeout: 5 * time.Second, MaxRetries: 1,
	})
	vcsStub := &stubVCS{diff: diffF3}

	res, err := review.Run(ctx, review.DefaultConfig(), st, gateway, stubAnalyzer{}, vcsStub, nil,
		review.ReviewInput{
			PullRequestID: pr.ID,
			RepositoryID:  repo.ID,
			HeadSHA:       headSHA,
			BaseSHA:       baseSHA,
			Workdir:       workdir,
		})
	if err != nil {
		t.Fatalf("review.Run: %v", err)
	}

	// -- 1. Desenlace de la corrida ------------------------------------------
	if res.Status != review.StatusSuccess {
		summaries, _ := vcsStub.snapshot()
		t.Fatalf("status = %q, querés %q (resúmenes publicados: %q)", res.Status, review.StatusSuccess, summaries)
	}
	if res.FindingsCount != 1 {
		t.Errorf("FindingsCount = %d, querés 1 (solo el high verificado queda publicado)", res.FindingsCount)
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

	// -- 2. Auditoría en BD: TODOS los hallazgos persisten con el estado
	// verified del Verifier (§6 F3). Lo filtrado por el perfil queda null
	// (jamás llegó al verifier); lo verificado lleva el veredicto.
	findings, err := st.ListFindingsByReview(ctx, rev.ID)
	if err != nil {
		t.Fatalf("ListFindingsByReview: %v", err)
	}
	if len(findings) != 4 {
		t.Errorf("la auditoría debe tener 4 hallazgos (2 high + low + style), tiene %d: %+v", len(findings), findings)
	}
	for _, f := range findings {
		if f.Source != review.SourceLLM || f.File != "main.go" {
			t.Errorf("finding inesperado: %+v", f)
			continue
		}
		switch {
		case f.Category == "logic" && f.Line == 3:
			// Falso positivo confirmado: persiste con verified=false (§6 F3).
			if !f.Verified.Valid || f.Verified.Bool {
				t.Errorf("el falso positivo confirmado debe quedar verified=false: %+v", f)
			}
		case f.Category == "logic" && f.Line == 6:
			// Publicado: persiste con verified=true.
			if !f.Verified.Valid || !f.Verified.Bool {
				t.Errorf("el hallazgo publicado debe quedar verified=true: %+v", f)
			}
		case (f.Category == "logic" && f.Line == 4), (f.Category == "style" && f.Line == 5):
			// Filtrados por chill (severidad low / categoría style): quedan
			// en la auditoría con verified null (§6 F3).
			if f.Verified.Valid {
				t.Errorf("lo filtrado por el perfil debe quedar verified null: %+v", f)
			}
		default:
			t.Errorf("finding inesperado: %+v", f)
		}
	}

	// -- 3. Publicación: el conjunto del perfil MENOS el falso positivo
	// confirmado (§6 F3) — un solo inline, main.go:6 RIGHT.
	sent, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "inline",
	})
	if err != nil {
		t.Fatalf("GetCommentsSentByPRAndType: %v", err)
	}
	if len(sent) != 1 || !sent[0].File.Valid || sent[0].File.String != "main.go" ||
		sent[0].Category.String != "logic" || sent[0].Anchor.String != "6" {
		t.Errorf("comments_sent inline incorrecto: %+v", sent)
	}

	summaries, inlines := vcsStub.snapshot()
	if len(summaries) != 2 {
		t.Errorf("PostSummary llamado %d veces, querés 2 (dos fases)", len(summaries))
	}
	if len(inlines) != 1 || inlines[0].File != "main.go" || inlines[0].Line != 6 || inlines[0].Side != vcs.SideRight {
		t.Errorf("inline incorrecto: %+v, querés main.go:6 RIGHT", inlines)
	}

	// El resumen provisional no lleva recuento; la re-edición de fase 2 lleva
	// el del conjunto publicado (§3.6.2/§6 F3): 1 alta — no el total auditado.
	if len(summaries) == 2 {
		if strings.Contains(summaries[0], "### Hallazgos:") {
			t.Errorf("el resumen provisional no debe llevar recuento: %q", summaries[0])
		}
		if !strings.Contains(summaries[1], "### Hallazgos: 1 alta · 0 media · 0 baja") {
			t.Errorf("la re-edición debe llevar el recuento del conjunto publicado: %q", summaries[1])
		}
		if strings.Contains(summaries[1], "⚠️ Verificación") {
			t.Errorf("el verifier corrió: la re-edición no debe declarar su omisión: %q", summaries[1])
		}
	}

	// -- 4. Prompts vistos por el stub: la config efectiva aplicada (§9.5) --
	calls := llmStub.reviewerCalls()
	if len(calls) != 1 {
		t.Fatalf("el Reviewer debe correr UNA vez (solo main.go pasa los path_filters), corrió %d", len(calls))
	}
	rp := calls[0]
	if !strings.Contains(rp.user, "Archivo: main.go") || strings.Contains(rp.user, "main_test.go") {
		t.Errorf("el Reviewer solo debe ver main.go (path_filters, §9.5): %q", rp.user)
	}
	if !strings.Contains(rp.system, "Reglas de revisión de este repositorio") ||
		!strings.Contains(rp.system, "manejo de errores de los handlers HTTP") {
		t.Errorf("el system del Reviewer debe traer las instructions del review.yaml: %q", rp.system)
	}
	if !strings.Contains(rp.system, "en es: ese es el idioma") {
		t.Errorf("el system del Reviewer debe traer el idioma efectivo del review.yaml: %q", rp.system)
	}
	if strings.Contains(rp.system, "reportá también nits") {
		t.Errorf("el perfil chill no debe activar el bloque de nits: %q", rp.system)
	}

	vcalls := llmStub.verifierCalls()
	if len(vcalls) != 1 {
		t.Fatalf("el Verifier debe correr UNA vez (main.go), corrió %d", len(vcalls))
	}
	vp := vcalls[0]
	if !strings.Contains(vp.user, verifierMarkerF3) || !strings.Contains(vp.user, "Archivo: main.go") {
		t.Errorf("prompt de usuario del Verifier incorrecto: %q", vp.user)
	}
	if !strings.Contains(vp.user, `"line": 3`) || !strings.Contains(vp.user, `"line": 6`) {
		t.Errorf("el Verifier debe ver los dos hallazgos high publicables: %q", vp.user)
	}
	// Solo verifica el conjunto publicado: lo filtrado por chill jamás llega.
	if strings.Contains(vp.user, "podría moverse") || strings.Contains(vp.user, "convención del repo") {
		t.Errorf("el Verifier no debe ver lo filtrado por el perfil: %q", vp.user)
	}
}
