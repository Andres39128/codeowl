# Loop de revisión de documentación (sin engram, con verificación web)

**Objetivo:** revisar los docs desde cero (sin leer engram ni hallazgos previos), verificar
claims contra fuentes web actuales, aplicar correcciones y repetir hasta no encontrar hallazgos.

**Ruta:** directa inline — ediciones mecánicas acotadas, contenido ya comprendido y verificado.

## Ejecución 2 (2026-10-01) — segunda pasada desde cero

Verificación web fresca (9 fuentes, todas OK): go.dev (go1.27.1), postgresql.org (18.6 +
PG19 Beta 4), River (v0.48.0, sigue 0.x), Podman (v6.1.3), Tailwind (v4.3 última v4.x),
docs.gitlab.com webhooks + webhook_events, hub.docker.com (pgvector pg18, 0.8.6),
docs.coderabbit.ai/reference/configuration, docs.github.com webhooks.
Contrastes WCAG recalculados con script propio (16 pares).

### Iteración 1 — 7 hallazgos

- [x] H1 (mayor) §3.6/§3.3: la identidad de staleness justificaba el ancla base=tip con
      "llega en el payload" — cierto en GitHub (`pull_request.base.sha`), FALSO en GitLab:
      el payload MR no trae ningún SHA de la rama base (verificado: solo nombres de ramas,
      `last_commit`, `oldrev`). Documentado per-VCS: GitLab resuelve el tip vía API
      (endpoint de branches, cache corto) al procesar el evento.
- [x] H2 §5.2: border.subtle oscuro decía 1.76:1 — real 1.55:1 (script). Conclusión
      (decorativo, <3:1) no cambia.
- [x] H3 §3.5 + mapa: PRs cerrados durante una desconexión nunca reciben su evento de
      cierre → quedarían abiertos en triage para siempre. Reconexión ahora reconcilia
      estado vía ListOpenPRs (adapter, sin LLM); métricas de esos cierres no se recuperan.
- [x] H4 §3.3: "las mismas keys que review.yaml especializa" era falso (drafts y chat
      son operativas del dashboard, §9.5) — reescrito.
- [x] H5 §9.6: cache de resultados no incluía `path_filters` en el hash de config efectiva
      — cambia qué archivos se revisan. Agregado.
- [x] H6 README: "linters y agentes LLM en sandbox" — los agentes LLM no corren en
      sandbox. Reordenado: "linters en sandbox y agentes LLM".
- [x] H7 §6 F2: idioma de respuesta del chat no estaba especificado — agregado
      (idioma configurado del repo, §3.3).

### Iteración 2 — 2 hallazgos

- [x] H8 §3.6: la frase nueva de H1 generaba tensión con "cero trabajo en el handler"
      (§3.5) — aclarado: llamada de metadatos acotada; lo vetado es LLM y análisis.
- [x] H9 manual §6: cifras de marketing del proveedor presentadas como hechos —
      calificadas con nota.

### Iteración 3 — 2 hallazgos

- [x] H10 §3.5: auto-referencia "(§3.5, cierre)" dentro del propio §3.5 — eliminada.
- [x] H11 §3.2: §9.2/§9.10 prometen runbook de backup "en deploy/" pero el árbol no lo
      listaba — agregado `deploy/backup.md` al árbol. Además: newline final del manual
      (faltaba, preexistente).

### Iteración 4 — convergencia

- [x] Pasada final sin hallazgos: refs § completas, anchors del ToC OK, YAML válido,
      diff completo re-revisado (4 archivos, +16/−10).

**Convergencia declarada a la iteración 4 de la ejecución 2.**
Rama: `docs/web-verified-review-loop`.

## Ejecución 3 (2026-10-01) — tercera pasada desde cero

Verificación web fresca (11 fuentes, todas OK): go.dev (go1.27.1), postgresql.org
(18.6 current + PG19 Beta 4 — versions.json no lista 19), River (v0.48.0), Podman
(v6.1.3), Tailwind (v4.3.3), pgvector (v0.8.6), docs.gitlab.com webhooks (signing
GA 19.1, Standard Webhooks whsec_/v1,/replay ✓) + webhook_events (changes.draft,
object_attributes.draft, acciones open/close/reopen/update/merge/approval) +
drafts ([Draft]/Draft:/(Draft), sin WIP:), docs.github.com (25 MB cap,
X-Hub-Signature-256, X-GitHub-Delivery), docs.coderabbit.ai/reference/configuration
(language/profile quiet-chill-assertive/request_changes_workflow/path_filters/
instructions/chat.auto_reply — actualizado 2026-09-30), preactjs.com (3kB).
Contrastes WCAG recalculados (21 pares: 12.67/14.12/6.30/8.25/3.90/1.93/1.55 —
todos los claims de la tabla siguen en pie).

