// Tests de contrato de Retrieve con stubs (§4.5: sin BD, sin podman, sin
// LLM real — misma mecánica que index_test.go, cuyo stubGateway se reutiliza).
package index

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Garantía de wiring en compilación: *store.Store satisface RetrieveStore
// (§4.5 — T7 obtiene el Retriever del wiring de workers).
var _ RetrieveStore = (*store.Store)(nil)

// stubRetrieveStore es la store en memoria del subconjunto RetrieveStore,
// con grabación de los params de ambas queries para asertar el wiring.
type stubRetrieveStore struct {
	count        int64                                   // CountRepoIndexByRepo
	dims         int32                                   // typmod del catálogo (default 1536)
	hits         []store.SearchRepoIndexBySimilarityRow  // filas del top-K coseno
	files        []string                                // universo indexado (ListRepoIndexFiles)
	syms         []store.ListRepoIndexByFilesRow         // símbolos por archivo (ListRepoIndexByFiles)
	searchParams store.SearchRepoIndexBySimilarityParams // params recibidos
	listParams   store.ListRepoIndexByFilesParams        // params recibidos
}

func newStubRetrieveStore() *stubRetrieveStore { return &stubRetrieveStore{count: 1, dims: 1536} }

func (s *stubRetrieveStore) CountRepoIndexByRepo(_ context.Context, _ int64) (int64, error) {
	return s.count, nil
}

func (s *stubRetrieveStore) GetRepoIndexDims(_ context.Context) (int32, error) { return s.dims, nil }

func (s *stubRetrieveStore) SearchRepoIndexBySimilarity(_ context.Context, arg store.SearchRepoIndexBySimilarityParams) ([]store.SearchRepoIndexBySimilarityRow, error) {
	s.searchParams = arg
	return s.hits, nil
}

func (s *stubRetrieveStore) ListRepoIndexFiles(_ context.Context, _ int64) ([]store.ListRepoIndexFilesRow, error) {
	rows := make([]store.ListRepoIndexFilesRow, len(s.files))
	for i, f := range s.files {
		rows[i] = store.ListRepoIndexFilesRow{File: f}
	}
	return rows, nil
}

func (s *stubRetrieveStore) ListRepoIndexByFiles(_ context.Context, arg store.ListRepoIndexByFilesParams) ([]store.ListRepoIndexByFilesRow, error) {
	s.listParams = arg
	return s.syms, nil
}

