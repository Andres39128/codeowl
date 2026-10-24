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
		{File: "main.go", Imports: []string{"cmd"}},                      // dir: path.Dir("cmd/c.go")
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

// Cohortes de dependencia del change stack (§6 F6, T4): el mismo grafo de
// imports de Dependents restringido a los CAMBIADOS presentes en el índice,
// pelado por niveles (Kahn determinista) con los tests al final por
// heurística de ruta. Las aristas usan la resolución real de matchImport:
// import "pkg/a" resuelve por directorio (path.Dir("pkg/a/a.go")); un
// import plano "a" NO matchea "a.go" — caso degenerado documentado en
// retrieve.go, acá irrelevante porque los fixtures usan directorios.
func TestCohorts(t *testing.T) {
	tests := []struct {
		name    string
		rows    []store.ListRepoIndexImportsRow
		changed []string
		want    []StackCohort
	}{
		{
			"cadena lineal: base nivel 0, dependientes apilan",
			[]store.ListRepoIndexImportsRow{
				{File: "pkg/a/a.go", Imports: nil},
				{File: "pkg/b/b.go", Imports: []string{"pkg/a"}},
				{File: "pkg/c/c.go", Imports: []string{"pkg/b"}},
			},
			[]string{"pkg/a/a.go", "pkg/b/b.go", "pkg/c/c.go"},
			[]StackCohort{
				{Level: 0, Kind: "code", Files: []string{"pkg/a/a.go"}},
				{Level: 1, Kind: "code", Files: []string{"pkg/b/b.go"}},
				{Level: 2, Kind: "code", Files: []string{"pkg/c/c.go"}},
			},
		},
		{
			"tests al final (sufijo _test. y segmento de ruta tests/)",
			[]store.ListRepoIndexImportsRow{
				{File: "pkg/a/a.go", Imports: nil},
				{File: "pkg/a/a_test.go", Imports: []string{"pkg/a"}},
				{File: "tests/e2e.go", Imports: []string{"pkg/a"}},
			},
			[]string{"pkg/a/a.go", "pkg/a/a_test.go", "tests/e2e.go"},
			[]StackCohort{
				{Level: 0, Kind: "code", Files: []string{"pkg/a/a.go"}},
				{Level: 1, Kind: "tests", Files: []string{"pkg/a/a_test.go", "tests/e2e.go"}},
			},
		},
		{
			"ciclo x↔y: no se rompe, se agrupa en la última cohorte code",
			[]store.ListRepoIndexImportsRow{
				{File: "pkg/x/x.go", Imports: []string{"pkg/y"}},
				{File: "pkg/y/y.go", Imports: []string{"pkg/x"}},
			},
			[]string{"pkg/x/x.go", "pkg/y/y.go"},
			[]StackCohort{
				{Level: 0, Kind: "code", Files: []string{"pkg/x/x.go", "pkg/y/y.go"}},
			},
		},
		{
			"ciclo tras cadena: la cohorte de ciclos va con nivel max+1",
			[]store.ListRepoIndexImportsRow{
				{File: "pkg/a/a.go", Imports: nil},
				{File: "pkg/b/b.go", Imports: []string{"pkg/a"}},
				{File: "pkg/x/x.go", Imports: []string{"pkg/y"}},
				{File: "pkg/y/y.go", Imports: []string{"pkg/x"}},
			},
			[]string{"pkg/a/a.go", "pkg/b/b.go", "pkg/x/x.go", "pkg/y/y.go"},
			[]StackCohort{
				{Level: 0, Kind: "code", Files: []string{"pkg/a/a.go"}},
				{Level: 1, Kind: "code", Files: []string{"pkg/b/b.go"}},
				{Level: 2, Kind: "code", Files: []string{"pkg/x/x.go", "pkg/y/y.go"}},
			},
		},
		{
			"independientes comparten el nivel 0, orden alfabético",
			[]store.ListRepoIndexImportsRow{
				{File: "pkg/z/z.go", Imports: nil},
				{File: "pkg/b/b.go", Imports: []string{"fmt"}},
			},
			[]string{"pkg/b/b.go", "pkg/z/z.go"},
			[]StackCohort{
				{Level: 0, Kind: "code", Files: []string{"pkg/b/b.go", "pkg/z/z.go"}},
			},
		},
		{
			"cambiado fuera del índice se ignora (solo aristas indexadas)",
			[]store.ListRepoIndexImportsRow{
				{File: "pkg/a/a.go", Imports: nil},
			},
			[]string{"pkg/a/a.go", "no-indexado.go"},
			[]StackCohort{
				{Level: 0, Kind: "code", Files: []string{"pkg/a/a.go"}},
			},
		},
		{
			"solo tests: única cohorte kind tests nivel 0",
			[]store.ListRepoIndexImportsRow{
				{File: "pkg/a/a.go", Imports: nil},
				{File: "pkg/a/a_test.go", Imports: []string{"pkg/a"}},
			},
			[]string{"pkg/a/a_test.go"},
			[]StackCohort{
				{Level: 0, Kind: "tests", Files: []string{"pkg/a/a_test.go"}},
			},
		},
		{
			"sin cambiados indexados no hay cohortes (el caller persiste null)",
			grafoFixture(),
			[]string{"no-indexado.go"},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Cohorts(context.Background(), &stubImpactStore{rows: tt.rows}, 1, tt.changed)
			if err != nil {
				t.Fatalf("Cohorts: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Cohorts(%v) = %+v, querés %+v", tt.changed, got, tt.want)
			}
		})
	}
}

// Sin índice del repo → ErrNoIndex: mismo sentinel que Dependents (el
// caller distingue la degradación visible de un fallo real de la store).
func TestCohortsSinIndice(t *testing.T) {
	_, err := Cohorts(context.Background(), &stubImpactStore{}, 1, []string{"main.go"})
	if !errors.Is(err, ErrNoIndex) {
		t.Errorf("índice vacío: querés ErrNoIndex, got %v", err)
	}
}

// Un error de la store PROPAGA envuelto (sin ErrNoIndex), como Dependents.
func TestCohortsErrorDeStore(t *testing.T) {
	_, err := Cohorts(context.Background(), &stubImpactStore{err: errors.New("bd caída")}, 1, []string{"main.go"})
	if err == nil || errors.Is(err, ErrNoIndex) {
		t.Errorf("error de la store debe propagar sin ErrNoIndex, got %v", err)
	}
}

// testish, la heurística de ruta de los tests (§6 F6, T4): base que empieza
// con "test", segmento test/tests, o sufijo _test./.test./.spec. — sin
// falsos positivos tipo "attest.go" o "contest.py".
func TestTestish(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"test.go", true},          // base empieza con test
		{"test_helpers.go", true},  // ídem (utilidades de test)
		{"pkg/a/a_test.go", true},  // sufijo _test.
		{"foo.test.ts", true},      // sufijo .test.
		{"bar.spec.js", true},      // sufijo .spec.
		{"tests/e2e.go", true},     // segmento de ruta tests
		{"src/test/util.go", true}, // segmento de ruta test
		{"main.go", false},
		{"attest.go", false}, // contiene "test" pero sin delimitador
		{"contest.py", false},
		{"latest.go", false},
	}
	for _, tt := range tests {
		if got := testish(tt.path); got != tt.want {
			t.Errorf("testish(%q) = %v, querés %v", tt.path, got, tt.want)
		}
	}
}
