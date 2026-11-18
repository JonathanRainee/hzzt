//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expectCrossSpace404 asserts that outsider (a user who is NOT a member of the
// space in the path) gets 404 for the route, never 403 and never data.
// Every later phase reuses it for its space-scoped routes.
func expectCrossSpace404(t *testing.T, s *TestServer, outsider session, method, path string, body any) {
	t.Helper()
	res := s.Do(method, path, body, bearer(outsider.AccessToken))
	require.Equal(t, http.StatusNotFound, res.Status, "%s %s must be 404 for a non-member, got body %s", method, path, res.Body)
	assert.Equal(t, "NOT_FOUND", errCode(t, res))
}

type spaceBody struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	AvatarEmoji          *string `json:"avatarEmoji"`
	AvatarColor          string  `json:"avatarColor"`
	BaseCurrency         string  `json:"baseCurrency"`
	AutoFxConversion     bool    `json:"autoFxConversion"`
	TrackingMode         string  `json:"trackingMode"`
	BudgetPeriodStartDay int     `json:"budgetPeriodStartDay"`
	Role                 string  `json:"role"`
	MemberCount          int     `json:"memberCount"`
}

func (s *TestServer) mySpaces(u session) []spaceBody {
	s.t.Helper()
	res := s.Get("/api/v1/spaces", bearer(u.AccessToken))
	require.Equal(s.t, http.StatusOK, res.Status, string(res.Body))
	var out []spaceBody
	res.JSON(s.t, &out)
	return out
}

func (s *TestServer) invite(owner session, spaceID string) string {
	s.t.Helper()
	res := s.Do(http.MethodPost, "/api/v1/spaces/"+spaceID+"/invitations", nil, bearer(owner.AccessToken))
	require.Equal(s.t, http.StatusCreated, res.Status, string(res.Body))
	var inv struct {
		Code string `json:"code"`
		Link string `json:"link"`
	}
	res.JSON(s.t, &inv)
	require.Len(s.t, inv.Code, 8)
	assert.Contains(s.t, inv.Link, inv.Code)
	return inv.Code
}

func (s *TestServer) accept(u session, code string) Response {
	return s.Do(http.MethodPost, "/api/v1/invitations/"+code+"/accept", nil, bearer(u.AccessToken))
}

func (s *TestServer) join(owner session, spaceID string, u session) {
	s.t.Helper()
	res := s.accept(u, s.invite(owner, spaceID))
	require.Equal(s.t, http.StatusOK, res.Status, string(res.Body))
}

func spaceIDOf(s session) string { return *s.User.LastActiveSpaceID }

func TestRegistrationCreatesDefaultSpace(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")

	require.NotNil(t, u.User.LastActiveSpaceID, "default space must be the active space")
	spaces := s.mySpaces(u)
	require.Len(t, spaces, 1)
	assert.Equal(t, "Jane's Space", spaces[0].Name)
	assert.Equal(t, "OWNER", spaces[0].Role)
	assert.Equal(t, 1, spaces[0].MemberCount)
	assert.Equal(t, "USD", spaces[0].BaseCurrency)
	assert.True(t, spaces[0].AutoFxConversion)
	assert.Equal(t, "FULL_NET_WORTH", spaces[0].TrackingMode)
	assert.Equal(t, spaces[0].ID, *u.User.LastActiveSpaceID)

	res := s.Get("/api/v1/me", bearer(u.AccessToken))
	require.Equal(t, http.StatusOK, res.Status)
	var me struct {
		LastActiveSpaceID *string `json:"lastActiveSpaceId"`
	}
	res.JSON(t, &me)
	assert.Equal(t, spaces[0].ID, *me.LastActiveSpaceID)
}

