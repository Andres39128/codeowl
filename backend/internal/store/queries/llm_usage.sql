-- Uso de LLM (guía §3.3): base de las métricas F5 y de los presupuestos (§9.6).

-- name: CreateLlmUsage :one
-- Una fila por llamada LLM; job_id null en la prueba de conexión de settings;
-- review_id null fuera de corrida (chat, index, pruebas — §6 F5).
INSERT INTO llm_usage (job_id, review_id, role, provider, model, tokens_in, tokens_out)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListLlmUsageByJob :many
SELECT * FROM llm_usage WHERE job_id = $1 ORDER BY id;

-- name: GetAvgLlmTokensPerReview :one
-- Costo LLM medio por review (§6 F5): costo = tokens (in+out), sin precio
-- por modelo modelado. Un grupo por review con linkage; las reviews sin
-- usage no cuentan. El AVG es sobre totales por review, no por llamada.
-- Ventana (T7): usos con created_at >= since — la misma convención del
-- resumen de métricas (los outcomes se evalúan sobre filas en la ventana),
-- así el endpoint /api/metrics es consistente en todas sus tasas.
SELECT COALESCE(AVG(per_review.total_tokens), 0)::float8 AS avg_tokens_per_review,
       COUNT(*) AS reviews_counted
FROM (
    SELECT SUM(tokens_in + tokens_out) AS total_tokens
    FROM llm_usage
    WHERE review_id IS NOT NULL AND created_at >= sqlc.arg('since')
    GROUP BY review_id
) per_review;

-- name: SumLlmUsageByDateRange :one
-- Presupuestos (§9.6): tokens consumidos en la ventana [desde, hasta).
SELECT COALESCE(SUM(tokens_in), 0)::bigint AS tokens_in,
       COALESCE(SUM(tokens_out), 0)::bigint AS tokens_out
FROM llm_usage
WHERE created_at >= $1 AND created_at < $2;
