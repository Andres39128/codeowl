# Fase 3 — Verificación y Configuración por Repo

## Objetivo

Implementar F3 según la guía §6: agente Verifier (rol `cheap`), resumen con walkthrough + Mermaid validado, `review.yaml` por repo (leído SOLO de la rama base), dashboard con detalle de PR + DiffViewer.

## Entregable (guía)

Falsos positivos filtrados, Mermaid en el resumen, revisión configurable por repo.

## Alcance autorizado

- Checklist F3 §6 completo (4 ítems).
- Branch `fase-3` desde fase-2 (2a314db + fix docs env).
- Convención de entrega establecida F0-F2: branch único por fase, commits work-unit, sin push/PR (excepción de tamaño aceptada por convención del maintainer).

Fuera de alcance: F4+ (RAG/IndexJob, anclas AST), triage/risk score, veredicto pre-merge, UI de settings para defaults de profile/path_filters (los defaults de sistema cubren §9.5 hasta que exista necesidad).

## Decisiones de diseño (basadas en exploración del código real)

1. **review.yaml**: YAML plano en la raíz del repo. Keys: `language` (string, default: `repositories.language`), `profile` (`chill|assertive|strict`, default `assertive` vía config `REVIEW_DEFAULT_PROFILE`), `path_filters` (lista de globs; `!patrón` excluye, patrón sin `!` restringe a los que coinciden; lista vacía = todos), `instructions` (string, entra al prompt del Reviewer). Sin `reviews:` anidado — la guía fija las 4 keys planas.
2. **Lectura de rama base**: `git -C workdir show <mergeBaseSHA>:review.yaml`. El merge-base está garantizado en el clon (deepen loop ya existe) y es el lado base del diff — más estricto que el tip de la base (que puede no estar en el shallow clone si la base avanzó desde el evento). Archivo ausente / YAML inválido / keys inválidas → ignorar con log estructurado y correr con defaults (§9.5: jamás rompe el job).
3. **Perfiles** (regulan cuánto se comenta, jamás veredicto de bloqueo):
   - `chill`: publica solo severity high|medium, excluye category=style.
   - `assertive` (default): publica todo (comportamiento F1/F2 preservado).
   - `strict`: mismo set de publicación que assertive + el prompt del Reviewer pide también nits (llegan como findings severity=low/category=style — el conjunto cerrado de severity NO cambia).
   - El filtro aplica a la publicación, NO a la persistencia: los findings se guardan todos (auditoría).
4. **Config efectiva y cache**: la cache §9.6 existente (review.go, key `headSHA|mergeBase|prompts.Version()|hash(cfg)`) gana el hash de la config efectiva (language/profile/path_filters/instructions mergeados con defaults) — dos configs distintas son corridas distintas (§9.6).
5. **Idioma**: `{{LANGUAGE}}` placeholder en `reviewer_system.md` y `summarizer_system.md` (patrón existente de chat_system.md). Valor: `review.yaml language` > `repositories.language`.
6. **Verifier** (rol `cheap`): corre sobre findings LLM post-dedup, pre-publicación fase 2. Una llamada cheap por archivo con findings LLM; input = findings JSON + SAST findings del archivo + hunks (evidencia determinista). Output JSON `[{index, verified, reason}]`. Solo `verified=false` CONFIRMADO suprime la publicación (queda en `findings` con `verified=false`, auditable). Malformado → retries → ese lote queda `verified=null` (se publica). Sin proveedor cheap (§9.6 guarda): todo queda `null`, se publica, y la re-edición fase 2 del resumen lo declara. SAST findings no se verifican (deterministas, §3.3). Razón del veredicto: log estructurado (sin columna nueva — §3.3 manda).
7. **Mermaid**: endurecer `validMermaid` (hoy: chequeo de prefijo) a validación estructural del subset `sequenceDiagram` (participantes, flechas, bloques alt/loop/opt/par con `end`, notes) + fallback prefix-check para otros kinds. Si no parsea: se omite y se registra (§9.8). Sin dependencia nueva.
8. **Globs**: matcher propio por segmentos (`**` cruza segmentos, `*` dentro del segmento), ~30 líneas con tests — sin dependencia glob externa.
9. **API**: `api` importa `vcs` (el mapa ya lo declara: depende_de vcs por GetDiff). Endpoints member (`withAuth`): `GET /api/prs` (lista PRs + estado última review + conteo findings), `GET /api/prs/{id}` (PR + última review + findings), `GET /api/prs/{id}/diff` (JSON `{diff}` vía `GetDiff` del adapter según `repo.Vcs` — sin copia en BD). Sin CSRF (GET only).
10. **Dashboard**: `features/prs` (lista + detalle), componentes nuevos `Card` + `DiffViewer` (parse del unified diff a mano, sin lib; único consumidor de DiffViewer es prs). Tokens nuevos `--diff-add-bg`/`--diff-del-bg` con contraste AA en ambos temas. Mermaid del resumen se muestra COMO TEXTO (§9.5: el dashboard no renderiza Mermaid). Findings con badge severity (high→alta mapping), category, source, estado verified (✓/✗/—). Ruta `prs` visible para member (§3.4). `usePR` en lib (mapa: F3).
11. **Sin migración nueva**: `findings.verified` ya existe (0003); profile/path_filters/instructions viven solo en review.yaml + config; idioma ya está en repositories.

