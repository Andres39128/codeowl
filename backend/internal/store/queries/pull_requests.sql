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
