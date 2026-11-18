-- name: CreateSpace :one
INSERT INTO spaces (name, avatar_emoji, avatar_color, base_currency, created_by_id)
VALUES ($1, sqlc.narg('avatar_emoji'), $2, $3, $4)
RETURNING *;

-- name: AddSpaceMember :exec
INSERT INTO space_members (space_id, user_id, role) VALUES ($1, $2, $3);

-- name: GetSpaceMembership :one
-- Space (live) plus the caller's role; no row if not a member or space deleted.
SELECT s.*, m.role AS member_role
FROM spaces s
JOIN space_members m ON m.space_id = s.id
WHERE s.id = $1 AND m.user_id = $2 AND s.deleted_at IS NULL;

-- name: ListUserSpaces :many
SELECT s.*, m.role AS member_role,
       (SELECT count(*) FROM space_members c WHERE c.space_id = s.id)::int AS member_count
FROM spaces s
JOIN space_members m ON m.space_id = s.id
WHERE m.user_id = $1 AND s.deleted_at IS NULL
ORDER BY m.joined_at, s.created_at;

-- name: CountUserSpaces :one
SELECT count(*)::int
FROM space_members m
JOIN spaces s ON s.id = m.space_id AND s.deleted_at IS NULL
WHERE m.user_id = $1;

-- name: CountSpaceMembers :one
SELECT count(*)::int FROM space_members WHERE space_id = $1;

-- name: UpdateSpace :one
UPDATE spaces SET
    name               = COALESCE(sqlc.narg('name'), name),
    avatar_emoji       = CASE WHEN sqlc.arg('set_avatar_emoji')::bool THEN sqlc.narg('avatar_emoji') ELSE avatar_emoji END,
    avatar_color       = COALESCE(sqlc.narg('avatar_color'), avatar_color),
    base_currency      = COALESCE(sqlc.narg('base_currency'), base_currency),
    auto_fx_conversion = COALESCE(sqlc.narg('auto_fx_conversion'), auto_fx_conversion),
    tracking_mode      = COALESCE(sqlc.narg('tracking_mode'), tracking_mode),
    updated_at         = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteSpace :exec
UPDATE spaces SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: SetLastActiveSpace :exec
UPDATE users SET last_active_space_id = $2, updated_at = now() WHERE id = $1;

-- name: RepointLastActiveForSpace :exec
-- After a space disappears for some users, move them to their oldest remaining live space (or NULL).
-- Pass user_id to limit it to one user (member removed/left), or NULL for every user pointing at it.
UPDATE users u SET last_active_space_id = (
    SELECT sm.space_id FROM space_members sm
    JOIN spaces s ON s.id = sm.space_id AND s.deleted_at IS NULL
    WHERE sm.user_id = u.id AND sm.space_id <> sqlc.arg('space_id')
    ORDER BY sm.joined_at LIMIT 1
)
WHERE u.last_active_space_id = sqlc.arg('space_id')
  AND (sqlc.narg('user_id')::uuid IS NULL OR u.id = sqlc.narg('user_id')::uuid);

-- name: ListSpaceMembers :many
SELECT u.id AS user_id, u.name, u.email, m.role, m.joined_at
FROM space_members m
JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
WHERE m.space_id = $1
ORDER BY (m.role = 'OWNER') DESC, m.joined_at;

-- name: GetSpaceMember :one
SELECT * FROM space_members WHERE space_id = $1 AND user_id = $2;

-- name: RemoveSpaceMember :exec
DELETE FROM space_members WHERE space_id = $1 AND user_id = $2;

-- name: RevokeActiveInvitations :exec
UPDATE space_invitations SET revoked_at = now() WHERE space_id = $1 AND revoked_at IS NULL;

-- name: CreateInvitation :one
INSERT INTO space_invitations (space_id, code, created_by_id, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetActiveInvitation :one
SELECT * FROM space_invitations
WHERE space_id = $1 AND revoked_at IS NULL AND expires_at > $2
ORDER BY created_at DESC LIMIT 1;

-- name: GetInvitationByCode :one
SELECT i.*, s.deleted_at AS space_deleted_at
FROM space_invitations i
JOIN spaces s ON s.id = i.space_id
WHERE i.code = $1;

-- name: LockSpace :one
-- Serializes concurrent joins so the member limit holds.
SELECT id FROM spaces WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: UpdateUserProfile :one
UPDATE users SET
    name       = COALESCE(sqlc.narg('name'), name),
    timezone   = COALESCE(sqlc.narg('timezone'), timezone),
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: CountOwnedSharedSpaces :one
-- Spaces the user owns that still have other members.
SELECT count(*)::int
FROM space_members me
JOIN spaces s ON s.id = me.space_id AND s.deleted_at IS NULL
WHERE me.user_id = $1 AND me.role = 'OWNER'
  AND EXISTS (SELECT 1 FROM space_members o WHERE o.space_id = me.space_id AND o.user_id <> $1);

-- name: SoftDeleteOwnedSoleSpaces :exec
UPDATE spaces SET deleted_at = now(), updated_at = now()
WHERE deleted_at IS NULL
  AND id IN (SELECT space_id FROM space_members WHERE user_id = $1 AND role = 'OWNER');

-- name: RemoveUserMemberships :exec
DELETE FROM space_members WHERE user_id = $1;

-- name: SoftDeleteUser :exec
UPDATE users SET deleted_at = now(), updated_at = now(), last_active_space_id = NULL WHERE id = $1;
