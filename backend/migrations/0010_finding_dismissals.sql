-- Dismiss de hallazgos (S4): un hallazgo marcado "no aplica" por un humano
-- registra su huella (repository, file, category) — el mismo match que usa
-- el metrics job (§6 F5) — para que las corridas futuras lo traigan marcado
-- y la UI lo atenúe. El finding original no se toca: auditoría intacta (§3.3).
CREATE TABLE finding_dismissals (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repository_id BIGINT      NOT NULL REFERENCES repositories (id),
    file          TEXT        NOT NULL,
    category      TEXT        NOT NULL,
    dismissed_by  BIGINT      NOT NULL REFERENCES users (id),
    reason        TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Una huella viva por repo: re-dismissar la actualiza, no duplica.
    UNIQUE (repository_id, file, category)
);

COMMENT ON TABLE finding_dismissals IS 'Hallazgos marcados "no aplica" por un humano: la huella marca las apariciones futuras';
