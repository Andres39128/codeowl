-- name: CreateSession :one
INSERT INTO sessions (token_hash, user_id, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetSessionByTokenHash :one
-- Devuelve la sesión vigente (no expirada) junto al usuario; el login decide
-- sobre must_change_password y disabled con los campos traídos.
SELECT s.*, u.username, u.role, u.must_change_password, u.disabled
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1
  AND s.expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteSessionsForUser :exec
-- Revoca todas las sesiones del usuario (reset de contraseña o baja, §3.4).
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :execrows
-- Retención del CleanupJob (guía §9.11): borra sesiones ya expiradas y
-- devuelve la cantidad eliminada.
DELETE FROM sessions
WHERE expires_at < now();
