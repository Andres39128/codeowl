-- Uso de LLM (guía §3.3): base de las métricas F5 y de los presupuestos (§9.6).

-- name: CreateLlmUsage :one
-- Una fila por llamada LLM; job_id null en la prueba de conexión de settings.
INSERT INTO llm_usage (job_id, role, provider, model, tokens_in, tokens_out)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListLlmUsageByJob :many
SELECT * FROM llm_usage WHERE job_id = $1 ORDER BY id;

-- name: SumLlmUsageByDateRange :one
-- Presupuestos (§9.6): tokens consumidos en la ventana [desde, hasta).
SELECT COALESCE(SUM(tokens_in), 0)::bigint AS tokens_in,
       COALESCE(SUM(tokens_out), 0)::bigint AS tokens_out
FROM llm_usage
WHERE created_at >= $1 AND created_at < $2;
