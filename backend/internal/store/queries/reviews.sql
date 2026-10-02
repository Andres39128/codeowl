-- Reviews (guía §3.3): una por corrida; head_sha + base_sha son la identidad
-- de la corrida (§3.6).

-- name: CreateReview :one
-- Nace running con textos vacíos: summary/walkthrough/mermaid se completan al
-- finalizar la corrida.
INSERT INTO reviews (pull_request_id, head_sha, base_sha)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetReview :one
SELECT * FROM reviews WHERE id = $1;

-- name: UpdateReviewStatus :one
-- success/partial/stale/failed según el desenlace (§3.3).
UPDATE reviews
SET status = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateReviewSummary :one
UPDATE reviews
SET summary = $2,
    walkthrough = $3,
    mermaid = $4,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetLatestReviewByPR :one
-- Última corrida del PR: el id es monotónico, ordenar por id DESC es estable.
SELECT * FROM reviews
WHERE pull_request_id = $1
ORDER BY id DESC
LIMIT 1;
