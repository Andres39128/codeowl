package store

// Tests F4 del índice simbólico RAG (guía §3.3): round-trips del ciclo
// index→retrieve contra el Postgres de desarrollo vía testStore
// (store_test.go): dims desde el catálogo, wipe, re-insert por archivo,
// resume-skip, modelo vigente y orden coseno del retrieval con exclusión de
// archivo. Los vectores son deterministas: direcciones diseñadas para que el
// orden por distancia <=> sea inequívoco.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// riDims es el vector(1536) fijado en la migración 0004 (§3.3).
const riDims = 1536

// riVec construye un vector determinista de riDims dims. dir elige la
// dirección: 0 = rampa positiva (referencia), 1 = alternante (≈ ortogonal a
// la rampa), 2 = rampa negada (opuesto coseno). Determinismo: el orden del
// retrieval depende solo de la dirección, no del azar.
func riVec(dir int) []float32 {
	v := make([]float32, riDims)
	for i := range v {
		switch dir {
		case 0:
			v[i] = float32(i%16) * 0.01
		case 1:
			if i%2 == 0 {
				v[i] = 0.15
			} else {
				v[i] = -0.15
			}
		default:
			v[i] = -float32(i%16) * 0.01
		}
	}
	return v
}

// riVecLiteral serializa []float32 al literal de pgvector '[0.1,0.2,...]':
// la forma en que el paquete index (T4/T5) pasará embeddings a la store.
func riVecLiteral(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// riInsert inserta una fila de símbolo con el embedding dado.
func riInsert(t *testing.T, st *Store, repoID int64, file, symbol, kind string, start, end int32, v []float32, imports []string) {
	t.Helper()
	if _, err := st.CreateRepoIndexRow(context.Background(), CreateRepoIndexRowParams{
		RepositoryID: repoID,
		File:         file,
		Symbol:       symbol,
		Kind:         kind,
		StartLine:    start,
		EndLine:      end,
		Column7:        riVecLiteral(v),
		EmbeddingModel: "test-embed-model",
		Dims:           riDims,
		Column10:       imports, // imports del símbolo (param posicional generado)
		FileHash:       "hash-" + file,
	}); err != nil {
		t.Fatalf("CreateRepoIndexRow(%s/%s): %v", file, symbol, err)
	}
}

func TestRepoIndexQueries(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	repo := f1Repo(t, st)
	defer func() {
		if err := st.DeleteRepoIndexByRepo(ctx, repo.ID); err != nil {
			t.Errorf("limpiando repo_index: %v", err)
		}
	}()

	// Dims desde el catálogo: typmod de la columna embedding ES 1536 (§3.3,
	// la config jamás diverge del DDL — el guard de settings compara acá).
	dims, err := st.GetRepoIndexDims(ctx)
	if err != nil {
		t.Fatalf("GetRepoIndexDims: %v", err)
	}
	if dims != riDims {
		t.Fatalf("dims del catálogo = %d, querés %d", dims, riDims)
	}

	// Inserción: f1.go con dos símbolos casi paralelos al query (uno levemente
	// perturbado para romper el empate coseno), f3.py ortogonal, f2.go opuesto.
	cerca := riVec(0)
	cercaPert := riVec(0)
	cercaPert[5] += 0.5
	riInsert(t, st, repo.ID, "f1.go", "Cerca", "function", 1, 10, cerca, []string{"fmt"})
	riInsert(t, st, repo.ID, "f1.go", "CercaPert", "function", 12, 20, cercaPert, nil)
	riInsert(t, st, repo.ID, "f3.py", "Medio", "class", 3, 30, riVec(1), []string{"os", "sys"})
	riInsert(t, st, repo.ID, "f2.go", "Lejos", "type", 7, 9, riVec(2), nil)

	// Count: chequeo de vacuidad del retrieval (§3.3).
	if n, err := st.CountRepoIndexByRepo(ctx, repo.ID); err != nil || n != 4 {
		t.Fatalf("CountRepoIndexByRepo = %d, %v; querés 4", n, err)
	}

	// Listado de archivos con hash y modelo: insumo del resume-skip (§9.6).
	files, err := st.ListRepoIndexFiles(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListRepoIndexFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("ListRepoIndexFiles: n=%d, querés 3 archivos", len(files))
	}
	byFile := map[string]ListRepoIndexFilesRow{}
	for _, f := range files {
		byFile[f.File] = f
	}
	for _, f := range []string{"f1.go", "f2.go", "f3.py"} {
		got, ok := byFile[f]
		if !ok || got.FileHash != "hash-"+f || got.EmbeddingModel != "test-embed-model" {
			t.Errorf("ListRepoIndexFiles[%s]: got %+v (hash/modelo incorrectos o ausente)", f, got)
		}
	}

	// Modelo vigente del índice: detección de cambio de proveedor (§9.6).
	modelo, err := st.GetRepoIndexEmbeddingModel(ctx, repo.ID)
	if err != nil || modelo != "test-embed-model" {
		t.Fatalf("GetRepoIndexEmbeddingModel = %q, %v; querés test-embed-model", modelo, err)
	}

	// Similarity search con exclusión: f1.go fuera → ortogonal antes que
	// opuesto (§3.3: <=> distancia coseno ascendente, menor = más similar).
	res, err := st.SearchRepoIndexBySimilarity(ctx, SearchRepoIndexBySimilarityParams{
		RepositoryID:   repo.ID,
		QueryEmbedding: riVecLiteral(cerca),
		ExcludeFile:    "f1.go",
		RowLimit:       10,
	})
	if err != nil {
		t.Fatalf("SearchRepoIndexBySimilarity: %v", err)
	}
	if len(res) != 2 || res[0].File+"/"+res[0].Symbol != "f3.py/Medio" || res[1].File+"/"+res[1].Symbol != "f2.go/Lejos" {
		t.Fatalf("orden con exclusión incorrecto: %+v", res)
	}

	// Sin exclusión: orden completo cerca → cercaPert → medio → lejos, y el
	// LIMIT recorta desde el más similar.
	res, err = st.SearchRepoIndexBySimilarity(ctx, SearchRepoIndexBySimilarityParams{
		RepositoryID:   repo.ID,
		QueryEmbedding: riVecLiteral(cerca),
		ExcludeFile:    "inexistente.go",
		RowLimit:       3,
	})
	if err != nil {
		t.Fatalf("SearchRepoIndexBySimilarity sin exclusión: %v", err)
	}
	orden := make([]string, 0, len(res))
	for _, r := range res {
		orden = append(orden, r.Symbol)
	}
	if len(res) != 3 || orden[0] != "Cerca" || orden[1] != "CercaPert" || orden[2] != "Medio" {
		t.Fatalf("orden coseno incorrecto: %v", orden)
	}
	if res[0].Kind != "function" || res[0].StartLine != 1 || res[0].EndLine != 10 {
		t.Errorf("columnas del row de búsqueda: got %+v", res[0])
	}

	// By-files: insumo de la expansión del grafo de imports (§3.3) — todas
	// las columnas hacen round-trip.
	porArchivos, err := st.ListRepoIndexByFiles(ctx, ListRepoIndexByFilesParams{RepositoryID: repo.ID, Files: []string{"f1.go", "f3.py"}})
	if err != nil {
		t.Fatalf("ListRepoIndexByFiles: %v", err)
	}
	if len(porArchivos) != 3 {
		t.Fatalf("ListRepoIndexByFiles: n=%d, querés 3", len(porArchivos))
	}
	seenImports := map[string][]int{}
	for _, r := range porArchivos {
		if r.RepositoryID != repo.ID || r.EmbeddingModel != "test-embed-model" || r.Dims != riDims || r.FileHash != "hash-"+r.File {
			t.Errorf("round-trip de %s/%s: got %+v", r.File, r.Symbol, r)
		}
		seenImports[r.File] = append(seenImports[r.File], len(r.Imports))
	}
	// f1.go trae un símbolo con import y otro sin ninguno; f3.py dos.
	if len(seenImports["f1.go"]) != 2 || seenImports["f3.py"] == nil || len(seenImports["f3.py"]) != 1 ||
		seenImports["f3.py"][0] != 2 || seenImports["f1.go"][0]+seenImports["f1.go"][1] != 1 {
		t.Errorf("imports file-level: got %v, querés f1.go={1,0} f3.py={2}", seenImports)
	}

	// Re-index por archivo (§9.6): delete + re-insert del mismo hash.
	if err := st.DeleteRepoIndexByRepoAndFiles(ctx, DeleteRepoIndexByRepoAndFilesParams{RepositoryID: repo.ID, Files: []string{"f1.go"}}); err != nil {
		t.Fatalf("DeleteRepoIndexByRepoAndFiles: %v", err)
	}
	if n, _ := st.CountRepoIndexByRepo(ctx, repo.ID); n != 2 {
		t.Fatalf("tras el delete por archivo: n=%d, querés 2", n)
	}
	riInsert(t, st, repo.ID, "f1.go", "Cerca", "function", 1, 10, cerca, []string{"fmt"})
	if n, _ := st.CountRepoIndexByRepo(ctx, repo.ID); n != 3 {
		t.Fatalf("tras el re-insert: n=%d, querés 3", n)
	}

	// Wipe completo (cambio de modelo, §9.6): vacío de verdad — sin filas no
	// hay modelo vigente.
	if err := st.DeleteRepoIndexByRepo(ctx, repo.ID); err != nil {
		t.Fatalf("DeleteRepoIndexByRepo: %v", err)
	}
	if n, _ := st.CountRepoIndexByRepo(ctx, repo.ID); n != 0 {
		t.Fatalf("tras el wipe: n=%d, querés 0", n)
	}
	if _, err := st.GetRepoIndexEmbeddingModel(ctx, repo.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("índice vacío no debe tener modelo vigente, got %v", err)
	}
}
