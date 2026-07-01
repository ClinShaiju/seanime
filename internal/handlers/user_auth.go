package handlers

import (
	"errors"
	"net/http"
	"seanime/internal/database/db"
	"seanime/internal/database/models"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// loginThrottle is a small in-memory brute-force gate for /user/login: after
// loginMaxFailures consecutive failures for a username, further attempts are rejected for
// loginLockout. bcrypt alone only slows a single-threaded guesser; this bounds parallel ones.
// Success clears the counter. Keyed by username (not IP — the server sits behind a proxy).
var loginThrottle = struct {
	sync.Mutex
	failures map[string]int
	lockedAt map[string]time.Time
}{failures: map[string]int{}, lockedAt: map[string]time.Time{}}

const (
	loginMaxFailures = 5
	loginLockout     = 30 * time.Second
)

func loginThrottleCheck(username string) bool {
	loginThrottle.Lock()
	defer loginThrottle.Unlock()
	if at, ok := loginThrottle.lockedAt[username]; ok {
		if time.Since(at) < loginLockout {
			return false
		}
		delete(loginThrottle.lockedAt, username)
		loginThrottle.failures[username] = 0
	}
	return true
}

func loginThrottleRecord(username string, success bool) {
	loginThrottle.Lock()
	defer loginThrottle.Unlock()
	if success {
		delete(loginThrottle.failures, username)
		delete(loginThrottle.lockedAt, username)
		return
	}
	loginThrottle.failures[username]++
	if loginThrottle.failures[username] >= loginMaxFailures {
		loginThrottle.lockedAt[username] = time.Now()
	}
}

// UserLoginResponse is returned on a successful user login.
type UserLoginResponse struct {
	Token string       `json:"token"`
	User  *models.User `json:"user"`
}

// HandleUserLogin
//
//	@summary logs in a Seanime user with username + password and returns a session token.
//	@desc This is the per-user identity layer that sits behind the server-password gate.
//	@desc The returned token must be sent as `Authorization: Bearer <token>` on subsequent requests.
//	@route /api/v1/user/login [POST]
//	@returns handlers.UserLoginResponse
func (h *Handler) HandleUserLogin(c echo.Context) error {
	type body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	if !loginThrottleCheck(b.Username) {
		return h.RespondWithStatusError(c, http.StatusTooManyRequests, errors.New("too many failed attempts, try again shortly"))
	}

	u, err := h.App.Database.GetUserByUsername(b.Username)
	ok := err == nil && u != nil && db.CheckUserPassword(u, b.Password)
	loginThrottleRecord(b.Username, ok)
	if !ok {
		return h.RespondWithStatusError(c, http.StatusUnauthorized, errors.New("invalid username or password"))
	}

	sess, err := h.App.Database.CreateSession(u.ID)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, UserLoginResponse{Token: sess.Token, User: u})
}

// HandleUserLogout
//
//	@summary logs out the current Seanime user by revoking their session token.
//	@route /api/v1/user/logout [POST]
//	@returns bool
func (h *Handler) HandleUserLogout(c echo.Context) error {
	if token := bearerToken(c.Request()); token != "" {
		_ = h.App.Database.DeleteSession(token)
	}
	return h.RespondWithData(c, true)
}

// HandleUserMe
//
//	@summary returns the currently authenticated Seanime user.
//	@route /api/v1/user/me [GET]
//	@returns models.User
func (h *Handler) HandleUserMe(c echo.Context) error {
	u := h.CurrentUser(c)
	if u == nil {
		return h.RespondWithStatusError(c, http.StatusUnauthorized, errors.New("not logged in"))
	}
	return h.RespondWithData(c, u)
}

// HandleUserList
//
//	@summary lists all Seanime users (admin only).
//	@route /api/v1/user/list [GET]
//	@returns []models.User
func (h *Handler) HandleUserList(c echo.Context) error {
	if err := h.RequireAdmin(c); err != nil {
		return h.RespondWithStatusError(c, http.StatusForbidden, err)
	}
	users, err := h.App.Database.ListUsers()
	if err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, users)
}

// HandleUserRegister
//
//	@summary creates a new Seanime user (admin only).
//	@route /api/v1/user/register [POST]
//	@returns models.User
func (h *Handler) HandleUserRegister(c echo.Context) error {
	if err := h.RequireAdmin(c); err != nil {
		return h.RespondWithStatusError(c, http.StatusForbidden, err)
	}
	type body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	if strings.TrimSpace(b.Password) == "" {
		return h.RespondWithStatusError(c, http.StatusBadRequest, errors.New("password is required"))
	}
	// Whitelist the role — a typo'd/arbitrary string would silently create a user that is
	// neither admin nor regular. Empty defaults to a regular user.
	switch b.Role {
	case "":
		b.Role = models.UserRoleUser
	case models.UserRoleAdmin, models.UserRoleUser:
	default:
		return h.RespondWithStatusError(c, http.StatusBadRequest, errors.New("invalid role"))
	}
	u, err := h.App.Database.CreateUser(b.Username, b.Password, b.Role)
	if err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, u)
}

