# Trazabilidad de merges sin PR

Header: registrar y analizar con el pipeline completo (igual que un PR) los
pushes/merges directos a la rama base que hoy son invisibles para codeowl.
Salida solo en dashboard: no hay PR donde publicar.

## Specs

- S1 (verbatim): "generemos otra vista de la trazabilidad de los merge que se
  han realizado sin pr, con un analisis de cambios como se hace en el pr"
- S2 (verbatim, alcance): el usuario eligió "Completo como PR": "SAST +
  agentes LLM + resumen Mermaid, idéntico a un PR" para cada push a main.
- S3 (contexto): los pushes a branches de feature NO se analizan (se
  analizan cuando abran el PR); solo la rama base (default_branch del repo).
- S4: detección por webhook `push` (GitHub/GitLab): before+after válidos
  (crear/borrar rama no cuenta), ref == rama base. Dedup por (repo, after).
- S5: el análisis respeta review.yaml (path_filters) y NO publica nada en
  el VCS: resumen, hallazgos y diff viven en el dashboard.
- S6: modelo — tabla nueva `direct_pushes` (repository, before_sha,
  after_sha, branch, committer, created_at); `reviews` pasa a target
  polimórfico: `pull_request_id` nullable + `direct_push_id` nullable,
  CHECK exactamente uno de los dos. Findings/dedup/dismissals/llm_usage
  siguen colgando de reviews sin cambios.

## Tasks

- T1 (S6): migración 0011 + store (direct_pushes, reviews polimórfico,
  queries upsert/list/get) + tests. [184210af]
- T2 (S4): webhook push GitHub + GitLab (parse, filtro rama base, dedup,
  enqueue PushReviewJob) + adapter GetCommitDiff (compare API ambos VCS)
  + tests. [184210af]
- T3 (S2, S5): PushReviewJob — pipeline reutilizado con target push (diff
  before..after, SAST + agentes + resumen, sin publicación), stale si llega
  push nuevo + tests de integración. [pending]
- T4 (S1): API — GET /api/pushes (lista), GET /api/pushes/{id} (detalle:
  resumen, findings, diff) + tests. [184210af]
- T5 (S1): dashboard — página Trazabilidad (lista + detalle + nav) reusando
  componentes del detalle de PR + tests. [184210af]
- T6 (docs): guía §3.3/§3.6 (modelo y flujo push), mapa, guía_usuario +
  log de cierre. [pending]

## Log

- L1 (pedido original, verbatim): "Quita todo lo de acme y adicionalmente
  generemos otra vista de la trazabilidad de los merge que se han realizado
  sin pr, con un analisis de cambios como se hace en el pr, ¿ si me
  entiendes?"
- L2: limpieza acme hecha antes del feature: 16 repos de test borrados de
  la BD de dev (residuo de corridas de test con cleanup fallido, bug del
  cascade ya fixeado en 796bf327).
- L3: el usuario eligió "Completo como PR" (SAST + agentes LLM + resumen,
  idéntico a un PR) para la profundidad del análisis.
- L4: rama trazabilidad-merges apilada sobre resolucion-hallazgos (aún no
  mergeada — el stack dev sirve ambas).
