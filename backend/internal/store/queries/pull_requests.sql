-- PRs observados (guía §3.3): clave natural repo + número/iid en el VCS.

-- name: UpsertPullRequest :one
-- Upsert idempotente por webhook: el mismo PR re-procesado no duplica fila.
-- merged_at usa COALESCE: un evento posterior sin merge no borra el registro.
-- created_at es la fecha del PR en el VCS: se mantiene la primera (§3.3).
INSERT INTO pull_requests (repository_id, number, author, state, head_sha, base_ref, base_sha, merged_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (repository_id, number) DO UPDATE
SET author = EXCLUDED.author,
    state = EXCLUDED.state,
    head_sha = EXCLUDED.head_sha,
    base_ref = EXCLUDED.base_ref,
    base_sha = EXCLUDED.base_sha,
    merged_at = COALESCE(EXCLUDED.merged_at, pull_requests.merged_at),
    updated_at = now()
RETURNING *;

-- name: GetPullRequest :one
-- Por ID: el pipeline de review resuelve la fila desde el job (§3.6.1.3 —
-- re-chequeo de la identidad de la corrida contra el estado actual del PR).
SELECT * FROM pull_requests WHERE id = $1;

-- name: GetPullRequestByRepoNumber :one
SELECT * FROM pull_requests WHERE repository_id = $1 AND number = $2;

-- name: ListOpenPullRequestsByRepo :many
SELECT * FROM pull_requests
WHERE repository_id = $1 AND state = 'open'
ORDER BY number DESC;

-- name: UpdatePullRequestState :one
-- Close/merge/reopen: el merge es un close con merged_at registrada (§3.3);
-- un reopen pasa merged_at null.
UPDATE pull_requests
SET state = $2,
    merged_at = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdatePullRequestRiskScore :exec
-- Risk score del tail de Run (§6 F5, T3): proxy 0-100 computable, se pisa en
-- cada corrida. Toca updated_at: la otra escritura de la tabla (estado) hace
-- lo mismo y el listado del dashboard ordena por ahí.
UPDATE pull_requests
SET risk_score = $2,
    updated_at = now()
WHERE id = $1;

-- name: ListPullRequestsWithLatestReview :many
-- Listado del dashboard (guía §6 F3): TODOS los PRs — los cerrados conservan
-- valor de auditoría — con su última corrida y el conteo de hallazgos de esa
-- corrida por severidad. El LATERAL trae la review más reciente (el id es
-- monotónico: ORDER BY id DESC es estable) y el COUNT con FILTER se resuelve
-- en la misma query: una sola ida a la BD, sin N+1.
--
-- Los COALESCE existen por una limitación de sqlc: no infiere nullabilidad a
-- través de LEFT JOIN LATERAL y generaría int64/string que revientan al
-- escanear el NULL real de un PR sin corridas. El sentinel es review_id = 0
-- (review_id/status/created_at interpolados): el handler lo mapea a
-- latest_review: null.
SELECT pr.id,
       pr.repository_id,
       pr.number,
       pr.author,
       pr.state,
       pr.head_sha,
       pr.base_ref,
       pr.updated_at,
       pr.risk_score,
       repo.owner   AS repo_owner,
       repo.name    AS repo_name,
       repo.vcs     AS repo_vcs,
       COALESCE(r.id, 0)                            AS review_id,
       COALESCE(r.status, '')                       AS review_status,
       COALESCE(r.created_at, to_timestamp(0))      AS review_created_at,
       COUNT(f.id) FILTER (WHERE f.severity = 'high')   AS high,
       COUNT(f.id) FILTER (WHERE f.severity = 'medium') AS medium,
       COUNT(f.id) FILTER (WHERE f.severity = 'low')    AS low
FROM pull_requests pr
JOIN repositories repo ON repo.id = pr.repository_id
LEFT JOIN LATERAL (
    SELECT id, status, created_at
    FROM reviews
    WHERE pull_request_id = pr.id
    ORDER BY id DESC
    LIMIT 1
) r ON true
LEFT JOIN findings f ON f.review_id = r.id
GROUP BY pr.id, repo.id, r.id, r.status, r.created_at
ORDER BY pr.updated_at DESC;
