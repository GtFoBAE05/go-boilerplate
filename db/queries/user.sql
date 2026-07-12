-- name: CreateUser :one
INSERT INTO users (name, email)
VALUES ($1, $2)
RETURNING id, name, email, created_at;

-- name: GetUser :one
SELECT id, name, email, created_at
FROM users
WHERE id = $1;

-- name: ListUsers :many
SELECT id, name, email, created_at
FROM users
ORDER BY id
LIMIT $1 OFFSET $2;

-- name: GetUserForUpdate :one
SELECT id, name, email, created_at
FROM users
WHERE id = $1
FOR UPDATE;

-- name: UpdateUser :one
UPDATE users
SET name = $2, email = $3
WHERE id = $1
RETURNING id, name, email, created_at;

-- name: DeleteUser :exec
DELETE FROM users
WHERE id = $1;
