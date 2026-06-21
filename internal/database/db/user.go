package db

import (
	"errors"
	"seanime/internal/database/models"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// CreateUser creates a new user with a bcrypt-hashed password.
// An empty password is allowed (the admin's identity is also covered by the server
// password gate); such a user cannot log in via username/password until a password
// is set.
func (db *Database) CreateUser(username, password, role string) (*models.User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	if role != models.UserRoleAdmin {
		role = models.UserRoleUser
	}

	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	u := &models.User{Username: username, PasswordHash: hash, Role: role}
	if err := db.gormdb.Create(u).Error; err != nil {
		return nil, err
	}
	return u, nil
}

func (db *Database) GetUserByUsername(username string) (*models.User, error) {
	var u models.User
	if err := db.gormdb.Where("username = ?", strings.TrimSpace(username)).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (db *Database) GetUserByID(id uint) (*models.User, error) {
	var u models.User
	if err := db.gormdb.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (db *Database) ListUsers() ([]*models.User, error) {
	var users []*models.User
	err := db.gormdb.Order("id asc").Find(&users).Error
	return users, err
}

func (db *Database) CountUsers() (int64, error) {
	var n int64
	err := db.gormdb.Model(&models.User{}).Count(&n).Error
	return n, err
}

// HasRegularUsers reports whether any non-admin user exists. Used to decide whether
// multi-user mode is "active" (vs. a legacy single-admin install).
func (db *Database) HasRegularUsers() bool {
	var n int64
	if err := db.gormdb.Model(&models.User{}).Where("role = ?", models.UserRoleUser).Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// GetAdminUser returns the first admin user (the server owner), if any.
func (db *Database) GetAdminUser() (*models.User, error) {
	var u models.User
	if err := db.gormdb.Where("role = ?", models.UserRoleAdmin).Order("id asc").First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteUser removes a user and revokes all of their sessions.
func (db *Database) DeleteUser(id uint) error {
	_ = db.gormdb.Where("user_id = ?", id).Delete(&models.Session{}).Error
	return db.gormdb.Delete(&models.User{}, id).Error
}

// SetUserPassword updates a user's password (bcrypt). An empty password clears it.
func (db *Database) SetUserPassword(id uint, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return db.gormdb.Model(&models.User{}).Where("id = ?", id).Update("password_hash", hash).Error
}

// LinkAnilistAccount links a user to an AniList Account row.
func (db *Database) LinkAnilistAccount(userID uint, accountID uint) error {
	return db.gormdb.Model(&models.User{}).Where("id = ?", userID).Update("anilist_account_id", accountID).Error
}

// SetAdminCredential creates the admin user if none exists, or updates the first
// admin's username and password. Used for first-time admin setup and recovery.
func (db *Database) SetAdminCredential(username, password string) (*models.User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		username = "admin"
	}

	admin, err := db.GetAdminUser()
	if err != nil || admin == nil {
		return db.CreateUser(username, password, models.UserRoleAdmin)
	}

	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	if err := db.gormdb.Model(&models.User{}).Where("id = ?", admin.ID).
		Updates(map[string]interface{}{"username": username, "password_hash": hash}).Error; err != nil {
		return nil, err
	}
	admin.Username = username
	admin.PasswordHash = hash
	return admin, nil
}

// CheckUserPassword verifies a plaintext password against the user's hash.
// A user with no password hash can never be authenticated this way.
func CheckUserPassword(u *models.User, password string) bool {
	if u == nil || u.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

func hashPassword(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
