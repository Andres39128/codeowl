# Sync periódico de PRs (sin webhook)

Header: el sistema debe traer los PRs que el webhook no trajo — en desarrollo
no hay túnel de webhooks (guía §3.2), y `pull_requests` se puebla SOLO por
webhook: los PRs nuevos no llegan al dashboard. Un job periódico en el worker
los descubre por el VCS y encola su review, sin túnel ni comando manual.

## Specs

- S1 (verbatim, opción elegida): "Job periódico en el worker (recomendado):
  synca PRs abiertos de los repos enabled cada X minutos → los PRs aparecen
  solos, sin túnel ni comando manual. Es el fix de producto."
- S2 (verbatim): "los PRs aparecen solos, sin túnel ni comando manual" — un PR
  creado en el VCS debe llegar al dashboard con su review encolada, sin
  intervención del operador.
- S3 (verbatim): "Es el fix de producto" — el sync es idempotente: un PR cuyo
  `head_sha` ya se revisó NO se re-revisa; un PR nuevo o con head nuevo sí.
- S4 (contexto, con evidencia): la causa NO es la privacidad del repo — el
  push review de un repo privado terminó `success` con clone y diff (la App lo
  lee bien). La causa: `webhook_deliveries` sin entregas desde las 02:45 y
  ningún túnel corriendo; `pull_requests` se puebla solo por webhook.
- S5: el sync no publica nada en el VCS (solo lee PRs abiertos y escribe en la
  base local + la cola de jobs).
- S6: el sync solo toca repos `enabled`; GitHub primero (el worker hoy solo
  tiene provider de GitHub — misma nota ponytail que el ReconcileJob).

## Tasks

- T1 — `internal/vcs`: extender `OpenPR` con `Author` y `CreatedAt` (aditivo)
  y mapearlos en los adapters github y gitlab. (S2)
- T2 — `internal/prsync`: helper de mapeo `OpenPR → prsync.PR`; las reglas de
  `Decide` ya existen y no cambian. (S3)
- T3 — `internal/jobs`: `KindSync`, `SyncJobArgs`, `SyncPeriodic(interval)` y
  `SyncJobWorker` (repos enabled → `ListOpenPRs` → `Decide` → upsert +
  `ReviewJob`). (S1, S3, S5, S6)
- T4 — `internal/config`: `SYNC_INTERVAL` (default 5m; 0 = deshabilitado). (S1)
- T5 — `cmd/worker`: registrar el worker y el job periódico. (S1)
- T6 — tests de los workers/config + docs (guía del proyecto: env vars y §3.5).
  (S1-S3)

## Log

- L1: el usuario preguntó por qué una PR nueva de EdTech-scann no aparecía
  ("Acabo de hacer una pr en EdTech pero no aparece en el sistema, será por lo
  que es privado aún no hemos ajustado eso?") y eligió la opción 1 del menú
  ("1 hazlo"): job periódico en el worker.
- L2: evidencia que descarta la privacidad: el push review de EdTech-scann
  (repo privado) terminó `success` con clone y diff; `pull_requests` de
  EdTech-scann estaba vacío (ni el PR 114 ni el 111-113); `webhook_deliveries`
  sin entregas desde las 02:45; ningún túnel corriendo.
- L3: el `ReconcileJob` existente solo CIERRA PRs locales que ya no están
  abiertos y se dispara al conectar un repo (one-shot): no inserta los
  nuevos ni encola review. El sync periódico lo complementa, no lo reemplaza.
- L4: `cmd/sync-prs` (manual, one-shot, ya implementado y verificado con el
  PR 114 → `partial`, 220 hallazgos) queda como herramienta puntual; el job
  periódico es el camino automático.
- L5: implementación (T1-T6). `vcs.OpenPR` gana `Author` y `CreatedAt`
  (aditivo; los dos adapters los mapean). `prsync.FromOpenPR` proyecta el PR
  del VCS al dominio del sync. `jobs.KindSync` + `SyncJobArgs{}` +
  `SyncPeriodic(interval)` + `SyncJobWorker` (repos enabled → `ListOpenPRs` →
  `Decide` → upsert + `ReviewJob`; un repo que falla no corta el sweep).
  Config: `SYNC_INTERVAL` (default 5m, 0 = deshabilitado) en Stage2Config.
  `cmd/worker`: worker registrado + periodic solo si el intervalo > 0.
  Tests: worker de sync (PR nuevo → upsert + review encolada; head ya
  revisado → NO encola; repo deshabilitado → skip), `SYNC_INTERVAL` (default,
  valor, 0, inválido) y `FromOpenPR`.
- L6: el test del sync inicial sincronizaba los PRs del stub contra TODOS los
  repos de la base compartida (escritura en repos ajenos al test): el stub
  `wProvider` ahora acota `openPRs` al repo del test (`openPRsRepo`). La
  contaminación de la base de desarrollo se limpió (30 filas de PR + 40
  reviews + jobs); los 3 PRs legítimos (codeowl #10 y #16, EdTech-scann #114)
  quedaron intactos.
- L7: `TestCancelPendingByPR` y `TestReviewJobWorkerReencoloTransaccionalInterleaving`
  fallaban con el worker local corriendo contra la BD compartida de los tests
  (el worker externo tomaba los jobs del test). Con el worker parado, el
  paquete `jobs` pasa completo: ninguno de los dos fallos es preexistente.
  Lección: los tests que escriben en la BD compartida no pueden correr con un
  worker vivo contra la misma base.