func TestProfile(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")

	t.Run("unauthenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/me").Status)
		assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPatch, "/api/v1/me", map[string]any{"name": "x"}).Status)
		assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodDelete, "/api/v1/me", map[string]any{"password": "x"}).Status)
	})

	t.Run("patch name and timezone", func(t *testing.T) {
		res := s.Do(http.MethodPatch, "/api/v1/me", map[string]any{"name": "  Janet ", "timezone": "Asia/Jakarta"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var me struct{ Name, Timezone string }
		res.JSON(t, &me)
		assert.Equal(t, "Janet", me.Name)
		assert.Equal(t, "Asia/Jakarta", me.Timezone)
	})

	t.Run("partial patch keeps other fields", func(t *testing.T) {
		res := s.Do(http.MethodPatch, "/api/v1/me", map[string]any{"name": "Jan"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status)
		var me struct{ Name, Timezone string }
		res.JSON(t, &me)
		assert.Equal(t, "Jan", me.Name)
		assert.Equal(t, "Asia/Jakarta", me.Timezone)
	})

	for name, body := range map[string]map[string]any{
		"bad timezone": {"timezone": "Mars/Base"},
		"blank name":   {"name": "   "},
		"long name":    {"name": string(make([]byte, 61))},
		"unknown":      {"email": "x@y.co"},
	} {
		t.Run("invalid: "+name, func(t *testing.T) {
			res := s.Do(http.MethodPatch, "/api/v1/me", body, bearer(u.AccessToken))
			assert.Equal(t, http.StatusBadRequest, res.Status, string(res.Body))
		})
	}
}

func TestDeleteAccount(t *testing.T) {
	s := NewTestServer(t)

	t.Run("wrong password", func(t *testing.T) {
		u := s.register("wrong@example.com")
		res := s.Do(http.MethodDelete, "/api/v1/me", map[string]any{"password": "nope nope nope"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INVALID_CURRENT_PASSWORD", errCode(t, res))
	})

	t.Run("sole-member spaces are deleted and tokens revoked", func(t *testing.T) {
		u := s.register("solo@example.com")
		res := s.Do(http.MethodDelete, "/api/v1/me", map[string]any{"password": goodPassword}, bearer(u.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))

		assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/me", bearer(u.AccessToken)).Status)
		assert.Equal(t, http.StatusUnauthorized, s.refresh(u.RT).Status)
		assert.Equal(t, http.StatusUnauthorized, s.login("solo@example.com", goodPassword).Status)

		var live int
		require.NoError(t, s.Tx.QueryRow(context.Background(),
			`SELECT count(*) FROM spaces WHERE id = $1 AND deleted_at IS NULL`, spaceIDOf(u)).Scan(&live))
		assert.Zero(t, live)

		// the email is free again
		s.register("solo@example.com")
	})

	t.Run("blocked while owning a shared space", func(t *testing.T) {
		owner := s.register("owner@example.com")
		member := s.register("member@example.com")
		s.join(owner, spaceIDOf(owner), member)

		res := s.Do(http.MethodDelete, "/api/v1/me", map[string]any{"password": goodPassword}, bearer(owner.AccessToken))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "OWNERSHIP_TRANSFER_REQUIRED", errCode(t, res))

		// a plain member can delete their account and simply leaves the space
		res = s.Do(http.MethodDelete, "/api/v1/me", map[string]any{"password": goodPassword}, bearer(member.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))
		res = s.Get("/api/v1/spaces/"+spaceIDOf(owner)+"/members", bearer(owner.AccessToken))
		var members []map[string]any
		res.JSON(t, &members)
		assert.Len(t, members, 1)
	})
}

func TestSpaceCRUD(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")
	base := spaceIDOf(u)

	t.Run("unauthenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/spaces").Status)
		assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/spaces/"+base).Status)
	})

	t.Run("create", func(t *testing.T) {
		res := s.Do(http.MethodPost, "/api/v1/spaces", map[string]any{
			"name": " Family ", "avatarEmoji": "🏠", "avatarColor": "#aabbcc", "baseCurrency": "idr"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusCreated, res.Status, string(res.Body))
		var sp spaceBody
		res.JSON(t, &sp)
		assert.Equal(t, "Family", sp.Name)
		assert.Equal(t, "IDR", sp.BaseCurrency)
		assert.Equal(t, "#AABBCC", sp.AvatarColor)
		assert.Equal(t, "OWNER", sp.Role)
		assert.Len(t, s.mySpaces(u), 2)
	})

	t.Run("create validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"missing name":  {},
			"blank name":    {"name": "  "},
			"long name":     {"name": "012345678901234567890123456789012345678901"},
			"bad currency":  {"name": "x", "baseCurrency": "XXX"},
			"bad color":     {"name": "x", "avatarColor": "red"},
			"unknown field": {"name": "x", "role": "OWNER"},
		} {
			res := s.Do(http.MethodPost, "/api/v1/spaces", body, bearer(u.AccessToken))
			assert.Equal(t, http.StatusBadRequest, res.Status, "%s: %s", name, res.Body)
		}
	})

	t.Run("get", func(t *testing.T) {
		res := s.Get("/api/v1/spaces/"+base, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var sp spaceBody
		res.JSON(t, &sp)
		assert.Equal(t, base, sp.ID)
	})

	t.Run("patch", func(t *testing.T) {
		res := s.Do(http.MethodPatch, "/api/v1/spaces/"+base, map[string]any{
			"name": "Renamed", "baseCurrency": "EUR", "autoFxConversion": false,
			"trackingMode": "CASH_ONLY", "avatarEmoji": "💸"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var sp spaceBody
		res.JSON(t, &sp)
		assert.Equal(t, "Renamed", sp.Name)
		assert.Equal(t, "EUR", sp.BaseCurrency)
		assert.False(t, sp.AutoFxConversion)
		assert.Equal(t, "CASH_ONLY", sp.TrackingMode)
		require.NotNil(t, sp.AvatarEmoji)

		// explicit null clears the emoji; absent leaves other fields alone
		res = s.Do(http.MethodPatch, "/api/v1/spaces/"+base, map[string]any{"avatarEmoji": nil}, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		res.JSON(t, &sp)
		assert.Nil(t, sp.AvatarEmoji)
		assert.Equal(t, "Renamed", sp.Name)
	})

	t.Run("patch validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"bad mode":     {"trackingMode": "NOPE"},
			"bad currency": {"baseCurrency": "ZZZ"},
			"blank name":   {"name": " "},
		} {
			res := s.Do(http.MethodPatch, "/api/v1/spaces/"+base, body, bearer(u.AccessToken))
			assert.Equal(t, http.StatusBadRequest, res.Status, "%s: %s", name, res.Body)
		}
	})

	t.Run("changing base currency does not rewrite stored data", func(t *testing.T) {
		// Space has no monetary columns in P3; assert the other settings are untouched.
		res := s.Do(http.MethodPatch, "/api/v1/spaces/"+base, map[string]any{"baseCurrency": "JPY"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status)
		var sp spaceBody
		res.JSON(t, &sp)
		assert.Equal(t, "JPY", sp.BaseCurrency)
		assert.Equal(t, "CASH_ONLY", sp.TrackingMode)
	})

	t.Run("activate", func(t *testing.T) {
		other := s.mySpaces(u)[1].ID
		res := s.Do(http.MethodPost, "/api/v1/spaces/"+other+"/activate", nil, bearer(u.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))
		res = s.Get("/api/v1/me", bearer(u.AccessToken))
		var me struct {
			LastActiveSpaceID string `json:"lastActiveSpaceId"`
		}
		res.JSON(t, &me)
		assert.Equal(t, other, me.LastActiveSpaceID)
	})

	t.Run("delete moves active space and keeps last one", func(t *testing.T) {
		spaces := s.mySpaces(u)
		require.Len(t, spaces, 2)
		active := spaces[1].ID // activated above
		res := s.Do(http.MethodDelete, "/api/v1/spaces/"+active, nil, bearer(u.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))

		assert.Equal(t, http.StatusNotFound, s.Get("/api/v1/spaces/"+active, bearer(u.AccessToken)).Status)
		remaining := s.mySpaces(u)
		require.Len(t, remaining, 1)
		res = s.Get("/api/v1/me", bearer(u.AccessToken))
		var me struct {
			LastActiveSpaceID string `json:"lastActiveSpaceId"`
		}
		res.JSON(t, &me)
		assert.Equal(t, remaining[0].ID, me.LastActiveSpaceID)

		res = s.Do(http.MethodDelete, "/api/v1/spaces/"+remaining[0].ID, nil, bearer(u.AccessToken))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "LAST_SPACE", errCode(t, res))
	})
}

func TestSpaceRoleMatrixAndIsolation(t *testing.T) {
	s := NewTestServer(t)
	owner := s.register("owner@example.com")
	member := s.register("member@example.com")
	outsider := s.register("outsider@example.com")
	sid := spaceIDOf(owner)
	s.join(owner, sid, member)
	base := "/api/v1/spaces/" + sid

	routes := []struct {
		method, path string
		body         any
		ownerOnly    bool
	}{
		{http.MethodGet, base, nil, false},
		{http.MethodPatch, base, map[string]any{"name": "x"}, true},
		{http.MethodDelete, base, nil, true},
		{http.MethodPost, base + "/activate", nil, false},
		{http.MethodGet, base + "/members", nil, false},
		{http.MethodDelete, base + "/members/" + member.User.ID, nil, false}, // self-leave allowed; checked below
		{http.MethodPost, base + "/invitations", nil, true},
		{http.MethodGet, base + "/invitations/active", nil, true},
		{http.MethodDelete, base + "/invitations/active", nil, true},
	}

	for _, r := range routes {
		t.Run("outsider 404 "+r.method+" "+r.path, func(t *testing.T) {
			expectCrossSpace404(t, s, outsider, r.method, r.path, r.body)
		})
	}
	t.Run("random id 404", func(t *testing.T) {
		expectCrossSpace404(t, s, owner, http.MethodGet, "/api/v1/spaces/00000000-0000-0000-0000-000000000000", nil)
		expectCrossSpace404(t, s, owner, http.MethodGet, "/api/v1/spaces/not-a-uuid", nil)
	})
	for _, r := range routes {
		if !r.ownerOnly {
			continue
		}
		t.Run("member 403 "+r.method+" "+r.path, func(t *testing.T) {
			res := s.Do(r.method, r.path, r.body, bearer(member.AccessToken))
			require.Equal(t, http.StatusForbidden, res.Status, string(res.Body))
			assert.Equal(t, "FORBIDDEN", errCode(t, res))
		})
	}

	t.Run("member can read", func(t *testing.T) {
		res := s.Get(base, bearer(member.AccessToken))
		require.Equal(t, http.StatusOK, res.Status)
		var sp spaceBody
		res.JSON(t, &sp)
		assert.Equal(t, "MEMBER", sp.Role)
		assert.Equal(t, 2, sp.MemberCount)
	})

	t.Run("member cannot remove others", func(t *testing.T) {
		res := s.Do(http.MethodDelete, base+"/members/"+owner.User.ID, nil, bearer(member.AccessToken))
		assert.Equal(t, http.StatusForbidden, res.Status)
	})
}

func TestMembers(t *testing.T) {
	s := NewTestServer(t)
	owner := s.register("owner@example.com")
	m1 := s.register("m1@example.com")
	m2 := s.register("m2@example.com")
	sid := spaceIDOf(owner)
	s.join(owner, sid, m1)
	s.join(owner, sid, m2)
	base := "/api/v1/spaces/" + sid

	res := s.Get(base+"/members", bearer(m1.AccessToken))
	require.Equal(t, http.StatusOK, res.Status)
	var members []struct {
		UserID, Name, Email, Role string
	}
	res.JSON(t, &members)
	require.Len(t, members, 3)
	assert.Equal(t, "OWNER", members[0].Role)
	assert.Equal(t, "owner@example.com", members[0].Email)

	t.Run("owner cannot leave or be removed", func(t *testing.T) {
		res := s.Do(http.MethodDelete, base+"/members/"+owner.User.ID, nil, bearer(owner.AccessToken))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "OWNER_CANNOT_LEAVE", errCode(t, res))
	})

	t.Run("owner removes a member", func(t *testing.T) {
		res := s.Do(http.MethodDelete, base+"/members/"+m1.User.ID, nil, bearer(owner.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))
		expectCrossSpace404(t, s, m1, http.MethodGet, base, nil)
	})

	t.Run("member leaves; active space falls back to own space", func(t *testing.T) {
		require.Equal(t, http.StatusNoContent, s.Do(http.MethodPost, base+"/activate", nil, bearer(m2.AccessToken)).Status)
		res := s.Do(http.MethodDelete, base+"/members/"+m2.User.ID, nil, bearer(m2.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))
		res = s.Get("/api/v1/me", bearer(m2.AccessToken))
		var me struct {
			LastActiveSpaceID string `json:"lastActiveSpaceId"`
		}
		res.JSON(t, &me)
		assert.Equal(t, spaceIDOf(m2), me.LastActiveSpaceID)
	})

	t.Run("unknown member 404", func(t *testing.T) {
		res := s.Do(http.MethodDelete, base+"/members/00000000-0000-0000-0000-000000000000", nil, bearer(owner.AccessToken))
		assert.Equal(t, http.StatusNotFound, res.Status)
	})
}

