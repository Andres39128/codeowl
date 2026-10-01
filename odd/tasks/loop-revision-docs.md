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

### Iteración 4 — 8 hallazgos (4 menores, 4 micro)

- [x] H1 §6 F1: re-indexación completa era alcance F4 → anotado (en F1 la
      reconexión solo dispara ReconcileJob).
- [x] H2 §9.2: ejemplo `GITHUB_APP_PRIVATE_KEY_FILE` montaba la key en la
      unidad api (prohibido por la propia sección) → ruta a worker.
- [x] H3 §9.4: "un .eslintrc es JS" factualmente wrong → `eslint.config.js`.
- [x] H4 §3.5: mitigación faltante ante caída de BD al encolar → excepción
      documentada: 5xx para que el VCS re-entregue; dedup la hace segura.
- [x] H5 mapa MetricsJob: par de raya roto "—," → reparado.
- [x] H6 mapa: excepción de forma documentada (vcs.reglas y features.expone
      usan key-mappings para lookup) — nota en convenciones, sin re-flatten.
- [x] H7 §3.2: desalineado real eran README/LICENSE (col 31 vs 32) — hallazgo
      señalaba la fila equivocada; corregidas las verdaderas.
- [x] H8 §9.3: >25MB GitHub ni siquiera entrega → chequeo aplica de facto a
      GitLab y tope propio.
- Commit: 4ddc9ba66dd4 (+29/−8, 3 archivos).

### Iteración 5 — 5 hallazgos (3 menores, 2 micro)

- [x] H1 §2/§2.1: cifra de Preact oficializada (padre verificó preactjs.com:
      "Fast 3kB") → "~3KB core / ~4KB con preact/compat".
- [x] H2 mapa dashboard.fase: omitía F1/F3 (que el propio features asigna) →
      F0 (shell) / F1 (queue) / F3 (prs) / F5 (triage completo).
- [x] H3 legibilidad: fila repositories (etiqueta corregida: el hallazgo decía
      reviews), §3.6 1.6, §9.6 cache, §3.5 draft → sub-bullets con paridad.
- [x] H4 §3.5: anotación inline de la postergación a F4 (consistente con F1).
- [x] H5 mapa vcs.reglas: nota de naming (keys = método del contrato).
- Commit: 16490dcb46b6 (+41/−8, 3 archivos).

## Ejecución 5 (2026-10-01) — quinta pasada desde cero

Verificación web fresca (12 fuentes, todas OK): go.dev (go1.27.1), postgresql.org
(18.6 + PG19 Beta 4, 24/09), riverqueue.com unique-jobs + maintenance-services
(ByState default sin cancelled/discarded; estados obligatorios; retention
horizons: completed/cancelled 24 h, discarded 7 días), docs.gitlab.com webhooks
(signing 19.0-FF/19.1-GA, Standard Webhooks exacto, secret token "not
recommended", webhook-id 19.0 = Idempotency-Key 17.4) + webhook_events
(object_attributes.draft, changes.draft, acciones open/update/close/reopen/
merge/approval), docs.github.com validating (HMAC-256 + timing-safe) +
best-practices + events-and-payloads (cap 25 MB, delivery GUID),
docs.coderabbit.ai/reference/configuration (2026-09-30: language, profile
quiet/chill/assertive, request_changes_workflow, auto_title_placeholder,
path_filters, path_instructions[].instructions, chat.auto_reply),
hub.docker.com pgvector (tags pg18 / 0.8.7-pg18), podman v6.1.3,
tailwindcss.com (v4.3; claims 5x/100x oficiales), preactjs.com (3kB).
Contrastes WCAG recalculados con script propio (36 pares: todos los claims
en pie; hallazgo H2 sobre el fondo del 4.45).

### Iteración 1 — 6 hallazgos (2 menores, 4 micro)

- [x] H1 (menor) §9.7/§9.11: "discarded nunca silenciado" sin horizonte —
      River poda estados terminales (completed/cancelled 24 h, discarded 7 d
      por defecto, config de River). §9.11 documenta la retención propia y
      el registro duradero (reviews.failed no se poda); §9.7 ancla la
      ventana. La unicidad de ReviewJob (sin completed) ya era independiente
      de esa retención — verificado en la fuente.
- [x] H2 (micro) §5.1: "amber-700 4.45:1 sobre crema" — esa cifra es sobre
      bg.surface (sobre base da 4.68 y pasaría AA ahí); corregido el fondo y
      la exigencia "en ambos fondos".
- [x] H3 (menor) mapa flujos.cerrar_pr: cortaba en el enqueue del
      MetricsJob; agregados los pasos de ejecución (FetchPRTimeline →
      outcome sobre filas existentes, sin LLM) — paridad con los demás
      flujos.
- [x] H4 (micro) §2: "zhipuai" → "ZhipuAI" (casing de marca, consistente
      con MiniMax/Ollama).
- [x] H5 (micro) §9.3: GitLab <17.4 no expone ningún header de entrega —
      documentado el límite y su mitigación (upsert idempotente + unicidad
      de jobs).
- [x] H6 (micro) .gitignore: .atl/ (caché del skill registry) sin ignorar —
      agregado; fuera del status.

### Iteración 2 — 6 hallazgos (1 mayor, 1 menor, 4 micro)

- [x] I1 (mayor) §3.3/§6 F1/§9.1: credencial API de GitLab inexistente en
      el doc — la deploy key solo clona (SSH) y el signing token solo
      verifica entregas; ninguna llamada REST del adapter (publicar
      comentarios, ListOpenPRs, FetchPRTimeline, tip de base) tenía
      credencial documentada. Agregado: API token de proyecto (cifrado) en
      repositories, flujo de conexión F1 (project access token scope api),
      lista de secrets de §9.1.
- [x] I2 (menor) §6 F3 + mapa: DiffViewer sin fuente de datos del diff —
      GetDiff agregado al contrato VCSProvider (bajo demanda, sin copia en
      BD); encadena el fix de §9.2: la private key de la App pasa a vivir
      en api y worker (la API resuelve el diff; el worker clona y publica).
      Mapa api.nota anotado.
- [x] I3 (micro) §9.11: el párrafo creció con H1 → reestructurado en
      lista (misma clase que exec 4 H6).
- [x] I4 (micro) §6 F1: "AES-GCM" → "AES-256-GCM" (paridad con §3.3/§9.2).
- [x] I5 (micro) §5.1: severidad media — paréntesis anidado eliminado
      (4.45 surface / 4.68 base en cláusulas planas).
- [x] I6 (micro) mapa cerrar_pr: pasos agregados con quoting correcto
      (escalar con ": " entre comillas — lección exec 4 H1) + YAML
      validado tras la edición.

### Iteración 3 — 1 hallazgo (micro)

- [x] J1 §9.2: el ejemplo `_FILE` apuntaba al dir de credenciales del
      worker solo — la private key ahora vive en ambas unidades →
      `/run/credentials/<unidad>/...` (cada unidad monta el suyo).
      Auditoría programática de refs §X.Y: ninguna rota (35 subsecciones).


