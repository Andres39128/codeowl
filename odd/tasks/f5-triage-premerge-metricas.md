# Fase 5 — Triage + Pre-merge + Métricas

## Objetivo

Implementar F5 según la guía §6: risk scoring, dashboard de triage, agente Pre-merge (veredicto como sección del resumen), métricas (cycle time, aceptados, tasa FP, costo LLM). Además cierra los gaps de ciclo de vida de jobs registrados en F4: descarte de jobs al cierre (§3.5) y re-encolo transaccional stale (§3.6.1.5).

## Entregable (guía)

Cola de triage por riesgo en dashboard; veredicto pre-merge; métricas.

## Alcance autorizado

- Checklist F5 §6 completo (4 ítems) + los dos gaps de jobs (§3.5 descarte, §3.6.1.5 re-encolo tx).
- Branch `fase-5` desde fase-4 (00d650bb).
- Merge y push: diferidos por el usuario hasta terminar todas las fases (F5 es la última).

## Realidad vs mapa (exploración 2026-10-03)

1. **PRTimeline devuelve solo commits metadata** (SHA/message/author): sin threads, sin patches, sin fechas. El MetricsJob necesita: threads con estado resuelto (tasa FP) y patches de commits (heurística de sugerencia aplicada). Ambos adapters ya declaran el TODO F5 en comentarios.
2. **Cancelación de jobs: NO existe** (ningún JobCancel/JobList/WorkWithTx en el código). Los comentarios de los handlers de cierre mienten ("lo hace el worker" — no existe tal tarea). River v0.48.0 soporta JobCancel/JobCancelTx/JobList/InsertTx/WorkWithTx.
3. **llm_usage.job_id siempre NULL** (logUsage nunca lo pisa) → costo por review imposible hoy. `llm.WithReview(ctx, revID)` YA viaja en el ctx de toda llamada de una review.
4. **risk_score/columnas de métricas ausentes**: migración nueva (la guía misma nombra el patrón `0007_add_risk_score...`).
5. **prs.go sin query params** (cero r.URL.Query() en la API) y Table sin sort/filter — el triage es el primero en ambos.
6. **features/triage no existe**; PAGES/Route hay que extenderlos.
7. Sin agente/prompt Pre-merge; patrón a copiar: runSummarizer (llamada única, rol review, retries + parse JSON).
8. Diff size no se persiste en ningún lado (transiente en Run).

## Decisiones de diseño

