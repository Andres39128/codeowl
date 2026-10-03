-- Comentarios publicados por PR (guía §3.3): idempotencia y actualización.

-- name: CreateCommentSent :one
INSERT INTO comments_sent (pull_request_id, review_id, comment_id, type, file, category, anchor, parent_comment_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetCommentsSentByPRAndType :many
-- Todos los comentarios de un tipo para el PR (el summary es único en la
-- práctica; los inline pueden ser varios).
SELECT * FROM comments_sent
WHERE pull_request_id = $1 AND type = $2
ORDER BY id;

-- name: GetCommentSentByAnchor :one
-- Dedup de inline: huella file + categoría + ancla dentro del PR (§3.6).
SELECT * FROM comments_sent
WHERE pull_request_id = $1 AND anchor = $2;

-- name: UpdateCommentSentCommentID :one
-- El comentario se edita in-place: cambia su ID externo en el VCS.
UPDATE comments_sent
SET comment_id = $2
WHERE id = $1
RETURNING *;

-- name: UpdateCommentsSentResolved :exec
-- Estado final del thread inline (§6 F5): el MetricsJob (T5) lo escribe al
-- cierre del PR. Null = sin evaluar.
UPDATE comments_sent
SET resolved = $2
WHERE id = $1;

-- name: UpdateCommentsSentApplied :exec
-- ¿La sugerencia terminó aplicada? Heurística por comentario inline (T5):
-- con resolved alimenta la tasa FP (§6 F5) sin joins por huella.
UPDATE comments_sent
SET applied = $2
WHERE id = $1;
