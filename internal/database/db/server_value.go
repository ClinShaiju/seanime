package db

import (
	"seanime/internal/database/models"

	"gorm.io/gorm/clause"
)

// GetServerValue returns the stored value for a key, or "" when unset.
func (db *Database) GetServerValue(key string) string {
	var v models.ServerValue
	if err := db.gormdb.Where("key = ?", key).First(&v).Error; err != nil {
		return ""
	}
	return v.Value
}

// SetServerValue upserts a key-value pair.
func (db *Database) SetServerValue(key string, value string) error {
	return db.gormdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&models.ServerValue{Key: key, Value: value}).Error
}
