package db

import (
	"seanime/internal/database/models"
	"seanime/internal/util"
	"testing"
	"time"
)

// TestUserSessionLifecycle is the ponytail self-check for the multi-user identity
// layer: bcrypt round-trip, session resolution + expiry, and delete cascade.
func TestUserSessionLifecycle(t *testing.T) {
	database, err := NewDatabase(t.TempDir(), "user_test", util.NewLogger())
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	// Close the connection before t.TempDir cleanup so Windows can unlink the file.
	t.Cleanup(func() {
		if sqlDB, err := database.Gorm().DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	// Create a user; password must verify and the hash must not be the plaintext.
	u, err := database.CreateUser("bob", "hunter2", models.UserRoleUser)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Role != models.UserRoleUser {
		t.Fatalf("role = %q, want %q", u.Role, models.UserRoleUser)
	}
	if u.PasswordHash == "" || u.PasswordHash == "hunter2" {
		t.Fatalf("password was not hashed: %q", u.PasswordHash)
	}
	if !CheckUserPassword(u, "hunter2") {
		t.Fatalf("CheckUserPassword: correct password rejected")
	}
	if CheckUserPassword(u, "wrong") {
		t.Fatalf("CheckUserPassword: wrong password accepted")
	}

	// Unique username.
	if _, err := database.CreateUser("bob", "x", models.UserRoleUser); err == nil {
		t.Fatalf("CreateUser: expected duplicate-username error")
	}

	// HasRegularUsers true now; admin has empty password and cannot log in.
	if !database.HasRegularUsers() {
		t.Fatalf("HasRegularUsers = false, want true")
	}
	admin, err := database.CreateUser("admin", "", models.UserRoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}
	if CheckUserPassword(admin, "") || CheckUserPassword(admin, "anything") {
		t.Fatalf("empty-password user must not authenticate")
	}

	// SetAdminCredential renames the existing admin and sets a usable password.
	updated, err := database.SetAdminCredential("cvslinc", "s3cret-pw")
	if err != nil {
		t.Fatalf("SetAdminCredential: %v", err)
	}
	if updated.Username != "cvslinc" {
		t.Fatalf("admin not renamed: %q", updated.Username)
	}
	if reFetched, _ := database.GetAdminUser(); reFetched == nil || !CheckUserPassword(reFetched, "s3cret-pw") {
		t.Fatalf("SetAdminCredential: password not applied")
	}

	// Session resolves to the right user.
	sess, err := database.CreateSession(u.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := database.GetSessionUser(sess.Token)
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetSessionUser = %+v, %v; want user %d", got, err, u.ID)
	}

	// Expired session is rejected.
	expired := &models.Session{Token: "expired-token", UserID: u.ID, ExpiresAt: time.Now().Add(-time.Hour)}
	if err := database.gormdb.Create(expired).Error; err != nil {
		t.Fatalf("seed expired session: %v", err)
	}
	if _, err := database.GetSessionUser("expired-token"); err == nil {
		t.Fatalf("GetSessionUser: expired token accepted")
	}

	// Deleting the user revokes their sessions.
	if err := database.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := database.GetSessionUser(sess.Token); err == nil {
		t.Fatalf("GetSessionUser: session survived user deletion")
	}
	if _, err := database.GetUserByID(u.ID); err == nil {
		t.Fatalf("GetUserByID: deleted user still present")
	}
}
