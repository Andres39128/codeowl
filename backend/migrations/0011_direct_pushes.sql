-- Trazabilidad de merges sin PR (trazabilidad-merges S6): los pushes directos
-- a la rama base se registran y se analizan con el pipeline de review. La
-- salida vive solo en el dashboard: no hay PR donde publicar.
CREATE TABLE direct_pushes (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repository_id BIGINT      NOT NULL REFERENCES repositories (id),
    -- Rama base del repo en el momento del push (default_branch): la trazabilidad
    -- es sobre lo que llegó a la rama principal, no sobre branches de feature.
    branch        TEXT        NOT NULL,
    before_sha    TEXT        NOT NULL,
    after_sha     TEXT        NOT NULL,
    -- Quién disparó el push (sender del webhook); vacío si el payload no trae.
    committer     TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Idempotencia del webhook: un reintento del mismo push (repo, after_sha)
    -- no duplica el análisis. El upsert reusa la fila existente.
    UNIQUE (repository_id, after_sha)
);

COMMENT ON TABLE direct_pushes IS 'Pushes/merges directos a la rama base sin PR: se analizan con el pipeline completo, salida solo dashboard';

-- reviews pasa a target polimórfico (S6): corrida de un PR o de un push
-- directo — exactamente uno de los dos. Findings/dedup/dismissals/llm_usage
-- siguen colgando de reviews sin cambios (§3.3). Para un push: head_sha =
-- after_sha, base_sha = before_sha.
ALTER TABLE reviews
    ALTER COLUMN pull_request_id DROP NOT NULL,
    ADD COLUMN direct_push_id BIGINT REFERENCES direct_pushes (id),
    ADD CONSTRAINT reviews_target_check CHECK (
        (pull_request_id IS NOT NULL AND direct_push_id IS NULL) OR
        (pull_request_id IS NULL AND direct_push_id IS NOT NULL)
    );
