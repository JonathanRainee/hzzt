-- name: CreateUser :one
INSERT INTO users (email, password_hash, name, timezone)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetRefreshTokenByHashForUpdate :one
SELECT * FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE;

-- name: RotateRefreshToken :exec
UPDATE refresh_tokens SET revoked_at = now(), replaced_by_id = $2 WHERE id = $1;

-- name: RevokeRefreshFamily :exec
UPDATE refresh_tokens SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeAllUserRefreshTokens :exec
UPDATE refresh_tokens SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeOtherUserRefreshFamilies :exec
UPDATE refresh_tokens SET revoked_at = now()
WHERE user_id = $1 AND family_id <> $2 AND revoked_at IS NULL;

-- name: InvalidateUserResetTokens :exec
UPDATE password_reset_tokens SET used_at = now()
WHERE user_id = $1 AND used_at IS NULL;

-- name: CreatePasswordResetToken :one
INSERT INTO password_reset_tokens (user_id, token_hash, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetPasswordResetTokenByHashForUpdate :one
SELECT * FROM password_reset_tokens WHERE token_hash = $1 FOR UPDATE;

-- name: MarkPasswordResetTokenUsed :exec
UPDATE password_reset_tokens SET used_at = now() WHERE id = $1;