1. **Migración 0005** (additive, forward-only): `pull_requests.risk_score INT NULL` (0-100), `comments_sent.resolved BOOLEAN NULL` (estado final del thread, lo escribe el MetricsJob), `findings.accepted BOOLEAN NULL` (sugerencia aplicada por heurística), `llm_usage.review_id BIGINT NULL` (linkage de costo). El gateway pisa `review_id` desde el ctx `WithReview` existente (chat/index/tests quedan NULL — el costo por review son las reviews).
2. **PRTimeline extendido**: `PRCommit{SHA, Message, Author, AuthoredAt, Patch}` (patch truncado a 64KB, tope 50 commits — insumo de la heurística por contenido) + `Thread{CommentID string, Resolved bool}`. GitHub: threads vía GraphQL (el token de instalación lo soporta; REST no expone resolución de threads) mapeando `databaseId`; commits con patch vía `GET /commits/{sha}` con media-type diff. GitLab: `GET /merge_requests/{iid}/discussions` (notes con `resolved`) y `GET /repository/commits/{sha}/diff` reensamblado. Best-effort: fallo de threads/patches NO falla el job (log + lo que se pudo).
3. **MetricsJob** (`KindMetrics`, cola ops, sin LLM, args {RepositoryID, PullRequestID}): encolado por el handler de cierre de ambos VCS. Guard: PR debe estar closed (si reabrió, skip+log). Proceso: FetchPRTimeline → resolved por comment_id match → `comments_sent.resolved`; aceptados: finding con suggestion publicada cuyo bloque aparece (normalizado whitespace) en el patch de un commit posterior a `comments_sent.created_at` (skew 5m) → `findings.accepted`. Tasas se computan al vuelo en la API desde los flags (no se persisten rates). Cycle time: `created_at → merged_at`. Costo: `SUM(llm_usage) JOIN reviews` por `review_id`.
4. **Descarte al cierre** (§3.5): `JobQueue` gana `CancelPendingByPR(ctx, prID) (int, error)` — RiverQueue lo implementa con SELECT raw sobre `river_job` (precedente: api/queue.go es el único SQL crudo fuera de sqlc) por kind IN (review, chat) + states vivos + `args->>'pull_request_id'`, y `JobCancel` por cada uno. Ambos handlers de cierre: descartan jobs, encolan MetricsJob, y merge además encola IndexJob (ya existe). Comentarios stale corregidos.
5. **Re-encolo transaccional** (§3.6.1.5): ReviewJobWorker pasa a `WorkWithTx` (river v0.48): al completar, dentro de la tx del job, re-chequear identidad `(head_sha, base_sha)` del PR vs args; si difiere → `InsertTx` de la review del estado actual en la MISMA tx. Cierra el interleaving que perdía reviews (push durante job en vuelo: el encolado del webhook era descartado por unicidad).
6. **Risk scoring** (en el tail de Run, tras conocer findings): score 0-100 transparente = suma ponderada normalizada: tamaño de diff (bandas), archivos sensibles tocados (patrones config `RISK_SENSITIVE_PATHS`, default: auth, secrets/credential, migrations, CI/workflows, deploy), severidad de findings publicados (high pesa más), proporción de archivos de test en el diff (menos test proportion = más riesgo). Pesos como consts documentadas; SOLO los patrones son config (guía pide "patrones configurables"). Persiste `pull_requests.risk_score` cada corrida (query nueva). El score es proxy computable — nada ejecuta tests (§6 F5).
7. **Agente Pre-merge** (rol review, prompt `premerge_system.md` con {{LANGUAGE}}): input = counts por severidad del set publicado, coverage/status, stats del diff, risk score; output JSON `{verdict: "apto"|"apto_con_observaciones"|"no_apto", checklist: [{item, ok}], resumen}`. Validación §9.8 (retries → descarte registrado). Corre tras la fase 2 de publicación, antes de la re-edición final: el veredicto es una SECCIÓN del resumen edit in place (comments_sent no gana tipo — §3.6). Fallo del agente → resumen sin sección + log (jamás falla la corrida). El veredicto NUNCA se publica como review action del VCS (no bloquea merge — §1.1: es texto informativo).
8. **API**: `GET /api/prs` gana query params (primer `r.URL.Query()` de la API): `sort=risk` (risk_score DESC NULLS LAST), `state`, `repo`, `severity` (≥1 finding de esa severidad en la última review). `prView` gana `risk_score *int`. Nuevo `GET /api/metrics` (member): cycle time medio (merged últimos 30d), % aceptados, tasa FP (resolved sin accepted), costo LLM medio por review.
9. **Dashboard triage** (`features/triage`): tabla de PRs ordenada por riesgo (badge alta/media/baja por bandas del score), filtros repo (select desde /api/prs data) y severidad + estado open default; server sort `sort=risk`, filtros severity/repo client-side (single-org, dataset chico). Ruta `triage` member-visible. Table/prs patterns reutilizados; sort/filter viven EN la feature (Table no cambia — sin abstracción prematura).

## Tareas

- [x] T1 — feat(store): migración 0005 (risk_score, resolved, accepted, review_id) + queries métricas + gateway pisa review_id desde ctx (ruta: delegado)
  - Commit f61d700592e7: 0005_f5_metrics.sql (risk_score INT, resolved/applied BOOLEAN en comments_sent, accepted BOOLEAN en findings, llm_usage.review_id), queries: UpdatePullRequestRiskScore/UpdateCommentsSentResolved/UpdateCommentsSentApplied/UpdateFindingAccepted/GetMetricsSummary, +652/−22
  - Desviación aceptada: comments_sent.applied (además de findings.accepted) — la heurística de contenido se evalúa por comentario inline; FP = resolved∧¬applied directo. Nota para T3/T5/T7: params pgtype.Int4/Bool, MergedPrs==0 sentinel
