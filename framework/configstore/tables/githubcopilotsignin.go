package tables

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// GitHub Copilot sign-in statuses. Only an active row supplies a token to requests.
const (
	GithubCopilotSignInActive      = "active"
	GithubCopilotSignInNeedsReauth = "needs_reauth"
)

// TableGithubCopilotSignIn holds the GitHub user token for one GitHub Copilot provider key
// that uses "Sign in with GitHub". GitHub rotates the refresh token on every use and the
// previous pair stops working, so the row is the single source of truth across nodes:
// Version guards every write and RefreshLeaseUntil lets one node refresh at a time.
type TableGithubCopilotSignIn struct {
	KeyID             string     `gorm:"type:varchar(255);primaryKey" json:"key_id"`
	ClientID          string     `gorm:"type:varchar(255);not null" json:"client_id"` // GitHub App client ID that issued the token, needed to refresh it
	AccessToken       string     `gorm:"type:text;not null" json:"-"`                 // ghu_ user token, encrypted at rest
	RefreshToken      string     `gorm:"type:text" json:"-"`                          // ghr_ refresh token, encrypted; empty when the App does not expire tokens
	AccessExpiresAt   *time.Time `json:"access_expires_at,omitempty"`                 // nil when the token does not expire
	RefreshExpiresAt  *time.Time `json:"refresh_expires_at,omitempty"`
	GithubLogin       string     `gorm:"type:varchar(255)" json:"github_login"`
	Status            string     `gorm:"type:varchar(20);not null;default:'active'" json:"status"`
	StatusReason      string     `gorm:"type:text" json:"status_reason,omitempty"`
	Version           int64      `gorm:"not null;default:0" json:"version"`
	RefreshLeaseUntil *time.Time `json:"-"`
	EncryptionStatus  string     `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt         time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt         time.Time  `gorm:"not null" json:"updated_at"`
}

// TableName sets the table name
func (TableGithubCopilotSignIn) TableName() string {
	return "github_copilot_sign_ins"
}

// BeforeSave encrypts the tokens when encryption is enabled.
func (t *TableGithubCopilotSignIn) BeforeSave(tx *gorm.DB) error {
	if t.Status == "" {
		t.Status = GithubCopilotSignInActive
	}
	if encrypt.IsEnabled() {
		if err := encryptString(&t.AccessToken); err != nil {
			return fmt.Errorf("failed to encrypt github copilot access token: %w", err)
		}
		if err := encryptString(&t.RefreshToken); err != nil {
			return fmt.Errorf("failed to encrypt github copilot refresh token: %w", err)
		}
		t.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind decrypts the tokens.
func (t *TableGithubCopilotSignIn) AfterFind(tx *gorm.DB) error {
	if t.EncryptionStatus == EncryptionStatusEncrypted {
		if err := decryptString(&t.AccessToken); err != nil {
			return fmt.Errorf("failed to decrypt github copilot access token: %w", err)
		}
		if err := decryptString(&t.RefreshToken); err != nil {
			return fmt.Errorf("failed to decrypt github copilot refresh token: %w", err)
		}
	}
	return nil
}
