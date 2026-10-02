-- Tablas núcleo de F1 (guía §3.3): proveedores LLM, repos conectados, PRs
-- observados, corridas de review, hallazgos, comentarios publicados, dedup de
-- webhooks y uso de LLM. Una sola migración forward-only: todas las tablas,
-- constraints e índices del modelo núcleo (users/sessions entran por 0001 y el
-- schema de River por 0002). Las columnas de secretos (api_key, api_token,
-- deploy_key) guardan texto cifrado AES-256-GCM en base64 (§9.2); los helpers
-- de cifrado viven en internal/store/crypto.go.

-- Proveedores LLM (guía §3.3): failover por rol — priority ordena dentro del
-- rol; empate rompe por id (orden estable).
CREATE TABLE llm_providers (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    base_url   TEXT        NOT NULL,
    model      TEXT        NOT NULL,
    api_key    TEXT        NOT NULL, -- cifrada AES-256-GCM (§9.2)
    role       TEXT        NOT NULL CHECK (role IN ('review', 'cheap', 'embedding')),
    priority   INT         NOT NULL,
    enabled    BOOLEAN     NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Repos conectados (guía §3.3): la desconexión es un flag (enabled), jamás
-- delete — el histórico queda intacto (§3.5). Las columnas de secretos GitLab
-- son nullable: GitHub usa el secret global de la App desde env (§9.3).
CREATE TABLE repositories (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    vcs            TEXT        NOT NULL CHECK (vcs IN ('github', 'gitlab')),
    external_id    BIGINT      NOT NULL, -- ID numérico del repo en el VCS
    owner          TEXT        NOT NULL,
    name           TEXT        NOT NULL,
    -- Solo GitLab: signing token del proyecto (§9.3).
    webhook_secret TEXT,
    -- Solo GitLab: token alternativo en self-managed anteriores a 19.0 (o con
    -- el flag webhook_signing_token deshabilitado), donde el signing token no
    -- existe.
    secret_token   TEXT,
    -- Solo GitLab: token de proyecto, cifrado (§9.2) — autentica las llamadas
    -- REST del adapter (comentarios, ListOpenPRs, timeline, GetDiff).
    api_token      TEXT,
    -- Solo GitLab: URL de instancia self-managed; vacío = gitlab.com.
    base_url       TEXT,
    -- Solo GitLab: deploy key de clonado SSH de solo lectura, cifrada (§9.2) —
    -- solo clona, no sirve para la API.
    deploy_key     TEXT,
    enabled        BOOLEAN     NOT NULL DEFAULT true,
    review_drafts  BOOLEAN     NOT NULL DEFAULT false, -- revisar drafts (§3.5)
    language       TEXT        NOT NULL DEFAULT 'es',
    chat_org_only  BOOLEAN     NOT NULL DEFAULT true,  -- chat solo-org (§3.5)
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (vcs, external_id)
);

-- PRs observados (guía §3.3): clave natural repo + número/iid en el VCS — el
-- upsert por webhook es idempotente. El merge es un close con merged_at.
CREATE TABLE pull_requests (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repository_id BIGINT      NOT NULL REFERENCES repositories (id),
    number        BIGINT      NOT NULL, -- número/iid del PR en el VCS
    author        TEXT        NOT NULL,
    state         TEXT        NOT NULL CHECK (state IN ('open', 'closed')),
    head_sha      TEXT        NOT NULL,
    base_ref      TEXT        NOT NULL,
    base_sha      TEXT        NOT NULL,
    merged_at     TIMESTAMPTZ,
    -- Fecha del PR en el VCS (no del registro local): con merged_at es insumo
    -- del cycle time de F5 (§3.3).
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repository_id, number)
);

-- Reviews (guía §3.3): una por corrida. head_sha + base_sha son la identidad
-- de la corrida (§3.6): la review vale para ese par de SHAs; un push o
-- retarget la vuelve stale. Los textos nacen vacíos y se completan al final.
CREATE TABLE reviews (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pull_request_id BIGINT      NOT NULL REFERENCES pull_requests (id),
    head_sha        TEXT        NOT NULL,
    base_sha        TEXT        NOT NULL,
    summary         TEXT        NOT NULL DEFAULT '',
    walkthrough     TEXT        NOT NULL DEFAULT '',
    mermaid         TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'success', 'partial', 'stale', 'failed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Hallazgos (guía §3.3): FK a la corrida que los produjo — dedup, métricas de
-- F5 y auditoría leen la atribución por corrida. verified queda null hasta que
-- el Verifier corre (F3); los SAST son deterministas y se publican sin
-- verificación. severity y category son conjuntos cerrados (§3.3): tocarlos
-- exige actualizar la guía y el schema de validación de agentes (§9.8).
CREATE TABLE findings (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    review_id  BIGINT      NOT NULL REFERENCES reviews (id),
    file       TEXT        NOT NULL,
    line       INT         NOT NULL,
    severity   TEXT        NOT NULL CHECK (severity IN ('high', 'medium', 'low')),
    category   TEXT        NOT NULL CHECK (category IN ('security', 'logic', 'performance', 'style', 'tests', 'other')),
    body       TEXT        NOT NULL,
    suggestion TEXT,
    source     TEXT        NOT NULL CHECK (source IN ('llm', 'sast')),
    verified   BOOLEAN,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Comentarios publicados por PR (guía §3.3): comment_id externo por finding
-- inline, resumen o respuesta de chat → idempotencia y actualización. anchor
-- es la huella de dedup, solo filas inline (§3.6); parent_comment_id ancla las
-- respuestas de chat para que un reintento del ChatJob no duplique (§9.12).
CREATE TABLE comments_sent (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pull_request_id   BIGINT      NOT NULL REFERENCES pull_requests (id),
    review_id         BIGINT      REFERENCES reviews (id),
    comment_id        TEXT        NOT NULL, -- ID externo del comentario en el VCS
    type              TEXT        NOT NULL CHECK (type IN ('inline', 'summary', 'chat')),
    file              TEXT,
    category          TEXT,
    anchor            TEXT,
    parent_comment_id TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Dedup de webhooks (guía §3.3): delivery IDs ya procesados, únicos por
-- (vcs, delivery_id) — los IDs de ambos proveedores no comparten espacio. El
-- created_at abre la medición del SLO de §6 y alimenta la retención de §9.11.
CREATE TABLE webhook_deliveries (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    vcs         TEXT        NOT NULL,
    delivery_id TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (vcs, delivery_id)
);

-- Uso de LLM (guía §3.3): una fila por llamada — base de las métricas F5 y de
-- los presupuestos (§9.6). job_id null en la prueba de conexión de settings:
-- no hay job que la respalde.
CREATE TABLE llm_usage (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id     BIGINT,
    role       TEXT        NOT NULL,
    provider   TEXT        NOT NULL,
    model      TEXT        NOT NULL,
    tokens_in  INT         NOT NULL,
    tokens_out INT         NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Índices de acceso (§3.3): lecturas por FK y métricas por fecha.
CREATE INDEX reviews_pull_request_id_idx ON reviews (pull_request_id);
CREATE INDEX findings_review_id_idx ON findings (review_id);
CREATE INDEX comments_sent_pull_request_id_idx ON comments_sent (pull_request_id);
CREATE INDEX llm_usage_created_at_idx ON llm_usage (created_at);
