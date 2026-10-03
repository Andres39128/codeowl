-- Métricas F5 (guía §6 F5): cycle time, aceptados y tasa FP salen directo de
-- las filas existentes de pull_requests/findings/comments_sent — sin tablas
-- nuevas. Los conteos crudos viajan acá; las tasas (% aceptados, tasa FP) se
-- computan al vuelo en la API desde estos numeradores y denominadores.

-- name: GetMetricsSummary :one
-- Ventana desde `since`: merged_at para PRs merged (el merge es un close con
-- merged_at, §3.3), created_at para findings y comments_sent (los outcomes
-- se evalúan sobre filas publicadas en la ventana).
-- AvgCycleTimeHours: COALESCE a 0 con el mismo motivo del sentinel del
-- listado de PRs — sqlc no infiere nullabilidad a través del subselect y
-- generaría float64 que revienta al escanear el NULL real de una ventana sin
-- PRs merged. Sentinel: MergedPrs = 0 → no hay dato de cycle time.
-- FalsePositives: threads inline resueltos cuya sugerencia NO terminó
-- aplicada (resolved ∧ ¬applied, §6 F5); denominador = resolved_comments.
WITH merged AS (
    SELECT COUNT(*) AS merged_prs,
           AVG(EXTRACT(EPOCH FROM (merged_at - created_at)) / 3600.0)::float8 AS avg_cycle_time_hours
    FROM pull_requests
    WHERE state = 'closed' AND merged_at IS NOT NULL AND merged_at >= sqlc.arg('since')
)
SELECT
    (SELECT merged_prs FROM merged)                            AS merged_prs,
    COALESCE((SELECT avg_cycle_time_hours FROM merged), 0.0)::float8 AS avg_cycle_time_hours,
    -- Columnas calificadas: el motor de sqlc resuelve refs sin calificar
    -- contra TODAS las tablas del statement (quirk suyo; PG scopea bien).
    (SELECT COUNT(*) FROM findings f
     WHERE f.accepted = true AND f.created_at >= sqlc.arg('since'))       AS accepted_findings,
    (SELECT COUNT(*) FROM findings f
     WHERE f.accepted IS NOT NULL AND f.created_at >= sqlc.arg('since'))  AS evaluated_findings,
    (SELECT COUNT(*) FROM comments_sent cs
     WHERE cs.resolved = true AND cs.created_at >= sqlc.arg('since'))     AS resolved_comments,
    (SELECT COUNT(*) FROM comments_sent cs
     WHERE cs.resolved = true AND cs.applied = false
       AND cs.created_at >= sqlc.arg('since'))                            AS false_positives;
