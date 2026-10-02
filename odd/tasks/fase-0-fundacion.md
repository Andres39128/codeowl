# Fase 0 — Fundación de codeowl

## Objetivo

Implementar la Fase 0 de la guía (`docs/guia_del_proyecto.md` §6): monorepo + auth + shell del dashboard + servicios base corriendo, más los 4 ajustes documentales detectados en la revisión pre-implementación.

## Problema / por qué

El repo está solo-documentación (5 loops de revisión convergidos). El usuario autorizó implementar Fase 0 y ajustar las 4 observaciones de la revisión de readiness. Regla de gobernanza del repo: si un cambio desactualiza la guía, primero se actualiza la guía y después se escribe código; el mapa vive en el mismo PR que la estructura que describe.

## Alcance autorizado

- 4 ajustes doc (guia + mapa).
- Fase 0 completa según checklist §6: estructura monorepo coincidente con mapa, API con healthcheck/login/migraciones, dashboard shell con login y tema, CI, `.env.example` completo, `deploy/backup.md`.
- Rama `fase-0` desde `docs/web-verified-review-loop` (HEAD f845e6fa77ae). Push/PR quedan decisión del usuario.

Fuera de alcance: F1+ (pipeline GitHub, settings CRUD funcional, queue panel con datos), analyzer, RAG, triage.

## Modo TDD

No configurado (sin sdd-init, sin config previa) → modo off. Se aplican checks funcionales ordinarios: la guía/mapa exigen pruebas por componente (mapa `pruebas:`), cada writer incluye tests y los ejecuta. Fuente: sin configuración existente; resolución de este documento.

## Estrategia de entrega

Forecast: >400 líneas autoradas (Fase 0 es un phase completo). Estrategia: `ask-on-risk`. No se crean PRs en esta sesión (decisión del usuario); los splits de PR se consultan si/el cuando el usuario pida PR. Boundario revisado RDD inicial: f845e6fa77ae.

## Tareas

- [x] T1 — docs: ajustar 4 observaciones (ruta: delegado; trigger: 2 archivos no triviales) — commit 915ee48562bd; diff verificado (12+/5-), YAML parse OK; RDD assess: medium (configuration_change), review_due false (under_budget)
  - Obs1: inventario de endpoints REST en mapa `api.expose` con tag de fase (F0: healthz/auth; F1: settings/repos/queue). Guia manda si nombra paths.
  - Obs2: explícitar que la migración oficial de River entra en F0 (guia §3.3 y/o checklist §6).
  - Obs3: defaults numéricos documentados: password min length (§3.4), presupuesto disco workdir (§3.3), cap tamaño diff (§9.6) — valores concretos + overridable por env.
  - Obs4: clarificar en checklist F1 que registro de repo GitLab = data entry a nivel store (fila + secretos cifrados), pipeline F2.
  - Verificación: greps de consistencia + relectura de secciones editadas. Commit.
- [ ] T2 — chore: scaffold y tooling de repo (ruta: delegado)
  - go.mod, justfile (dev/lint/build/test §2.2), `.env.example` completo con defaults de Obs3, `.gitignore`, `compose.dev.yml`, `deploy/` (Quadlet + backup.md §9.2/§9.10), CI GitHub Actions reusando just.
  - Verificación: `just --list`, validación YAML/CI. Commit.
- [x] T2 — chore: scaffold y tooling de repo (ruta: delegado) — commit "chore: fase-0 repo tooling"; verificado: go build OK (go1.27 toolchain auto), just --fmt --check OK, YAML+Quadlet validados con generador podman 5.7, `just up` levanta postgres healthy en 127.0.0.1:15432, `just down` limpio. .env.example: 23 vars. RDD: assess high (shell_source ci.yml) → consentimiento relayed → usuario DECLINED este candidato (sin receipt; próximas revisiones siguen habilitadas).
- [x] T3 — feat: backend foundation (ruta: delegado) — commit "feat(backend): fase-0 foundation"; spot check orquestador: build+vet+test OK. RDD: assess high → usuario GRANTED → transacción 4R review-c82811c6492a95b9; risk+readability capturados, resilience+reliability unachievable (transporte, 3 intentos) → stop `unachievable_lens_slot` SIN receipt; usuario decidió dejar el review pendiente y continuar. Boundary NO avanza (sigue f845e6fa77ae).
  - `internal/config` (validación escalonada), `internal/store` (golang-migrate embedded, migración River, sqlc queries users/sessions, seed admin env), `cmd/api`/`cmd/worker` esqueletos levantables.
  - Verificación: `go build ./...`, `go test ./...`. Commit.
