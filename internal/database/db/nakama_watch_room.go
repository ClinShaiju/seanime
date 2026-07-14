package db

import "seanime/internal/database/models"

// UpsertNakamaWatchRoom stores (or replaces) a persisted watch room by its room id.
func (db *Database) UpsertNakamaWatchRoom(rec *models.NakamaWatchRoom) error {
	var existing models.NakamaWatchRoom
	err := db.gormdb.Where("room_id = ?", rec.RoomID).First(&existing).Error
	if err == nil {
		rec.ID = existing.ID
		rec.CreatedAt = existing.CreatedAt
		return db.gormdb.Save(rec).Error
	}
	return db.gormdb.Create(rec).Error
}

// GetAllNakamaWatchRooms returns every persisted watch room (used to rehydrate on boot).
func (db *Database) GetAllNakamaWatchRooms() ([]*models.NakamaWatchRoom, error) {
	var recs []*models.NakamaWatchRoom
	if err := db.gormdb.Find(&recs).Error; err != nil {
		return nil, err
	}
	return recs, nil
}

// DeleteNakamaWatchRoom removes a persisted watch room (on host-close / reap / empty).
func (db *Database) DeleteNakamaWatchRoom(roomID string) {
	_ = db.gormdb.Where("room_id = ?", roomID).Delete(&models.NakamaWatchRoom{}).Error
}
