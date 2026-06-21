package db

import (
	"seanime/internal/database/models"

	"github.com/goccy/go-json"
)

// Theme is per-user (multi-user profiles). Reads/writes are scoped by userID.
//
// ponytail: no in-memory cache — a single indexed row per user is cheap, and a
// global cache would have to become per-user to stay correct. Add a per-user cache
// only if a profiler says theme reads matter.

// GetTheme returns the user's theme, or an empty (default) theme if they have none.
func (db *Database) GetTheme(userID uint) (*models.Theme, error) {
	var theme models.Theme
	err := db.gormdb.Where("user_id = ?", userID).Limit(1).Find(&theme).Error
	if err != nil {
		return nil, err
	}
	theme.UserID = userID
	return &theme, nil
}

// GetThemeCopy returns a copy of the user's theme with HomeItems removed.
func (db *Database) GetThemeCopy(userID uint) (*models.Theme, error) {
	theme, err := db.GetTheme(userID)
	if err != nil {
		return nil, err
	}

	marshaledTheme, err := json.Marshal(theme)
	if err != nil {
		return nil, err
	}

	var themeCopy models.Theme
	if err := json.Unmarshal(marshaledTheme, &themeCopy); err != nil {
		return nil, err
	}

	return &themeCopy, nil
}

// UpsertTheme creates or updates the given user's theme row.
func (db *Database) UpsertTheme(userID uint, settings *models.Theme) (*models.Theme, error) {
	settings.UserID = userID

	// Resolve the existing row for this user so Save updates it instead of inserting.
	var existing models.Theme
	if err := db.gormdb.Where("user_id = ?", userID).Limit(1).Find(&existing).Error; err == nil && existing.ID != 0 {
		settings.ID = existing.ID
	} else {
		settings.ID = 0
	}

	if err := db.gormdb.Save(settings).Error; err != nil {
		db.Logger.Error().Err(err).Msg("db: Failed to save theme in the database")
		return nil, err
	}

	db.Logger.Debug().Msg("db: Theme saved")
	return settings, nil
}