- [ ] T4 — feat: api auth + jobs esqueleto (ruta: delegado)
  - `internal/api`: ServeMux, healthz, endpoints auth según mapa (Obs1), argon2id, cookie sesión, CSRF. `internal/jobs`: interfaz `JobQueue` + wrapper River esqueleto.
  - Verificación: `go build ./...`, `go test ./...` (auth middleware/CSRF). Commit.
- [x] T4 — feat: api auth + jobs esqueleto (ruta: delegado) — commit "feat(backend): auth REST endpoints..."; 35/35 tests (11 nuevos: 8 api e2e + 3 jobs), smoke curl completo (login→session→logout revoca), CSRF = SHA-256 derivado del token de sesión vía X-CSRF-Token; worker con RiverQueue skeleton. RDD: assess high (3856 líneas) → review dejado PENDIENTE por instrucción del usuario (transporte roto); boundary no avanza.
- [ ] T5 — feat: dashboard shell (ruta: delegado)
  - Vite + Preact (compat) + Tailwind v4 tokens §5.1 (light/dark persistido), login funcional, nav vacía, Button/Badge/Table, `lib/apiClient` + `useSession`.
  - Verificación: `pnpm build`, tests si aplica. Commit.
- [x] T5 — feat: dashboard shell (ruta: delegado) — commit "feat(dashboard): fase-0 shell..."; 23/23 tests, build 57KB JS (gzip 19KB), lint biome OK; 12/12 tokens × 2 temas con contraste AA verificado computacionalmente; desviaciones a favor de la guía (data-theme, UI español §4.7, rutas del mapa).
- [x] T6 — verify: pasada final (ruta: inline + spot check) — backend build/vet/test 35/35; dashboard build/test/lint OK; smoke live: postgres healthy, healthz ok, login 401 en credencial inválida, round-trip login→logout verificado (T4/T5); coincidencia mapa §3.2 COMPLETA (config/store/api/jobs + lib/components/theme/features/auth en rutas exactas; F1+ ausente como corresponde); CI validado (se activa por hashFiles al pushear).

## Evidencia de progreso

- T1 commit 915ee48562bd — 4 ajustes doc, YAML OK, assess medium/under_budget
- T2 commit c52a24a29717 — assess high → consent DECLINED por usuario (ese candidato)
- T3 commit a1ea1aa67717 — assess high → GRANTED → transacción 4R review-c82811c6492a95b9 → stop `unachievable_lens_slot` (transporte; risk+readability capturados, resilience+reliability 3x vacíos) → usuario: review PENDIENTE, continuar
- T4 commit 2186a6cb2aec — assess high/3856 líneas → review pendiente por instrucción de sesión
- T5 commit 5d727cf9debd — review pendiente por instrucción de sesión
- Boundary RDD SIN avances en toda la fase (sigue f845e6fa77ae): ningún receipt emitido. Total: 69 archivos, ~8100 líneas.

## Criterios de aceptación

Checklist F0 §6 completo y verificado; docs actualizados primero; cada tarea cierra con commit work-unit Conventional Commit en `fase-0`.

## Entrega (resuelta)

- Estrategia: `ask-on-risk` → usuario eligió **PRs encadenados por tarea** + **stacked-to-main** (sin tracker).
- Docs convergidas: merge ff a main (f845e6fa77ae). 4 branches acumulativas, todas → main, merge en orden; tras cada merge el diff del siguiente se reduce.
- Todos los slices superan 400 líneas (una pasada honesta de slicing: T3 sin config/migraciones no compila; River schema es vendor verbatim) → `size:exception` aceptado por el maintainer en cada PR.
- Issues: #1 (T1+T2 tooling), #2 (T3 backend), #3 (T4 auth), #4 (T5+T6 dashboard) — status:approved.
