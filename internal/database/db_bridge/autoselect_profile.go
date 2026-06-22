package db_bridge

import (
	"errors"
	"seanime/internal/database/db"
	"seanime/internal/database/models"
	"seanime/internal/library/anime"

	"github.com/goccy/go-json"
	"gorm.io/gorm"
)

// FindAutoSelectProfile returns the given user's auto-select profile and whether one
// exists (DbID != 0).
func FindAutoSelectProfile(db *db.Database, userID uint) (*anime.AutoSelectProfile, bool) {
	profile, err := GetAutoSelectProfile(db, userID)
	return profile, err == nil && profile.DbID != 0
}

// GetAutoSelectProfile returns the user's auto-select profile if it exists.
func GetAutoSelectProfile(db *db.Database, userID uint) (*anime.AutoSelectProfile, error) {
	var res models.AutoSelectProfile
	err := db.Gorm().Where("user_id = ?", userID).First(&res).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &anime.AutoSelectProfile{}, nil
		}
		return nil, err
	}

	// Unmarshal the data
	var profile anime.AutoSelectProfile
	if err := json.Unmarshal(res.Value, &profile); err != nil {
		return nil, err
	}
	profile.DbID = res.ID

	return &profile, nil
}

// SaveAutoSelectProfile saves or updates the user's auto-select profile (one per user).
func SaveAutoSelectProfile(db *db.Database, userID uint, profile *anime.AutoSelectProfile) error {
	// Marshal the data
	bytes, err := json.Marshal(profile)
	if err != nil {
		return err
	}

	// Check if a profile already exists for this user
	var existing models.AutoSelectProfile
	err = db.Gorm().Where("user_id = ?", userID).First(&existing).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Create new profile
		return db.Gorm().Create(&models.AutoSelectProfile{
			UserID: userID,
			Value:  bytes,
		}).Error
	} else if err != nil {
		return err
	}

	// Update existing profile
	return db.Gorm().Model(&models.AutoSelectProfile{}).
		Where("id = ?", existing.ID).
		Update("value", bytes).Error
}

// DeleteAutoSelectProfile deletes the user's auto-select profile.
func DeleteAutoSelectProfile(db *db.Database, userID uint) error {
	return db.Gorm().Where("user_id = ?", userID).Delete(&models.AutoSelectProfile{}).Error
}

// GetServerAutoSelectProfile returns the server-default (admin's) auto-select profile.
// Used by server-wide callers (plugins, torrentstream, the debrid server-default path)
// that aren't scoped to a particular logged-in user.
func GetServerAutoSelectProfile(database *db.Database) (*anime.AutoSelectProfile, bool) {
	adminID := uint(0)
	if admin, err := database.GetAdminUser(); err == nil && admin != nil {
		adminID = admin.ID
	}
	return FindAutoSelectProfile(database, adminID)
}
