// Tests de reachability inversa del grafo de imports (§6 F6, T3): Dependents
// con un stub en memoria de la store. Los tres modos de matchImport (ruta
// exacta, sufijo, directorio) quedan cubiertos — la semántica es la MISMA
// que la expansión de Retrieve (§6 F4).
package index

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// stubImpactStore sirve las filas (file, imports) fijas del índice, o error.
type stubImpactStore struct {
	rows []store.ListRepoIndexImportsRow
	err  error
}

func (s *stubImpactStore) ListRepoIndexImports(_ context.Context, _ int64) ([]store.ListRepoIndexImportsRow, error) {
	return s.rows, s.err
}

// grafoFixture: reachability esperada —
//
//	main.go ──importa "cmd"──▶ cmd/c.go ──importa "pkg/util"──▶ pkg/util/util.go
//	                       (regla de directorio: imp == path.Dir(file); un
//	                       archivo raíz tiene dir "." y no matchea — caso
//	                       degenerado documentado en matchImport)
//	web/app.ts ──importa "app.ts"──▶ src/app.ts (regla de sufijo "/app.ts")
//	otro.go importa "otro" y no resuelve a nadie del universo (fantasma).
func grafoFixture() []store.ListRepoIndexImportsRow {
	return []store.ListRepoIndexImportsRow{
		{File: "pkg/util/util.go", Imports: []string{"fmt", "net/http"}}, // del sistema: no matchean nada
		{File: "cmd/c.go", Imports: []string{"pkg/util"}},                // dir: path.Dir("pkg/util/util.go")
		{File: "main.go", Imports: []string{"cmd"}},                     // dir: path.Dir("cmd/c.go")
		{File: "src/app.ts", Imports: nil},
		{File: "web/app.ts", Imports: []string{"app.ts"}}, // sufijo: "/app.ts"
		{File: "otro.go", Imports: []string{"otro"}},
	}
}

func TestDependents(t *testing.T) {
	tests := []struct {
		name  string
		rows  []store.ListRepoIndexImportsRow
		files []string
		want  []string
	}{
		{
			"dependiente directo (regla de sufijo)",
			grafoFixture(),
			[]string{"src/app.ts"},
			[]string{"web/app.ts"},
		},
		{
			"dependiente transitivo (regla de directorio, 2 saltos)",
			grafoFixture(),
			[]string{"pkg/util/util.go"},
			[]string{"cmd/c.go", "main.go"},
		},
		{
			"sin falsos positivos: nadie importa al cambiado",
			grafoFixture(),
			[]string{"otro.go"},
			nil,
		},
		{
			"archivo cambiado fuera del índice no aporta dependientes",
			grafoFixture(),
			[]string{"no-indexado.go"},
			nil,
		},
		{
			"varios orígenes: unión ordenada y sin duplicados",
			grafoFixture(),
			[]string{"pkg/util/util.go", "src/app.ts"},
			[]string{"cmd/c.go", "main.go", "web/app.ts"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Dependents(context.Background(), &stubImpactStore{rows: tt.rows}, 1, tt.files)
			if err != nil {
				t.Fatalf("Dependents: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Dependents(%v) = %v, querés %v", tt.files, got, tt.want)
			}
		})
	}
}

// Sin índice del repo → ErrNoIndex: el caller distingue la degradación
// visible (impact null) de un fallo real de la store.
func TestDependentsSinIndice(t *testing.T) {
	_, err := Dependents(context.Background(), &stubImpactStore{}, 1, []string{"main.go"})
	if !errors.Is(err, ErrNoIndex) {
		t.Errorf("índice vacío: querés ErrNoIndex, got %v", err)
	}
}

// Un error de la store PROPAGA envuelto (sin ErrNoIndex): es fallo de
// infraestructura, no ausencia de índice.
func TestDependentsErrorDeStore(t *testing.T) {
	_, err := Dependents(context.Background(), &stubImpactStore{err: errors.New("bd caída")}, 1, []string{"main.go"})
	if err == nil || errors.Is(err, ErrNoIndex) {
		t.Errorf("error de la store debe propagar sin ErrNoIndex, got %v", err)
	}
}
