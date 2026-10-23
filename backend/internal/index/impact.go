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
