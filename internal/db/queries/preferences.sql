-- name: EnsureUserPreferences :one
-- Creates the default row if missing and returns it (the no-op update makes RETURNING work on conflict).
INSERT INTO user_preferences (user_id) VALUES ($1)
ON CONFLICT (user_id) DO UPDATE SET user_id = user_preferences.user_id
RETURNING *;

-- name: LockUserPreferences :one
SELECT * FROM user_preferences WHERE user_id = $1 FOR UPDATE;

-- name: UpdateUserPreferences :one
UPDATE user_preferences SET
    hide_amounts  = COALESCE(sqlc.narg('hide_amounts'), hide_amounts),
    keyboard_type = COALESCE(sqlc.narg('keyboard_type'), keyboard_type),
    updated_at    = now()
WHERE user_id = $1
RETURNING *;

-- name: EnableAppLock :exec
UPDATE user_preferences SET
    app_lock_enabled = true, app_lock_pin_hash = $2,
    app_lock_failed_attempts = 0, app_lock_locked_until = NULL, updated_at = now()
WHERE user_id = $1;

-- name: DisableAppLock :exec
UPDATE user_preferences SET
    app_lock_enabled = false, app_lock_pin_hash = NULL,
    app_lock_failed_attempts = 0, app_lock_locked_until = NULL, updated_at = now()
WHERE user_id = $1;

-- name: SetAppLockAttempts :exec
UPDATE user_preferences SET
    app_lock_failed_attempts = $2, app_lock_locked_until = $3, updated_at = now()
WHERE user_id = $1;

-- name: SetAppLockPin :exec
UPDATE user_preferences SET
    app_lock_pin_hash = $2, app_lock_failed_attempts = 0, app_lock_locked_until = NULL, updated_at = now()
WHERE user_id = $1;