// Happy path (decisión 6): top-K coseno devuelve a.go con import "pkg"; el
// universo indexado tiene pkg/b.go (Dir("pkg/b.go") == "pkg" → matchea) y
// other.go (no matchea). La expansión trae los símbolos de pkg/b.go con
// Via=import, después de los hits, en orden determinista.
func TestRetrieveHappyPath(t *testing.T) {
	st := newStubRetrieveStore()
	st.hits = []store.SearchRepoIndexBySimilarityRow{
		{File: "pkg/a.go", Symbol: "Alpha", Kind: "func", StartLine: 1, EndLine: 3, Imports: []string{"pkg"}},
		{File: "zeta.go", Symbol: "Zed", Kind: "func", StartLine: 9, EndLine: 10},
	}
	st.files = []string{"pkg/a.go", "pkg/b.go", "other.go"}
	st.syms = []store.ListRepoIndexByFilesRow{
		{File: "pkg/b.go", Symbol: "Zeta", Kind: "type", StartLine: 20, EndLine: 25},
		{File: "pkg/b.go", Symbol: "Beta", Kind: "func", StartLine: 10, EndLine: 12},
	}
	gw := &stubGateway{}

	got, err := Retrieve(context.Background(), st, gw, 7, "pkg/a.go", "hunks del diff", 8)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	want := []RelatedSymbol{
		{File: "pkg/a.go", Symbol: "Alpha", Kind: "func", StartLine: 1, EndLine: 3, Imports: []string{"pkg"}, Via: ViaSimilar},
		{File: "zeta.go", Symbol: "Zed", Kind: "func", StartLine: 9, EndLine: 10, Via: ViaSimilar},
		{File: "pkg/b.go", Symbol: "Beta", Kind: "func", StartLine: 10, EndLine: 12, Via: ViaImport},
		{File: "pkg/b.go", Symbol: "Zeta", Kind: "type", StartLine: 20, EndLine: 25, Via: ViaImport},
	}
	if len(got) != len(want) {
		t.Fatalf("cantidad de símbolos: got %d want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("símbolo %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}

	// El embedding fue UNA llamada con el texto file+"\n"+code.
	if len(gw.calls) != 1 {
		t.Fatalf("llamadas Embed: got %d want 1", len(gw.calls))
	}
	// Expansión solo con pkg/b.go: other.go no matchea, a.go excluido (hit),
	// zeta.go no está en el universo indexado.
	if len(st.listParams.Files) != 1 || st.listParams.Files[0] != "pkg/b.go" {
		t.Errorf("archivos de expansión: got %v want [pkg/b.go]", st.listParams.Files)
	}
	if st.listParams.RepositoryID != 7 {
		t.Errorf("RepositoryID de expansión: got %d want 7", st.listParams.RepositoryID)
	}
}

// Índice vacío → (nil, nil) SIN llamar al gateway (§6 F4: silencioso).
func TestRetrieveIndiceVacio(t *testing.T) {
	st := newStubRetrieveStore()
	st.count = 0
	gw := &stubGateway{}

	got, err := Retrieve(context.Background(), st, gw, 1, "a.go", "hunks", 8)
	if err != nil {
		t.Fatalf("Retrieve con índice vacío: %v", err)
	}
	if got != nil {
		t.Errorf("resultado con índice vacío: got %v want nil", got)
	}
	if len(gw.calls) != 0 {
		t.Errorf("Embed con índice vacío NO debe llamarse: %d llamadas", len(gw.calls))
	}
}

// La query embebida es exactamente file+"\n"+code (contrato §6 F4, decisión 6).
func TestRetrieveTextoDeQuery(t *testing.T) {
	st := newStubRetrieveStore()
	gw := &stubGateway{}

	if _, err := Retrieve(context.Background(), st, gw, 1, "main.go", "func Main() {}", 8); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(gw.calls) != 1 || len(gw.calls[0]) != 1 {
		t.Fatalf("Embed: got %v want 1 llamada con 1 texto", gw.calls)
	}
	if got := gw.calls[0][0]; got != "main.go\nfunc Main() {}" {
		t.Errorf("texto embebido: got %q want %q", got, "main.go\nfunc Main() {}")
	}
}

// topK <= 0 → default 8 (§9.6): el RowLimit del search lo evidencia.
func TestRetrieveTopKDefault(t *testing.T) {
	for _, topK := range []int{0, -3} {
		st := newStubRetrieveStore()
		if _, err := Retrieve(context.Background(), st, &stubGateway{}, 1, "a.go", "x", topK); err != nil {
			t.Fatalf("Retrieve(topK=%d): %v", topK, err)
		}
		if st.searchParams.RowLimit != 8 {
			t.Errorf("topK=%d: RowLimit got %d want 8", topK, st.searchParams.RowLimit)
		}
	}
}

// El error del gateway PROPAGA (T7 decide degradar sin contexto, §9.6).
func TestRetrieveErrorEmbed(t *testing.T) {
	st := newStubRetrieveStore()
	gw := &stubGateway{err: errors.New("proveedor caído")}

	if _, err := Retrieve(context.Background(), st, gw, 1, "a.go", "x", 8); err == nil {
		t.Fatal("error de Embed debe propagar")
	}
}

// Pre-check de dims (§3.3): proveedor con dims distintas al catálogo →
// ErrDimsMismatch tipado, antes del SQL.
func TestRetrieveDimsMismatch(t *testing.T) {
	st := newStubRetrieveStore()
	st.dims = 1536
	gw := &stubGateway{dims: 4}

	_, err := Retrieve(context.Background(), st, gw, 1, "a.go", "x", 8)
	if !errors.Is(err, ErrDimsMismatch) {
		t.Fatalf("dims 4 vs catálogo 1536: got %v want ErrDimsMismatch", err)
	}
}

// Tope de expansión (decisión 6): topK*2 filas, cortadas tras el orden
// determinista (quedan los primeros por (file, start_line)).
func TestRetrieveTopeExpansion(t *testing.T) {
	st := newStubRetrieveStore()
	st.hits = []store.SearchRepoIndexBySimilarityRow{
		{File: "a.go", Symbol: "Alpha", Kind: "func", StartLine: 1, EndLine: 2, Imports: []string{"pkg"}},
	}
	st.files = []string{"pkg/f1.go", "pkg/f2.go", "pkg/f3.go", "pkg/f4.go", "pkg/f5.go", "pkg/f6.go"}
	for i := 1; i <= 6; i++ {
		st.syms = append(st.syms, store.ListRepoIndexByFilesRow{
			File: "pkg/f" + string(rune('0'+i)) + ".go", Symbol: "Sym", Kind: "func",
			StartLine: int32(i), EndLine: int32(i + 1),
		})
	}

	got, err := Retrieve(context.Background(), st, &stubGateway{}, 1, "a.go", "x", 2)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	// 1 hit + tope de expansión 2*2=4 (de 6 filas expandidas).
	if len(got) != 5 {
		t.Fatalf("filas: got %d want 5 (1 similar + 4 de tope): %+v", len(got), got)
	}
	if got[0].Via != ViaSimilar {
		t.Errorf("primera fila: got Via=%q want %q", got[0].Via, ViaSimilar)
	}
	for i, r := range got[1:] {
		wantFile := "pkg/f" + string(rune('0'+i+1)) + ".go"
		if r.File != wantFile || r.Via != ViaImport {
			t.Errorf("expansión %d: got %s(Via=%s) want %s(Via=%s)", i, r.File, r.Via, wantFile, ViaImport)
		}
	}
}

// Regla v1 de matching de imports (§6 F4, decisión 6) — tabla unitaria.
func TestMatchImport(t *testing.T) {
	cases := []struct {
		name string
		imp  string
		file string
		want bool
	}{
		{"ruta exacta", "b.go", "b.go", true},
		{"sufijo de subdirectorio", "store/store.go", "internal/store/store.go", true},
		{"import de paquete/directorio", "pkg", "pkg/b.go", true},
		{"import de paquete anidado", "internal/b", "internal/b/b.go", true},
		{"sin match: nombre suelto", "other", "pkg/other.go", false},
		{"sin match: stdlib", "fmt", "cmd/main.go", false},
		{"sin match: prefijo no-sufijo", "pkg", "pkgx/y.go", false},
		{"degenerado: punto matchea raíz", ".", "x.go", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchImport(tc.imp, tc.file); got != tc.want {
				t.Errorf("matchImport(%q, %q) = %v, want %v", tc.imp, tc.file, got, tc.want)
			}
		})
	}
}
