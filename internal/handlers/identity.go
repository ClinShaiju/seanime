package handlers

import (
	"errors"
	"net/http"
	"seanime/internal/core"
	"seanime/internal/database/models"
	"strings"

	"github.com/labstack/echo/v4"
)

// errAdminRequired is returned by RequireAdmin; handlers render it as a 403.
var errAdminRequired = errors.New("admin privileges required")

const (
	ctxUserID   = "userId"
	ctxUserRole = "userRole"
)

// IdentityMiddleware resolves the acting user for a request and stashes the user id
// and role in the echo context. It runs after OptionalAuthMiddleware, so it only
// sees requests that already passed the server-password gate.
//
// Resolution:
//  1. A valid `Authorization: Bearer <session token>` resolves to that user (any role).
//  2. No session, and NO server password configured → the operator is local and
//     trusted, so they act as admin. This keeps the desktop app and password-less
//     setups working exactly as before.
//  3. No session, but a server password IS configured (a networked/shared server) →
//     no implicit identity. Knowing the shared server password is not enough to
//     configure the server; admin actions require logging in as the admin. This is
//     the hardened ceiling: the outer network gate is separated from identity.
func (h *Handler) IdentityMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		// 1. Bearer session token → that user (any role).
		if token := bearerToken(c.Request()); token != "" {
			if u, err := h.App.Database.GetSessionUser(token); err == nil && u != nil {
				setIdentity(c, u)
				return next(c)
			}
		}

		// 2. Local install (no server password) → trusted operator acts as admin.
		if h.App.Config.Server.Password == "" {
			if admin, err := h.App.Database.GetAdminUser(); err == nil && admin != nil {
				setIdentity(c, admin)
			}
		}

		// 3. Networked server, no session → no identity (login required to be anyone).
		return next(c)
	}
}

// errStreamingRequiresUser is returned when an unauthenticated request (knows the
// server password but presents no user session) tries to start playback/streaming.
var errStreamingRequiresUser = errors.New("streaming requires logging in")

// guardStreamingUser rejects anonymous requests from STARTING playback/streaming —
// anon users (server-password only, no session) may browse but not stream. On a
// local password-less install dataUserID resolves to the admin, so this only blocks
// the networked-anon case. Apply at the top of each stream/playback start handler.
// (Serve endpoints are NOT guarded by user — the media player presents a stream id +
// HMAC, not a session; an anon can't obtain a valid stream id without first starting,
// which this blocks.)
func (h *Handler) guardStreamingUser(c echo.Context) error {
	if h.dataUserID(c) == 0 {
		return h.RespondWithStatusError(c, http.StatusForbidden, errStreamingRequiresUser)
	}
	return nil
}

// AdminOnly is per-route middleware that rejects non-admin requests with a 403. Apply
// it to server-configuration endpoints so configuring the server always requires an
// authenticated admin.
func (h *Handler) AdminOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := h.RequireAdmin(c); err != nil {
			return h.RespondWithStatusError(c, http.StatusForbidden, err)
		}
		return next(c)
	}
}

func setIdentity(c echo.Context, u *models.User) {
	c.Set(ctxUserID, u.ID)
	c.Set(ctxUserRole, u.Role)
}

// bearerToken extracts the token from an `Authorization: Bearer <token>` header.
func bearerToken(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if len(authz) > 7 && strings.EqualFold(authz[:7], "Bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	return ""
}

// CurrentUserID returns the resolved user id for the request, or 0 if none.
func (h *Handler) CurrentUserID(c echo.Context) uint {
	if v, ok := c.Get(ctxUserID).(uint); ok {
		return v
	}
	return 0
}

// CurrentUserRole returns the resolved role for the request, or "" if none.
func (h *Handler) CurrentUserRole(c echo.Context) string {
	if v, ok := c.Get(ctxUserRole).(string); ok {
		return v
	}
	return ""
}

// dataUserID returns the user id to scope per-user data (theme, playlists, progress,
// collection) by.
//
// It falls back to the admin ONLY when no server password is configured — i.e. a
// local, trusted, single-operator install, where the operator legitimately is the
// admin and per-user rows must not be orphaned. On a networked (password-protected)
// server, an unauthenticated request (one that passed the shared-password gate but
// did NOT present a user session) must NOT inherit the admin's identity or data:
// knowing the server password is not the same as being the admin. Such requests
// resolve to 0 → an anonymous, data-less session (the client must log in).
func (h *Handler) dataUserID(c echo.Context) uint {
	if id := h.CurrentUserID(c); id != 0 {
		return id
	}
	// Admin fallback applies only for local (password-less) installs, where the
	// operator legitimately is the admin. On a networked (password-protected) server
	// an unauthenticated request must NOT inherit the admin's identity/data: knowing
	// the shared server password is not the same as being the admin.
	if h.App.Config.Server.Password == "" {
		if admin, err := h.App.Database.GetAdminUser(); err == nil && admin != nil {
			return admin.ID
		}
	}
	return 0
}

// userSession resolves the per-user identity session (their own AniList platform +
// collection) for a request. Falls back to the admin/App-global delegate when no
// user is resolved, so single-user installs are unchanged.
func (h *Handler) userSession(c echo.Context) *core.UserSession {
	return h.App.SessionFor(h.dataUserID(c))
}

// CurrentUser returns the resolved user for the request, or nil if none.
func (h *Handler) CurrentUser(c echo.Context) *models.User {
	id := h.CurrentUserID(c)
	if id == 0 {
		return nil
	}
	u, err := h.App.Database.GetUserByID(id)
	if err != nil {
		return nil
	}
	return u
}

// IsAdmin reports whether the request resolves to an admin user.
func (h *Handler) IsAdmin(c echo.Context) bool {
	return h.CurrentUserRole(c) == models.UserRoleAdmin
}

// RequireAdmin returns errAdminRequired when the request does not resolve to an
// admin. Handlers call it at the top to gate admin-only actions:
//
//	if err := h.RequireAdmin(c); err != nil {
//		return h.RespondWithStatusError(c, http.StatusForbidden, err)
//	}
func (h *Handler) RequireAdmin(c echo.Context) error {
	if !h.IsAdmin(c) {
		return errAdminRequired
	}
	return nil
}
