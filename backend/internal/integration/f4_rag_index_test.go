//go:build integration

// Test de integración F4 e2e (mapa: backend.internal/integration — F4): la
// cadena completa del índice simbólico RAG contra la store real — Fase A:
// index.Index (chunking simbólico filtrado por review.yaml → embeddings del
// gateway real contra un stub OpenAI-compatible → upsert en repo_index con
// presupuesto y resume por file_hash); Fase B: retrieval → Reviewer con el
// bloque "Símbolos relacionados" del repo y anclas de dedup simbólicas
// (comments_sent.anchor) que sobreviven una segunda corrida. Nada toca
// internet: el LLM es un stub httptest (embeddings one-hot deterministas +
// chat por marcador), el extractor y el analyzer son stubs. Requiere
// DATABASE_URL — sin ella el test se salta, igual que el resto.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
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

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/index"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// f4PRNumber es el número del PR de la corrida F4 (repo propio: sin
// colisión con los PRs de los demás tests de integración).
const f4PRNumber = int64(401)

// f4Dims es la dims del vector del DDL (vector(1536), §3.3): la que el
// catálogo devuelve y la que el stub de embeddings sirve.
const f4Dims = 1536

// reviewYAMLF4 es el review.yaml del fixture: path_filters que excluyen los
// *_test.go (§9.5, decisión 9 del doc F4) — el índice jamás debe contener
// filas de main_test.go.
const reviewYAMLF4 = "path_filters:\n  - \"!*_test.go\"\n"

// Contenido de los archivos del fixture F4 (el clon de la rama default).
const (
	f4Main = `package main

import "fmt"

func main() {
	fmt.Println("hola")
	saluda()
}

func saluda() {
	fmt.Println("chau")
}
`
	f4Util = `package main

func saludaFuerte() string {
	return "HOLA"
}

func susurra() string {
	return "chau"
}
`
	f4Test = `package main

import "testing"

func TestSaluda(t *testing.T) {
	_ = t
}
`
)

// f4Symbols son los símbolos que los stubs reportan para el fixture (el CLI
// real con tree-sitter se prueba en analyzer/src; acá importa la integración
// store + gateway + índice/review). "main" cubre la línea 3 del diff del
// reviewer: el hallazgo ancla al símbolo, no al dígito.
func f4Symbols() []analyze.Symbol {
	return []analyze.Symbol{
		{File: "main.go", Symbol: "main", Kind: "function", StartLine: 1, EndLine: 7},
		{File: "main.go", Symbol: "saluda", Kind: "function", StartLine: 9, EndLine: 11},
		{File: "util.go", Symbol: "saludaFuerte", Kind: "function", StartLine: 3, EndLine: 5},
		{File: "util.go", Symbol: "susurra", Kind: "function", StartLine: 7, EndLine: 9},
		{File: "main_test.go", Symbol: "TestSaluda", Kind: "function", StartLine: 4, EndLine: 6},
	}
}

// f4Extractor satisface index.Extractor con los símbolos conocidos.
type f4Extractor struct{}

func (f4Extractor) ExtractSymbols(context.Context, string) ([]analyze.Symbol, error) {
	return f4Symbols(), nil
}

// f4Analyzer satisface review.Analyzer (SAST vacío: la corrida queda
// success) y review.SymbolExtractor — el pipeline resuelve la ancla
// simbólica del hallazgo (§6 F4).
type f4Analyzer struct{}

func (f4Analyzer) Run(context.Context, string) (*analyze.AnalysisResult, error) {
	return &analyze.AnalysisResult{
		FilesAnalyzed: 1,
		LintersRun:    []analyze.LinterStatus{{Name: "stub", Status: "ok"}},
		Findings:      []analyze.Finding{},
	}, nil
}

func (f4Analyzer) ExtractSymbols(_ context.Context, _ string) ([]analyze.Symbol, error) {
	return f4Symbols(), nil
}

// f4Vec genera el embedding determinista del texto: one-hot de f4Dims con el
// 1 en hash(texto) % f4Dims — coseno 0 entre textos distintos y estable
// entre corridas: siembra y consulta comparan en el mismo espacio.
func f4Vec(text string) []float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	v := make([]float64, f4Dims)
	v[h.Sum64()%f4Dims] = 1
	return v
}

