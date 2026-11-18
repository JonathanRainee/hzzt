-- name: ListLiveParticipants :many
-- Non-removed participants, owner first, then in invitation order (a stable order the allocation relies on).
SELECT * FROM investment_participants
WHERE investment_id = $1 AND status <> 'REMOVED'
ORDER BY (role = 'OWNER') DESC, invited_at, id;

-- name: ListLiveParticipantsForInvestments :many
SELECT * FROM investment_participants
WHERE investment_id = ANY($1::uuid[]) AND status <> 'REMOVED'
ORDER BY investment_id, (role = 'OWNER') DESC, invited_at, id;

-- name: InsertParticipant :one
INSERT INTO investment_participants (investment_id, role, user_id, display_name, email, share_bps, status, invited_at)
VALUES ($1, $2, sqlc.narg('user_id'), $3, sqlc.narg('email'), $4, $5, $6)
RETURNING *;

-- name: UpdateParticipantShare :exec
UPDATE investment_participants SET display_name = $2, share_bps = $3 WHERE id = $1;

-- name: RemoveParticipant :exec
-- Removing frees the user slot (so the person can be invited again) and detaches the space.
UPDATE investment_participants SET
    status = 'REMOVED', user_id = NULL, attached_space_id = NULL, responded_at = now()
WHERE id = $1;

-- name: GetParticipant :one
SELECT * FROM investment_participants WHERE id = $1;

-- name: AcceptInvitation :one
UPDATE investment_participants SET
    status = 'ACTIVE', user_id = $2, attached_space_id = $3, responded_at = now()
WHERE id = $1 AND status = 'INVITED'
RETURNING *;

-- name: DeclineInvitation :one
UPDATE investment_participants SET status = 'DECLINED', responded_at = now()
WHERE id = $1 AND status = 'INVITED'
RETURNING *;

-- name: LeaveSharedInvestment :one
-- The share stays allocated but becomes a name-only participant.
UPDATE investment_participants SET
    user_id = NULL, email = NULL, attached_space_id = NULL, status = 'ACTIVE', responded_at = now()
WHERE id = $1 AND role = 'PARTICIPANT' AND status = 'ACTIVE'
RETURNING *;

-- name: SetInvestmentShared :exec
UPDATE investments SET is_shared = $2, updated_at = now() WHERE id = $1;

-- name: InsertShareChange :exec
INSERT INTO investment_share_changes (investment_id, changed_by_id, before, after) VALUES ($1, $2, $3, $4);

-- name: ListShareChanges :many
SELECT * FROM investment_share_changes WHERE investment_id = $1 ORDER BY created_at DESC, id DESC;

-- name: ListInvitationsForUser :many
-- Pending invitations addressed to the user's id or (case-insensitive) account email.
SELECT p.*, i.name AS investment_name, i.symbol AS investment_symbol, i.icon AS investment_icon,
       i.asset_type AS investment_asset_type, o.display_name AS owner_name
FROM investment_participants p
JOIN investments i ON i.id = p.investment_id AND i.deleted_at IS NULL
JOIN investment_participants o ON o.investment_id = p.investment_id AND o.role = 'OWNER' AND o.status <> 'REMOVED'
WHERE p.status = 'INVITED'
  AND (p.user_id = $1 OR lower(p.email::text) = lower($2::text))
ORDER BY p.invited_at DESC, p.id;

-- name: GetInvitationForUser :one
SELECT p.* FROM investment_participants p
JOIN investments i ON i.id = p.investment_id AND i.deleted_at IS NULL
WHERE p.id = $1 AND (p.user_id = $2 OR lower(p.email::text) = lower($3::text)) AND p.status <> 'REMOVED';

-- name: GetMyParticipation :one
-- An ACTIVE participation of the user that can be left.
SELECT p.* FROM investment_participants p
JOIN investments i ON i.id = p.investment_id AND i.deleted_at IS NULL
WHERE p.id = $1 AND p.user_id = $2 AND p.status = 'ACTIVE';

-- name: ListSharedInInvestments :many
-- Investments of other spaces that a participant attached to this space.
SELECT i.*, p.share_bps AS my_share_bps, p.id AS my_participant_id
FROM investment_participants p
JOIN investments i ON i.id = p.investment_id AND i.deleted_at IS NULL AND i.space_id <> p.attached_space_id
WHERE p.attached_space_id = $1 AND p.status = 'ACTIVE' AND p.role = 'PARTICIPANT'
  AND (sqlc.narg('asset_type')::text IS NULL OR i.asset_type = sqlc.narg('asset_type')::text)
ORDER BY i.created_at, i.id;

-- name: SharedInAttachedSpaceIDs :many
-- Spaces where the investment is attached as shared-in.
SELECT DISTINCT attached_space_id FROM investment_participants
WHERE investment_id = $1 AND status = 'ACTIVE' AND attached_space_id IS NOT NULL;