// HandleUserChangePassword
//
//	@summary changes the current user's password.
//	@desc Verifies the old password unless the user has none set yet (e.g. the bootstrapped admin).
//	@route /api/v1/user/change-password [POST]
//	@returns bool
func (h *Handler) HandleUserChangePassword(c echo.Context) error {
	u := h.CurrentUser(c)
	if u == nil {
		return h.RespondWithStatusError(c, http.StatusUnauthorized, errors.New("not logged in"))
	}
	type body struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	if strings.TrimSpace(b.NewPassword) == "" {
		return h.RespondWithStatusError(c, http.StatusBadRequest, errors.New("new password is required"))
	}
	if u.PasswordHash != "" && !db.CheckUserPassword(u, b.OldPassword) {
		return h.RespondWithStatusError(c, http.StatusForbidden, errors.New("current password is incorrect"))
	}
	if err := h.App.Database.SetUserPassword(u.ID, b.NewPassword); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}

// HandleSaveUserSettings
//
//	@summary saves the current user's settings overrides (multi-user profiles).
//	@desc Any logged-in user may save their own overrides; admin-only fields are never part of the payload.
//	@route /api/v1/user/settings [PATCH]
//	@returns bool
func (h *Handler) HandleSaveUserSettings(c echo.Context) error {
	userID := h.dataUserID(c)
	if userID == 0 {
		return h.RespondWithStatusError(c, http.StatusUnauthorized, errors.New("not logged in"))
	}
	// The client posts the same settings shape as /settings; we extract only the
	// user-overridable fields server-side, so admin-only fields can never be set here.
	var s models.Settings
	if err := c.Bind(&s); err != nil {
		return h.RespondWithError(c, err)
	}
	next := models.ExtractUserOverrides(&s)

	// Preserve the user's debrid-override fields (not part of the Settings payload).
	if existing, _ := h.App.Database.GetUserOverrides(userID); existing != nil {
		next.UseServerDebrid = existing.UseServerDebrid
		next.DebridProvider = existing.DebridProvider
		next.DebridApiKey = existing.DebridApiKey
		next.UseServerDebridAutoSelect = existing.UseServerDebridAutoSelect
	}

	if err := h.App.Database.UpsertUserOverrides(userID, next); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}

// HandleSaveUserDebrid
//
//	@summary saves the current user's debrid choice (use the server debrid, or their own).
//	@route /api/v1/user/debrid [PATCH]
//	@returns bool
func (h *Handler) HandleSaveUserDebrid(c echo.Context) error {
	userID := h.dataUserID(c)
	if userID == 0 {
		return h.RespondWithStatusError(c, http.StatusUnauthorized, errors.New("not logged in"))
	}
	type body struct {
		UseServerDebrid     bool   `json:"useServerDebrid"`
		Provider            string `json:"provider"`
		ApiKey              string `json:"apiKey"`
		UseServerAutoSelect bool   `json:"useServerAutoSelect"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	overrides, _ := h.App.Database.GetUserOverrides(userID)
	if overrides == nil {
		// Seed from the current server settings so existing per-user prefs aren't reset.
		serverSettings, _ := h.App.Database.GetSettings()
		overrides = models.ExtractUserOverrides(serverSettings)
	}
	overrides.UseServerDebrid = b.UseServerDebrid
	overrides.DebridProvider = b.Provider
	if b.ApiKey != "" { // blank = keep the existing key
		overrides.DebridApiKey = b.ApiKey
	}
	overrides.UseServerDebridAutoSelect = b.UseServerAutoSelect

	if err := h.App.Database.UpsertUserOverrides(userID, overrides); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}

// HandleUserDelete
//
//	@summary deletes a Seanime user (admin only). Admin users cannot be deleted.
//	@route /api/v1/user/:id [DELETE]
//	@returns bool
func (h *Handler) HandleUserDelete(c echo.Context) error {
	if err := h.RequireAdmin(c); err != nil {
		return h.RespondWithStatusError(c, http.StatusForbidden, err)
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return h.RespondWithError(c, err)
	}
	target, err := h.App.Database.GetUserByID(uint(id))
	if err != nil {
		return h.RespondWithError(c, err)
	}
	if target.Role == models.UserRoleAdmin {
		return h.RespondWithStatusError(c, http.StatusBadRequest, errors.New("cannot delete an admin user"))
	}
	if err := h.App.Database.DeleteUser(uint(id)); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}
