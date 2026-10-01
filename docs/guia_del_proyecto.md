# Guía de Proyecto — Plataforma de Code Review con IA

> Documento fundacional. Define cómo se aborda el proyecto, el stack, la arquitectura, las reglas de escritura de código, el diseño visual y las fases de entrega. Toda decisión de implementación debe poder rastrearse a este documento. Si algo contradice esta guía, gana la guía; si la guía queda desactualizada, se actualiza primero y luego se escribe código.

---

## Contenido

1. [Introducción y Cómo Abordar el Proyecto](#1-introducción-y-cómo-abordar-el-proyecto)
2. [Stack Tecnológico](#2-stack-tecnológico)
3. [Arquitectura y Estructuras](#3-arquitectura-y-estructuras)
4. [Reglas de Escritura de Código](#4-reglas-de-escritura-de-código)
5. [Diseño Visual — Identidad Crema + Verde (tema claro y oscuro)](#5-diseño-visual--identidad-crema--verde-tema-claro-y-oscuro)
6. [Fases del Proyecto](#6-fases-del-proyecto)
7. [Estructura del Código para Revisión Fácil](#7-estructura-del-código-para-revisión-fácil)
8. [Pruebas](#8-pruebas)
9. [Seguridad y Operación](#9-seguridad-y-operación)
10. [Checklist de Revisión por Componente](#10-checklist-de-revisión-por-componente)
11. [Siguiente Paso](#11-siguiente-paso)

---

## 1. Introducción y Cómo Abordar el Proyecto

### 1.1. Qué construimos

Una plataforma **self-hosted, single-org** de revisión automática de código con IA, inspirada en CodeRabbit.ai: recibe webhooks de GitHub y GitLab, analiza cada Pull Request con analizadores estáticos + agentes LLM, y publica resumen, comentarios inline y diagramas Mermaid directamente en el PR. Incluye dashboard de triage con cola de PRs priorizada por riesgo.

**Fuera de alcance (explícito, tan vinculante como lo incluido):**

- Otros VCS: solo GitHub y GitLab. Bitbucket y Azure DevOps, nunca — el contrato `VCSProvider` (§3.2) los haría agregables si algún día importara.
- GitHub Enterprise Server: la GitHub App apunta a github.com; GitLab self-managed sí está soportado — URL de instancia por repo (`repositories`, §3.3).
- Multi-tenant / multi-org: un solo workspace, 1-5 usuarios (§3.4).
- SSO/OAuth externo (§3.4).
- Issue Planner e integraciones con Jira, Linear o GitHub Issues.
- `request_changes_workflow`: el bot comenta y sugiere, jamás bloquea merges ni emite veredictos formales de cambio requerido.
- El esquema de perfiles de CodeRabbit (`quiet`/`chill`/`assertive`, manual §4): `review.yaml` usa designación propia — `chill`/`assertive`/`strict` (§6 F3).
- "50+ analizadores": la lista es la de §2 (5 linters). Crece solo por necesidad demostrada.
- Cumplimiento formal SOC 2 / GDPR. El procesamiento efímero sí aplica: los clones se borran al terminar el job (§3.3) y el código no entrena modelos.

### 1.2. Principios de abordaje (no negociables)

1. **Funcional sobre completo.** Cada fase entrega software que corre de punta a punta. Nunca se acumulan semanas de "infraestructura invisible".
2. **Vertical, no horizontal.** Se construye en slices verticales (webhook → análisis → comentario) antes que en capas horizontales completas (todos los modelos, todos los endpoints).
3. **Librerías ligeras.** Stdlib primero; cada dependencia nueva debe justificar su peso. Prohibido framework pesado donde alcanzan 200 líneas propias.
4. **Cada componente se revisa contra su contexto.** Al terminar un componente se verifica: ¿encaja con el dominio al que pertenece? ¿respeta las convenciones de esta guía? ¿queda obsoleto o duplicado algo que ya existía? Un componente no está "done" hasta pasar ese ajuste.
5. **El historial vive en git.** Comentarios, docs y código describen el presente. Nada de "antes era X, ahora Y".
6. **Deletion over addition.** Código muerto se borra en el mismo PR que lo reemplaza. No se marca `// TODO: borrar después`.

### 1.3. Cómo trabajar cada fase

```
Fase → hitos verticales → PR por hito → revisión con checklist (§10) → merge → demo runnable
```

- Un PR = una unidad de trabajo revisable (idealmente < 400 líneas de diff).
- Al cerrar cada hito se ejecuta el sistema completo (`just dev`) y se verifica el resultado visible.
- Nada de "ya casi": o el hito demuestra su valor corriendo, o no está terminado.

---

## 2. Stack Tecnológico

| Capa | Tecnología | Justificación |
|------|-----------|---------------|
| API + Workers | **Go 1.27** (pgx) | Router: `net/http.ServeMux` estándar (method patterns y path params desde Go 1.22) — cero dependencia de router; concurrencia nativa; binario único |
| Datos + Cola | **PostgreSQL 18 + pgvector + River** | OLTP, embeddings y cola de jobs en **un solo servicio**: el webhook y su job se escriben en la misma transacción — un servicio menos para operar |
| Frontend | **Preact + Vite + TanStack Query + Tailwind v4** | Runtime ~4KB vía alias `preact/compat` (misma API que React); server-state sin boilerplate |
| Parseo AST | **Tree-sitter** (en contenedor analyzer) | Aísla cgo del binario Go; un solo punto de parseo multi-lenguaje |
| SAST/Linters | ESLint, Ruff, golangci-lint, Clippy, Gitleaks | Dentro de la imagen analyzer; salida JSON normalizada que reporta qué linter corrió y cuál no pudo (§9.4) |
| LLM | **Gateway OpenAI-compatible propio** | Multi-proveedor (zhipuai, MiniMax, Ollama local, cualquier endpoint compatible); routing por rol configurado en dashboard |
| Contenedores | **Podman 6 rootless + Quadlet** | Sin daemon residente, sin root; servicios como unidades systemd nativas. `compose.dev.yml` solo como fallback de desarrollo |
| Sandbox | Podman (`--network=none --read-only`) | Ejecución de analizadores sin riesgo de fuga ni escape |
| Runner de tareas | **just** | Recetas declarativas y listables (`just --list`), sin los gotchas de Make (timestamps, quoting de shell) |

### 2.1. Decisiones de stack verificadas (octubre 2026)

| Decisión | Elegido | Alternativa descartada | Motivo |
|----------|---------|------------------------|--------|
| Cola de jobs | **River** (sobre Postgres) | asynq + Redis/Valkey | Elimina el broker completo: menos RAM, menos ops, y el encolado es transaccional con los datos del PR. River está en 0.x → mitigación: interfaz `JobQueue` propia en `internal/jobs` que aísla el reemplazo |
| Broker/cache | Ninguno | Valkey 8 | Single-org no necesita caché distribuida. Si llegara a hacer falta, Valkey (BSD, drop-in de Redis, soportado por AWS/GCP) es la elección |
| Frontend | **Preact** (`preact/compat`) | React 19 | ~4KB vs ~45KB de runtime con la misma API; el ecosistema (TanStack Query, etc.) funciona vía alias |
| Router HTTP | **net/http.ServeMux** (stdlib) | chi 5, echo | Go 1.22+ cubre method patterns y `r.PathValue`; el chaining de middleware son ~10 líneas propias. chi/echo reaparecen solo si el routing crece más que eso |
| Contenedores | **Podman + Quadlet** | Docker + daemon | Rootless por defecto, sin daemon consumiendo RAM, integración systemd nativa. Docker Compose sigue siendo válido para CI |
| Motor CSS | **Tailwind v4.3** | Tailwind v3 | Motor reescrito (builds completos 5x más rápidos, incrementales 100x), configuración CSS-first, plugin Vite oficial |
| PostgreSQL | **18** | 16 / 17 | Estable actual (18.6, verificado a octubre de 2026); PG 19 sigue en beta (Beta 4) — no se usa beta para base de datos del sistema |

### 2.2. Regla de dependencias

- Toda dependencia nueva se justifica en el PR con una línea: *qué problema resuelve que la stdlib no resuelve*.
- Prohibido: frameworks ORM pesados (usamos **sqlc** + migraciones golang-migrate), frameworks de agentes LLM (el loop de agente es código propio, ~200 líneas), UI kits completos (Tailwind + 4-5 componentes propios alcanzan).
- Todo comando de desarrollo vive en el `justfile` — nadie memoriza comandos: `just --list`.
- Métrica de control: si `go.mod` crece más rápido que la funcionalidad, algo va mal.

---

## 3. Arquitectura y Estructuras

### 3.1. Diagrama de contexto

```
[ GitHub App / GitLab Webhook ]
              │ firma verificada + idempotencia por delivery ID
              ▼
   [ API Go (ServeMux stdlib) ]
              │ encola (River — transaccional)
              ▼
   [ Workers Go ── orquestador multi-agente ]
        │            │               │
        ▼            ▼               ▼
  [ Analyzer    [ RAG:           [ LLM Gateway
    sandbox ]    pgvector +       (routing por rol:
    Podman       grafo de         review / cheap /
    sin red      símbolos ]       embedding)
        │            │
        └──────┬─────┘
               ▼
      [ PostgreSQL: datos + findings + jobs + índice ]
               │
               ▼
   [ Comentarios inline + resumen + Mermaid → PR ]
               │
               ▼
   [ Dashboard Preact: Queue / Triage / PRs / Settings ]
```

### 3.2. Estructura del monorepo

```
codeowl/                   (raíz del monorepo — justfile; la guía, el manual y el mapa viven en docs/)
├── README.md                   # entrada del repo: qué es codeowl y cómo navegar docs/
├── LICENSE                     # Apache-2.0
├── justfile                    # recetas: dev, test, lint, migrate, up, down, deploy (§9.13)
├── .github/workflows/          # CI: lint + build + test (F0), reutiliza recetas del justfile
├── odd/                        # documentos de trabajo de las sesiones de edición de docs — bookkeeping del repo, no es producto ni parte del monorepo objetivo
├── backend/
│   ├── cmd/
│   │   ├── api/main.go          # servidor HTTP: webhooks + REST dashboard
│   │   └── worker/main.go       # worker River: consume jobs de Postgres
│   ├── internal/
│   │   ├── api/                 # handlers, middleware, routing (ServeMux stdlib)
│   │   ├── jobs/                # interfaz JobQueue: wrapper de River, enqueue transaccional
│   │   ├── vcs/                 # contrato VCSProvider + adapters github/, gitlab/
│   │   ├── review/              # pipeline de revisión y agentes LLM
│   │   ├── analyze/             # cliente del analyzer sandbox + normalización
│   │   ├── index/               # indexación RAG: chunking, embeddings, grafo
│   │   ├── llm/                 # gateway multi-proveedor, roles, rate limit
│   │   ├── store/               # sqlc queries + modelos de dominio
│   │   └── config/              # carga de env; validación por etapas — esenciales al arranque, credenciales VCS al conectar el primer repo (mapa: backend.config)
│   ├── migrations/              # SQL versionado (golang-migrate)
│   └── prompts/                 # prompts de agentes versionados como código
├── analyzer/
│   ├── Containerfile            # tree-sitter + linters, sin red
│   └── src/                     # CLI de análisis y normalización JSON + modo symbols (binario Go; cgo/tree-sitter vive solo en este contenedor)
├── dashboard/
│   ├── src/
│   │   ├── features/            # queue/, triage/, prs/, settings/, auth/
│   │   ├── components/          # componentes compartidos (Button, Badge, Table…)
│   │   ├── lib/                 # cliente API, hooks, utilidades
│   │   └── theme/               # tokens de diseño (§5)
│   └── index.html
├── deploy/
│   ├── quadlet/                 # unidades systemd: caddy.container (proxy TLS, único puerto expuesto) y postgres.container (fuente Quadlet); api.service y worker.service (unidades de host para los binarios — aquí, solo .container/.build son fuentes Quadlet; las .service son unidades de host escritas a mano); analyzer.build construye la imagen del sandbox
│   ├── compose.dev.yml          # fallback de desarrollo (compatible Podman/Docker)
│   ├── backup.md                # runbook de dump diario y restore verificado (§9.10) + respaldo de la master key (§9.2)
│   └── .env.example             # toda variable de entorno documentada
└── docs/
    ├── guia_del_proyecto.md     # este documento
    ├── manual_completo_de_coderabbit_ai.md  # referencia funcional
    └── mapa_arquitectura.yaml   # mapa estructural: componentes, dependencias, flujos
```

**Regla de estructura:** cada paquete `internal/` tiene un solo propósito y una frontera clara. Si un paquete necesita importar a otro para algo puntual, se extrae interfaz en el consumidor; prohibidas dependencias circulares y paquetes `utils`/`helpers` cajón de sastre.

**Nota:** el nombre del directorio/repo es ASCII sin acentos (`codeowl`) — los acentos en paths rompen tooling, CI y shell scripting.

### 3.3. Modelo de datos (núcleo)

| Tabla | Responsabilidad |
|-------|----------------|
| `users` | usuarios del dashboard (1-5, single-org): username (único — identificador de login), role (admin/member), hash de contraseña (argon2id), must_change_password (en true tras invitación o reset — bloquea toda operación hasta el cambio) |
| `llm_providers` | proveedores LLM: base_url, model, api_key cifrada, rol (review/cheap/embedding), priority (orden de failover dentro del rol; empate rompe por id — orden estable), enabled |
| `repositories` | repos conectados:<br>• vcs, external_id (ID numérico del repo en el VCS)<br>• webhook_secret (solo GitLab: signing token del proyecto — o secret token en instancias self-managed anteriores a 19.0 (o 19.0 con el flag `webhook_signing_token` deshabilitado), donde el signing token no existe, §9.3; GitHub App usa el secret global de la App en env)<br>• base_url de instancia (solo GitLab: default gitlab.com — soporta self-managed)<br>• deploy key de clonado de solo lectura (cifrada, solo GitLab)<br>• enabled (desconexión como flag, no delete — §3.5)<br>• config default (revisar drafts §3.5, idioma, chat solo-org §3.5 — idioma y reglas de revisión son las keys que `review.yaml` especializa (§9.5); drafts y chat son operativas, siempre del dashboard) |
| `pull_requests` | PRs observados (clave natural: repo + número/iid del PR en el VCS — el upsert por webhook es idempotente): author, state (conjunto cerrado: open/closed; el merge es un close con `merged_at` registrada), created_at (fecha del PR en el VCS — con `merged_at`, insumo del cycle time de F5: creación → merge), risk_score, head_sha, base_ref, base_sha (del último evento — en GitHub llega en el payload; en GitLab el payload no trae SHAs de la base y el adapter la resuelve por API, §3.6 — la base es necesaria para el retarget de §3.6 y la cache por merge-base de §9.6) |
| `reviews` | una por corrida (FK a pull_requests): summary, walkthrough, mermaid, status — conjunto cerrado: `running`, `success`, `partial` (presupuesto agotado, diff sobre el tope de solo-resumen, archivo sobre el tope por archivo o failover agotado, §9.6-§9.7), `stale` (identidad reemplazada — push o retarget — o PR cerrado en vuelo, §3.6), `failed` (intentos agotados, §9.7); estados nuevos solo con actualización de este doc |
| `findings` | hallazgos (FK a `reviews` — la corrida que los produjo: dedup, métricas de F5 y auditoría leen la atribución por corrida): file, line, severity, category, body, suggestion, source (llm/sast), verified — null hasta que el Verifier corre (F3), luego true/false; aplica a hallazgos `source=llm`: los SAST son deterministas y se publican sin verificación |
| `comments_sent` | comentarios publicados por PR (FK a pull_requests): comment_id externo por finding inline, resumen o respuesta de chat → idempotencia y actualización; cada fila registra su tipo (`inline`/`summary`/`chat`), la huella de dedup — solo filas `inline`: file + categoría + ancla (§3.6); las filas `chat` anclan al comentario que las disparó (comment_id padre) — un reintento del ChatJob no duplica la respuesta (§9.12) — y `created_at` — extremo final de la medición de los SLO de §6, cada SLO sobre sus propias filas |
| `repo_index` | símbolos indexados:<br>• columnas: file, symbol, kind, start_line, end_line, embedding, embedding_model (+dims), imports<br>• política de invalidación del índice: cambiar el proveedor `embedding` invalida el índice (pgvector fija dims en DDL) y dispara re-index; el cambio se guarda en settings: mismas dimensiones → re-index completo, dimensiones distintas → migración nueva (forward-only, migraciones de §3.3) + re-index<br>• settings valida las dims del proveedor contra las del índice antes de habilitarlo |
| `llm_usage` | una fila por llamada LLM: job_id (null en la llamada de prueba de conexión de settings — no hay job que la respalde), rol, provider, model, tokens_in, tokens_out, created_at → base de las métricas F5 y de los presupuestos (§9.6) |
| `webhook_deliveries` | delivery IDs de webhooks ya procesados:<br>• delivery ID (`X-GitHub-Delivery` / `webhook-id` de GitLab — §9.3) con su `created_at`<br>• único por (vcs, delivery_id): los IDs de ambos proveedores no comparten un espacio garantizado<br>• dedup ante reintentos y re-entregas del VCS<br>• el `created_at` marca el inicio de la medición del SLO de §6 y la edad que la retención de §9.11 limpia |
| `sessions` | sesiones del dashboard: hash de token opaco, user_id, expiración |

**Clonado y workspace por job:**

- Cada job de review/index clona shallow a un workdir efímero (`/var/tmp/rev-<job-id>`) que se borra al terminar, éxito o error.
- El clon trae head y rama base; si `git merge-base` falla por profundidad insuficiente, se reintenta con `--deepen` hasta el tope configurado.
- PRs de forks: el head de un fork externo no es accesible con el token de instalación del repo base — el clon trae el ref del PR desde el repo base (`refs/pull/<n>/head` en GitHub; `refs/merge-requests/<iid>/head` en GitLab), que sí es accesible.
- Se trae el ref de head, no el merge ref de GitHub (`refs/pull/<n>/merge`): el merge ref es el test-merge de GitHub — mezcla la resolución de la fusión en el diff revisado y queda desactualizado o ausente en PRs con conflictos; con head en ambos VCS, el diff revisado es siempre head...base.
- PRs del mismo repo: head branch directo.
- Credenciales en GitHub:
  - installation token de la App solicitado por job — efímero (~1 h), nunca persiste.
  - la private key PEM de la App (`GITHUB_APP_ID` + `GITHUB_APP_PRIVATE_KEY`) se carga desde env o su variante `_FILE` (§9.2), jamás en BD.
  - el webhook secret de la App es global por App (`GITHUB_WEBHOOK_SECRET` en env), no por repo.
- Credenciales en GitLab: deploy key y webhook secret por repo, cifrados (AES-256-GCM, misma master key de §9.2).
- El presupuesto de disco por workdir es configurable.

**Taxonomía cerrada de `findings.category`:** `security`, `logic`, `performance`, `style`, `tests`, `other`. No se agregan categorías sin actualizar este doc y el schema de validación de agentes (§9.8) en el mismo cambio — el dedup por huella (§3.6) y las métricas de F5 dependen de este conjunto cerrado, y una categoría nueva a mitad de camino ensucia el histórico de dedup.

**Severidad cerrada de `findings.severity`:** `high`, `medium`, `low` — mapea directo a los tokens `severity.alta/media/baja` del tema (§5.1). Mismo régimen que las categorías: el schema de validación de agentes (§9.8) la exige, y cambiar el conjunto toca este doc en el mismo PR.

Migraciones: solo adelante (forward-only), una por cambio, nombre descriptivo (`0007_add_risk_score_to_pull_requests.sql`). El schema propio de River entra por su migración oficial incluida en `backend/migrations/`.

### 3.4. Autenticación del dashboard

Single-org, 1-5 usuarios, sin registro público. El primer usuario (admin) se crea por seed desde env en Fase 0 (`ADMIN_USERNAME` + `ADMIN_PASSWORD`, con su variante `_FILE` en Quadlet — §9.2) y arranca con `must_change_password` en false — la credencial la definió el operador; el cambio obligatorio aplica a invitaciones y resets; los demás los invita el admin desde el dashboard, que también resetea contraseñas — no hay flujo de auto-reset por email: no hay mailer en el stack — y desactiva miembros: la baja es un flag, no un delete — revoca el acceso (sus sesiones activas incluidas); nada se borra en cascada y el histórico del sistema queda intacto (sin bitácora de auditoría por usuario — single-org, §3.3). El reset de una contraseña revoca todas las sesiones del usuario. La invitación genera una contraseña temporal que el dashboard muestra al admin una única vez (la entrega queda fuera del sistema) y su cambio es obligatorio en el primer login. Frontera de roles: `member` opera el triage y consulta PRs, findings y estado de la cola; todo settings — proveedores LLM, repos conectados, usuarios — es exclusivo del `admin`.

1. Contraseñas con hash **argon2id**; longitud mínima configurable; login con rate limit (backoff tras N fallos por usuario).
2. Sesión: cookie `HttpOnly` + `Secure` + `SameSite=Lax` con token opaco aleatorio (256 bits); en BD se guarda solo su hash (`sessions`); expiración configurable; logout revoca la sesión.
3. CSRF: token de sesión verificado en todo endpoint mutante (`POST/PUT/DELETE`), además del `SameSite`. El CSRF aplica a endpoints autenticados por cookie de sesión; los webhooks se autentican por firma HMAC, sin cookie, y quedan fuera del CSRF.
4. Sin OAuth/SSO externo: es self-hosted single-org, no vale el costo.

### 3.5. Topología de red y recepción de webhooks

Los webhooks de GitHub/GitLab exigen un endpoint HTTPS públicamente alcanzable — esto condiciona el deploy completo:

- **Producción (self-host):** VPS con dominio propio; Caddy como reverse proxy con TLS automático (ACME) hacia la API en `:8080`. Único puerto expuesto: 443. Postgres, worker y analyzer nunca exponen puertos. Las unidades Quadlet viven en el manager de usuario (`systemctl --user`): `rootless` aplica también al analyzer que el worker invoca — un worker en unidad de sistema correría podman rootful y vaciaría la promesa del sandbox. El usuario del deploy lleva `loginctl enable-linger` activado: sin linger, las unidades de usuario no arrancan al boot sin sesión abierta.
- **Desarrollo:** `compose.dev.yml` + túnel para webhooks (smee.io para la GitHub App, o cloudflared). Los tests de integración usan payloads firmados de fixtures y no necesitan salida a internet.
- Dashboard y API comparten origen (la API sirve los estáticos de `dashboard/` con fallback SPA: las rutas de cliente que no son `/api` ni `/webhooks` sirven `index.html`) → sin CORS.
- **Respuesta inmediata, cero trabajo en el handler:** valida firma e idempotencia, filtra, y upserta/encola en la misma transacción — responde 2xx inmediato y jamás llama LLM ni ejecuta análisis: todo el trabajo vive en jobs (`ReviewJob`/`ChatJob`), nunca en el proceso del webhook. Excepción: si el upsert/enqueue falla (p. ej. BD caída), el handler responde 5xx para que el VCS re-entregue; la dedup por delivery ID hace la re-entrega segura.
- **Eventos suscritos (y nada más):**
  - GitHub App — `pull_request` (opened/synchronize/reopened/ready_for_review disparan review (`reopened` se trata como un `opened`: devuelve `state` a `open`); closed y converted_to_draft no; `edited` tampoco, salvo que cambió la base del PR (`changes.base` en el payload): un retarget deja vigente una review calculada contra la base vieja y debe disparar re-review), `issue_comment` (created), `pull_request_review_comment` (created).
  - GitLab:
    - `merge_request` (open/update/reopen/close/merge) y `note`.
    - `update` solo pasa el filtro si cambió el source (push), `target_branch` (retarget) o la marca draft — en GitLab el draft es un flag del MR, no un evento propio: el payload trae `object_attributes.draft` y la transición llega en `changes.draft` (`{previous, current}` — la lista oficial de atributos de `changes` incluye `draft`).
    - el título con prefijo `Draft:`, `[Draft]` o `(Draft)` es solo la forma visible del flag: el botón *Mark as ready* y la acción rápida `/ready` lo cambian sin tocar el título, y comparar prefijos perdería esas transiciones; los viejos `WIP:` ya no aplican.
    - el filtro pasa solo la transición de draft a ready — el equivalente de `ready_for_review`.
    - la vuelta a draft descarta los jobs pendientes del PR, mismo tratamiento que `converted_to_draft` en GitHub.
    - el resto del título, labels o descripción se descartan.
  - Cualquier otro evento o `action` se descarta temprano en el handler, sin tocar la cola.
  - Repo ausente o desconectado:
    - webhook de un repo ausente de `repositories`: 2xx inmediato y descarte con log estructurado, sin job ni fila en `webhook_deliveries` — solo repos conectados generan estado.
    - desconectar un repo desde el dashboard es un flag `enabled`, no un delete — preserva el histórico: descarta los `ReviewJob`/`ChatJob`/`MetricsJob` de sus PRs y también el `IndexJob` pendiente (la reconexión re-indexa completo; correrlo gasta embeddings de un repo deshabilitado), conserva hallazgos e índice RAG.
    - la reconexión dispara el `IndexJob` completo por la política de conexión de F4 y encola un `ReconcileJob` de reconciliación de estado: el worker consulta los PRs abiertos del repo al VCS vía `ListOpenPRs` del adapter (sin LLM) y marca cerrados los que ya no figuran — los cierres ocurridos durante la desconexión no llegaron como evento y sin esto quedarían abiertos en la cola de triage para siempre; las métricas de esos cierres no se recuperan — su `MetricsJob` se alimentaba del evento descartado.
    - mientras está desconectado, sus webhooks reciben el mismo tratamiento que los de un repo ausente: 2xx inmediato y descarte, sin job ni estado.
    - desinstalar la App del repo en GitHub (o eliminar el proyecto en GitLab) no genera ningún evento suscrito — los webhooks simplemente dejan de llegar: el operador desconecta el repo en settings para que la cola de triage no acumule estado huérfano de PRs que ya no informan cierres.
  - PRs pre-conexión: los PRs abiertos antes de conectar — o mientras el repo estuvo desconectado — no se revisan retroactivamente: la revisión es reactiva a eventos, y el primer push o retarget posterior a la (re)conexión dispara la primera review. Un comentario sobre ese PR sí upsertea la fila y el chat responde — el upsert idempotente por evento aplica a todo evento suscrito de un repo conectado; solo la review espera su evento de diff.
  - Filtros de comentario:
    - `issue_comment` llega por issues y PRs por igual: solo pasa el filtro el comentario en un PR que mencione al bot.
    - en GitLab, `note` se filtra por `noteable_type == MergeRequest` + mención.
    - los dos eventos de comentario (`issue_comment` en la conversación del PR, `pull_request_review_comment` en el hilo de un inline) alimentan el mismo Chat de F2: la mención al bot puede llegar por cualquiera de los dos.
    - los comandos de chat de autores fuera de la organización se ignoran (config) — en repos públicos, cualquier tercero podría quemar presupuesto LLM con una mención.
    - GitHub trae `author_association` en el payload (pasan MEMBER/OWNER); GitLab no expone equivalente en `note`: el adapter resuelve la membresía del proyecto por username con cache de TTL corto (config).
- **Cierre del PR (`closed` en GitHub; `close`/`merge` en GitLab):** actualiza `pull_requests.state` (y `merged_at` si el close es un merge), descarta los jobs `ReviewJob`/`ChatJob` pendientes de ese PR — cero LLM — y encola el `MetricsJob` de F5: al cerrar, la conversación y los commits del PR quedaron completos, que es el insumo de sus métricas (§6 F5); el `IndexJob` es por repo, no por PR, y sigue su curso — tras un merge, indexar la nueva base es exactamente lo que corresponde, y el propio merge encola además un `IndexJob`: la rama por defecto cambió y, si ya hay uno vivo, la unicidad lo deduplica — el que corre ve el estado del momento. Sin este evento, la tabla nunca se entera del cierre y la cola de triage (F5) mostraría PRs fusionados como abiertos para siempre.
- **PRs draft:** no se revisan (default, config por repo); la review arranca cuando el PR pasa a ready (`ready_for_review` en GitHub; en GitLab, quitar la marca draft re-envía el evento `merge_request`). El chat sigue operativo sobre un draft — responde en el hilo con su presupuesto propio (§9.6) — pero `/review` se rehúsa mientras el PR siga draft: la review está deshabilitada por config, misma clase de negativa que sobre un PR cerrado (§6 F2). La vuelta a draft (`converted_to_draft`) descarta los `ReviewJob`/`ChatJob` pendientes del PR — mismo tratamiento que el cierre, sin gastar LLM: un PR que su autor marcó draft no pide revisión.
- **Anti-bucle del chat:** los handlers de comentario ignoran todo comentario cuyo autor sea el propio bot (App ID / bot username) o cualquier bot — GitHub marca el tipo de autor en el payload; GitLab no expone flag de bot y la comparación es contra el username del bot (config). Responderse a sí mismo es el incidente clásico de loop infinito con presupuesto LLM quemado.

### 3.6. Re-review y ciclo de vida de comentarios

Qué pasa en cada nuevo push (nuevo `head_sha`) o retarget (nueva base) del PR:

1. **Corridas stale:** el job guarda la identidad de la corrida que lo disparó — `(head_sha, base_sha)`: el head cambia con cada push, la base con cada retarget.
   - **1.1. Ancla de la base:** el tip de la rama, no el merge-base, y se compara sin clonar. Cómo llega difiere por proveedor: GitHub lo trae en el payload (`pull_request.base.sha`), pero el payload de GitLab no incluye ningún SHA de la rama base (solo nombres de ramas, `last_commit` y `oldrev`) — su adapter resuelve el tip con una llamada API al procesar el evento (endpoint de branches, cache corto de TTL config — llamada de metadatos acotada: lo vetado en el handler es LLM y análisis, §3.5). El `base.sha` del payload puede quedar anclado al evento de apertura si la base avanza sin retarget: si se necesita identidad estricta de la base, el adapter la resuelve por API (mismo camino que GitLab).
   - **1.2. Falsos `stale` aceptados:** un avance de la base entre encolado e inicio puede marcar `stale` una corrida cuyo diff no cambió (merge-base intacto) — re-review espuria, rara y en la dirección segura: nunca se pierde una review. La cache de §9.6 sí ancla en el merge-base: al computarla el clon ya existe y es la frontera exacta del diff.
   - **1.3. Re-chequeo doble:** se compara contra el estado actual del PR al iniciar y antes de publicar; si difiere, la corrida se marca `stale` y termina sin gastar LLM ni publicar comentarios de un diff viejo. El chequeo pre-publicación incluye el cierre del PR: el handler de cierre descarta los jobs pendientes, pero una corrida ya en vuelo no se puede descartar — PR cerrado → sin publicación, la corrida queda `stale` (comentar un PR cerrado no aporta y quema cuota del VCS).
   - **1.4. Unicidad del `ReviewJob`:** el `ReviewJob` se encola además como job único por PR (River unique opts) — el encolado duplicado se descarta mientras viva el anterior.
     - `ByArgs` por PR y `ByState` **sin `completed`** — el default de River incluye `completed` en la unicidad y con él cada PR quedaría con una sola review hasta que la retención borre la fila completada.
     - al customizar `ByState`, los estados `pending`, `scheduled`, `available` y `running` son obligatorios, y `retryable` se conserva para que un reintento no muera descartado por conflicto.
   - **1.5. Re-encolo transaccional:** el re-encolo vive dentro de la transacción que completa el job: esa tx re-chequea la identidad `(head_sha, base_sha)` y, si difiere, marca `stale` y encola la corrida del estado actual en la misma tx — el job completado ya no bloquea la unicidad y el insert pasa.
   - **1.6. Cobertura de interleavings:** con esto no existe interleaving que pierda una review — ni por push ni por retarget: un encolado descartado porque el job vivía, o su cambio aterrizó antes de la tx de finalización — que entonces ve el estado nuevo y re-encola —, o aterrizó después — y su encolado ya no choca con nada. El caso residual (push o retarget entre el chequeo pre-publicación y la publicación) publica una vez para un estado viejo; la corrida siguiente lo corrige sola vía resumen edit-in-place + dedup (puntos 2 y 3). Resultado: máximo una review activa por PR y ninguna review perdida.
2. **Resumen:** un único comentario de resumen por PR, **editado in place** en cada corrida (`comment_id` persistido en `comments_sent`). Nunca un comentario nuevo por push. Si el comentario fue borrado a mano (404 al editar), se crea uno nuevo y se actualiza `comments_sent`. Al cerrar la fase 2 de la publicación (§6), el resumen se re-edita una vez más para incluir el recuento de findings por severidad — el de la fase 1 de la publicación es provisional por diseño (SLO de §6).
3. **Inline:** solo findings sobre líneas del diff actual. Dedup por huella de hallazgo (file + categoría + ancla) contra `comments_sent`: el ancla es el símbolo contenedor cuando hay AST (F4+) y, hasta entonces, la línea con tolerancia de drift (±3 líneas, config) — un push que corra líneas no republica el mismo hallazgo.
4. **Hallazgos desaparecidos** (línea corregida en el nuevo push) no se retiran: el diff lo muestra; el veredicto pre-merge se recalcula igual.
5. **Mapeo finding → posición:** el adapter VCS convierte `finding.line` en `CommentPosition{file, line, side}` reconciliando contra los hunks del diff unificado: las líneas nuevas anclan al lado RIGHT; los hallazgos sobre líneas eliminadas (un cleanup borrado, una validación quitada) anclan al lado LEFT del hunk. Si la línea quedó fuera del diff visible, el hallazgo baja al resumen. Este mapeo vive solo en `internal/vcs` y se testea con fixtures de diff reales, contexto colapsado incluido.

---

## 4. Reglas de Escritura de Código

### 4.1. Verificación previa y cero invención de APIs

1. **Se lee el código real antes de escribir una sola línea:** el archivo que se va a tocar, sus llamadores y los tipos con los que interactúa. Escribir contra lo que uno *cree* que existe es el defecto más caro de corregir después.
2. **Prohibido inventar funciones, métodos, parámetros, campos, flags, valores de error o claves de config** que no existan en el código o en la dependencia. Si la firma exacta no se conoce, se verifica antes de escribir: `go doc`, el fuente del módulo en el mod cache, o la documentación oficial de la **versión fijada** — nunca la memoria ni "debería existir".
3. **Desactualizado es igual a inexistente:** un símbolo que ya no está en la versión fijada en `go.mod`/`package.json` no se usa, aunque aparezca en tutoriales, issues viejos o en la memoria. La versión que manda es la fijada en el repo, no la última publicada.
4. Si un símbolo referenciado no aparece (búsqueda vacía), no se "corrige" adivinando la firma: se investiga el fuente real o se pregunta. El build y el lint del CI son la red de seguridad, no el método de verificación.

### 4.2. Cero hardcodeo

| Prohibido | Correcto |
|-----------|----------|
| `time.Sleep(90 * time.Second)` inline | `cfg.ReviewTimeout` desde env/config con default documentado |
| `"https://api.github.com/..."` en el código | constante en `config` o valor de BD |
| `"gpt-4o"` disperso en llamadas | rol del LLM Gateway (`review`, `cheap`, `embedding`) resuelto por config |
| Umbrales, límites, timeouts, puertos, paths | siempre `config/` con default explícito en `.env.example` |

Regla: **si un valor puede cambiar por entorno u operador, no vive en el código.** Excepciones: constantes de dominio genuinas (formatos de commit, nombres de evento) → constantes nombradas en mayúsculas junto a su uso.

### 4.3. Cero duplicación y código obsoleto

- Si un bloque aparece 2 veces, a la segunda se extrae (función, tipo o componente). Si aparece 1 vez "por las dudas", no existe (YAGNI).
- Al modificar un componente se **buscan y borran** las versiones anteriores que quedaron sin llamadores. `grep` del símbolo antes de cerrar el PR.
- Prohibido mantener código comentado "por si acaso": git tiene el historial.
- Un refactor nunca mezclado con un cambio funcional en el mismo PR.

### 4.4. Comentarios de código: qué hace, no historia

Los comentarios describen **el presente del código**:

```go
// BIEN: dice qué hace y por qué existe la decisión no obvia.
// Retry con backoff exponencial: la API de GitHub aplica
// secondary rate limits a la creación de comentarios.
func (c *Commenter) postWithBackoff(...) { ... }

// MAL: historial. Esto es labor del git log.
// Antes esto usaba REST, pero lo cambiamos a GraphQL
// porque en la v0.2 fallaba.
func (c *Commenter) postWithBackoff(...) { ... }

// MAL: redundante con el código.
// Incrementa i en 1
i++
```

Reglas:
1. Comentario en **español, tiempo presente**, voz activa.
2. Comenta el **qué** cuando el código no lo dice solo, y el **porqué** cuando hay una decisión no obvia (trabajo alrededor de un bug, límite externo, tradeoff).
3. **Nunca** referencias a PRs, issues, versiones, autores o cambios pasados en comentarios. Eso va al mensaje de commit.
4. Código que necesita un párrafo de comentario para entenderse, primero se intenta simplificar.
5. Todo endpoint, handler y job expone su contrato: doc-comment de una línea en Go (`// Handler de webhook de GitHub...`), JSDoc breve en TS solo en funciones exportadas de `lib/`.

### 4.5. Estilo Go (backend)

- `gofmt` + `golangci-lint` sin excepciones; CI bloquea si falla.
- Errores: siempre `if err != nil` con `return fmt.Errorf("...: %w", err)` — envuelto con contexto, jamás `panic` fuera de `main`.
- `context.Context` como primer parámetro en todo lo que hace I/O; timeouts siempre explícitos.
- Interfaces definidas donde se consumen, no donde se implementan; interfaz con un solo método, nombre `-er`.
- Concurrencia con errgroup; sin goroutines huérfanas (toda goroutine tiene cancelación o cierre documentado).
- Tablas de test para lógica con ramas; sin mocks donde alcanza un stub de 10 líneas.

### 4.6. Estilo TypeScript/Preact (dashboard)

- Componentes funcionales + hooks; estado de servidor siempre vía TanStack Query (sin `useEffect` + `fetch` manual).
- Tipos explícitos en fronteras (respuestas de API), inferencia dentro.
- Un componente por archivo; componentes de features NO importan de otras features — lo compartido baja a `components/` o `lib/`.
- Tailwind con tokens de `theme/` (§5); prohibido color hex inline fuera de `theme/`.

### 4.7. Idioma de artefactos

- Código, identificadores y comentarios: **español** para comentarios, **inglés** para identificadores (`PostInlineComment`, no `PublicarComentarioInline`) — el ecosistema y las librerías son en inglés.
- UI del dashboard: español.
- Commits: conventional commits en inglés o español consistente (`feat:`, `fix:`, `refactor:`...).

---

## 5. Diseño Visual — Identidad Crema + Verde (tema claro y oscuro)

Inspiración CodeRabbit: fondo crema cálido, verdes profundos, acento naranja. Alto contraste, sin ruido visual. **Dos temas: claro (crema) y oscuro (verde profundo)** — la identidad se mantiene, el valor de cada token cambia.

### 5.1. Tokens por rol (un valor por tema)

Los tokens se nombran por **rol**, no por color — así el tema cambia sin tocar componentes:

| Token | Claro | Oscuro | Uso |
|-------|-------|--------|-----|
| `bg.base` | `#FDF6EC` | `#0E1F16` | Fondo general |
| `bg.surface` | `#FAF0DF` | `#142B1D` | Cards, paneles, tablas |
| `bg.elevated` | `#FFFFFF` | `#1C3A28` | Sidebar, headers, modals |
| `text.primary` | `#16341F` | `#F2E8D8` | Texto principal (12.7:1 / 14.1:1) |
| `text.muted` | `#4A6B58` | `#A8C4B2` | Texto secundario (≥ 4.5:1 en ambos) |
| `action.primary` | `#2E6B4F` | `#8FBF9F` | Botones primarios, links activos |
| `action.primary.text` | `#FFFFFF` | `#0E1F16` | Texto sobre botón primario (6.3:1 / 8.3:1) |
| `border.subtle` | `#8FBF9F` | `#2E4A3A` | Bordes suaves, dividers — decorativo (§5.2) |
| `accent` | `#C2611A` | `#E8853D` | Focus rings, highlights, indicadores (≥ 3:1 no-texto también en claro; naranja quemado en claro — el naranja pleno no llega) |
| `severity.alta` | `#B3261E` | `#F87171` | Error / riesgo alto (≥ 4.5:1) |
| `severity.media` | `#92400E` | `#FBBF24` | Warning / riesgo medio (≥ 4.5:1 — amber-700 quedaba en 4.45:1 sobre crema; 800 da 6.3-6.6:1) |
| `severity.baja` | `#1E6B3C` | `#4ADE80` | Success / riesgo bajo (≥ 4.5:1) |

Implementación: variables CSS nativas en `theme/`, activas por atributo `data-theme` en `<html>`. Toggle manual (persistido en localStorage) + `prefers-color-scheme` como default inicial. Tailwind v4 referencia los tokens vía `@theme` — los componentes NUNCA conocen el tema activo.

### 5.2. Reglas de contraste y accesibilidad

1. **WCAG 2.1 AA en AMBOS temas**: texto normal ≥ 4.5:1, texto grande ≥ 3:1. Cada token de texto de la tabla ya cumple — un par nuevo se verifica antes de entrar. `border.subtle` es la excepción consciente: 1.93:1 sobre `bg.base` en claro y 1.55:1 sobre `bg.surface` en oscuro, debajo del 3:1 no-texto (WCAG 1.4.11) — por eso es decorativo (dividers); un componente interactivo en reposo no se identifica por ese borde solo: se apoya en label, placeholder o texto `text.muted`, y el focus ring naranja cubre el estado activo (regla 4).
2. `accent` (naranja) sobre `bg.base` claro NO se usa para texto de cuerpo (contraste insuficiente); sobre el tema oscuro sí se permite en énfasis.
3. Estados nunca comunicados solo con color: severidad = color + ícono + texto (`● Alta`).
4. Focus visible en todo elemento interactivo (ring naranja 2px, en ambos temas).
5. Tipografía: system stack (`-apple-system, Segoe UI, Roboto...`) — sin webfonts que penalizan carga. Escala 12/14/16/20/24, line-height 1.5.

### 5.3. Componentes base del dashboard

`Button` (primary/secondary/ghost), `Badge` (severidad/estado), `Table` (cola triage), `Card` (PR detail), `DiffViewer` (diff con comentarios), `Modal`, `Toast`. Nada más hasta que la necesidad exista.

---

## 6. Fases del Proyecto

Cada fase termina con **demo runnable** y su checklist de aceptación completa. Las duraciones asumen ~15 h/semana; lo vinculante es el entregable, no la semana.

**SLO de latencia (vinculante desde F1):** resumen publicado en el PR en menos de 5 minutos (P95) desde la recepción del webhook; comentarios inline completos en menos de 15 minutos (P95). Cumplir ambos exige **publicación en dos fases**: el Summarizer (rol `review` — es el artefacto visible del producto) corre al inicio de la corrida — no depende de los findings, solo del diff — y su resumen se publica apenas listo; los inline salen al cerrar el análisis per-archivo. Publicar todo junto al final viola el SLO del resumen por construcción. Este target dimensiona la concurrencia (§9.6) y la elección de proveedores por rol: un proveedor que no lo cumple no es candidato a rol `review`. Se mide desde `webhook_deliveries` hasta `comments_sent` — cada fila registra su tipo (`inline`/`summary`/`chat`) y cada SLO se mide sobre sus propias filas.

### Fase 0 — Fundación (semanas 1-2)

**Entregable:** monorepo corriendo: API con auth, dashboard con login, servicios base completos.

- [ ] Estructura de monorepo (§3.2) coincidente con `docs/mapa_arquitectura.yaml` — coincidente = cada componente listado en el mapa existe exactamente en la ruta que el mapa declara (el mapa es la autoridad estructural de F0; lo del árbol de §3.2 que el mapa no lista — README, LICENSE, justfile, docs/ — no lo contradice); `just dev` levanta todo (Quadlet en self-host, compose.dev en desarrollo)
- [ ] API: healthcheck, login por sesión, migraciones aplicadas
- [ ] Dashboard: shell con login y navegación vacía, tokens de tema claro/oscuro aplicados con toggle persistido
- [ ] CI (GitHub Actions, reutilizando recetas del `justfile`): lint + build + test en backend y dashboard
- [ ] `.env.example` completo y documentado
- [ ] `deploy/backup.md`: runbook de dump diario con restore verificado y respaldo de la master key (§9.2, §9.10)

### Fase 1 — Pipeline de revisión GitHub (semanas 3-5)

**Entregable:** abrir un PR de prueba en GitHub → resumen + comentarios inline publicados.

- [ ] GitHub App: webhook firmado, tokens de instalación, idempotencia; private key y secret de la App solo en env; permisos mínimos documentados: Contents: Read; Pull requests: Read & write; Issues: Read & write; Metadata: Read (obligatorio)
- [ ] Job `ReviewJob`: clon shallow (merge-base con `--deepen`), parseo de diff, skip de corridas stale (§3.6)
- [ ] Jobs de operación: `CleanupJob` (retención diaria, §9.11) y `RotationJob` (re-cifrado al rotar la master key, §9.2 — acción de settings, admin)
- [ ] Analyzer sandbox: detección de lenguaje + 5 linters (best-effort según dependencias, §9.4), JSON normalizado con reporte de qué corrió
- [ ] Agente Reviewer (rol `review`) sobre hunks con contexto de archivo
- [ ] Publicación en dos fases (§6): resumen apenas el Summarizer termina, inline al cerrar el análisis — comentarios en el idioma default de config (español; por repo desde F3 vía `review.yaml`); registro en `findings`/`comments_sent`
- [ ] Dashboard: panel de estado de la cola (pending/running/discarded por tipo de job — `features/queue`, §3.2)
- [ ] Dashboard: CRUD de proveedores LLM (API keys cifradas AES-GCM) con acción de prueba de conexión; repos conectados
  - prueba de conexión — una llamada mínima por rol vía gateway, único uso de `internal/llm` en la API; los handlers de webhook jamás lo tocan (§3.5)
  - repos conectados — conectar es registrar el repo en settings (GitHub `owner/repo`; GitLab project path + su signing token) antes de instalar el webhook: el de un repo no registrado se descarta (§3.5)
  - la instalación en el VCS es manual del operador — GitHub: instalar la App en el repo; GitLab: crear el webhook del proyecto apuntando a `https://<host>/webhooks/gitlab` generando su signing token, y registrar en el proyecto una deploy key de solo lectura cuya privada se guarda en settings (§3.3 — el clonado del MR la necesita)
  - el sistema no escribe configuración del VCS
- [ ] Desconexión/reconexión de repos: flag `enabled`, descarte de jobs pendientes, `ReconcileJob` (ListOpenPRs) y re-indexación completa (§3.5) — la re-indexación completa se materializa en F4: en F1 la reconexión solo dispara el `ReconcileJob`
- [ ] Dashboard: gestión de usuarios — invitación de miembros, reset de contraseña y desactivación por el admin (§3.4)
- [ ] Tests de integración del pipeline con webhook simulado

### Fase 2 — GitLab + Chat + Sugerencias (semanas 6-8)

**Entregable:** mismo pipeline en GitLab; charlar con el bot; aplicar sugerencias con 1 clic. La herramienta es usable en el día a día desde acá — la mejora de contexto simbólico viene después (F4).

- [ ] Adapter GitLab cumpliendo el contrato `VCSProvider`
- [ ] Sugerencias aplicables (GitHub suggestion blocks / GitLab suggested changes)
- [ ] Agente Chat (rol `review` del gateway): respuestas a comentarios `@bot`
  - `/review`: re-encola la corrida con la identidad actual del PR — misma unicidad de §3.6; sobre un PR cerrado no encola y responde que está cerrado: una corrida sobre un PR cerrado terminaría `stale` quemando LLM (§3.6)
  - `/tests` y `/explain`: responden en el hilo; lista cerrada (§9.5)
  - los demás comandos responden igual sobre un PR cerrado — el hilo sigue vivo, solo `/review` se rehúsa
  - anti-bucle: comentarios del propio bot y de otros bots se ignoran (§3.5)
  - corre como `ChatJob` asíncrono — el webhook responde 2xx inmediato y jamás llama LLM en el handler (§3.5)
  - presupuesto propio por comando (§9.6)
  - responde en el idioma configurado del repo (§3.3)
  - respuesta P95 < 2 min (orientativo)
- [ ] Generación de pruebas unitarias con framework detectado del proyecto

### Fase 3 — Verificación y Configuración por Repo (semanas 9-10)

**Entregable:** falsos positivos filtrados, Mermaid en el resumen, revisión configurable por repo.

- [ ] Agente Verifier (rol `cheap` — cross-check mecánico contra evidencia determinista): findings LLM vs SAST/AST → marca `verified`; los falsos positivos confirmados no se publican — quedan en `findings` con `verified=false`, auditables en el dashboard
- [ ] Resumen con walkthrough + diagrama de secuencia Mermaid
- [ ] `review.yaml` por repo: `path_filters`, `instructions`, `profile` (chill: solo hallazgos alta/media; assertive: + baja y estilo; strict: + nits — regula cuánto comenta el bot, jamás un veredicto de bloqueo, §1.1), idioma — se lee **solo de la rama base** del PR; los cambios al archivo dentro del diff se ignoran (§9.5)
- [ ] Dashboard: detalle de PR con findings y diff (`features/prs` + `DiffViewer`, §5.3 — su única consumidora)

### Fase 4 — RAG: Indexación y Contexto Simbólico (semanas 11-12)

**Entregable:** el Reviewer recibe contexto simbólico del repo (símbolos relacionados vía pgvector + grafo de imports) en lugar de solo el diff. Es mejora de calidad de revisión, no prerequisito de nada — por eso va después de que la herramienta está en uso diario.

- [ ] Job `IndexJob`: chunking simbólico vía modo `--symbols` del CLI analyzer (único punto de acceso a tree-sitter, §3.2 — `internal/index` consume `internal/analyze`; cgo no sale del contenedor), embeddings a pgvector
- [ ] Grafo de imports; retrieval de símbolos relacionados para el Reviewer
- [ ] Re-indexación incremental: `IndexJob` (job único por repo mientras vive otro, misma mecánica de unicidad que `ReviewJob`) se encola al completar cada review (success o partial — ambas dejaron código revisado; stale y failed no), al conectar el repo y al fusionar el PR (merge) — la unicidad de `ReviewJob` ya debouncea los push seguidos; indexa la rama por defecto del repo en su estado al ejecutar (clon propio del job vía `FetchDefaultBranch` del contrato `VCSProvider`), solo código, respetando los `path_filters` vigentes
- [ ] Anclas de dedup por símbolo contenedor — reemplazan la tolerancia de drift de §3.6

### Fase 5 — Triage + Pre-merge (semanas 13-15)

**Entregable:** cola de triage por riesgo en dashboard; veredicto pre-merge; métricas.

- [ ] Risk scoring: tamaño de diff, archivos sensibles (patrones configurables — p. ej. auth, secrets, migraciones, CI), severidad de findings, proporción de archivos de test tocados en el diff — proxy computable; nada en el sistema ejecuta tests ni mide cobertura real
- [ ] Dashboard triage: PRs ordenados por riesgo, filtros por severidad/repo
- [ ] Agente Pre-merge (rol `review`): veredicto final con checklist, publicado como sección del comentario de resumen (edit in place de §3.6 — `comments_sent` no gana tipo nuevo)
- [ ] Métricas: cycle time, findings aceptados, tasa de falsos positivos, costo LLM por review
  - findings aceptados (sugerencias aplicadas vía 1-click) — ningún VCS notifica el apply: se detecta comparando el bloque sugerido contra los commits posteriores del PR, heurística por contenido
  - tasa de falsos positivos: inline comments resueltos sin aplicar, leídos del estado de conversación del VCS
  - las métricas que leen el VCS se calculan una sola vez al cierre del PR: `MetricsJob` encolado por el handler de cierre (§3.5), sin LLM, input vía `FetchPRTimeline` del adapter, outcome persistido sobre las filas existentes de `findings`/`comments_sent` — sin tablas nuevas
  - cycle time y costo salen directo de las tablas

---

## 7. Estructura del Código para Revisión Fácil

1. **Diff pequeño**: PR < 400 líneas; si crece, se divide en PRs encadenados.
2. **Una responsabilidad por PR**: feature O refactor O migración, nunca dos.
3. **Nombre de archivos predecibles**: en Go, `github.go` contiene el adapter de GitHub y nada más; test en `github_test.go` al lado.
4. **Señales de revisión**: todo handler/job/agent expone en su firma qué entra y qué sale; nada de `map[string]any` cruzando fronteras de paquete.
5. **Orden de lectura del PR**: tests primero (qué se espera), interfaz después (qué expone), implementación al final.

---

## 8. Pruebas

| Nivel | Qué cubre | Herramienta |
|-------|-----------|-------------|
| Unitario | lógica con ramas: parsing de diff, risk scoring, normalización, heurísticas de métricas (sugerencia aplicada) | `testing` stdlib, tablas de test |
| Integración | pipeline completo con webhook simulado + Postgres real | testcontainers (compatible Podman) |
| E2E | PR de humo en repo fixture → comentarios publicados (mock del VCS API) | suite de smoke en CI |
| Evals de prompts | fixtures de diff reales → hallazgos esperados contra proveedor real | suite fuera de CI (rol de producción, default `review`) |
| Dashboard | componentes críticos (DiffViewer, triage table) | Vitest + Testing Library |

Regla: **todo bug fix llega con el test que lo habría atrapado.** El testing que involucra LLM tiene dos capas separadas:

- **CI (cada push, determinista):** el gateway se apunta a un stub OpenAI-compatible; se cubren validación de schema de salida, parsing, dedup, reintentos/failover del gateway y el plumbing del pipeline. Cero llamadas a proveedores reales: el CI no puede ser flaky ni quemar tokens en cada run.
- **Evals de prompts (fuera de CI):** fixtures de diff reales (entrada → hallazgos esperados) contra el proveedor del mismo rol que producción (default `review`, configurable — evaluar un prompt con un modelo distinto del que reseña mide el drift equivocado), corridas manuales o nocturnas; el resultado queda registrado para detectar drift al cambiar un prompt. Un eval fallido no rompe el build — dispara ajuste del prompt y re-eval.

El mapeo finding→posición de comentario (§3.6) se testea igual: fixtures de hunks reales, incluyendo líneas que quedaron fuera del diff visible. El skip de corridas stale (incluido el re-encolo transaccional de §3.6) y el anti-bucle del chat (§3.5) también: fixture de webhook con head viejo o comentario del bot → cero llamadas LLM, cero publicaciones.

---

## 9. Seguridad y Operación

> **Regla rectora:** todo lo que llega del VCS — diff, contenido del repo, comentarios, configs del repo revisado — es hostil hasta que se verifica. Todo lo que sale de un LLM es dato hasta que se valida contra schema. La seguridad no es esta sección: es el orden de los párrafos que siguen. Una feature nueva declara dónde entra en el mapa de §9.1 o no se aprueba.

### 9.1. Modelo de amenazas y frontera de confianza

| Activo | Amenaza | Mitigación |
|--------|---------|------------|
| Secrets (API keys LLM, deploy keys, private key de la App, master key) | Robo vía BD, logs o `systemctl show` | Cifrado en reposo + `LoadCredential` (§9.2) |
| Webhooks | Spoofing y re-entrega duplicada | Firma HMAC/token + idempotencia (§9.3) |
| Worker y analyzer | Código arbitrario del repo revisado — un PR forkeado es territorio hostil por definición | Sandbox sin red (§9.4) |
| Prompts y agentes | Inyección vía diff, comentarios o `review.yaml` | Contenido = dato, lista cerrada de acciones (§9.5) |
| Presupuesto LLM | Gasto descontrolado por ráfagas, bucles o PRs enormes | Budgets, topes y cache (§9.6) |
| Código del repo revisado | Salida de IP hacia proveedores LLM externos | BYOK: el operador elige cada destino (§3.3); Ollama local soportado (§2); sin entrenamiento de modelos (§1.1) |
| Dashboard | Brute-force de login, CSRF, robo de sesión | §3.4 |
| Datos del sistema | Pérdida irrecuperable — sin master key no hay descifrado | Backups verificados (§9.10) |

Entrada **no confiable** por diseño: diff y archivos del repo, `review.yaml`, configs del repo, comentarios del VCS, salidas de LLM. Confiables solo: los operadores del dashboard y los secrets del deploy. No existe tercera categoría.

### 9.2. Secretos y cifrado

API keys LLM cifradas en reposo (AES-256-GCM, master key en env, nunca en BD plana ni logs). La master key tiene backup documentado en `deploy/` — sin ella, todo lo cifrado es irrecuperable.

**Rotación de la master key** (por `RotationJob` de re-cifrado de las tablas cifradas — bajo demanda, encolado desde el dashboard, settings, admin; corre con la cola drenada — single worker: espera a que terminen los jobs en vuelo, para que ningún descifrado agarre la clave a mitad de cambio):

1. Transición dual: `MASTER_KEY` (nueva) + `MASTER_KEY_PREVIOUS` (vieja) en las unidades `api` y `worker`, reinicio de ambas; mientras las dos vivan, la API cifra con la nueva y ambas leen la vieja.
2. Recién entonces se encola el `RotationJob`. El job se rehúsa a arrancar sin la previa — re-cifrar lo que no se puede descifrar no es rotación.
3. Terminado el job, `_PREVIOUS` se retira del env.

**Inyección de secretos:**

- En el deploy Quadlet, los secretos de las unidades `api` y `worker` se inyectan con `LoadCredential=` — nunca con `Environment=`: `systemctl show` expone el Environment de una unidad. Ambas necesitan la master key (la API cifra al guardar credenciales; el worker descifra al usarlas); el webhook secret de la App vive en la unidad `api`; la private key de la App (mint de installation tokens para clonar) vive en la unidad `worker`.
- La contraseña de Postgres sigue el mismo criterio: `LoadCredential=` + `POSTGRES_PASSWORD_FILE` en la unidad `postgres` — `systemctl show` expone el `Environment` de cualquier unidad, no solo de api y worker.
- `config` acepta cada secreto como valor directo o como ruta `_FILE` (p. ej. `GITHUB_APP_PRIVATE_KEY_FILE=/run/credentials/worker/...`); en Quadlet se usa siempre la variante `_FILE`.

### 9.3. Webhooks: firma e idempotencia

Webhooks verificados por firma + idempotencia por delivery ID.

- **GitHub:** HMAC-SHA256 sobre el body crudo (`X-Hub-Signature-256`) y delivery GUID (`X-GitHub-Delivery`).
- **GitLab** (signing token, GA desde 19.1): HMAC-SHA256 en formato Standard Webhooks — la firma se computa sobre `webhook-id.webhook-timestamp.body` con la clave del signing token, que llega con prefijo `whsec_`: se quita el prefijo y se base64-decodea para obtener la clave cruda del HMAC. El header `webhook-signature` trae una o más firmas `v1,{base64}` separadas por espacio; la verificación es en tiempo constante contra cada una, con chequeo de frescura del timestamp contra replay.
- **GitLab legacy** (secret token plano, `X-Gitlab-Token`): queda como fallback solo para instancias self-managed viejas — GitLab no lo recomienda para webhooks nuevos.
- **Idempotencia por delivery ID:** toda entrega de GitLab lleva `webhook-id`, que es el delivery ID de la dedup; en self-managed anteriores a 19.0 — donde `webhook-id` no existe — el header `Idempotency-Key`, presente desde 17.4, porta el mismo valor y hace de delivery ID de la dedup.
- **Topes de tamaño:** por encima de 25 MB GitHub ni siquiera entrega la solicitud; el chequeo de tamaño aplica de facto a GitLab —que no publica un tope comparable— y a cualquier tope propio (config) más bajo que el del proveedor; en esos casos el payload se rechaza antes de parsear — el tope propio vale igual para ambos proveedores.

El handler procesa en orden de costo — tope de tamaño → firma → idempotencia por delivery ID → parseo → filtro de evento — y nada toca la cola ni la BD antes de pasar la firma.

### 9.4. Sandbox del analyzer

`--network=none --read-only --pids-limit --memory` (tope de RAM, config — un linter compilando un repo enorme no tumba el VPS), sin privilegios, timeout duro. El rootfs queda read-only: los caches y artefactos de compilación de los linters (`target/` de Clippy, `GOCACHE` de golangci-lint) se escriben en el workdir montado o en un tmpfs propio con tope de tamaño (config) — sin espacio escribible, los linters que compilan no corren ni con las deps disponibles. Las versiones de tree-sitter y de cada linter se fijan (pinning) en el Containerfile: la salida del analyzer debe ser reproducible entre rebuilds de la imagen. Los linters corren con **configs propios de la imagen, pasados explícitamente por el CLI** — los configs del repo revisado se ignoran: un config de repo es código arbitrario ejecutándose en el sandbox (un `eslint.config.js` es JS ejecutable) y además permite silenciar reglas — un repo malicioso no apaga su propia detección. Configs fijos es también condición de la salida reproducible.

**Linters best-effort según dependencias:** Clippy y golangci-lint necesitan compilar contra dependencias que el sandbox sin red no puede descargar (y casi ningún repo las vendorea). Reglas: tree-sitter corre siempre; los linters sin grafo de dependencias (Ruff, ESLint, Gitleaks) corren siempre; los que necesitan deps corren solo si las deps están disponibles en el clon y se saltan si no. **La salida reporta qué linter corrió y cuál no pudo** — la ausencia de un linter jamás se interpreta como ausencia de hallazgos, ni por el Verifier ni por el resumen. La normalización de la salida mapea la severidad propia de cada linter a los niveles del conjunto cerrado de `findings.severity` (§3.3). Gitleaks sobre clon shallow solo ve la historia clonada: detección de secrets limitada al tramo traído — tradeoff aceptado y documentado, no una promesa de escaneo histórico.

El cuerpo de un hallazgo de secrets nunca incluye el valor detectado: se publica enmascarado (ruta, línea y regla). Publicar el secreto en un comentario del PR lo duplicaría en un lugar público — el valor queda solo en la evidencia interna. El enmascarado vive en el publicador y aplica a todo comentario — inline, resumen y chat, venga de Gitleaks o de un agente LLM: un Reviewer puede citar un secret del diff igual que un linter puede detectarlo.

### 9.5. Prompts, inyección y contenido no confiable

- El diff, los comentarios del PR y todo contenido del repo son entrada no confiable. Los system prompts tratan el contenido del PR como datos, nunca como instrucciones; la salida del LLM no es confianza — no se auto-aplican sugerencias ni se ejecutan acciones fuera de la lista cerrada de comandos del chat (§6 F2).
- **El dashboard renderiza contenido externo como texto, siempre.** Findings, cuerpos de comentarios y diffs son dato del LLM/VCS: escaping nativo de Preact, jamás HTML crudo ni `innerHTML`. Mermaid lo renderiza el VCS en el PR; si algún día el dashboard lo renderiza client-side, va con `securityLevel=strict` — un diagrama salido de un LLM es entrada no confiable, no decoración. La API entrega el dashboard con CSP restrictiva (`default-src 'self'`, sin scripts inline): si el escaping falla una vez, el CSP limita el radio del fallo.
- **`review.yaml` se lee solo de la rama base del PR.** Sus `instructions` entran en los prompts: leerlas del head del PR sería inyección por diseño — un PR malicioso escribiría sus propias reglas de revisión. Los cambios a `review.yaml` dentro del diff se ignoran para la corrida en curso.
- Prompts versionados en `backend/prompts/`: los prompts son código — cambio de prompt = PR con justificación, igual que cualquier cambio.
- **Precedencia de configuración:** el dashboard define defaults por repo; `review.yaml` (rama base) especializa y gana cuando ambos definen lo mismo — lo operativo (conexión, secrets, enabled) es siempre del dashboard. Un `review.yaml` inválido se ignora con registro y la corrida sigue con defaults: jamás rompe el job.

### 9.6. Presupuesto y límites LLM

Todos los topes de esta sección son config. La regla común: nada se recorta en silencio — todo recorte declara la corrida `partial` (§3.3) y se publica en el resumen (§9.7).

- **Presupuesto por review** (máx tokens): agotado, los archivos restantes se analizan solo con SAST y la review queda `partial`.
- **Presupuestos por operación:** el chat tiene presupuesto propio por comando; la indexación consume el rol `embedding` con tope propio por `IndexJob` — agotado, la indexación queda parcial y se retoma en el próximo `IndexJob`: la revisión nunca se degrada por indexar.
- **Cache de resultados** por **head SHA + merge-base + versión del prompt + hash de la config efectiva** (instructions/profile/path_filters/idioma de `review.yaml` + defaults del repo del dashboard — dos configs distintas son corridas distintas: prompt distinto o selección de archivos distinta): un PR que retargetea la base mantiene el head SHA pero cambia el diff, y cambiar un prompt o la config efectiva sin invalidar la cache sirve hallazgos calculados con las reglas viejas. Vive en memoria del worker, con tope de entradas y TTL — single worker por diseño: los topes de esta sección y esta cache asumen un solo proceso worker; no justifica tabla ni servicio, se pierde al reiniciar y un miss solo recomputa.
- **Concurrencia:** tope de llamadas LLM simultáneas por review (default 4) y tope global en el gateway (default 8) — una ráfaga de PRs simultáneos no dispara rate limits del proveedor; tope de `ReviewJob`s concurrentes en el worker (default 2) — dimensiona el SLO de §6 junto con los dos anteriores. El `ChatJob` corre con slots propios (default 2, config): su SLO de respuesta (§6 F2) no espera detrás de reviews largas; el `IndexJob` comparte los slots de review — dos corridas largas simultáneas ya acotan la RAM del worker. Cada llamada registra su usage en `llm_usage` (tokens in/out por modelo y rol): sin ese registro no hay métrica de costo en F5.
- **Topes de contenido:** límite de tamaño de diff por encima del cual el PR recibe solo resumen, sin inline ni per-file — esa corrida termina `partial` (§3.3): solo-resumen es un resultado incompleto, nunca un éxito silencioso. Tope análogo por archivo: el archivo que lo excede se procesa solo con SAST y la corrida también termina `partial` — un solo archivo patológico no desata overflow de contexto ni quema los reintentos de la corrida.
- **Declaración de cobertura:** la re-edición de fase 2 de la publicación (§6) declara cobertura parcial y su motivo — presupuesto agotado, diff o archivo sobre el tope, o failover agotado — igual que `failed` re-edita su estado final (§9.7): el estado incompleto también se publica, no solo se registra. Dashboard de consumo en settings.
- **Guardas de rol:** conectar o re-habilitar un repo exige al menos un proveedor `enabled` en el rol `review` — settings lo bloquea con mensaje claro: sin ese guard, cada PR del repo quemaría una corrida entera de reintentos hasta `failed`. Los roles `cheap` y `embedding` no bloquean: degradan. Sin proveedor `cheap`, el Verifier no corre — los findings LLM se publican con `verified` null (§3.3) y la re-edición de fase 2 de la publicación lo declara, igual que cualquier cobertura parcial. Sin proveedor `embedding`, el `IndexJob` no se encola (log estructurado): la indexación queda latente hasta configurar el rol — encolarla igual quemaría reintentos en cada review completada.

### 9.7. Reintentos, rate limits y failover

Jobs River con máximo de intentos y backoff exponencial (config); un job que agota intentos queda visible como `discarded` en el dashboard — nunca silenciado. Su corrida queda `failed` (§3.3) y el resumen del PR se re-edita una vez, mejor esfuerzo, con el estado final: un resumen provisional de la fase 1 de la publicación jamás queda como último estado visible. El poster del VCS respeta `Retry-After` y aplica backoff ante secondary rate limits de GitHub antes de reintentar. El gateway LLM aplica el mismo criterio ante el proveedor: timeout por llamada (config), backoff ante 429/5xx (respeta `Retry-After` cuando existe) con reintentos acotados (config); agotados, conmuta al siguiente proveedor `enabled` del mismo rol (orden `priority`); si ninguno responde, la review se marca `partial` (§9.6) — nunca se recorta en silencio.

### 9.8. Validación de salida de agentes

Todo agente responde contra un schema JSON (contrato de finding/resumen); la salida malformada se reintenta un máximo configurable de veces y luego se descarta registrándolo — nunca crashea el job. El `category` y la `severity` de todo finding se validan contra los conjuntos cerrados de §3.3. El diagrama Mermaid del resumen se parsea antes de publicar; si no parsea, se omite y se registra.

### 9.9. Logs y observabilidad

Logs estructurados (JSON): request ID propagado de webhook a job a llamada LLM. Prohibido loguear secrets, diffs completos o API keys. El dashboard expone el estado de la cola (pending/running/discarded por tipo de job), la última ejecución de cada uno y el P95 de los SLO de §6 por tipo de comentario (summary/inline/chat) — así, revisar el backlog, un job caído o un SLO incumplido no requiere `psql`.

### 9.10. Backups

Dump diario de Postgres documentado en `deploy/`, con restore verificado mensualmente contra una instancia efímera — un dump jamás restaurado no es backup. La master key (§9.2) se respalda por separado y fuera del dump: sin ella el dump es indecifrable para las columnas cifradas.

### 9.11. Retención y limpieza

Job diario de limpieza (`CleanupJob`) de `webhook_deliveries` (default 30 días, config — esa retención acota la ventana de cálculo del P95 de §6 a la misma ventana), sesiones expiradas de `sessions` y workdirs huérfanos `/var/tmp/rev-*` con más de N días (config) — un worker crasheado a mitad de job no debe llenar el disco. `findings`, `comments_sent` y `llm_usage` se conservan — son el histórico de dedup y la base de las métricas F5; el volumen single-org lo permite.

### 9.12. Apagado ordenado del worker

En SIGTERM el worker deja de tomar jobs nuevos, termina el job en curso dentro de una ventana de gracia (config) y cierra limpio; pasada la ventana, el job vuelve a la cola por el mecanismo de reintentos (§9.7). Las publicaciones al VCS son idempotentes: el resumen edita in place por `comment_id` persistido, y el publicador de inline reconcilia antes de reintentar — chequea `comments_sent` y los comentarios ya publicados por el bot en el PR, porque un corte entre publicar y registrar deja el comentario vivo sin fila. La respuesta de chat reconcilia igual contra el comentario padre: si `comments_sent` ya registra la respuesta a ese comentario, no se republica. Un corte a mitad de publicación no duplica comentarios en la reintentada. La unidad `worker.service` fija `TimeoutStopSec` (ventana de gracia, config) en consecuencia.

### 9.13. Deploy y actualización

Deploy al VPS = receta replicable (`just deploy`), nunca pasos recordados: construye los binarios, sube binarios y fuente del analyzer, reconstruye la imagen del sandbox en el VPS con la unidad `analyzer.build` (la imagen no viaja preconstruida), aplica migraciones y reinicia `api` y `worker` — Caddy y Postgres no se reinician en un deploy normal. Orden seguro: la migración corre primero y solo expande (additive), los binarios viejos siguen funcionando hasta el restart. Como las migraciones son forward-only (§3.3), el rollback es redeployar el binario anterior — toda migración se escribe compatible con el binario en producción: expandir primero, contraer recién cuando ningún binario viejo corre, en deploys separados.

---

## 10. Checklist de Revisión por Componente

Aplicar a cada PR antes de aprobar (self-review incluida):

- [ ] ¿Sin hardcodeo? (valores de entorno/config/BD fuera del código)
- [ ] ¿Sin duplicación? (bloques repetidos extraídos; código reemplazado borrado y sin llamadores)
- [ ] ¿Comentarios en presente, describiendo qué hace, sin historial?
- [ ] ¿El componente encaja en su paquete/feature y respeta las fronteras (§3.2)?
- [ ] ¿Dependencias nuevas justificadas y ligeras?
- [ ] ¿Errores manejados con contexto, sin `panic`, sin ignorar `err`?
- [ ] ¿Test del comportamiento nuevo o corregido incluido?
- [ ] ¿UI usa solo tokens de tema (claro y oscuro) y cumple contraste AA?
- [ ] Si cambió la estructura de componentes, ¿`docs/mapa_arquitectura.yaml` se actualizó en el mismo PR?
- [ ] ¿CI verde (lint, build, test)?
- [ ] ¿La demo del hito sigue funcionando de punta a punta?

---

## 11. Siguiente Paso

Iniciar **Fase 0**: crear la estructura del monorepo, el `justfile`, los servicios base con Quadlet/compose.dev (Postgres 18 con pgvector), el esqueleto de API con healthcheck y auth, y el shell del dashboard con los tokens visuales aplicados.
