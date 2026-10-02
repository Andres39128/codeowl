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
