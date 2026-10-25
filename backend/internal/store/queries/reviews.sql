-- Reviews (guía §3.3): una por corrida; head_sha + base_sha son la identidad
-- de la corrida (§3.6).

-- name: CreateReview :one
-- Nace running con textos vacíos: summary/walkthrough/mermaid se completan al
-- finalizar la corrida. Target polimórfico (S6): exactamente uno de
-- pull_request_id / direct_push_id — el CHECK de la migración 0011 lo exige.
INSERT INTO reviews (pull_request_id, direct_push_id, head_sha, base_sha)
VALUES ($1, $2, $3, $4)
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
WHERE pull_request_id = sqlc.arg('pull_request_id')::bigint
ORDER BY id DESC
LIMIT 1;

-- name: GetPreviousReviewByPR :one
-- Corrida inmediatamente anterior a la última del PR (resolucion-hallazgos
-- S2): misma clave de orden que GetLatestReviewByPR, OFFSET 1. Sin filas con
-- una sola corrida → ErrNoRows: no hay comparación posible.
SELECT * FROM reviews
WHERE pull_request_id = sqlc.arg('pull_request_id')::bigint
ORDER BY id DESC
LIMIT 1 OFFSET 1;