### Iteración 1 — 3 hallazgos

- [x] H1 (mayor) §3.5: filtro draft de GitLab comparaba prefijos del título — el
      draft es un flag del MR (`object_attributes.draft`; transición en
      `changes.draft {previous,current}`, lista oficial de atributos de changes).
      Botón "Mark as ready" y acción rápida /ready cambian el flag sin tocar el
      título: comparar prefijos pierde esas transiciones y esos MRs nunca se
      revisan. Reescrito sobre el flag; prefijo documentado como forma visible.
- [x] H2 (menor) mapa vcs.reglas: "la vuelta a draft descarta los jobs pendientes"
      duplicado (dentro del filtro update y al final) — duplicado final eliminado.
- [x] H3 (menor) §3.5: desinstalar la App / borrar el proyecto GitLab no genera
      evento suscrito → repo queda enabled con estado huérfano en triage. Agregada
      la mitigación: el operador desconecta el repo en settings.

### Iteración 2 — 2 hallazgos (micro)

- [x] I1 §3.5: inciso anidado con triple raya en el spec de eventos GitHub
      ("— reopened como un opened — y devuelve state a open —;") → paréntesis.
- [x] I2 §3.3: "self-managed viejas" sin umbral → "anteriores a 19.1, donde el
      signing token no existe" (conecta con el GA de §9.3 sin salto de sección).

### Iteración 3 — 1 hallazgo (mayor técnico)

- [x] J1 §3.6 + mapa jobs: unicidad de River documentada como "mientras viva",
      pero el default de ByState incluye `completed` → con defaults, cada PR
      tendría una sola review hasta que la retención borre la fila. El diseño
      (re-encolo en la tx de finalización) exige ByState sin completed.
      Documentado: ByArgs + ByState sin completed; pending/scheduled/available/
      running obligatorios al customizar; retryable se conserva (quitarlo puede
      descartar un reintento por conflicto). Fuente: riverqueue.com/docs/unique-jobs.

### Iteración 4 — convergencia

- [x] Pasada final sin hallazgos: pasajes editados re-leídos en contexto
      (balance de paréntesis ✓, sin palabras duplicadas ✓, refs § y anchors ✓,
      YAML ✓), terminología ByState/changes.draft consistente guía↔mapa ✓.

**Convergencia declarada a la iteración 4 de la ejecución 3 (6 hallazgos: 2 mayores, 2 menores, 2 micro).**
Rama: `docs/web-verified-review-loop`.

## Ejecución 4 (2026-10-01) — cuarta pasada desde cero

Verificación web fresca (agente frío, ~20 claims): go1.27.1, PG 18.6, River v0.48.0
(unicidad ByState verificada literal), Podman v6.1.3 (Quadlet [Service] pasa a systemd),
pgvector 0.8.7-pg18, GitLab signing 19.0-flag/19.1-GA + webhook-id desde 19.0 +
Idempotency-Key desde 17.4, GitHub 25 MB + acciones PR + retarget edited, permisos
Contents:Read, CodeRabbit reference 2026-09-30, Tailwind 4.3.3, Preact 3kB, WCAG
recalculados (todos OK).

### Iteración 1 — 9 hallazgos (1 mayor, 5 menores, 3 micro)

- [x] H1 (mayor) manual §4: `reviews.instructions` no existe en el schema oficial de
      CodeRabbit → reemplazado por `reviews.path_instructions[].instructions`
      (`path: "**"` + `path: "**/*.ts"`).
- [x] H2 §9.3: dedup GitLab anclada a `webhook-id` (solo 19.0+) → agregado
      `Idempotency-Key` (17.4+) como delivery ID en self-managed viejas.
