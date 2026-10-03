// Tests de contrato del índice con stubs (mapa: backend.index — sin podman
// ni LLM real, §4.5: stubs de pocas líneas sobre mocks). La store es un stub
// en memoria al estilo de review_test.go; repoconfig corre REAL contra un
// clon git de juguete (path_filters del review.yaml del tip — §6 F4).
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Garantía de wiring en compilación (T6 las cablea): los tipos concretos
// satisfacen las interfaces locales de index (§4.5).
var (
	_ Store     = (*store.Store)(nil)
	_ Gateway   = (*llm.Gateway)(nil)
	_ Extractor = (*analyze.Runner)(nil)
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// stubStore es la store en memoria: solo el subconjunto index.Store.
type stubStore struct {
	mu         sync.Mutex
	providers  []store.LlmProvider             // cola del rol embedding
	dims       int32                           // typmod del catálogo (default 1536)
	indexFiles []store.ListRepoIndexFilesRow   // estado previo del índice
	wiped      bool                            // DeleteRepoIndexByRepo recibido
	deleted    [][]string                      // archivos borrados por re-index
	rows       []store.CreateRepoIndexRowParams // filas creadas
}

func newStubStore() *stubStore { return &stubStore{dims: 1536} }

func (s *stubStore) ListEnabledLlmProvidersByRole(_ context.Context, role string) ([]store.LlmProvider, error) {
	if role != roleEmbedding {
		return nil, nil
	}
	return s.providers, nil
}

func (s *stubStore) GetRepoIndexDims(_ context.Context) (int32, error) { return s.dims, nil }

// GetRepoIndexEmbeddingModel: el modelo de las filas previas; sin filas →
// pgx.ErrNoRows (primera indexación).
func (s *stubStore) GetRepoIndexEmbeddingModel(_ context.Context, _ int64) (string, error) {
	if len(s.indexFiles) == 0 {
		return "", pgx.ErrNoRows
	}
	return s.indexFiles[0].EmbeddingModel, nil
}

func (s *stubStore) ListRepoIndexFiles(_ context.Context, _ int64) ([]store.ListRepoIndexFilesRow, error) {
	return s.indexFiles, nil
}

func (s *stubStore) DeleteRepoIndexByRepo(_ context.Context, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wiped = true
	s.indexFiles = nil
	return nil
}

func (s *stubStore) DeleteRepoIndexByRepoAndFiles(_ context.Context, arg store.DeleteRepoIndexByRepoAndFilesParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, arg.Files)
	return nil
}

func (s *stubStore) CreateRepoIndexRow(_ context.Context, arg store.CreateRepoIndexRowParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, arg)
	return int64(len(s.rows)), nil
}

// stubGateway graba los textos y devuelve vectores fijos de dims configurables.
type stubGateway struct {
	mu    sync.Mutex
	calls [][]string // textos por llamada Embed
	dims  int
	err   error
}

func (g *stubGateway) Embed(_ context.Context, texts []string) ([][]float32, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, texts)
	if g.err != nil {
		return nil, g.err
	}
	d := g.dims
	if d == 0 {
		d = 1536
	}
	vecs := make([][]float32, len(texts))
	for i := range vecs {
		v := make([]float32, d)
		v[0] = 0.5 // determinista; el valor no importa para el contrato
		vecs[i] = v
	}
	return vecs, nil
}

// stubExtractor devuelve el fixture fijo (o error).
type stubExtractor struct {
	syms []analyze.Symbol
	err  error
}

func (e *stubExtractor) ExtractSymbols(_ context.Context, _ string) ([]analyze.Symbol, error) {
	return e.syms, e.err
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// aGoContent/bPyContent son los archivos del clon de juguete: el extractor
// "extrajo" sus símbolos y el hash de contenido debe salir de estos bytes.
const aGoContent = "package a\n\nfunc Alpha() {}\n\ntype Beta struct{}\n"
const bPyContent = "class Solo:\n    pass\n"

// fixtureSymbols es la salida del extractor: duplicado exacto (dedupe), kind
// fuera del conjunto cerrado (filtrado) y dos archivos.
func fixtureSymbols() []analyze.Symbol {
	return []analyze.Symbol{
		{File: "src/a.go", Symbol: "Alpha", Kind: "function", StartLine: 3, EndLine: 3, Imports: []string{"fmt"}},
		{File: "src/a.go", Symbol: "Alpha", Kind: "function", StartLine: 3, EndLine: 3, Imports: []string{"fmt"}},
		{File: "src/a.go", Symbol: "Beta", Kind: "type", StartLine: 6, EndLine: 6},
		{File: "src/a.go", Symbol: "Ghost", Kind: "macro", StartLine: 9, EndLine: 9},
		{File: "b.py", Symbol: "Solo", Kind: "class", StartLine: 1, EndLine: 2},
	}
}

// gitWorkdir crea un clon mínimo con los archivos dados (git init + commit):
// Index resuelve HEAD para leer el review.yaml del tip (§6 F4).
func gitWorkdir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@codeowl.local")
	run("config", "user.name", "codeowl-test")
	run("add", ".")
	run("commit", "-qm", "fixture")
	return dir
}

