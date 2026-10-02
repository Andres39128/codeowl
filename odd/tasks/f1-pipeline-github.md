# Fase 1 — Pipeline de revisión GitHub

## Objetivo

Implementar el pipeline completo de revisión de GitHub según la guía §6 F1: webhook firmado → ReviewJob → analyzer sandbox → agente Reviewer → publicación en dos fases. Incluye settings CRUD (proveedores LLM, repos, usuarios) y panel de cola.

## Entregable (guía)

Abrir un PR de prueba en GitHub → resumen + comentarios inline publicados.

## Alcance autorizado

- Checklist F1 §6 completo (10 ítems).
- Branch `fase-1` desde main (afbe0ea94bc8). Push/PR después de converger.

Fuera de alcance: F2+ (GitLab adapter, Chat, sugerencias, tests generation), RAG, triage, pre-merge.

## Modo TDD

No configurado → off. Tests funcionales ordinarios por componente según mapa `pruebas:`.

## Estrategia de entrega

Forecast >>400 líneas. `ask-on-risk` → evaluar al cierre (chained PRs o exception).

## Tareas

- [x] T1 — feat: migraciones F1 + store (ruta: delegado) — commit 7ad757269fa3; 8 tablas, 35 queries, 21 tests (14 previos + 7 nuevos); migración BD limpia OK (16 tablas, version=3); spot check orquestador build/vet/test OK
  - Migración 0003: llm_providers, repositories, pull_requests, reviews, findings, comments_sent, webhook_deliveries, llm_usage
  - Queries sqlc para CRUD de cada tabla
  - Verificación: `go build ./...`, `go test ./...`, migración sobre BD limpia
- [x] T2 — feat: internal/llm gateway multi-proveedor (ruta: delegado) — commit bc93e2717942; 10 tests (stub server: éxito+usage, failover, 429+Retry-After, client-error sin retry, exhausted, concurrency per-review+global, TestConnection); 4 vars config nuevas
  - Gateway OpenAI-compatible: base_url + model + api_key; roles (review/cheap/embedding); failover por priority; rate limit; usage logging a llm_usage
  - Verificación: tests con stub server OpenAI-compatible
- [ ] T3 — feat: internal/vcs GitHub adapter (ruta: delegado)
  - VCSProvider contract (interface); github.VCSProvider: HandleWebhook (firma HMAC-SHA256, filtro eventos, dedup delivery ID, upsert PR, enqueue ReviewJob); FetchPR (clon shallow + merge-base); PostInlineComment, PostSummary; instalación token efímero
  - Verificación: tests con payloads firmados de fixtures
- [ ] T4 — feat: internal/analyze + analyzer Containerfile (ruta: delegado)
  - Containerfile: tree-sitter + 5 linters pinned, --network=none --read-only, configs propios de la imagen
  - internal/analyze: cliente del contenedor, normalización JSON (severidad mapeada al conjunto cerrado), reporte qué corrió/saltó
  - Verificación: build de imagen + test del CLI con repo fixture
- [ ] T5 — feat: internal/review pipeline + agentes (ruta: delegado)
  - Run(ctx, ReviewInput) (ReviewResult, error); Reviewer sobre hunks con contexto; Summarizer; dedup por huella; publicación en dos fases; prompts versionados en backend/prompts/
  - Verificación: contract tests con stub del gateway
- [ ] T6 — feat: jobs workers (ReviewJob, CleanupJob, RotationJob, ReconcileJob) (ruta: delegado)
  - Worker River con workers registrados; ReviewJob con unicidad ByArgs por PR + ByState sin completed; CleanupJob retención; RotationJob re-cifrado; ReconcileJob ListOpenPRs
  - Verificación: integration test con webhook simulado
- [ ] T7 — feat: API endpoints settings/repos/users/queue (ruta: delegado)
  - REST según mapa rest_endpoints: GET/POST /api/providers, PUT/DELETE /api/providers/{id} (CRUD completo con prueba de conexión); GET/POST /api/repos, PUT /api/repos/{id} (conectar/desconectar); GET/POST /api/users, PUT /api/users/{id}; GET /api/jobs (queue panel)
  - Verificación: httptest e2e
- [ ] T8 — feat: Dashboard settings + queue (ruta: delegado)
  - features/settings: proveedores LLM (CRUD + prueba conexión), repos conectados (conectar/desconectar), usuarios (invitar/reset/desactivar); features/queue: panel de estado
  - Verificación: pnpm build + test + lint
- [ ] T9 — test: integration test webhook simulado end-to-end (ruta: delegado)
  - Webhook firmado → handler → job encolado → review ejecutada (con stub LLM) → publicación verificada
  - Verificación: go test integration tag

## Evidencia de progreso

(se llena por tarea)

## Criterios de aceptación

Checklist F1 §6 completo; demo runnable (PR de prueba → resumen + inline); CI verde.