- [x] T2 — feat(vcs): PRTimeline extendido — commits con patch+fecha, threads con resolución (GitHub GraphQL + GitLab discussions) (ruta: delegado)
  - Commit ea744adad94a: +773/−24, 6 tests nuevos (patches/fechas/threads, caps, best-effort); MaxTimelineCommits=50/MaxPatchBytes=64KB compartidos
  - BUG REAL de F1 corregido: newAPIRequest usaba paths relativos en http.Client.Do — toda llamada REST de GitHub fallaría en runtime (los tests integración stubean el adapter, por eso nunca se vio). Fix en el punto único + baseURL stubeable
  - Mappings: GitHub comment_id = REST id = GraphQL databaseId; GitLab comment_id = ID de DISCUSSION (postDiscussion persiste eso — T5 matchea as-is)
- [x] T3 — feat(review): risk scoring — cálculo transparente, patrones sensibles config, persist en pull_requests (ruta: delegado)
  - Commit 6f89e01e7707: risk.go (147) + tests (14 subtests ComputeRisk), RISK_SENSITIVE_PATHS config, +449/−15
  - Fórmula (máx 100): size 0-35 bandas · sensitive min(hits,5)×5 = 0-25 · findings high12/med5/low2 cap 30 · testShare (1-ratio)×10 solo >50 líneas. Persist solo en no-stale; fallo de persist no degrada
  - GAP F4 detectado (fix en T4): cmd/worker/main.go no cablea ReviewContextMaxChars al review.Config — el override del env no efectivo (default 4000 corre igual)
- [x] T4 — feat(review): agente Pre-merge — prompt, veredicto+checklist como sección del resumen edit in place (ruta: delegado)
  - Commit 5fa707a5a159: premerge_system.md + runPreMerge/parsePreMerge + premergeSection en finalSummary, 6 tests (9 subtests parse), +579/−35
  - Notas: veredicto advisory (nunca VCS review action §1.1); enum de veredicto fijo en español, resumen/checklist en {{LANGUAGE}}; corre post fase-2 (inline-failure re-edita sin veredicto — honesto); NO se persiste como dato (solo texto del summary, §3.6); fix del wiring ReviewContextMaxChars (gap F4) incluido; 6 asserts de call-count actualizados (+1 llamada premerge, no cacheada)
- [x] T5 — feat(jobs): descarte de jobs al cierre (CancelPendingByPR) + MetricsJob (threads/commits → resolved/accepted) + handlers de cierre (ruta: delegado)
  - Commit fa009fd21aca: +789/−56 en 13 archivos. JobQueue.CancelPendingByPR (SQL crudo river_job + JobCancel por id, best-effort); KindMetrics/MetricsJobArgs/EnqueueMetricsJob (cola ops, 3 intentos, sin unicidad); MetricsJobWorker{Store, Provider} (guard closed, resolved por comment_id, applied/accepted por heurística de contenido sobre líneas '+' de patches posteriores, skew 5m); handlers de cierre de ambos adapters: cancel PRIMERO → MetricsJob → (merge) IndexJob; registro en cmd/worker/main.go
  - Heurística: findings.suggestion guarda código crudo (el fence lo agrega la publicación) → normalizar whitespace + substring contra líneas agregadas del patch; match comentario↔finding por huella (review,file,categoría): 1 candidato gana, N se desambigan por ancla numérica (=line), simbólica ambigua → NULL honesto
  - Notas para T6-T8: merge encola MetricsJob TAMBIÉN (todo cierre); stubs JobQueue de tests ganaron CancelPendingByPR; comment_id GitLab = discussion id matchea as-is; commit viejo al comentario no aplica (skew 5m, AuthoredAt zero → elegible)
- [x] T6 — feat(jobs): re-encolo transaccional stale — ReviewJobWorker a WorkWithTx (§3.6.1.5) (ruta: delegado)
  - Commit 3f9869c9ffac: +320; mecanismo VERIFICADO contra river v0.48 (no existe WorkWithTx ahí): ClientFromContextSafely + tx propia + JobCompleteTx + InsertTx; unicidad sin conflicto (JobCompleteTx corre ANTES del insert en la misma tx — evidencia del partial index del module cache)
  - Corrección de premisa: unique_key es por IDENTIDAD (sha256 kind+args) — el push con nueva identidad encola aunque el job viejo corra; el re-encolo tx cierra la ventana residual same-identity/entre-chequeos (§3.6.1.6). Guard: state==open. Interleaving e2e PASS (6.17s)
