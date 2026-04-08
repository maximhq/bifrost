package tables

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableSSOProvider stores OIDC or SAML identity provider configuration.
type TableSSOProvider struct {
	ID      string `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name    string `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"`
	Type    string `gorm:"type:varchar(20);not null;index" json:"type"` // "oidc" | "saml"
	Enabled bool   `gorm:"default:true" json:"enabled"`

	// OIDC fields
	IssuerURL    string `gorm:"type:text" json:"issuer_url,omitempty"`
	ClientID     string `gorm:"type:varchar(512)" json:"client_id,omitempty"`
	ClientSecret string `gorm:"type:text" json:"-"` // encrypted at rest
	Scopes       string `gorm:"type:text" json:"scopes,omitempty"` // JSON array: ["openid","email","profile"]

	// SAML fields
	EntityID    string `gorm:"type:text" json:"entity_id,omitempty"`
	SSOURL      string `gorm:"type:text" json:"sso_url,omitempty"`
	Certificate string `gorm:"type:text" json:"-"` // IdP certificate PEM

	// Role mapping
	DefaultRole     string `gorm:"type:varchar(255)" json:"default_role"` // RBAC role for new SSO users
	RoleMappingJSON string `gorm:"type:text" json:"-"`                    // JSON: {group_name → role}

	// SCIM
	SCIMEnabled  bool   `gorm:"default:false" json:"scim_enabled"`
	SCIMToken    string `gorm:"type:text" json:"-"` // hashed bearer token for SCIM requests

	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for TableSSOProvider
func (TableSSOProvider) TableName() string { return "sso_providers" }

// BeforeSave encrypts sensitive fields
func (s *TableSSOProvider) BeforeSave(tx *gorm.DB) error {
	if !encrypt.IsEnabled() {
		return nil
	}
	encrypted := false
	if s.ClientSecret != "" {
		if err := encryptString(&s.ClientSecret); err != nil {
			return fmt.Errorf("failed to encrypt SSO client secret: %w", err)
		}
		encrypted = true
	}
	if s.Certificate != "" {
		if err := encryptString(&s.Certificate); err != nil {
			return fmt.Errorf("failed to encrypt SSO certificate: %w", err)
		}
		encrypted = true
	}
	if s.SCIMToken != "" {
		if err := encryptString(&s.SCIMToken); err != nil {
			return fmt.Errorf("failed to encrypt SCIM token: %w", err)
		}
		encrypted = true
	}
	if encrypted {
		s.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind decrypts sensitive fields
func (s *TableSSOProvider) AfterFind(tx *gorm.DB) error {
	if s.EncryptionStatus != EncryptionStatusEncrypted {
		return nil
	}
	if err := decryptString(&s.ClientSecret); err != nil {
		return fmt.Errorf("failed to decrypt SSO client secret: %w", err)
	}
	if err := decryptString(&s.Certificate); err != nil {
		return fmt.Errorf("failed to decrypt SSO certificate: %w", err)
	}
	if err := decryptString(&s.SCIMToken); err != nil {
		return fmt.Errorf("failed to decrypt SCIM token: %w", err)
	}
	return nil
}

// TableExternalUser stores users provisioned via SSO or SCIM.
type TableExternalUser struct {
	ID          string     `gorm:"primaryKey;type:varchar(255)" json:"id"`
	ExternalID  string     `gorm:"type:varchar(512);uniqueIndex;not null" json:"external_id"` // IdP subject claim
	ProviderID  string     `gorm:"type:varchar(255);index;not null" json:"provider_id"`
	Email       string     `gorm:"type:varchar(512);index;not null" json:"email"`
	DisplayName string     `gorm:"type:varchar(512)" json:"display_name"`
	Active      bool       `gorm:"default:true" json:"active"`
	LastLoginAt *time.Time `gorm:"index" json:"last_login_at,omitempty"`
	SCIMVersion int64      `gorm:"default:0" json:"scim_version"` // monotonic version for SCIM etag
	CreatedAt   time.Time  `gorm:"index;not null" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for TableExternalUser
func (TableExternalUser) TableName() string { return "sso_external_users" }

// TableSSOSession stores active SSO-originated sessions.
type TableSSOSession struct {
	ID         string    `gorm:"primaryKey;type:varchar(255)" json:"id"` // session token (hashed)
	UserID     string    `gorm:"type:varchar(255);index;not null" json:"user_id"`
	ProviderID string    `gorm:"type:varchar(255);index" json:"provider_id"`
	ExpiresAt  time.Time `gorm:"index;not null" json:"expires_at"`
	IP         string    `gorm:"type:varchar(64)" json:"ip,omitempty"`
	UserAgent  string    `gorm:"type:text" json:"user_agent,omitempty"`
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
}

// TableName sets the table name for TableSSOSession
func (TableSSOSession) TableName() string { return "sso_sessions" }
