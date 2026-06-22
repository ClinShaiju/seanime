package db

import (
	"errors"
	"seanime/internal/database/models"

	"gorm.io/gorm/clause"
)

var accountCache *models.Account

func (db *Database) UpsertAccount(acc *models.Account) (*models.Account, error) {
	err := db.gormdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(acc).Error

	if err != nil {
		db.Logger.Error().Err(err).Msg("Failed to save account in the database")
		return nil, err
	}

	if acc.Username != "" {
		accountCache = acc
	} else {
		accountCache = nil
	}

	return acc, nil
}

// GetAccount returns the admin's (server owner's) AniList account. This is the
// app-global account used by the admin session and all legacy single-user paths.
//
// In a multi-user install there are several Account rows (one per linked AniList);
// the bare `Last` lookup would return whichever user linked most recently, so we
// resolve the admin's linked account first and only fall back to `Last` for a
// legacy single-user DB where the admin link hasn't been backfilled yet.
func (db *Database) GetAccount() (*models.Account, error) {

	if accountCache != nil {
		return accountCache, nil
	}

	// Prefer the admin's explicitly-linked account (multi-user correctness).
	if admin, err := db.GetAdminUser(); err == nil && admin != nil && admin.AnilistAccountID != nil {
		if acc, err := db.GetAccountByID(*admin.AnilistAccountID); err == nil {
			accountCache = acc
			return acc, nil
		}
	}

	var acc models.Account
	err := db.gormdb.Last(&acc).Error
	if err != nil {
		return nil, err
	}
	if acc.Username == "" || acc.Token == "" || acc.Viewer == nil {
		return nil, errors.New("account not found")
	}

	accountCache = &acc

	return &acc, err
}

// GetAccountByID fetches a specific AniList Account row. It does not use the global
// account cache (which holds only the admin's account), so it is safe for per-user
// session resolution.
func (db *Database) GetAccountByID(id uint) (*models.Account, error) {
	var acc models.Account
	if err := db.gormdb.First(&acc, id).Error; err != nil {
		return nil, err
	}
	if acc.Username == "" || acc.Token == "" || acc.Viewer == nil {
		return nil, errors.New("account not found")
	}
	return &acc, nil
}

// GetAccountForUser resolves the AniList account linked to a user (via
// User.AnilistAccountID). Returns an error when the user has not linked one.
func (db *Database) GetAccountForUser(u *models.User) (*models.Account, error) {
	if u == nil || u.AnilistAccountID == nil {
		return nil, errors.New("account not found")
	}
	return db.GetAccountByID(*u.AnilistAccountID)
}

// UpsertAccountForUser creates or updates the AniList account linked to a user and
// ensures the link exists. Unlike UpsertAccount it never writes the global
// accountCache (that belongs to the admin) and never forces id=1, so each user owns
// an independent Account row. Returns the saved account.
func (db *Database) UpsertAccountForUser(userID uint, username, token string, viewer []byte) (*models.Account, error) {
	u, err := db.GetUserByID(userID)
	if err != nil {
		return nil, err
	}

	acc := &models.Account{Username: username, Token: token, Viewer: viewer}
	if u.AnilistAccountID != nil {
		acc.ID = *u.AnilistAccountID
	}

	if err := db.gormdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(acc).Error; err != nil {
		return nil, err
	}

	if u.AnilistAccountID == nil || *u.AnilistAccountID != acc.ID {
		if err := db.LinkAnilistAccount(userID, acc.ID); err != nil {
			return nil, err
		}
	}

	// If this user is the admin, the app-global cache must reflect the change.
	if u.Role == models.UserRoleAdmin {
		accountCache = acc
	}

	return acc, nil
}

// GetAnilistToken retrieves the AniList token from the account or returns an empty string
func (db *Database) GetAnilistToken() string {
	acc, err := db.GetAccount()
	if err != nil {
		return ""
	}
	return acc.Token
}
