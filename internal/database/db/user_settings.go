package db

import (
	"seanime/internal/database/models"

	"github.com/goccy/go-json"
)

// GetUserOverrides returns a user's settings overrides, or nil if they have none yet.
func (db *Database) GetUserOverrides(userID uint) (*models.UserOverrides, error) {
	var row models.UserSettings
	err := db.gormdb.Where("user_id = ?", userID).Limit(1).Find(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == 0 || len(row.Value) == 0 {
		return nil, nil
	}
	var o models.UserOverrides
	if err := json.Unmarshal(row.Value, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// UpsertUserOverrides saves a user's settings overrides (one row per user).
func (db *Database) UpsertUserOverrides(userID uint, overrides *models.UserOverrides) error {
	data, err := json.Marshal(overrides)
	if err != nil {
		return err
	}

	var existing models.UserSettings
	if err := db.gormdb.Where("user_id = ?", userID).Limit(1).Find(&existing).Error; err == nil && existing.ID != 0 {
		existing.Value = data
		return db.gormdb.Save(&existing).Error
	}
	return db.gormdb.Create(&models.UserSettings{UserID: userID, Value: data}).Error
}