## Tareas

- [x] T1 — feat(review): review.yaml — parser+validación (yaml.v3), lectura por merge-base de la rama base, precedencia/defaults, hash de config efectiva en cache key, {{LANGUAGE}} en prompts reviewer/summarizer (ruta: delegado)
  - Commit a2e6dfce553d: repoconfig.go (175) + repoconfig_test.go (243), 509 insertions, 30/30 tests nuevos, 10 packages verde, go vet clean
  - Notas: yaml.v3 ya estaba indirecto en go.mod (promovido a directo); punteros *string para distinguir clave ausente de inválida; matchPath exportado sin cablear (T2); comentarios en español (convención del repo)
  - Dependencia nueva: gopkg.in/yaml.v3 (justificada: no hay YAML en stdlib)
  - Config nueva: REVIEW_DEFAULT_PROFILE (default assertive) → .env.example
  - Tests: parse/validación/precedencia/ignora-inválido; cache key cambia con config distinta
- [x] T2 — feat(review): aplicar config efectiva — path_filters en selección de archivos, instructions en prompt del Reviewer, filtro de profile en publicación (ruta: delegado)
  - Commit c771b4334949: 380 insertions, 7 tests nuevos (path filters, chill profile, isPublishable tabla, instructions/nits fill), todo verde
  - Notas: dos conjuntos de findings — publishable (persiste, auditoría) vs toPublish (publica); SAST también respeta path_filters; runReviewer ahora recibe RepoConfig
  - El filtro de profile vive entre dedup y publicación; findings NO verificados-falsos ni filtrados se persisten igual
  - Tests: chill filtra low+style; assertive no filtra; strict pasa prompt de nits; path_filters excluye/restringe
- [x] T3 — feat(review): agente Verifier rol cheap (ruta: delegado)
  - Commit 5d66df635105: verifier_system.md + runVerifier (batch por archivo, errgroup), 511 insertions, 4 tests + 8 subtests
  - Notas: CreateFindingParams ya tenía Verified (set al insert, sin sqlc generate); verifierNote separado de coverage (verifier jamás marca partial §9.6); review.Store expone ListEnabledLlmProvidersByRole
  - prompts/verifier_system.md + embed + Version(); agente en agents.go; review.Store gana ListEnabledLlmProvidersByRole (guarda §9.6)
  - verified=true/false a findings; publicación salta confirmed=false; re-edición fase 2 declara verificación omitida
  - Tests: stub gateway marca verified; sin cheap → null + declarado; malformado → null + publish
- [x] T4 — feat(review): Mermaid sequenceDiagram — validación estructural, omit+log si no parsea (ruta: delegado)
  - Commit d7ade087a1f3: mermaid.go (262) + mermaid_test.go (148), 54 tests, iterative stack (sin blowup), caps 200 líneas/500 chars
  - Notas: fence-strip persiste el valor limpio (publication re-envuelve); header match por primer token + stateDiagram-v2; otros kinds quedan en prefix-check (documentado)
  - Tests: diagrams válidos/inválidos del subset; regresión de los kinds existentes
- [x] T5 — feat(api): endpoints PR — GET /api/prs, GET /api/prs/{id}, GET /api/prs/{id}/diff vía adapter (ruta: delegado)
  - Commit 287a4d6138d5: prs.go (300) + prs_test.go (379) + query LATERAL, +878/−40, 5 tests handler
  - Notas: Server.github/gitlab ahora vcs.VCSProvider (antes http.Handler closures — fix causa raíz, mapa lo sanciona); sin columna title en pull_requests (no inventada); COALESCE sentinel review_id=0 ↔ latest_review null; JSON contracts documentados para T6/T7
  - sqlc: ListPullRequestsWithLatestReview (+counts findings); api importa vcs; selector de adapter por repo.Vcs
  - Tests: handlers con stub adapters (auth member, 404, diff proxy)
