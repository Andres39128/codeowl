// Retrieval de símbolos relacionados para el Reviewer (F4, guía §6,
// decisión 6): por archivo revisado, query = path + hunks → 1 llamada de
// embed → top-K coseno del repo excluyendo el mismo archivo → expansión de
// 1 salto por el grafo de imports file-level (best-effort). Índice vacío →
// silencioso (sin llamada ni contexto). El caller (T7) decide la degradación
// ante error: la review JAMÁS falla por retrieval (§9.6).
package index

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Cómo se encontró un símbolo relacionado (§6 F4: coseno o grafo de imports).
const (
	ViaSimilar = "similar" // top-K coseno
	ViaImport  = "import"  // expansión de 1 salto por imports de los hits
)

// defaultTopK espejo de config.DefaultRetrievalTopK (§9.6): K por defecto
// del top-K coseno cuando el caller no especifica uno válido.
const defaultTopK = 8

// RelatedSymbol es un símbolo del repo relacionado con el archivo bajo
// revisión (§6 F4): insumo del bloque "Símbolos relacionados" del prompt
// del Reviewer (T7, que aplica el tope de chars REVIEW_CONTEXT_MAX_CHARS).
type RelatedSymbol struct {
	File      string
	Symbol    string
	Kind      string
	StartLine int32
	EndLine   int32
	Imports   []string // imports file-level del archivo del símbolo
	Via       string   // ViaSimilar | ViaImport — cómo se encontró
}

// RetrieveStore es el subconjunto de la store que consume Retrieve (§4.5:
// interfaces donde se consumen). *store.Store la satisface (queries de T3).
type RetrieveStore interface {
	CountRepoIndexByRepo(ctx context.Context, repositoryID int64) (int64, error)
	GetRepoIndexDims(ctx context.Context) (int32, error)
	SearchRepoIndexBySimilarity(ctx context.Context, arg store.SearchRepoIndexBySimilarityParams) ([]store.SearchRepoIndexBySimilarityRow, error)
	ListRepoIndexFiles(ctx context.Context, repositoryID int64) ([]store.ListRepoIndexFilesRow, error)
	ListRepoIndexByFiles(ctx context.Context, arg store.ListRepoIndexByFilesParams) ([]store.ListRepoIndexByFilesRow, error)
}

// matchImport es la regla v1 de resolución best-effort de imports (§6 F4,
// decisión 6): el import I resuelve al archivo indexado P sii
// P == I (import con ruta exacta), o P termina en "/"+I (import de
// subdirectorio), o I == path.Dir(P) (import de paquete/directorio: I apunta
// al directorio, P es un archivo dentro). Sin resolución de módulos ni
// extensiones implícitas: lo que no matchea simplemente no expande.
// Caso degenerado conocido: I="." matchea archivos raíz (path.Dir("x.go")
// == ".") — inofensivo, los imports reales nunca son ".".
func matchImport(imp, file string) bool {
	return imp == file || strings.HasSuffix(file, "/"+imp) || imp == path.Dir(file)
}