func TestInvitationLifecycle(t *testing.T) {
	s := NewTestServer(t)
	owner := s.register("owner@example.com")
	sid := spaceIDOf(owner)
	base := "/api/v1/spaces/" + sid

	t.Run("no active invitation initially", func(t *testing.T) {
		res := s.Get(base+"/invitations/active", bearer(owner.AccessToken))
		require.Equal(t, http.StatusOK, res.Status)
		var b struct {
			Invitation *struct{ Code string } `json:"invitation"`
		}
		res.JSON(t, &b)
		assert.Nil(t, b.Invitation)
	})

	code := s.invite(owner, sid)
	for _, c := range code {
		assert.NotContains(t, "01OI", string(c), "ambiguous characters must not appear")
	}

	t.Run("active shows the code; new code revokes the old one", func(t *testing.T) {
		res := s.Get(base+"/invitations/active", bearer(owner.AccessToken))
		var b struct {
			Invitation struct{ Code string } `json:"invitation"`
		}
		res.JSON(t, &b)
		assert.Equal(t, code, b.Invitation.Code)

		code2 := s.invite(owner, sid)
		assert.NotEqual(t, code, code2)
		joiner := s.register("late@example.com")
		res = s.accept(joiner, code)
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INVITATION_INVALID", errCode(t, res))
		code = code2
	})

	t.Run("accept is case-insensitive and joins as MEMBER", func(t *testing.T) {
		u := s.register("joiner@example.com")
		res := s.accept(u, "  "+lower(code)+" ")
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var sp spaceBody
		res.JSON(t, &sp)
		assert.Equal(t, sid, sp.ID)
		assert.Equal(t, "MEMBER", sp.Role)
		assert.Equal(t, 2, sp.MemberCount)
		assert.Len(t, s.mySpaces(u), 2)

		res = s.accept(u, code)
		require.Equal(t, http.StatusConflict, res.Status)
		assert.Equal(t, "ALREADY_A_MEMBER", errCode(t, res))
	})

	t.Run("owner is already a member", func(t *testing.T) {
		res := s.accept(owner, code)
		require.Equal(t, http.StatusConflict, res.Status)
		assert.Equal(t, "ALREADY_A_MEMBER", errCode(t, res))
	})

	t.Run("unknown code", func(t *testing.T) {
		res := s.accept(owner, "ZZZZZZZZ")
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INVITATION_INVALID", errCode(t, res))
	})

	t.Run("unauthenticated accept", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPost, "/api/v1/invitations/"+code+"/accept", nil).Status)
	})

	t.Run("revoke", func(t *testing.T) {
		code := s.invite(owner, sid)
		res := s.Do(http.MethodDelete, base+"/invitations/active", nil, bearer(owner.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status)
		res = s.accept(s.register("revoked@example.com"), code)
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INVITATION_INVALID", errCode(t, res))
	})

	t.Run("expiry", func(t *testing.T) {
		code := s.invite(owner, sid)
		s.Clock.Advance(7*24*time.Hour + time.Minute)
		res := s.accept(s.register("slow@example.com"), code)
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INVITATION_EXPIRED", errCode(t, res))

		res = s.Get(base+"/invitations/active", bearer(owner.AccessToken))
		var b struct {
			Invitation *struct{ Code string } `json:"invitation"`
		}
		res.JSON(t, &b)
		assert.Nil(t, b.Invitation, "expired invitation is not active")
	})
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestMemberLimit(t *testing.T) {
	s := NewTestServer(t)
	owner := s.register("owner@example.com")
	sid := spaceIDOf(owner)
	for i := 1; i <= 4; i++ {
		s.join(owner, sid, s.register("m"+string(rune('0'+i))+"@example.com"))
	}
	res := s.accept(s.register("sixth@example.com"), s.invite(owner, sid))
	require.Equal(t, http.StatusUnprocessableEntity, res.Status, string(res.Body))
	assert.Equal(t, "SPACE_MEMBER_LIMIT", errCode(t, res))
}
