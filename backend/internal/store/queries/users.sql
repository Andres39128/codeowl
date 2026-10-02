-- name: GetByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id;

-- name: CreateUser :one
INSERT INTO users (username, role, password_hash, must_change_password)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: SetUserPassword :exec
-- Reset de contraseña del admin (§3.4): hash nuevo + must_change_password.
UPDATE users
SET password_hash = $2,
    must_change_password = $3,
    updated_at = now()
WHERE id = $1;

-- name: SetUserDisabled :exec
-- Habilitar/deshabilitar (§3.4: la baja es un flag; las sesiones las revoca
-- el caller con DeleteSessionsForUser).
UPDATE users
SET disabled = $2,
    updated_at = now()
WHERE id = $1;