// contentHash es el SHA-256 hex del contenido (el mismo esquema que Index).
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func embeddingProviders() []store.LlmProvider {
	return []store.LlmProvider{
		{ID: 1, BaseUrl: "https://uno.example.com", Model: "embed-1", Role: roleEmbedding, Priority: 1, Enabled: true},
		{ID: 2, BaseUrl: "https://dos.example.com", Model: "embed-2", Role: roleEmbedding, Priority: 2, Enabled: true},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Flujo completo: 2 archivos indexados, dedupe y kind inválido filtrados,
// anotación del proveedor de MÁXIMA prioridad, hash real, imports pasan y
// el texto embebido usa el formato compartido.
func TestIndexFullFlow(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"src/a.go": aGoContent, "b.py": bPyContent})
	st := newStubStore()
	st.providers = embeddingProviders()
	gw := &stubGateway{}

	res, err := Index(context.Background(), st, gw, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42, Language: "es"}, dir, Config{MaxSymbolsPerRun: 100, DefaultProfile: "assertive"})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if res.FilesIndexed != 2 || res.SymbolsIndexed != 3 || res.FilesSkipped != 0 || res.Truncated {
		t.Fatalf("Result = %+v, want 2/3/0 sin truncar", res)
	}

	if len(st.rows) != 3 {
		t.Fatalf("filas = %d, want 3", len(st.rows))
	}
	for _, r := range st.rows {
		if r.RepositoryID != 42 || r.EmbeddingModel != "embed-1" || r.Dims != 1536 {
			t.Errorf("anotación de %s/%s incorrecta: %+v", r.File, r.Symbol, r)
		}
		if r.Column7 == "" || r.Column7[0] != '[' {
			t.Errorf("embedding de %s/%s no es literal pgvector: %q", r.File, r.Symbol, r.Column7)
		}
	}
	byKey := map[string]store.CreateRepoIndexRowParams{}
	for _, r := range st.rows {
		byKey[r.File + "/" + r.Symbol] = r
	}
	// Dedupe: Alpha aparece UNA vez; Ghost (kind fuera del conjunto) filtrado.
	if _, ok := byKey["src/a.go/Ghost"]; ok {
		t.Error("kind fuera del conjunto cerrado debe filtrarse")
	}
	alpha := byKey["src/a.go/Alpha"]
	if alpha.Kind != "function" || alpha.StartLine != 3 || alpha.EndLine != 3 || alpha.FileHash != contentHash(aGoContent) {
		t.Errorf("Alpha mal persistido: %+v", alpha)
	}
	if len(alpha.Column10) != 1 || alpha.Column10[0] != "fmt" {
		t.Errorf("imports de Alpha = %v, want [fmt]", alpha.Column10)
	}
	if solo := byKey["b.py/Solo"]; solo.Kind != "class" || solo.Column10 != nil {
		t.Errorf("Solo mal persistido: %+v (imports nil esperado)", solo)
	}

	// Una llamada Embed por archivo, con el formato compartido.
	if len(gw.calls) != 2 {
		t.Fatalf("llamadas Embed = %d, want 2 (una por archivo)", len(gw.calls))
	}
	textos := map[string][]string{}
	for _, call := range gw.calls {
		if len(call) > 0 {
			textos[call[0]] = call
		}
	}
	if len(textos) != 2 {
		t.Errorf("formato de texto inesperado: %v", gw.calls)
	}
	if got := textos["src/a.go\nfunction Alpha"]; len(got) != 2 || got[1] != "src/a.go\ntype Beta" {
		t.Errorf("textos de src/a.go = %v", got)
	}
	if got := textos["b.py\nclass Solo"]; len(got) != 1 {
		t.Errorf("textos de b.py = %v", got)
	}

	// Re-index por archivo: delete de las filas viejas ANTES de insertar.
	if len(st.deleted) != 2 {
		t.Errorf("deletes por archivo = %v, want 2", st.deleted)
	}
}

