-- Métricas de F5 (guía §6 F5): columnas de outcome sobre las filas existentes
-- — sin tablas nuevas (§6 F5: "outcome persistido sobre las filas existentes").
-- Additive-only, forward-only (§3.3). Todo nullable: null = aún sin evaluar,
-- así las métricas no mezclan filas viejas con outcomes nuevos.

-- Riesgo 0-100 del PR (§6 F5, triage): proxy computable del tail de Run —
-- tamaño del diff, rutas sensibles, severidad publicada, proporción de tests.
-- Se pisa en cada corrida (T3); null mientras ninguna corrida lo compute.
ALTER TABLE pull_requests ADD COLUMN risk_score INT NULL;

-- Estado final del thread inline (§6 F5, insumo de la tasa de falsos
-- positivos): lo escribe el MetricsJob al cierre del PR. Null: thread aún no
-- evaluado (o no-inline: summary/chat no tienen thread).
ALTER TABLE comments_sent ADD COLUMN resolved BOOLEAN NULL;

-- ¿La sugerencia del comentario inline terminó aplicada? La heurística de
-- contenido (bloque normalizado en el patch de un commit posterior, §6 F5) se
-- evalúa POR COMENTARIO inline: el insumo del VCS es el thread, no el finding
-- (T5). Desviación chica de la decisión 1 del doc F5 (justificada): la tasa
-- FP sale de resolved ∧ ¬applied sobre ESTA tabla, sin joins por huella;
-- findings.accepted queda como el espejo por finding para el % aceptados.
ALTER TABLE comments_sent ADD COLUMN applied BOOLEAN NULL;

-- Espejo por finding de la heurística de aceptación (§6 F5 "% de hallazgos
-- aceptados"): la sugerencia vive en el finding; el MetricsJob (T5) lo marca
-- cuando su bloque aparece aplicado. Null: sin evaluación.
ALTER TABLE findings ADD COLUMN accepted BOOLEAN NULL;

-- Linkage de costo por review (§6 F5): el gateway lo pisa desde el ctx
-- WithReview (id decimal de reviews.id). Null fuera de corrida: chat,
-- indexación y pruebas de conexión — el costo por review son las reviews.
-- Sin FK, igual que job_id (precedente 0003): es un dato de métrica, la
-- agregación filtra por IS NOT NULL.
ALTER TABLE llm_usage ADD COLUMN review_id BIGINT NULL;