- [x] T7 — feat(api): query params en /api/prs (sort/state/repo/severity) + risk_score en prView + GET /api/metrics (ruta: delegado)
  - Commit f540f820ac84: sort/filter en Go sobre la query sqlc única (ponytail: single-org, cientos de PRs), params estrictos fail closed (400 en español), risk_score DESC estable con updated_at de tiebreak y nil al final; GET /api/metrics member-visible (composeMetrics pura: tasas null cuando denominador 0, days clamp 1-365 / no numérico 400); GetAvgLlmTokensPerReview ganó ventana `since` (misma convención del resumen — T1 no la traía); 10 tests api verdes (6 nuevos + 3 de F3 regRESIÓN + compose puro), suite completa verde con y sin DATABASE_URL, go vet OK
- [x] T8 — feat(dashboard): features/triage — PRs por riesgo, filtros repo/severidad, ruta member (ruta: delegado)
  - Commit 80329f6e4391: features/triage/{Triage.tsx (320), Triage.test.tsx (253, 11 tests), view.ts (riskBandOf)} + Route/PAGES/Layout nav + PrView.risk_score y MetricsView en lib/types, +659/−8
  - Métricas: Card compacta "Métricas (últimos 30 días)" con /api/metrics (refetch 60s) — cycle time (1 decimal), % aceptados, tasa FP, costo medio tokens/review; null → "—" con title "Sin datos suficientes" (null honesto §9.9), sin gráficos
  - Filtros: estado server-side (abiertos default → state=open; todos omite el param — la API trata ausencia como sin filtro, verificado contra prs.go queryChoice), severidad/repo client-side; repo select derivado de los datos traídos; sort queda 100% server (el test asume el orden servido)
  - Badges de riesgo por bandas 0-39/40-69/70-100 con tonos alta/media/baja; null → "—" muted; fila → #/prs/<id> (patrón Prs)
  - 89 tests verdes (77 previos + 11 triage + 1 router), lint/tsc/build OK
  - Gotcha reconfirmado: selects con onInput, NO onChange — la normalización de preact/compat quiebra el change bajo jsdom (convención ya documentada en ProviderModal; costó 5 tests fallando antes de recordar)
- [x] T9 — test(integration): F5 e2e (cierre → descarte+MetricsJob → outcome; pre-merge en resumen; riesgo persistido; /api/metrics) + mapa/docs final (ruta: delegado)
  - Commit 2383526fe3c8: f5_triage_metrics_test.go (3 tests, +660) + fix de estabilidad en pipeline_test.go (newTempClone) + mapa (rest_endpoints /api/metrics + params /api/prs, reglas de jobs con la semántica del MetricsJob, triage con métricas, RISK_SENSITIVE_PATHS), +677/−4
  - TestF5CierreMetricsOutcome: webhook closed (merge) por el Server real → SOLO MetricsJob encolado (sin proveedor embedding no va IndexJob) + state=closed con merged_at; worker directo contra stub con timeline fixture: resolved por comment_id (inline-1 true, inline-2 false, hilo ajeno ignorado), applied por contenido en commit POSTERIOR (commit anterior al comentario con la sugerencia NO cuenta — fecha manda), accepted espejo en findings (línea 3 true, línea 5 false); resumen sin outcome (no tiene thread)
  - TestF5VeredictoPreMerge: stub LLM responde los 3 marcadores del rol review (Reviewer/Summarizer/Pre-merge) → re-edición final trae "### Veredicto pre-merge: apto" + resumen del veredicto, 2 inlines intactos, risk_score no null en 0-100
  - TestF5MetricsEndpoint: sesión member directa en BD (user+session+cookie codeowl_session); sin cookie → 401; baseline-delta exacto: merged_prs+1 (cycle time 1h sembrado), findings evaluados+2 (1 aceptado), resolved+1, false_positives+1 (resolved ∧ ¬applied), reviews_with_cost+1; tasas exactas contra el delta (accepted_rate y false_positive_rate); nulls honestos ausentes con dato
  - BUG LATENTE de la suite corregido: newTempClone commiteaba contenido idéntico con fecha de mismo segundo → SHA idéntico entre tests → la cache global de resultados del pipeline (clave incluye head SHA, §9.6) cruzaba entradas entre tests (F4 contaminaba F5 en suite completa, F5 podía contaminar pipeline_test). Fix: nanosegundo en el mensaje del commit. Suite verde 3 corridas seguidas
  - GAP .env.example: NO verificado — la lectura de .env.example / deploy/.env.example está bloqueada por reglas de permisos del workspace (patrón *.env.* denegado); quien cierre la fase debe confirmar que RISK_SENSITIVE_PATHS figura documentada (config.go la lee con defaults si no está seteada)

