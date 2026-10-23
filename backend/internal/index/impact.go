// Reachability inversa del grafo de imports del índice (§6 F6, T3): los
// dependientes de un archivo cambiado son los archivos indexados que lo
// importan, directa o transitivamente — el blast radius que alimenta el
// impact_score del triage (y el change stack de T4). La resolución de
// imports es la MISMA regla best-effort de Retrieve (matchImport, §6 F4):
// sin resolución de módulos, lo que no matchea simplemente no conecta.

package index

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// ErrNoIndex marca el repo sin índice (§6 F6): el caller distingue la
// ausencia de datos (impact null visible en el dashboard) de un fallo real
// de infraestructura, que propaga envuelto.
var ErrNoIndex = errors.New("el repo no tiene índice todavía")

// ImpactStore es el subconjunto de la store que consume Dependents (§4.5:
// interfaces donde se consumen). *store.Store la satisface
// (ListRepoIndexImports).
type ImpactStore interface {
	ListRepoIndexImports(ctx context.Context, repositoryID int64) ([]store.ListRepoIndexImportsRow, error)
}

// Dependents devuelve los archivos indexados que dependen (directa o
// transitivamente) de algún archivo del conjunto cambiado: BFS sobre la
// adjacencia inversa del grafo de imports file-level. Un archivo F depende
// de C cuando algún import de F resuelve a C por matchImport (misma regla
// que la expansión de Retrieve, §6 F4). Archivos cambiados que no están en
// el índice no aportan dependientes: sin fila indexada no hay arista que
// resolver. Sin índice → ErrNoIndex. Salida ordenada y sin duplicados —
// determinista para el mismo índice y la misma entrada.
func Dependents(ctx context.Context, st ImpactStore, repoID int64, files []string) ([]string, error) {
	rows, err := st.ListRepoIndexImports(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("listando los imports del índice del repo %d: %w", repoID, err)
	}
	if len(rows) == 0 {
		return nil, ErrNoIndex
	}

	// Universo indexado en orden determinista + imports por archivo.
	universe := make([]string, 0, len(rows))
	imports := make(map[string][]string, len(rows))
	for _, r := range rows {
		universe = append(universe, r.File)
		imports[r.File] = r.Imports
	}
	sort.Strings(universe)

	// resolve memoiza por import-spec: cada spec se resuelve UNA vez contra
	// el universo completo con la MISMA matchImport de Retrieve (§6 F4).
	memo := map[string][]string{}
	resolve := func(imp string) []string {
		if cs, ok := memo[imp]; ok {
			return cs
		}
		var cs []string
		for _, f := range universe {
			if matchImport(imp, f) {
				cs = append(cs, f)
			}
		}
		memo[imp] = cs
		return cs
	}

	// Adjacencia inversa sobre TODO el índice: C → F cuando algún import de
	// F resuelve a C. Un archivo puede resolver dos imports al mismo C: el
	// BFS deduplica por visitado, no hace falta dedup de aristas.
	rev := make(map[string][]string, len(universe))
	for _, f := range universe {
		for _, imp := range imports[f] {
			for _, c := range resolve(imp) {
				if c != f {
					rev[c] = append(rev[c], f)
				}
			}
		}
	}

	// BFS desde el conjunto cambiado: cada dependiente alcanza descubre el
	// siguiente salto (transitividad).
	seen := map[string]bool{}
	queue := append([]string(nil), files...)
	var deps []string
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		for _, d := range rev[c] { // rev[c] nil para archivos sin dependientes
			if !seen[d] {
				seen[d] = true
				deps = append(deps, d)
				queue = append(queue, d)
			}
		}
	}
	sort.Strings(deps)
	return deps, nil
}

// StackCohort es una capa del change stack (§6 F6, T4): los archivos
// cambiados que comparten nivel topológico dentro del diff. Level 0 es la
// base (aquello de lo que dependen los demás — revisarlo primero); kind
// "tests" marca la cohorte de pruebas, que va SIEMPRE al final.
type StackCohort struct {
	Level int      `json:"level"`
	Kind  string   `json:"kind"` // "code" | "tests"
	Files []string `json:"files"`
}

// testish es la heurística de ruta de los archivos de prueba (§6 F6, T4):
// el nombre base empieza con "test", algún segmento de la ruta es test o
// tests, o el nombre base contiene "_test." / ".test." / ".spec.". Los
// tests van SIEMPRE a la última cohorte: no aportan capas de dependencia —
// validan. Inconsciente de lenguaje a propósito (mismo espíritu que la
// heurística de F5, adaptada al change stack): un "attest.go" NO matchea
// porque "test" viene pegado sin delimitador.
func testish(file string) bool {
	base := file[strings.LastIndex(file, "/")+1:]
	if strings.HasPrefix(base, "test") ||
		strings.Contains(base, "_test.") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") {
		return true
	}
	for _, seg := range strings.Split(file, "/") {
		if seg == "test" || seg == "tests" {
			return true
		}
	}
	return false
}

