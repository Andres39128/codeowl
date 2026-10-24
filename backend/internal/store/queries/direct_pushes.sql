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
-- Trazabilidad cronológica (S1): el listado trae repo (owner/name) y el
-- estado de su corrida más reciente — LEFT JOIN: un push puede estar aún sin
-- analizar (o con la corrida en vuelo). repository_id null = todos los
-- repos (narg: el dashboard global no filtra).
SELECT dp.*,
       r.owner AS repo_owner,
       r.name  AS repo_name,
       rv.id   AS latest_review_id,
       rv.status AS latest_review_status,
       rv.created_at AS latest_review_at
FROM direct_pushes dp
JOIN repositories r ON r.id = dp.repository_id
LEFT JOIN reviews rv ON rv.direct_push_id = dp.id
WHERE dp.repository_id = sqlc.narg('repository_id') OR sqlc.narg('repository_id') IS NULL
ORDER BY dp.created_at DESC, dp.id DESC
LIMIT 100;
