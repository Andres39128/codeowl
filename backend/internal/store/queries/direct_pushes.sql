-- Pushes directos a la rama base (trazabilidad-merges S6): registro e
-- idempotencia del webhook. El análisis vive en reviews (target polimórfico)
-- con sus findings — mismas queries de findings, otra ventana.

-- name: UpsertDirectPush :one
-- Idempotente por (repository_id, after_sha): el reintento del webhook reusa
-- la fila (ON CONFLICT refresca branch/committer y devuelve la fila original
-- — el análisis pendiente no se duplica).
INSERT INTO direct_pushes (repository_id, branch, before_sha, after_sha, committer)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (repository_id, after_sha)
DO UPDATE SET branch = EXCLUDED.branch,
              committer = EXCLUDED.committer
RETURNING *;

-- name: GetDirectPush :one
SELECT * FROM direct_pushes WHERE id = $1;

-- name: ListDirectPushes :many
-- Trazabilidad cronológica (S1): el listado trae repo (owner/name/vcs) y el
-- estado de su corrida más reciente — UNA fila por push: el JOIN con
-- reviews matchea SOLO la última corrida (subquery por id DESC; el id es
-- monotónico, mismo criterio que GetLatestReviewByPR). LEFT: un push puede
-- estar aún sin analizar (o con la corrida en vuelo). repository_id null =
-- todos los repos (narg: el dashboard global no filtra).
SELECT dp.*,
       r.owner AS repo_owner,
       r.name  AS repo_name,
       r.vcs   AS repo_vcs,
       rv.id   AS latest_review_id,
       rv.status AS latest_review_status,
       rv.created_at AS latest_review_at
FROM direct_pushes dp
JOIN repositories r ON r.id = dp.repository_id
LEFT JOIN reviews rv ON rv.id = (
    SELECT rv2.id FROM reviews rv2
    WHERE rv2.direct_push_id = dp.id
    ORDER BY rv2.id DESC
    LIMIT 1
)
WHERE dp.repository_id = sqlc.narg('repository_id') OR sqlc.narg('repository_id') IS NULL
ORDER BY dp.created_at DESC, dp.id DESC
LIMIT 100;

-- name: GetLatestReviewByDirectPush :one
-- ESPEJO de GetLatestReviewByPR (reviews.sql) con la ventana del push: el id
-- es monotónico, ordenar por id DESC es estable. ErrNoRows = push sin
-- corridas. Vive en este archivo (no en reviews.sql) para mantener junta la
-- ventana del push (T4).
SELECT * FROM reviews
WHERE direct_push_id = sqlc.arg('direct_push_id')::bigint
ORDER BY id DESC
LIMIT 1;

-- name: ListFindingsByDirectPush :many
-- ESPEJO de ListFindingsByPR (findings.sql), scoping por direct_push_id:
-- todos los hallazgos de todas las corridas del push. dismissed marca la
-- huella "no aplica" del repo (S4): el hallazgo viaja igual (auditoría) y
-- la UI lo atenúa.
SELECT f.*, (fd.id IS NOT NULL)::bool AS dismissed
FROM findings f
JOIN reviews r ON r.id = f.review_id
JOIN direct_pushes dp ON dp.id = r.direct_push_id
LEFT JOIN finding_dismissals fd
       ON fd.repository_id = dp.repository_id
      AND fd.file = f.file
      AND fd.category = f.category
WHERE r.direct_push_id = sqlc.arg('direct_push_id')::bigint
ORDER BY f.id;