// path_filters del review.yaml del tip (§6 F4/§9.5): el archivo fuera de los
// filtros no llega al índice — repoconfig corre real.
func TestIndexPathFiltersDrop(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{
		"src/a.go":    aGoContent,
		"docs/x.py":   bPyContent,
		"review.yaml": "language: es\npath_filters:\n  - \"src/**\"\n",
	})
	st := newStubStore()
	st.providers = embeddingProviders()

	res, err := Index(context.Background(), st, &stubGateway{}, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42, Language: "es"}, dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if res.FilesIndexed != 1 || res.SymbolsIndexed != 2 {
		t.Fatalf("Result = %+v, want solo src/a.go (2 símbolos)", res)
	}
	for _, r := range st.rows {
		if r.File != "src/a.go" {
			t.Errorf("docs/x.py debe quedar fuera de los path_filters: %+v", r)
		}
	}
}

// Filtrar TODO no es error: corrida completa con cero filas y cero Embed.
func TestIndexAllFilteredOut(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{
		"a.go":        aGoContent,
		"review.yaml": "path_filters:\n  - \"docs/**\"\n",
	})
	st := newStubStore()
	st.providers = embeddingProviders()
	gw := &stubGateway{}

	res, err := Index(context.Background(), st, gw, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42}, dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Index() con todo filtrado no debe fallar: %v", err)
	}
	if res.FilesIndexed != 0 || res.SymbolsIndexed != 0 || len(gw.calls) != 0 {
		t.Errorf("Result = %+v, llamadas = %d; want cero todo", res, len(gw.calls))
	}
}

// Resume-skip (§9.6): hash igual + mismo modelo → se salta; hash distinto →
// se re-indexa.
func TestIndexResumeSkip(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"src/a.go": aGoContent, "b.py": bPyContent})
	st := newStubStore()
	st.providers = embeddingProviders()
	st.indexFiles = []store.ListRepoIndexFilesRow{
		{File: "src/a.go", FileHash: contentHash(aGoContent), EmbeddingModel: "embed-1"}, // al día
		{File: "b.py", FileHash: "hash-viejo", EmbeddingModel: "embed-1"},                // cambió el contenido
	}
	gw := &stubGateway{}

	res, err := Index(context.Background(), st, gw, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42}, dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if res.FilesSkipped != 1 || res.FilesIndexed != 1 || res.SymbolsIndexed != 1 {
		t.Fatalf("Result = %+v, want skip=1 index=1 syms=1 (Solo)", res)
	}
	for _, r := range st.rows {
		if r.File == "src/a.go" {
			t.Errorf("src/a.go estaba al día y se re-indexó: %+v", r)
		}
	}
}

// Presupuesto (§9.6): el corte es ENTRE archivos en orden lexicográfico —
// b.py entra, src/a.go ya no y queda marcado para el próximo IndexJob.
func TestIndexBudgetTruncation(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"src/a.go": aGoContent, "b.py": bPyContent})
	st := newStubStore()
	st.providers = embeddingProviders()
	gw := &stubGateway{}

	res, err := Index(context.Background(), st, gw, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42}, dir, Config{MaxSymbolsPerRun: 1})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if !res.Truncated || res.FilesIndexed != 1 || res.SymbolsIndexed != 1 {
		t.Fatalf("Result = %+v, want truncado con 1 archivo/1 símbolo", res)
	}
	if len(st.rows) != 1 || st.rows[0].File != "b.py" {
		t.Errorf("solo b.py (primer archivo en orden) debe indexarse: %+v", st.rows)
	}
}

// Cambio de modelo (§9.6): índice con otro embedding_model → wipe completo
// y re-index de TODO (el resume-skip no aplica tras el wipe).
func TestIndexModelChangeWipes(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"src/a.go": aGoContent, "b.py": bPyContent})
	st := newStubStore()
	st.providers = embeddingProviders()
	st.indexFiles = []store.ListRepoIndexFilesRow{
		{File: "src/a.go", FileHash: contentHash(aGoContent), EmbeddingModel: "modelo-viejo"},
		{File: "b.py", FileHash: contentHash(bPyContent), EmbeddingModel: "modelo-viejo"},
	}
	gw := &stubGateway{}

	res, err := Index(context.Background(), st, gw, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42}, dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if !st.wiped {
		t.Fatal("cambio de modelo debe disparar DeleteRepoIndexByRepo (§9.6)")
	}
	if res.FilesSkipped != 0 || res.FilesIndexed != 2 || res.SymbolsIndexed != 3 {
		t.Fatalf("Result = %+v, want re-index completo 2/3 sin skips", res)
	}
	for _, r := range st.rows {
		if r.EmbeddingModel != "embed-1" {
			t.Errorf("fila con modelo viejo tras el wipe: %+v", r)
		}
	}
}

