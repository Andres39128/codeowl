# Fase 2 — GitLab + Chat + Sugerencias

## Objetivo

Implementar F2 según la guía §6: adapter GitLab (VCSProvider), agente Chat (comandos @bot), sugerencias aplicables 1-click, generación de pruebas unitarias.

## Entregable (guía)

Mismo pipeline en GitLab; charlar con el bot; aplicar sugerencias con 1 clic.

## Alcance autorizado

- Checklist F2 §6 completo (4 ítems).
- Branch `fase-2` desde fase-1.

Fuera de alcance: F3+ (Verifier, review.yaml, DiffViewer), RAG, triage.

## Tareas

- [x] T1 — feat: GitLab adapter (webhook firma Standard Webhooks, API REST, clon SSH con deploy key) (ruta: delegado)
  - gitlab.VCSProvider cumpliendo contrato completo (9 métodos del mapa)
  - Firma: Standard Webhooks HMAC-SHA256 sobre webhook-id.webhook-timestamp.body (§9.3); fallback legacy X-Gitlab-Token
  - Eventos: merge_request (open/update/reopen/close/merge con filtros §3.5) + note (solo MR + mención)
  - API: token de proyecto cifrado en repositories; ListOpenPRs, PostSummary, PostInlineComment, PostSuggestion via API v4
  - Clon: deploy key SSH (cifrada en repositories)
  - Tests: payloads firmados fixtures
- [x] T2 — feat: Agente Chat + ChatJob (ruta: delegado)
  - ChatJob worker River (async, presupuesto propio §9.6, slots CHAT_CONCURRENCY)
  - Comandos: /review (re-encola, se rehúsa en cerrado/draft), /tests y /explain (responden hilo, lista cerrada §9.5), respuesta general a mención
  - Anti-bucle §3.5; chat_org_only (fuera de org se ignora)
  - Prompt chat_system.md embed; idioma del repo
  - Idempotencia por parent_comment_id en comments_sent
  - Webhook GitHub: issue_comment + pull_request_review_comment → filtro mención → ChatJob (completar el stub de F1)
  - Webhook GitLab: note → filtro MR + mención → ChatJob
- [x] T3 — feat: Sugerencias aplicables 1-click (ruta: delegado)
  - PostSuggestion GitHub: suggestion blocks ya existe del F1; validar formato
  - PostSuggestion GitLab: suggested changes via API v4 (merge request approvals o discussion resolve)
  - Encontrar el camino de aplicabilidad en ambos VCS; testear que el cuerpo produce un comentario aplicable
- [x] T4 — feat: Generación de pruebas unitarias (ruta: delegado)
  - Agente adicional del pipeline: dado un finding con suggestion, genera un test unitario que lo cubriría
  - Framework detectado (go test, jest/vitest, pytest, cargo test) según lenguaje del archivo
  - Se publica como sugerencia (código aplicable) en el comentario inline o como respuesta separada
- [x] T5 — test: integration test GitLab webhook + ChatJob end-to-end (ruta: delegado)
  - Webhook GitLab firmado → filtro → ChatJob → respuesta publicada
  - Comandos /review /tests /explain verificados

## Evidencia de progreso

(se llena por tarea)
