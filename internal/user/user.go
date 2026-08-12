package user

import (
	"errors"
	"seanime/internal/api/anilist"
	"seanime/internal/database/models"

	"github.com/goccy/go-json"
)

const SimulatedUserToken = "SIMULATED"

type User struct {
	Viewer *anilist.GetViewer_Viewer `json:"viewer"`
	Token  string                    `json:"token"`
	// IsSimulated indicates whether the user is not a real AniList account.
	IsSimulated bool `json:"isSimulated"`
}

// NewUser creates a new User entity from a models.User
// This is returned to the client
func NewUser(model *models.Account) (*User, error) {
	if model == nil {
		return nil, errors.New("account is nil")
	}
	var acc anilist.GetViewer_Viewer
	if err := json.Unmarshal(model.Viewer, &acc); err != nil {
		return nil, err
	}
	return &User{
		Viewer: &acc,
		Token:  model.Token,
	}, nil
}

// NewSimulatedUserNamed is NewSimulatedUser carrying a display name — the Seanime profile
// name on a multi-user server. Without it every profile that hasn't linked an AniList
// account renders as the literal string "User", so separate logins are indistinguishable
// in the UI even though the server resolves them as different users.
func NewSimulatedUserNamed(name string) *User {
	u := NewSimulatedUser()
	if name != "" {
		u.Viewer.Name = name
	}
	return u
}

func NewSimulatedUser() *User {
	acc := anilist.GetViewer_Viewer{
		Name:        "User",
		Avatar:      nil,
		BannerImage: nil,
		IsBlocked:   nil,
		Options:     nil,
	}
	return &User{
		Viewer:      &acc,
		Token:       SimulatedUserToken,
		IsSimulated: true,
	}
}
