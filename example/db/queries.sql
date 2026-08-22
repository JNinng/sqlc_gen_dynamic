-- 固定形态查询示例(sqlc-gen-go 承担;动态 List 由本插件承担)。

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: CreateUser :one
INSERT INTO users (name, email, password, status)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: SetUserPassword :exec
UPDATE users SET password = $2 WHERE id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: CreateOrder :one
INSERT INTO orders (order_no, user_id, amount_cents, status)
VALUES ($1, $2, $3, $4)
RETURNING *;
