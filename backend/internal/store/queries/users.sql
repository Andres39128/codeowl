-- name: GetByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: CreateUser :one
INSERT INTO users (username, role, password_hash, must_change_password)
VALUES ($1, $2, $3, $4)
RETURNING *;