// Sin proveedor del rol embedding: sentinela tipado, cero llamadas.
func TestIndexNoEmbeddingProvider(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"src/a.go": aGoContent})
	st := newStubStore()
	gw := &stubGateway{}

	_, err := Index(context.Background(), st, gw, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42}, dir, DefaultConfig())
	if !errors.Is(err, ErrNoEmbeddingProvider) {
		t.Fatalf("Index() error = %v, want ErrNoEmbeddingProvider", err)
	}
	if len(gw.calls) != 0 {
		t.Errorf("sin proveedor no debe llamarse Embed: %v", gw.calls)
	}
}

// Backstop de dims (§3.3): el proveedor sirve 768 dims contra vector(1536)
// del catálogo → ErrDimsMismatch antes del INSERT, cero filas.
func TestIndexDimsMismatch(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"src/a.go": aGoContent})
	st := newStubStore()
	st.providers = embeddingProviders()
	gw := &stubGateway{dims: 768}

	_, err := Index(context.Background(), st, gw, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42}, dir, DefaultConfig())
	if !errors.Is(err, ErrDimsMismatch) {
		t.Fatalf("Index() error = %v, want ErrDimsMismatch", err)
	}
	if len(st.rows) != 0 {
		t.Errorf("dims inválidas no deben persistir filas: %d", len(st.rows))
	}
}

// Fallo del extractor: error de infraestructura reintentable, con contexto.
func TestIndexExtractorError(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"src/a.go": aGoContent})
	st := newStubStore()
	st.providers = embeddingProviders()

	_, err := Index(context.Background(), st, &stubGateway{},
		&stubExtractor{err: errors.New("podman murió")}, store.Repository{ID: 42}, dir, DefaultConfig())
	if err == nil || !strings.Contains(err.Error(), "extrayendo símbolos") {
		t.Fatalf("Index() debe propagar el error del extractor con contexto, got %v", err)
	}
}

// Archivo desaparecido entre el sandbox y el hash: se salta con registro y
// los demás archivos se indexan (§6 F4: un archivo malo no frena nada).
func TestIndexMissingFileSkipped(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"b.py": bPyContent}) // src/a.go NO existe
	st := newStubStore()
	st.providers = embeddingProviders()

	res, err := Index(context.Background(), st, &stubGateway{}, &stubExtractor{syms: fixtureSymbols()},
		store.Repository{ID: 42}, dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if res.FilesSkipped != 1 || res.FilesIndexed != 1 || res.SymbolsIndexed != 1 {
		t.Fatalf("Result = %+v, want skip=1 (src/a.go fantasma) e index=1", res)
	}
}

// Ruta fuera del clon (defensa en el límite de confianza): se rechaza.
func TestIndexPathEscapeSkipped(t *testing.T) {
	dir := gitWorkdir(t, map[string]string{"b.py": bPyContent})
	st := newStubStore()
	st.providers = embeddingProviders()
	syms := []analyze.Symbol{
		{File: "b.py", Symbol: "Solo", Kind: "class", StartLine: 1, EndLine: 2},
		{File: "../escape.go", Symbol: "X", Kind: "function", StartLine: 1, EndLine: 1},
	}

	res, err := Index(context.Background(), st, &stubGateway{}, &stubExtractor{syms: syms},
		store.Repository{ID: 42}, dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	for _, r := range st.rows {
		if r.File == "../escape.go" {
			t.Errorf("ruta fuera del clon no debe indexarse: %+v", r)
		}
	}
	if res.FilesSkipped != 1 || res.FilesIndexed != 1 {
		t.Errorf("el archivo con escape debe omitirse y b.py indexarse, got %+v", res)
	}
}

// El formato del texto embebido es contrato compartido con Retrieve (T5):
// congelado acá.
func TestSymbolQueryText(t *testing.T) {
	got := SymbolQueryText("src/a.go", "method", "Do")
	if got != "src/a.go\nmethod Do" {
		t.Errorf("SymbolQueryText = %q, want %q", got, "src/a.go\nmethod Do")
	}
}

// El literal de pgvector: forma idéntica al helper riVecLiteral de store.
func TestVecLiteral(t *testing.T) {
	if got := vecLiteral([]float32{0.1, -2, 3}); got != "[0.1,-2,3]" {
		t.Errorf("vecLiteral = %q, want %q", got, "[0.1,-2,3]")
	}
}
