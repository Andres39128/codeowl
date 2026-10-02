-- Proveedores LLM (guía §3.3): CRUD de settings + cadena de failover por rol.

-- name: ListLlmProviders :many
-- Orden estable por (priority, id): el orden de failover dentro del rol (§3.3).
SELECT * FROM llm_providers ORDER BY priority, id;

-- name: GetLlmProvider :one
SELECT * FROM llm_providers WHERE id = $1;

-- name: CreateLlmProvider :one
-- api_key viaja cifrada AES-256-GCM en base64 (§9.2).
INSERT INTO llm_providers (base_url, model, api_key, role, priority, enabled)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateLlmProvider :one
UPDATE llm_providers
SET base_url = $2,
    model = $3,
    api_key = $4,
    role = $5,
    priority = $6,
    enabled = $7,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteLlmProvider :exec
DELETE FROM llm_providers WHERE id = $1;

-- name: ListEnabledLlmProvidersByRole :many
-- Cadena de failover del rol: solo habilitados, en orden de prioridad (§3.3).
SELECT * FROM llm_providers
WHERE role = $1 AND enabled
ORDER BY priority, id;
