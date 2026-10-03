-- repo_index (guía §3.3): índice simbólico RAG por repo. La columna embedding
-- jamás aparece en SELECTs (evita el tipo vector en los modelos generados) y
-- se INSERTea como literal de texto '[0.1,0.2,...]' con cast ::vector —
-- pgvector lo parsea con el input function (§3.3).

-- name: DeleteRepoIndexByRepo :exec
-- Wipe completo del repo: cambio de modelo de embeddings (§9.6).
DELETE FROM repo_index WHERE repository_id = $1;

-- name: DeleteRepoIndexByRepoAndFiles :exec
-- Borra los símbolos de los archivos dados antes de re-insertarlos (§9.6).
DELETE FROM repo_index WHERE repository_id = $1 AND file = ANY(sqlc.arg(files)::text[]);

-- name: CreateRepoIndexRow :one
-- Una fila por símbolo; el embedding viaja como string con cast a vector.
-- NOTA sqlc: el param queda como Column7 (string) — sqlc no infiere nombres
-- de params posicionales y el override de sqlc.yaml mapea vector → string.
INSERT INTO repo_index (repository_id, file, symbol, kind, start_line, end_line, embedding, embedding_model, dims, imports, file_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7::vector, $8, $9, COALESCE($10::text[], '{}'::text[]), $11)
RETURNING id;

-- name: ListRepoIndexFiles :many
-- Un renglón por archivo con su hash y modelo: resume-skip de la reindexación
-- (§9.6: hash igual y mismo embedding_model → se salta).
SELECT DISTINCT file, file_hash, embedding_model
FROM repo_index
WHERE repository_id = $1;

-- name: CountRepoIndexByRepo :one
-- Chequeo de vacuidad del índice (retrieval: índice vacío → sin contexto).
SELECT COUNT(1) FROM repo_index WHERE repository_id = $1;

-- name: GetRepoIndexEmbeddingModel :one
-- Modelo vigente del índice: todas las filas del repo comparten modelo —
-- distinto al del proveedor actual → wipe + re-index completo (§9.6).
-- Índice vacío → pgx.ErrNoRows (el caller decide: primera indexación).
SELECT DISTINCT embedding_model
FROM repo_index
WHERE repository_id = $1
LIMIT 1;

-- name: SearchRepoIndexBySimilarity :many
-- Top-K por distancia coseno (§3.3): <=> es la distancia de pgvector — menor
-- = más similar. Excluye el archivo bajo revisión (sus símbolos ya están en
-- el diff del Reviewer). Sin embedding en el resultado.
SELECT file, symbol, kind, start_line, end_line, imports
FROM repo_index
WHERE repository_id = $1
  AND file <> sqlc.arg(exclude_file)
ORDER BY embedding <=> sqlc.arg(query_embedding)::vector
LIMIT sqlc.arg(row_limit);

-- name: ListRepoIndexByFiles :many
-- Todos los símbolos de los archivos dados: insumo de la expansión de un
-- salto por el grafo de imports (§3.3).
SELECT id, repository_id, file, symbol, kind, start_line, end_line, embedding_model, dims, imports, file_hash, created_at, updated_at
FROM repo_index
WHERE repository_id = $1
  AND file = ANY(sqlc.arg(files)::text[]);

-- name: GetRepoIndexDims :one
-- Dims vigentes desde el catálogo de PG: para el tipo vector, typmod de la
-- columna ES la cantidad de dims (§3.3: config nunca diverge del DDL — el
-- guard de settings de F4 compara contra esto).
SELECT atttypmod AS dims
FROM pg_catalog.pg_attribute
WHERE attrelid = 'repo_index'::regclass
  AND attname = 'embedding';
