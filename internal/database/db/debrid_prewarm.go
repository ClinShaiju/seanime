package db

import "seanime/internal/database/models"

// debridPrewarmKeyWhere is the shared lookup for the (account, media, episode, anidb, profile) tuple.
const debridPrewarmKeyWhere = "account_hash = ? AND media_id = ? AND episode_number = ? AND anidb_episode = ? AND profile_hash = ?"

// UpsertDebridPrewarm stores (or replaces) a shared prewarm row keyed by its account/content/profile.
func (db *Database) UpsertDebridPrewarm(rec *models.DebridPrewarm) error {
	var existing models.DebridPrewarm
	err := db.gormdb.Where(debridPrewarmKeyWhere,
		rec.AccountHash, rec.MediaId, rec.EpisodeNumber, rec.AniDBEpisode, rec.ProfileHash,
	).First(&existing).Error
	if err == nil {
		rec.ID = existing.ID
		rec.CreatedAt = existing.CreatedAt
		return db.gormdb.Save(rec).Error
	}
	return db.gormdb.Create(rec).Error
}

// GetDebridPrewarm returns a shared prewarm row for the given key, if any.
func (db *Database) GetDebridPrewarm(accountHash string, mediaId, episodeNumber int, anidbEpisode, profileHash string) (*models.DebridPrewarm, bool) {
	var rec models.DebridPrewarm
	if err := db.gormdb.Where(debridPrewarmKeyWhere,
		accountHash, mediaId, episodeNumber, anidbEpisode, profileHash,
	).First(&rec).Error; err != nil {
		return nil, false
	}
	return &rec, true
}

// ListDebridPrewarms returns all shared prewarm rows (used by the TTL sweeper; the table is small,
// bounded by distinct prewarmed episodes per account).
func (db *Database) ListDebridPrewarms() ([]*models.DebridPrewarm, error) {
	var res []*models.DebridPrewarm
	err := db.gormdb.Find(&res).Error
	return res, err
}

// DeleteDebridPrewarmByID removes one shared prewarm row.
func (db *Database) DeleteDebridPrewarmByID(id uint) error {
	return db.gormdb.Delete(&models.DebridPrewarm{}, id).Error
}