- [x] T6 — feat(dashboard): componentes Card + DiffViewer + tokens diff AA (ruta: delegado)
  - Commit bb94cab82784: DiffViewer (214, parser+renderer+highlights con data-file/data-line), Card, tokens --diff-add/--diff-del/--diff-hl con AA en ambos temas, 514 insertions, 13 tests nuevos (62 total)
  - Notas: sin virtualización (max-h + overflow, techo documentado); highlight pisa bg add/del; contrato de anclas por data-attrs
  - DiffViewer: parser unified diff (hunks, +/-, líneas old/new), render texto monospace, anclas file/line para findings
  - Tests: parse fixture diff real (GitHub compare format); render add/del/hunk; contraste AA tokens
- [x] T7 — feat(dashboard): features/prs + usePR + ruta prs (ruta: delegado)
  - Commit 73d0c769c8bb: Prs + PrDetail + view.ts labels + usePR + types + router prs + nav member, 963 insertions, 15 tests nuevos (77 total), build verde
  - Notas: mermaid como texto (assert sin svg/canvas); diff 502 → Banner sin romper página; finding click → scroll a data-file/data-line; Table ganó onRowClick opcional
  - Lista (Table: PR, repo, estado, review status, findings por severidad) + detalle (Card summary/walkthrough/mermaid-texto, findings con badges y verified, DiffViewer anclado)
  - Nav member; route "prs"; apiClient GET; Tests: feature render + estados
- [x] T8 — test(integration): pipeline F3 end-to-end (ruta: delegado)
  - Commit 785b80977402: f3_review_config_test.go (514 líneas) + mapa (endpoints /api/prs, actualizado 2026-10-03)
  - Corrió contra Postgres real (just up): 3/3 PASS (F3 + GitLab chat + GitHub review). Sin DB salta limpio
  - Verifica: review.yaml (chill + !*_test.go + instructions) aplicado; verified false/null/null/true persistidos; solo lo publicado comenta; re-edición fase 2 cuenta el set publicado; prompts con instructions + idioma; archivo filtrado jamás llega al reviewer/verifier

## Evidencia de progreso

| Tarea | Commit | Verificación |
|-------|--------|--------------|
| T1 review.yaml | a2e6dfce553d | 30/30 tests nuevos, 10 packages ok, vet clean |
| T2 config aplicada | c771b4334949 | 7 tests nuevos, suite verde |
| T3 Verifier | 5d66df635105 | 4 tests + 8 subtests, suite verde |
| T4 Mermaid | d7ade087a1f3 | 54 tests Mermaid/Summarizer, suite verde |
| T5 API PRs | 287a4d6138d5 | 5 tests handlers, sqlc generate limpio |
| T6 Card+DiffViewer | bb94cab82784 | 13 tests nuevos, 62 total, AA ambos temas |
| T7 features/prs | 73d0c769c8bb | 15 tests nuevos, 77 total, build verde |
| T8 integration | 785b80977402 | 3/3 PASS contra Postgres real |

## Cierre F3 (verificación final del orchestrator)

- Backend: `go test -count=1 -p 1 ./...` → 10 packages ok. Integración (`-tags integration`, DATABASE_URL real): 3/3 PASS.
- Dashboard: 19 files / 77 tests PASS; lint y tsc clean; build 26.3 kB gzip.
- Checklist §6 F3: los 4 ítems materializados y ejercitados end-to-end.
- 8 commits work-unit en fase-3 (a2e6dfce..785b8097) + docs(odd) de cierre. Sin push (convención F0-F2).
- Hallazgo del spot-check final: la DATABASE_URL de .env usa el password default del example — correcto para dev, el deploy self-host la cambia (§9.2).

## Verificación por tarea (comando canónico)

- Backend: `cd backend && go test -count=1 -p 1 ./...`
- Dashboard: `pnpm --dir dashboard test`
- Lint: `just lint`
- Spot-check del orchestrator por tarea: re-ejecutar un test del paquete tocado + leer el diff

## Modo TDD

Off (no establecido en F1/F2; convención del proyecto: tests con cada tarea, verde antes del commit).
