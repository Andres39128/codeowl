-- Hallazgos (guía §3.3): FK a la corrida que los produjo.

-- name: CreateFinding :one
-- verified null hasta que el Verifier corre (F3); los SAST se publican sin
-- verificación (§3.3).
INSERT INTO findings (review_id, file, line, severity, category, body, suggestion, source, verified)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListFindingsByReview :many
SELECT * FROM findings WHERE review_id = $1 ORDER BY id;

-- name: ListFindingsByPR :many
-- Atribución por corrida: todos los hallazgos de todas las corridas del PR.
SELECT f.*
FROM findings f
JOIN reviews r ON r.id = f.review_id
WHERE r.pull_request_id = $1
ORDER BY f.id;

-- name: UpdateFindingAccepted :exec
-- Outcome F5 (§6): el MetricsJob (T5) marca si la sugerencia del hallazgo
-- terminó aplicada (heurística por contenido sobre los patches posteriores).
-- Null = sin evaluar; nunca se des-marca a null una vez escrito.
UPDATE findings
SET accepted = $2
WHERE id = $1;
