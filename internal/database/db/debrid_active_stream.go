package db

import "seanime/internal/database/models"

// UpsertDebridActiveStream stores (or replaces) a user's last active debrid stream.
func (db *Database) UpsertDebridActiveStream(rec *models.DebridActiveStream) error {
	var existing models.DebridActiveStream
	err := db.gormdb.Where("user_id = ?", rec.UserID).First(&existing).Error
	if err == nil {
		rec.ID = existing.ID
		rec.CreatedAt = existing.CreatedAt
		return db.gormdb.Save(rec).Error
	}
	return db.gormdb.Create(rec).Error
}

// GetDebridActiveStream returns a user's persisted active stream, if any.
func (db *Database) GetDebridActiveStream(userID uint) (*models.DebridActiveStream, bool) {
	var rec models.DebridActiveStream
	if err := db.gormdb.Where("user_id = ?", userID).First(&rec).Error; err != nil {
		return nil, false
	}
	return &rec, true
}

// DeleteDebridActiveStream clears a user's persisted active stream.
func (db *Database) DeleteDebridActiveStream(userID uint) {
	_ = db.gormdb.Where("user_id = ?", userID).Delete(&models.DebridActiveStream{}).Error
}
