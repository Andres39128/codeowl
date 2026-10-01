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

- [ ] T1 — docs: ajustar 4 observaciones (ruta: delegado; trigger: 2 archivos no triviales)
  - Obs1: inventario de endpoints REST en mapa `api.expose` con tag de fase (F0: healthz/auth; F1: settings/repos/queue). Guia manda si nombra paths.
  - Obs2: explícitar que la migración oficial de River entra en F0 (guia §3.3 y/o checklist §6).
  - Obs3: defaults numéricos documentados: password min length (§3.4), presupuesto disco workdir (§3.3), cap tamaño diff (§9.6) — valores concretos + overridable por env.
  - Obs4: clarificar en checklist F1 que registro de repo GitLab = data entry a nivel store (fila + secretos cifrados), pipeline F2.
  - Verificación: greps de consistencia + relectura de secciones editadas. Commit.
- [ ] T2 — chore: scaffold y tooling de repo (ruta: delegado)
  - go.mod, justfile (dev/lint/build/test §2.2), `.env.example` completo con defaults de Obs3, `.gitignore`, `compose.dev.yml`, `deploy/` (Quadlet + backup.md §9.2/§9.10), CI GitHub Actions reusando just.
  - Verificación: `just --list`, validación YAML/CI. Commit.
- [ ] T3 — feat: backend foundation (ruta: delegado)
  - `internal/config` (validación escalonada), `internal/store` (golang-migrate embedded, migración River, sqlc queries users/sessions, seed admin env), `cmd/api`/`cmd/worker` esqueletos levantables.
  - Verificación: `go build ./...`, `go test ./...`. Commit.
- [ ] T4 — feat: api auth + jobs esqueleto (ruta: delegado)
  - `internal/api`: ServeMux, healthz, endpoints auth según mapa (Obs1), argon2id, cookie sesión, CSRF. `internal/jobs`: interfaz `JobQueue` + wrapper River esqueleto.
  - Verificación: `go build ./...`, `go test ./...` (auth middleware/CSRF). Commit.
- [ ] T5 — feat: dashboard shell (ruta: delegado)
  - Vite + Preact (compat) + Tailwind v4 tokens §5.1 (light/dark persistido), login funcional, nav vacía, Button/Badge/Table, `lib/apiClient` + `useSession`.
  - Verificación: `pnpm build`, tests si aplica. Commit.
- [ ] T6 — verify: pasada final (ruta: inline + spot check)
  - Build+test backend y dashboard, `just dev` best-effort (podman pull), regla de coincidencia mapa §3.2, RDD assess por commit, reporte honesto.

## Evidencia de progreso

(se llena por tarea: commit, checks, resultado RDD)

## Criterios de aceptación

Checklist F0 §6 completo y verificado; docs actualizados primero; cada tarea cierra con commit work-unit Conventional Commit en `fase-0`.
