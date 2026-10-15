package tables

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// Managed-client identities: which principal a token minted from an ID-JAG acts as.
const (
	// ManagedClientIdentityVK binds every token to one virtual key.
	ManagedClientIdentityVK = "vk"
	// ManagedClientIdentityUser maps the ID-JAG subject to a Bifrost user (needs an identity resolver).
	ManagedClientIdentityUser = "user"
)

// TableOAuth2ManagedClient is an administrator-registered client allowed to use
// the jwt-bearer (ID-JAG) grant. It is separate from the DCR table because the
// IdP pins the client_id inside each assertion, so the client must exist before
// the first assertion arrives, and it must not be collected by the DCR sweeper.
// ClientID is free-form and may be a client ID metadata document URL, so it is
// looked up through ClientIDHash: text columns carry no portable unique index.
type TableOAuth2ManagedClient struct {
	ID               string     `gorm:"type:varchar(255);primaryKey" json:"id"`
	ClientID         string     `gorm:"type:text;not null" json:"client_id"`
	ClientIDHash     string     `gorm:"type:varchar(64);uniqueIndex;not null" json:"-"` // SHA-256 of ClientID
	Name             string     `gorm:"type:varchar(255)" json:"name"`
	ClientSecretHash string     `gorm:"type:varchar(64)" json:"-"` // SHA-256 of the secret; "" = public client
	Identity         string     `gorm:"type:varchar(16);not null" json:"identity"`
	VirtualKeyID     string     `gorm:"type:varchar(255)" json:"virtual_key_id,omitempty"`
	Disabled         bool       `gorm:"not null;default:false" json:"disabled"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	CreatedAt        time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"not null" json:"updated_at"`
}

// TableName returns the managed-client table name.
func (TableOAuth2ManagedClient) TableName() string { return "oauth2_managed_clients" }

// BeforeSave keeps ClientIDHash in step with ClientID.
func (c *TableOAuth2ManagedClient) BeforeSave(tx *gorm.DB) error {
	c.ClientIDHash = ManagedClientIDHash(c.ClientID)
	return nil
}

// HasSecret reports whether the client is confidential.
func (c *TableOAuth2ManagedClient) HasSecret() bool { return c.ClientSecretHash != "" }

// OAuth2ManagedClientPatch names the columns an update writes; a nil field is
// left untouched, so concurrent edits to different fields never clobber each
// other and a secret rotation cannot resurrect a client disabled in between.
type OAuth2ManagedClientPatch struct {
	Name             *string
	ClientSecretHash *string
	Identity         *string
	VirtualKeyID     *string
	Disabled         *bool
}

// ManagedClientIDHash returns the lookup hash for a managed client_id.
func ManagedClientIDHash(clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	return hex.EncodeToString(sum[:])
}