// Retrieve devuelve los símbolos del índice relacionados con un archivo bajo
// revisión (§6 F4, decisión 6):
//
//  1. Índice vacío → (nil, nil) SIN llamar al gateway: el bloque de contexto
//     simplemente no existe en el prompt (silencioso, §6 F4).
//  2. Una sola llamada Embed con la query `file + "\n" + code` (code = los
//     hunks del diff, ya truncados por el caller T7). El error de Embed
//     PROPAGA — el caller decide saltar el contexto y loguear.
//  3. Pre-check de dims contra el catálogo (§3.3, mismo backstop que Index):
//     proveedor con dims distintas al vector del DDL → ErrDimsMismatch
//     antes del SQL.
//  4. Top-K coseno excluyendo el archivo bajo revisión (sus símbolos ya
//     están en el diff) → Via=ViaSimilar, en el orden del SQL (distancia).
//  5. Expansión de 1 salto: los imports de los hits resuelven best-effort
//     (matchImport) contra los archivos indexados (universo de
//     ListRepoIndexFiles — un archivo no indexado no aporta símbolos),
//     excluyendo el archivo bajo revisión y los ya devueltos por similitud.
//  6. Tope de expansión = topK*2 filas (múltiplo constante simple, §6 F4):
//     ordenadas por (file, start_line, symbol) y cortadas — recorte
//     determinista, siempre los mismos símbolos para la misma entrada.
//
// topK < 1 → default 8 (defaultTopK, §9.6): la config inválida nunca pide
// 0 filas ni cuelga la retrieval.
func Retrieve(ctx context.Context, st RetrieveStore, gw Gateway, repoID int64, file, code string, topK int) ([]RelatedSymbol, error) {
	if topK < 1 {
		topK = defaultTopK
	}

	// (1) Guard de vacuidad: sin filas no hay contexto ni costo de embed.
	count, err := st.CountRepoIndexByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("contando el índice del repo %d: %w", repoID, err)
	}
	if count == 0 {
		return nil, nil
	}

	// (2) Dims del catálogo: el typmod de repo_index.embedding ES la cantidad
	// de dims (§3.3). Se lee antes del embed para no gastar una llamada que
	// el SQL rechazaría de todos modos.
	dims, err := st.GetRepoIndexDims(ctx)
	if err != nil {
		return nil, fmt.Errorf("leyendo dims del catálogo: %w", err)
	}

	// (3) UNA llamada de embed con la query del archivo (§6 F4, decisión 6).
	// Mismo espacio vectorial que los símbolos del índice por el rol embedding.
	vecs, err := gw.Embed(ctx, []string{file + "\n" + code})
	if err != nil {
		return nil, fmt.Errorf("embebiendo la query de %s: %w", file, err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("el gateway devolvió %d vectores para 1 texto de %s", len(vecs), file)
	}
	if len(vecs[0]) != int(dims) {
		return nil, fmt.Errorf("%w: el proveedor sirve %d dims, el catálogo exige %d", ErrDimsMismatch, len(vecs[0]), dims)
	}

	// (4) Top-K coseno (§3.3: <=> es distancia — menor = más similar).
	hits, err := st.SearchRepoIndexBySimilarity(ctx, store.SearchRepoIndexBySimilarityParams{
		RepositoryID:   repoID,
		ExcludeFile:    file,
		QueryEmbedding: vecLiteral(vecs[0]),
		RowLimit:       int32(topK),
	})
	if err != nil {
		return nil, fmt.Errorf("buscando símbolos similares: %w", err)
	}

	out := make([]RelatedSymbol, 0, len(hits))
	similarFiles := make(map[string]bool, len(hits))
	for _, h := range hits {
		similarFiles[h.File] = true
		out = append(out, RelatedSymbol{
			File: h.File, Symbol: h.Symbol, Kind: h.Kind,
			StartLine: h.StartLine, EndLine: h.EndLine,
			Imports: h.Imports, Via: ViaSimilar,
		})
	}

	// (5/6) Expansión de 1 salto por el grafo de imports.
	if len(hits) > 0 {
		indexed, err := st.ListRepoIndexFiles(ctx, repoID)
		if err != nil {
			return nil, fmt.Errorf("listando archivos indexados: %w", err)
		}
		targets := map[string]bool{}
		for _, h := range hits {
			for _, imp := range h.Imports {
				for _, f := range indexed {
					if f.File != file && !similarFiles[f.File] && matchImport(imp, f.File) {
						targets[f.File] = true
					}
				}
			}
		}
		if len(targets) > 0 {
			files := make([]string, 0, len(targets))
			for f := range targets {
				files = append(files, f)
			}
			sort.Strings(files) // lista IN determinista
			rows, err := st.ListRepoIndexByFiles(ctx, store.ListRepoIndexByFilesParams{
				RepositoryID: repoID,
				Files:        files,
			})
			if err != nil {
				return nil, fmt.Errorf("expandiendo símbolos por imports: %w", err)
			}
			exp := make([]RelatedSymbol, 0, len(rows))
			for _, r := range rows {
				exp = append(exp, RelatedSymbol{
					File: r.File, Symbol: r.Symbol, Kind: r.Kind,
					StartLine: r.StartLine, EndLine: r.EndLine,
					Imports: r.Imports, Via: ViaImport,
				})
			}
			// Orden determinista y tope de expansión (§6 F4): topK*2 filas.
			sort.Slice(exp, func(i, j int) bool {
				a, b := exp[i], exp[j]
				if a.File != b.File {
					return a.File < b.File
				}
				if a.StartLine != b.StartLine {
					return a.StartLine < b.StartLine
				}
				return a.Symbol < b.Symbol
			})
			if limit := topK * 2; len(exp) > limit {
				exp = exp[:limit]
			}
			out = append(out, exp...)
		}
	}
	return out, nil
}
