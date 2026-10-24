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

-- name: DeleteRepositoryCascade :exec
-- Borrado real del repo con sus dependientes: comments_sent, findings,
-- reviews, pull_requests y repo_index. Las FKs no tienen ON DELETE CASCADE
-- (§3.3) y el producto nunca borra un repo — la baja es el flag enabled
-- (§3.5). Esta query la usa la limpieza de los fixtures de test, que de
-- otro modo dejan repos basura en la base de desarrollo.
WITH d_comments AS (
    DELETE FROM comments_sent
    WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE repository_id = $1)
    RETURNING 1
), d_findings AS (
    DELETE FROM findings
    WHERE review_id IN (SELECT id FROM reviews WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE repository_id = $1))
    RETURNING 1
), d_reviews AS (
    DELETE FROM reviews
    WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE repository_id = $1)
    RETURNING 1
), d_pulls AS (
    DELETE FROM pull_requests WHERE repository_id = $1 RETURNING 1
), d_index AS (
    DELETE FROM repo_index WHERE repository_id = $1 RETURNING 1
)
DELETE FROM repositories AS r WHERE r.id = $1;
