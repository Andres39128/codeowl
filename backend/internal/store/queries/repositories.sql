-- Repos conectados (guía §3.3): CRUD de settings; la desconexión es flag
-- (SetRepositoryEnabled), jamás delete (§3.5).

-- name: CreateRepository :one
-- Los secretos viajan cifrados AES-256-GCM en base64 (§9.2); en GitHub solo
-- se completan owner/name — el secret de webhook es global de la App (§9.3).
INSERT INTO repositories (vcs, external_id, owner, name, webhook_secret, secret_token, api_token, base_url, deploy_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetRepository :one
SELECT * FROM repositories WHERE id = $1;

-- name: GetRepositoryByVCSExternalID :one
-- Clave natural (vcs, external_id): el webhook resuelve el repo por ella.
SELECT * FROM repositories WHERE vcs = $1 AND external_id = $2;

-- name: ListRepositories :many
SELECT * FROM repositories ORDER BY owner, name;

-- name: UpdateRepository :one
-- Config editable desde settings; enabled va por SetRepositoryEnabled. vcs y
-- external_id son identidad: no se editan.
UPDATE repositories
SET owner = $2,
    name = $3,
    webhook_secret = $4,
    secret_token = $5,
    api_token = $6,
    base_url = $7,
    deploy_key = $8,
    review_drafts = $9,
    language = $10,
    chat_org_only = $11,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRepositoryEnabled :one
-- Desconexión como flag, sin delete (§3.5).
UPDATE repositories
SET enabled = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteRepository :exec
DELETE FROM repositories WHERE id = $1;