## Evidencia de progreso

| Tarea | Commit | Verificación |
|-------|--------|--------------|
| T1 migración 0005 | f61d700592e7 | queries métricas + gateway review_id, BD real |
| T2 PRTimeline | ea744adad94a | threads+patches ambos VCS + FIX bug F1 URLs relativas |
| T3 risk scoring | 6f89e01e7707 | fórmula transparente máx 100, 14 subtests |
| T4 Pre-merge | 5fa707a5a159 | veredicto como sección del summary, 6 tests |
| T5 descarte+MetricsJob | fa009fd21aca | cancel→metrics→index, heurística líneas +, BD real |
| T6 re-encolo tx | 3f9869c9ffac | river v0.48 verificado, interleaving e2e PASS |
| T7 API triage+métricas | f540f820ac84 | query params + /api/metrics, 10 tests |
| T8 dashboard triage | 80329f6e4391 | 89 tests dashboard (+12), 4 gates |
| T9 integración F5 | 2383526fe3c8 | 3 e2e nuevos (8/8 suite ×4 corridas), mapa final |

## Cierre F5 (verificación final del orchestrator)

- Backend: 12 packages ok. Integración (BD real): **8/8 PASS** (F1+F2+F3+F4×2+F5×3). Analyzer ok. Dashboard 89/89 + lint + tsc + build. `just test` verde.
- Checklist §6 F5: los 4 ítems materializados y e2e: riesgo persistido, triage dashboard con filtros, veredicto pre-merge en el summary edit in place, métricas (cycle/aceptados/FP/costo) desde flags+tablas.
- Gaps de ciclo de vida cerrados: descarte de jobs al cierre (§3.5) y re-encolo transaccional stale (§3.6.1.5).
- Bug latente de F1 corregido en el camino (URLs relativas GitHub REST). Flake de suite corregido (SHAs idénticos entre tests).
- 9 commits work-unit + docs(odd). Higiene pendiente (cosmético): 8 archivos con gofmt desviado (pre-existentes, no tocados).
- F5 CIERRA EL PROYECTO: F0-F5 completas. Merge a main + push autorizados por el usuario al finalizar las fases.

- T9 (cierre de fase): commit 2383526fe3c8. Verificación completa (verbatim):
  - `cd backend && go test -count=1 -p 1 ./...` → 11 paquetes ok, 0 fallos
  - `go test -count=1 -p 1 -tags integration ./internal/integration/ -v` → 8/8 PASS (F3: 1, F4: 2, F5: 3, GitLab chat: 1, pipeline: 1), 0.374s
  - `cd analyzer/src && go test ./...` → ok
  - `pnpm --dir dashboard test` → 89/89 (20 archivos); lint (biome) limpio; tsc limpio; build ok
  - `go vet ./...` + `go vet -tags integration ./...` → limpios
  - `just test` → backend + dashboard + analyzer ok
  - `gofmt -l .` → 8 archivos pre-existentes sin formatear (NO tocados, reportados): internal/api/api_test.go, internal/index/index_test.go, internal/jobs/workers_test.go, internal/store/repo_index_test.go, internal/vcs/github/methods_test.go, internal/vcs/gitlab/methods.go, internal/vcs/types.go, internal/integration/pipeline_test.go (misalignment pre-existente en capturedQueue; el hunk de T9 sí está formateado)

## Verificación por tarea (comando canónico)

- Backend: `cd backend && go test -count=1 -p 1 ./...`
- Integración: `export $(grep ^DATABASE_URL= .env)` (desde raíz) + `cd backend && go test -count=1 -p 1 -tags integration ./internal/integration/`
- Dashboard: `pnpm --dir dashboard test`
- Spot-check del orchestrator por tarea

## Modo TDD

Off (convención F1-F4: tests con cada tarea, verde antes del commit).
