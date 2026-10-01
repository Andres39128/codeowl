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