// f4StubLLM arma el ÚNICO server stub de F4: /v1/embeddings (vectores
// one-hot deterministas) y /v1/chat/completions enrutado por marcador
// (Reviewer/Summarizer), grabando los prompts del Reviewer para las
// aserciones del bloque de contexto.
type f4StubLLM struct {
	srv      *httptest.Server
	mu       sync.Mutex
	reviewer []string
}

func newF4StubLLM(t *testing.T) *f4StubLLM {
	t.Helper()
	s := &f4StubLLM{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/embeddings":
			var req struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "request inválido", http.StatusBadRequest)
				return
			}
			data := make([]map[string]any, len(req.Input))
			for i, in := range req.Input {
				data[i] = map[string]any{"index": i, "embedding": f4Vec(in)}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/v1/chat/completions":
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
			user := req.Messages[len(req.Messages)-1].Content
			switch {
			case strings.HasPrefix(user, reviewerMarker):
				s.mu.Lock()
				s.reviewer = append(s.reviewer, user)
				s.mu.Unlock()
				content = reviewerOut
			case strings.HasPrefix(user, summarizerMarker):
				content = summarizerOut
			default:
				http.Error(w, "prompt de agente desconocido", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`, content)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *f4StubLLM) reviewerCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reviewer...)
}

// newF4FixtureRepo arma el repo git que hace de clon de la rama default: un
// commit con main.go, util.go, main_test.go y review.yaml (path_filters que
// excluyen los *_test.go). index.Index resuelve HEAD por su cuenta.
func newF4FixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
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
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("escribiendo %s del fixture: %v", name, err)
		}
	}
	run("init")
	write("main.go", f4Main)
	write("util.go", f4Util)
	write("main_test.go", f4Test)
	write("review.yaml", reviewYAMLF4)
	run("add", ".")
	run("-c", "commit.gpgsign=false", "-c", "user.name=integration",
		"-c", "user.email=integration@test", "commit", "-m", "base del fixture F4")
	return dir
}

// f4SeedRepo conecta un repo de prueba y su proveedor de embeddings sobre el
// stub; devuelve el repo y registra la limpieza (repo_index incluido).
func f4SeedRepo(t *testing.T, st *store.Store, masterKey []byte, stubURL string) store.Repository {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixNano()
	repo, err := st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs:        "github",
		ExternalID: now,
		Owner:      "test",
		Name:       fmt.Sprintf("repo-f4-%d", now),
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	encKey, err := store.Encrypt(masterKey, []byte("sk-stub-integration"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	provider, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl: stubURL, Model: "stub-embed-model", ApiKey: encKey,
		Role: "embedding", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider (embedding): %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(ctx, "DELETE FROM repo_index WHERE repository_id = $1", repo.ID); err != nil {
			t.Errorf("limpieza repo_index: %v", err)
		}
		cleanupRows(t, st, repo.ID, provider.ID, stubURL, nil)
	})
	return repo
}

// f4Rows lee el índice del repo (file, symbol, kind, dims, modelo) ordenado.
type f4Row struct {
	file, symbol, kind, model string
	dims                      int32
}

func f4Rows(t *testing.T, st *store.Store, repoID int64) []f4Row {
	t.Helper()
	rows, err := st.Pool.Query(context.Background(),
		`SELECT file, symbol, kind, dims, embedding_model
		 FROM repo_index WHERE repository_id = $1 ORDER BY file, start_line`, repoID)
	if err != nil {
		t.Fatalf("leyendo repo_index: %v", err)
	}
	defer rows.Close()
	var out []f4Row
	for rows.Next() {
		var r f4Row
		if err := rows.Scan(&r.file, &r.symbol, &r.kind, &r.dims, &r.model); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// TestF4IndexRAGIndexacion (Fase A): index.Index contra la store real con el
// gateway real apuntando al stub de embeddings — presupuesto por corrida con
// corte ENTRE archivos (Truncated), resume por file_hash en la corrida
// siguiente, anotación de embedding_model + dims del catálogo y ausencia del
// archivo excluido por path_filters.
func TestF4IndexRAGIndexacion(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	masterKey := make([]byte, 32) // clave AES-256 de test

	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "request inválido", http.StatusBadRequest)
			return
		}
		data := make([]map[string]any, len(req.Input))
		for i, in := range req.Input {
			data[i] = map[string]any{"index": i, "embedding": f4Vec(in)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(embedSrv.Close)

	repo := f4SeedRepo(t, st, masterKey, embedSrv.URL)
	gateway := llm.New(st, masterKey, llm.Limits{
		MaxPerReview: 2, MaxGlobal: 2, Timeout: 5 * time.Second, MaxRetries: 1,
	})
	workdir := newF4FixtureRepo(t)

	// Run 1: presupuesto de 2 símbolos → main.go entra entero (2), util.go
	// (2 símbolos) queda PARA EL PRÓXIMO job: corte entre archivos, flag
	// Truncated (§9.6).
	res, err := index.Index(ctx, st, gateway, f4Extractor{}, repo, workdir, index.Config{MaxSymbolsPerRun: 2})
	if err != nil {
		t.Fatalf("index.Index (run 1): %v", err)
	}
	if !res.Truncated || res.FilesIndexed != 1 || res.SymbolsIndexed != 2 {
		t.Errorf("run 1: querés Truncated con 1 archivo y 2 símbolos, fue %+v", res)
	}
	rows := f4Rows(t, st, repo.ID)
	if len(rows) != 2 {
		t.Fatalf("run 1: querés 2 filas (solo main.go), hay %d: %+v", len(rows), rows)
	}

	// Run 2: presupuesto completo → retoma (§9.6): util.go indexado,
	// main.go saltado por resume (hash + modelo iguales).
	res, err = index.Index(ctx, st, gateway, f4Extractor{}, repo, workdir, index.Config{})
	if err != nil {
		t.Fatalf("index.Index (run 2): %v", err)
	}
	if res.Truncated || res.FilesIndexed != 1 || res.FilesSkipped != 1 || res.SymbolsIndexed != 2 {
		t.Errorf("run 2: querés reanudar 1 y saltar 1, fue %+v", res)
	}

	// Run 3: todo al día → resume puro, cero escrituras.
	res, err = index.Index(ctx, st, gateway, f4Extractor{}, repo, workdir, index.Config{})
	if err != nil {
		t.Fatalf("index.Index (run 3): %v", err)
	}
	if res.FilesIndexed != 0 || res.FilesSkipped != 2 || res.SymbolsIndexed != 0 {
		t.Errorf("run 3: querés todo saltado, fue %+v", res)
	}

	// Estado final del índice: 4 filas, modelo anotado, dims del catálogo y
	// main_test.go ausente (path_filters del review.yaml del HEAD, §9.5).
	rows = f4Rows(t, st, repo.ID)
	if len(rows) != 4 {
		t.Fatalf("querés 4 filas (2 de main.go + 2 de util.go), hay %d: %+v", len(rows), rows)
	}
	vistas := map[string]bool{}
	for _, r := range rows {
		if r.file == "main_test.go" {
			t.Errorf("main_test.go debe quedar fuera por path_filters: %+v", r)
		}
		if r.model != "stub-embed-model" {
			t.Errorf("embedding_model debe anotar el modelo del proveedor: %+v", r)
		}
		if r.dims != f4Dims {
			t.Errorf("dims debe ser la del catálogo (%d): %+v", f4Dims, r)
		}
		vistas[r.file+"/"+r.symbol] = true
	}
	for _, want := range []string{"main.go/main", "main.go/saluda", "util.go/saludaFuerte", "util.go/susurra"} {
		if !vistas[want] {
			t.Errorf("falta el símbolo %s en el índice: %+v", want, rows)
		}
	}
}

// TestF4RetrievalReviewContextYAnclas (Fase B): con el índice sembrado, la
// review corre con jobs.NewIndexRetriever real — el prompt del Reviewer
// lleva el bloque "Símbolos relacionados" con los símbolos sembrados, el
// inline publicado ancla al SÍMBOLO contenedor (no al dígito) y una segunda
// corrida sobre el mismo hallazgo deduplica sin republicar.
func TestF4RetrievalReviewContextYAnclas(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	masterKey := make([]byte, 32) // clave AES-256 de test

	llmStub := newF4StubLLM(t)
	repo := f4SeedRepo(t, st, masterKey, llmStub.srv.URL)

	// El Reviewer habla por el rol review contra el MISMO stub (ruteo por
	// marcador); el retrieval usa el rol embedding del seed.
	encKey, err := store.Encrypt(masterKey, []byte("sk-stub-integration"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	providerReview, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl: llmStub.srv.URL, Model: "stub-model", ApiKey: encKey,
		Role: "review", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider (review): %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(ctx, "DELETE FROM llm_providers WHERE id = $1", providerReview.ID); err != nil {
			t.Errorf("limpieza del proveedor review: %v", err)
		}
	})

	gateway := llm.New(st, masterKey, llm.Limits{
		MaxPerReview: 4, MaxGlobal: 4, Timeout: 5 * time.Second, MaxRetries: 1,
	})

	// Sembrar el índice con la misma mecánica del IndexJob (T4): el fixture
	// índice main.go y util.go (main_test.go queda fuera por path_filters).
	if _, err := index.Index(ctx, st, gateway, f4Extractor{}, repo, newF4FixtureRepo(t), index.Config{}); err != nil {
		t.Fatalf("sembrando el índice: %v", err)
	}

	// El PR existe antes de la corrida con la identidad del clon: el chequeo
	// stale (§3.6.1.3) compara contra esta fila.
	workdir, headSHA := newTempClone(t)
	pr, err := st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID,
		Number:       f4PRNumber,
		Author:       "dev-integration",
		State:        "open",
		HeadSha:      headSHA,
		BaseRef:      "main",
		BaseSha:      fakeBase,
		CreatedAt:    pgtype.Timestamptz{Time: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}

	// Retriever REAL de producción: el mismo que cablea el worker (T7).
	retr := jobs.NewIndexRetriever(st, gateway, 8)
	vcsStub := &stubVCS{diff: diffFixture}
	input := review.ReviewInput{
		PullRequestID: pr.ID,
		RepositoryID:  repo.ID,
		HeadSHA:       headSHA,
		BaseSHA:       fakeBase,
		Workdir:       workdir,
	}

	res, err := review.Run(ctx, review.DefaultConfig(), st, gateway, f4Analyzer{}, vcsStub, retr, input)
	if err != nil {
		t.Fatalf("review.Run: %v", err)
	}
	if res.Status != review.StatusSuccess || res.FindingsCount != 1 {
		t.Fatalf("corrida 1: querés success con 1 hallazgo publicado, fue %+v", res)
	}

	// 1. El prompt del Reviewer llevó el bloque de contexto con los símbolos
	// sembrados (util.go — main.go, el archivo bajo revisión, se excluye).
	calls := llmStub.reviewerCalls()
	if len(calls) != 1 {
		t.Fatalf("el Reviewer debe correr UNA vez (solo main.go en el diff), corrió %d", len(calls))
	}
	prompt := calls[0]
	if !strings.Contains(prompt, "Símbolos relacionados del repositorio") {
		t.Errorf("el prompt debe llevar el bloque de contexto (§6 F4): %q", prompt)
	}
	if !strings.Contains(prompt, "util.go") || !strings.Contains(prompt, "saludaFuerte") {
		t.Errorf("el bloque debe traer los símbolos sembrados de util.go: %q", prompt)
	}

	// 2. El inline publicado ancla al SÍMBOLO contenedor (la línea 3 cae
	// dentro del span de "main") — no al dígito de siempre.
	sent, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "inline",
	})
	if err != nil {
		t.Fatalf("GetCommentsSentByPRAndType: %v", err)
	}
	if len(sent) != 1 || sent[0].Anchor.String != "main" || sent[0].File.String != "main.go" {
		t.Errorf("comments_sent inline debe llevar ancla simbólica \"main\": %+v", sent)
	}

	// 3. Segunda corrida con la misma identidad: el hallazgo deduplica por
	// el ancla simbólica (§6 F4, el payoff del anclaje) — cero inlines nuevos.
	res2, err := review.Run(ctx, review.DefaultConfig(), st, gateway, f4Analyzer{}, vcsStub, retr, input)
	if err != nil {
		t.Fatalf("review.Run (corrida 2): %v", err)
	}
	if res2.FindingsCount != 0 {
		t.Errorf("corrida 2: el hallazgo debe deduplicar, publicó %d", res2.FindingsCount)
	}
	sent2, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "inline",
	})
	if err != nil {
		t.Fatalf("GetCommentsSentByPRAndType (corrida 2): %v", err)
	}
	if len(sent2) != 1 {
		t.Errorf("corrida 2 no debe agregar filas inline, hay %d: %+v", len(sent2), sent2)
	}
	if _, inlines := vcsStub.snapshot(); len(inlines) != 1 {
		t.Errorf("el VCS debe haber recibido UN solo inline en total, recibió %d", len(inlines))
	}
}
