-- Usuarios del dashboard (guía §3.3): single-org, 1-5 usuarios, sin registro
-- público. La baja es un flag (disabled), jamás delete — el histórico queda
-- intacto (§3.4). La contraseña se guarda como hash argon2id.
CREATE TABLE users (
    id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username             TEXT        NOT NULL UNIQUE,
    role                 TEXT        NOT NULL CHECK (role IN ('admin', 'member')),
    password_hash        TEXT        NOT NULL,
    must_change_password BOOLEAN     NOT NULL DEFAULT false,
    disabled             BOOLEAN     NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sesiones del dashboard (guía §3.4): token opaco de 256 bits en cookie
-- HttpOnly; en BD solo su hash. Sin ON DELETE: nada se borra en cascada (§3.4).
CREATE TABLE sessions (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash TEXT        NOT NULL UNIQUE,
    user_id    BIGINT      NOT NULL REFERENCES users (id),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