- [x] H3 §6 F0: `deploy/backup.md` prometido (§9.2/§9.10) sin fase dueña → ítem
      checkbox en F0 con restore verificado + respaldo de master key.
- [x] H4 mapa servicios + §3.2: mezcla `.container` (fuente Quadlet) vs `.service`
      (unidad generada) → categorías explícitas: caddy/postgres = Quadlet fuente;
      api/worker = unidades de usuario escritas a mano para binarios host.
- [x] H5 §3.6: `pull_request.base.sha` puede quedar anclado al tip de apertura si la
      base avanza sin retarget → gotcha + fallback por API documentados.
- [x] H6 §3.3: `repo_index` sin rangos de línea vs `--symbols` → agregados
      `start_line`/`end_line`.
- [x] H7 mapa: `Complete(ctx, Rol, ...)` → `Complete(ctx, Role, ...)` (§4.7).
- [x] H8 §3.6 punto 1: párrafo-bullet gigante trozado en 6 sub-bullets (1.1–1.6)
      sin perder contenido normativo.
- [x] H9 §3.3: umbral signing token "anteriores a 19.1" → "anteriores a 19.0 (o
      19.0 con flag `webhook_signing_token` deshabilitado)".
- Commit: d2d7b103d89c (+25/−14, 3 archivos).

### Iteración 2 — 7 hallazgos (3 mayores, 3 menores, 1 micro)

- [x] H1 (mayor) mapa `settings.proposito`: flow mapping sin comillas con comas →
      valor truncado + claves espurias null (confirmado con pyyaml antes del fix).
      Audit posterior del archivo completo encontró 9 instancias más de la misma
      clase en `flujos.*.pasos` (escalares con `: ` parseados como dicts de una
      clave) — las 10 citadas, contenido idéntico verificado por diff.
- [x] H2 (mayor) §3.5 vs §9.2: reconciliación por ListOpenPRs exigía la private
      key que solo vive en el worker → la API ahora encola un `ReconcileJob`
      (nuevo en jobs del mapa, fase F1); el worker ejecuta ListOpenPRs sin LLM.
- [x] H3 (mayor) manual: `auto_title_placeholder` llevaba el default de
      `high_level_summary_placeholder` → corregido a `"@coderabbitai"`.
- [x] H4 §1.1: divergencia de perfiles (CR quiet/chill/assertive vs propio
      chill/assertive/strict) ahora listada en Fuera-de-alcance.
- [x] H5 §6 F0: "coincidente" definido = cada componente del mapa existe en su
      ruta (mapa = autoridad estructural); `odd/` en el árbol §3.2 anotado como
      bookkeeping, no parte del monorepo objetivo.
- [x] H6 legibilidad: mega-bullets de §3.3/§3.5/§3.6/F1/F2/F5 trozados en
      sub-listas (una decisión por bullet), política de invalidación de
      repo_index extraída a bullet propio; paridad de cláusulas verificada.
- [x] H7 §5.2: fondos de las cifras border.subtle anotados (1.93:1 sobre
      bg.base claro / 1.55:1 sobre bg.surface oscuro).
- Commit: 63c8dbddea78 (+106/−27, 4 archivos).

### Iteración 3 — 8 hallazgos (3 menores, 5 micro; cero mayores)

- [x] H1 §6 F1: checklist sin los flujos de desconexión/reconexión que §3.5 y el
      mapa asignan a F1 → checkbox agregado (enabled, descarte, ReconcileJob,
      re-indexo).
- [x] H2 §9.12: "Quadlet fija TimeoutStopSec" → atribuido a la unidad host
      `worker.service`.
- [x] H3 mapa vcs.reglas: párrafo plegado ~2.000 chars → mapping por método
      (9 claves), descripción de PostSuggestion agregada, huérfano
      "config por repo)," corregido.
- [x] H4 §3.2: "Quadlet solo genera..." acotado a este deploy (.container/.build
      son fuentes aquí).
- [x] H5 §9.9: fragmento "— ver backlog" agramatical → corregido.
- [x] H6 §6 F0: "no contradicta" → "no lo contradice".
- [x] H7 mapa analyzer: key `imagen:` → `build:` (convención podman-build).
- [x] H8 §9.2/§9.3: muros de texto → listas de pasos con paridad de cláusulas.
- Commit: 8a7383169ac2 (+62/−43, 3 archivos).



