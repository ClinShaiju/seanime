package db

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"seanime/internal/database/models"
	"time"
)

// sessionDuration is how long an issued session token stays valid.
const sessionDuration = 30 * 24 * time.Hour

// CreateSession issues a new server-side session token for a user.
func (db *Database) CreateSession(userID uint) (*models.Session, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	s := &models.Session{
		Token:     hex.EncodeToString(tokenBytes),
		UserID:    userID,
		ExpiresAt: time.Now().Add(sessionDuration),
	}
	if err := db.gormdb.Create(s).Error; err != nil {
		return nil, err
	}
	return s, nil
}

// GetSessionUser resolves a session token to its user, or an error if the token is
// missing or expired. Expired tokens are deleted on access.
func (db *Database) GetSessionUser(token string) (*models.User, error) {
	if token == "" {
		return nil, errors.New("empty session token")
	}
	var s models.Session
	if err := db.gormdb.Where("token = ?", token).First(&s).Error; err != nil {
		return nil, err
	}
	if time.Now().After(s.ExpiresAt) {
		_ = db.gormdb.Delete(&models.Session{}, s.ID).Error
		return nil, errors.New("session expired")
	}
	return db.GetUserByID(s.UserID)
}

// DeleteSession revokes a single session token (logout).
func (db *Database) DeleteSession(token string) error {
	return db.gormdb.Where("token = ?", token).Delete(&models.Session{}).Error
}

// CleanupExpiredSessions deletes all expired session rows.
func (db *Database) CleanupExpiredSessions() error {
	return db.gormdb.Where("expires_at < ?", time.Now()).Delete(&models.Session{}).Error
}
