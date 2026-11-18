package spaces

import (
	"net/http"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

// Error codes owned by this package (see plan Appendix B).
const (
	CodeLastSpace         = "LAST_SPACE"
	CodeOwnerCannotLeave  = "OWNER_CANNOT_LEAVE"
	CodeInvitationInvalid = "INVITATION_INVALID"
	CodeInvitationExpired = "INVITATION_EXPIRED"
	CodeAlreadyAMember    = "ALREADY_A_MEMBER"
	CodeSpaceMemberLimit  = "SPACE_MEMBER_LIMIT"
)

var (
	errLastSpace         = apperror.Unprocessable(CodeLastSpace, "You cannot delete your last remaining space")
	errOwnerCannotLeave  = apperror.Unprocessable(CodeOwnerCannotLeave, "The owner cannot leave or be removed from the space")
	errInvitationInvalid = apperror.New(http.StatusUnprocessableEntity, CodeInvitationInvalid, "Invitation code is invalid or has been revoked")
	errInvitationExpired = apperror.New(http.StatusUnprocessableEntity, CodeInvitationExpired, "Invitation code has expired")
	errAlreadyMember     = apperror.Conflict(CodeAlreadyAMember, "You are already a member of this space")
	errMemberLimit       = apperror.Unprocessable(CodeSpaceMemberLimit, "This space already has the maximum number of members")
	errSpaceNotFound     = apperror.NotFound("Space not found")
)