// Cohorts devuelve las cohortes de dependencia de los archivos cambiados
// (§6 F6, T4): el MISMO grafo de imports de Dependents (matchImport, §6 F4)
// restringido a los CAMBIADOS presentes en el índice, pelado por niveles —
// A depende de B cuando algún import de A resuelve a B y ambos cambiaron.
// Level 0 es la base (sin dependencias cambiadas); level(f) = max(levels de
// sus dependencias)+1. Los archivos de tests (testish) no participan del
// grafo: van SIEMPRE en la última cohorte kind "tests". Los archivos
// trabados en ciclos NO se rompen: se agrupan en una cohorte code final con
// level = max asignado+1 (documentado: ciclos no se rompen, se agrupan).
// Cambiados fuera del índice se ignoran: sin fila indexada no hay arista que
// resolver (misma regla que Dependents). Sin índice → ErrNoIndex; sin
// cambiados indexados → (nil, nil) y el caller persiste null. Salida
// determinista: archivos ordenados dentro de cada cohorte, cohortes
// ascendentes por nivel, tests al final.
func Cohorts(ctx context.Context, st ImpactStore, repoID int64, changed []string) ([]StackCohort, error) {
	rows, err := st.ListRepoIndexImports(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("listando los imports del índice del repo %d: %w", repoID, err)
	}
	if len(rows) == 0 {
		return nil, ErrNoIndex
	}

	// Imports por archivo indexado + universo de cambiados presentes en el
	// índice (los no indexados se ignoran — documentado arriba).
	imports := make(map[string][]string, len(rows))
	for _, r := range rows {
		imports[r.File] = r.Imports
	}
	indexed := make([]string, 0, len(changed))
	seen := map[string]bool{}
	for _, f := range changed {
		if _, ok := imports[f]; ok && !seen[f] {
			seen[f] = true
			indexed = append(indexed, f)
		}
	}
	sort.Strings(indexed)

	// Los tests salen del grafo: sus aristas no generan capas (van al final).
	var code, tests []string
	for _, f := range indexed {
		if testish(f) {
			tests = append(tests, f)
		} else {
			code = append(code, f)
		}
	}

	// Aristas solo entre cambiados: deps[a] = code cambiados que a importa.
	deps := make(map[string]map[string]bool, len(code))
	for _, a := range code {
		for _, imp := range imports[a] {
			for _, b := range code {
				if b != a && matchImport(imp, b) {
					if deps[a] == nil {
						deps[a] = map[string]bool{}
					}
					deps[a][b] = true
				}
			}
		}
	}

	// Pelado por niveles estilo Kahn, determinista: cada pasada asigna (en
	// orden alfabético) los archivos cuyas dependencias ya tienen nivel;
	// level = max(niveles de deps)+1. Iterar hasta que una pasada completa no
	// asigne nada: lo que queda está en ciclos.
	level := make(map[string]int, len(code))
	maxAssigned := -1
	for {
		progress := false
		for _, f := range code {
			if _, ok := level[f]; ok {
				continue
			}
			ready, max := true, -1
			for d := range deps[f] {
				ld, ok := level[d]
				if !ok {
					ready = false
					break
				}
				if ld > max {
					max = ld
				}
			}
			if !ready {
				continue
			}
			level[f] = max + 1
			if max+1 > maxAssigned {
				maxAssigned = max + 1
			}
			progress = true
		}
		if !progress {
			break
		}
	}

	// Cohortes por nivel ascendente (code ya viene ordenado → cada cohorte
	// queda ordenada alfabéticamente).
	byLevel := make(map[int][]string, len(code))
	var levels []int
	for _, f := range code {
		if l, ok := level[f]; ok {
			if _, exists := byLevel[l]; !exists {
				levels = append(levels, l)
			}
			byLevel[l] = append(byLevel[l], f)
		}
	}
	sort.Ints(levels)
	var cohorts []StackCohort
	for _, l := range levels {
		cohorts = append(cohorts, StackCohort{Level: l, Kind: "code", Files: byLevel[l]})
	}

	// Ciclos: una sola cohorte code al final del código (no se rompen, se
	// agrupan); los tests van después de todo el código, siempre últimos.
	next := maxAssigned + 1
	var stuck []string
	for _, f := range code {
		if _, ok := level[f]; !ok {
			stuck = append(stuck, f)
		}
	}
	if len(stuck) > 0 {
		cohorts = append(cohorts, StackCohort{Level: next, Kind: "code", Files: stuck})
		next++
	}
	if len(tests) > 0 {
		cohorts = append(cohorts, StackCohort{Level: next, Kind: "tests", Files: tests})
	}
	return cohorts, nil
}
