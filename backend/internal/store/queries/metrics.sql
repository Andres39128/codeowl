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

-- name: ListResolutionPerWeek :many
-- Tasa de resolución por repo/semana (resolucion-hallazgos S3): cada corrida
-- con una inmediata anterior en el mismo PR compara huellas (file+category,
-- el MISMO match que dismissals 0010): cada hallazgo de la anterior queda
-- resuelto (huella no reapareció) o persistente (reapareció). Semana =
-- created_at de la corrida NUEVA, cuando la resolución se hizo visible.
-- Huellas dismissadas no cuentan: veredicto humano (S4).
-- Predecesor inmediato por self-join (reviews.id es monótono, §3.3) y no
-- por LAG: el motor de sqlc no rastrea columnas de window functions en
-- CTEs. Solo la corrida NUEVA se filtra a la ventana — la anterior puede
-- quedar fuera y la comparación sigue siendo válida.
WITH prev AS (
    SELECT rv2.id AS run_id, rv2.created_at, pr2.repository_id,
           f.file, f.category
    FROM reviews rv2
    JOIN pull_requests pr2 ON pr2.id = rv2.pull_request_id
    JOIN reviews rv1 ON rv1.pull_request_id = rv2.pull_request_id
                   AND rv1.id < rv2.id
    JOIN findings f ON f.review_id = rv1.id
    WHERE rv2.created_at >= sqlc.arg('since')
      -- rv1 es la inmediata anterior: no hay corrida entre rv1 y rv2.
      AND NOT EXISTS (
          SELECT 1 FROM reviews rx
          WHERE rx.pull_request_id = rv2.pull_request_id
            AND rx.id > rv1.id AND rx.id < rv2.id
      )
      AND NOT EXISTS (
          SELECT 1 FROM finding_dismissals fd
          WHERE fd.repository_id = pr2.repository_id
            AND fd.file = f.file AND fd.category = f.category
      )
)
SELECT date_trunc('week', p.created_at)::date AS week,
       p.repository_id,
       (r.owner || '/' || r.name)::text AS repository,
       COUNT(*) FILTER (WHERE NOT EXISTS (
           SELECT 1 FROM findings cf
           WHERE cf.review_id = p.run_id AND cf.file = p.file AND cf.category = p.category
       ))::bigint AS resolved,
       COUNT(*) FILTER (WHERE EXISTS (
           SELECT 1 FROM findings cf
           WHERE cf.review_id = p.run_id AND cf.file = p.file AND cf.category = p.category
       ))::bigint AS persistent
FROM prev p
JOIN repositories r ON r.id = p.repository_id
GROUP BY week, p.repository_id, repository
ORDER BY week;

-- name: ListReviewsPerWeek :many
-- PRs revisados por semana ISO (S3): una fila por semana con corridas en la
-- ventana; DISTINCT review.pull_request_id cuenta PRs, no corridas (un PR
-- con 3 push = 1 PR revisado esa semana). Semanas sin actividad no existen
-- (null honesto: la serie es puntos, no promedios inflados).
SELECT date_trunc('week', rv.created_at)::date AS week,
       COUNT(DISTINCT rv.pull_request_id)::bigint AS prs,
       COUNT(*)::bigint AS reviews
FROM reviews rv
WHERE rv.created_at >= $1
GROUP BY week
ORDER BY week;
