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
-- dismissed marca la huella "no aplica" del repo (S4): el hallazgo viaja
-- igual (auditoría) y la UI lo atenúa.
SELECT f.*, (fd.id IS NOT NULL)::bool AS dismissed
FROM findings f
JOIN reviews r ON r.id = f.review_id
JOIN pull_requests pr ON pr.id = r.pull_request_id
LEFT JOIN finding_dismissals fd
       ON fd.repository_id = pr.repository_id
      AND fd.file = f.file
      AND fd.category = f.category
WHERE r.pull_request_id = sqlc.arg('pull_request_id')::bigint
ORDER BY f.id;

-- name: UpdateFindingAccepted :exec
-- Outcome F5 (§6): el MetricsJob (T5) marca si la sugerencia del hallazgo
-- terminó aplicada (heurística por contenido sobre los patches posteriores).
-- Null = sin evaluar; nunca se des-marca a null una vez escrito.
UPDATE findings
SET accepted = $2
WHERE id = $1;

-- name: UpdateFindingGithubCommentID :exec
-- El publicador (§3.6.2) escribe el ID del comentario que produjo el
-- hallazgo: el dashboard permalinkea al comentario real, donde el bloque
-- suggestion de GitHub se aplica con un clic.
UPDATE findings
SET github_comment_id = $2
WHERE id = $1;

-- name: UpsertFindingDismissal :one
-- Marca la huella "no aplica" (S4): re-dismissar actualiza autor, motivo y
-- fecha (ON CONFLICT por la UNIQUE de huella+repo). El hallazgo original no
-- se toca — auditoría (§3.3).
INSERT INTO finding_dismissals (repository_id, file, category, dismissed_by, reason)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (repository_id, file, category)
DO UPDATE SET dismissed_by = EXCLUDED.dismissed_by,
              reason = EXCLUDED.reason,
              created_at = now()
RETURNING *;

-- name: GetFinding :one
SELECT * FROM findings WHERE id = $1;
