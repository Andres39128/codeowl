-- Dedup de webhooks (guía §3.3): delivery IDs ya procesados.

-- name: CreateWebhookDelivery :one
-- Idempotente: solo la primera entrega inserta y devuelve la fila; en
-- duplicado ON CONFLICT no devuelve filas (pgx: pgx.ErrNoRows = ya procesado).
INSERT INTO webhook_deliveries (vcs, delivery_id)
VALUES ($1, $2)
ON CONFLICT (vcs, delivery_id) DO NOTHING
RETURNING *;

-- name: WebhookDeliveryExists :one
SELECT EXISTS (
    SELECT 1 FROM webhook_deliveries
    WHERE vcs = $1 AND delivery_id = $2
);

-- name: DeleteWebhookDeliveriesOlderThan :execrows
-- Retención del CleanupJob (guía §9.11): borra entregas más viejas que el
-- corte y devuelve la cantidad eliminada.
DELETE FROM webhook_deliveries
WHERE created_at < $1;

-- name: LastWebhookDeliveryAt :one
-- La última entrega recibida por VCS: prueba de que el webhook alcanza la
-- instancia (salud del repo, §9.9). Es por VCS y no por repo — la tabla es
-- un registro de dedup global (§3.6).
SELECT max(created_at)::timestamptz AS last_at FROM webhook_deliveries WHERE vcs = $1;
