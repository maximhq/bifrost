package tables

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TempToken is a short-lived, narrow-scope credential that authorizes access
// to a specific set of endpoints without requiring dashboard login.
//
// Each row is bound to a (scope, resource_id) pair: the scope names a set of
// allowed routes (registered in framework/temptoken), and the resource_id ties
// the token to the specific resource those routes act on (e.g. the OAuth flow
// ID for the mcp_auth scope). The plaintext token is hashed for lookup and
// encrypted at rest, matching the SessionsTable pattern.
type TempToken struct {
	ID               string    `gorm:"type:varchar(255);primaryKey" json:"id"`                    // UUID
	Token            string    `gorm:"type:text;not null" json:"-"`                               // encrypted at rest when encryption is enabled
	TokenHash        string    `gorm:"type:varchar(64);uniqueIndex:idx_temp_token_hash" json:"-"` // SHA-256 of plaintext for lookup
	Scope            string    `gorm:"type:varchar(64);index;not null" json:"scope"`              // e.g. "mcp_auth" — keys into the scope registry
	ResourceID       string    `gorm:"type:text;index" json:"resource_id,omitempty"`              // resource the scope binds to (semantics per scope); indexed for lifecycle-driven deletes
	ExpiresAt        time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
}

// TableName sets the table name for the model.
func (TempToken) TableName() string { return "temp_tokens" }

// BeforeSave hashes the plaintext for lookup and encrypts it for storage.
// Hash must be computed before encryption so it always covers the plaintext.
func (t *TempToken) BeforeSave(tx *gorm.DB) error {
	if t.Token != "" {
		t.TokenHash = encrypt.HashSHA256(t.Token)
	}
	if encrypt.IsEnabled() && t.Token != "" {
		if err := encryptString(&t.Token); err != nil {
			return fmt.Errorf("failed to encrypt temp token: %w", err)
		}
		t.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind decrypts the stored plaintext when encryption is in effect.
func (t *TempToken) AfterFind(tx *gorm.DB) error {
	if t.EncryptionStatus == EncryptionStatusEncrypted {
		if err := decryptString(&t.Token); err != nil {
			return fmt.Errorf("failed to decrypt temp token: %w", err)
		}
	}
	return nil
}
