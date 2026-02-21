package tables

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableOauthConfig represents an OAuth configuration in the database
// This stores the OAuth client configuration and flow state
type TableOauthConfig struct {
	ID              string    `gorm:"type:varchar(255);primaryKey" json:"id"`           // UUID
	ClientID        string    `gorm:"type:varchar(512)" json:"client_id"`               // OAuth provider's client ID (optional for public clients)
	ClientSecret    string    `gorm:"type:text" json:"-"`                               // Encrypted OAuth client secret (optional for public clients)
	AuthorizeURL    string    `gorm:"type:text" json:"authorize_url"`                   // Provider's authorization endpoint (optional, can be discovered)
	TokenURL        string    `gorm:"type:text" json:"token_url"`                       // Provider's token endpoint (optional, can be discovered)
	RegistrationURL *string   `gorm:"type:text" json:"registration_url,omitempty"`      // Provider's dynamic registration endpoint (optional, can be discovered)
	RedirectURI     string    `gorm:"type:text;not null" json:"redirect_uri"`           // Callback URL
	Scopes          string    `gorm:"type:text" json:"scopes"`                          // JSON array of scopes (optional, can be discovered)
	State           string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"-"`  // CSRF state token
	CodeVerifier    string    `gorm:"type:varchar(255)" json:"-"`                       // PKCE code verifier (generated, kept secret)
	CodeChallenge   string    `gorm:"type:varchar(255)" json:"code_challenge"`          // PKCE code challenge (sent to provider)
	Status          string    `gorm:"type:varchar(50);not null;index" json:"status"`    // "pending", "authorized", "failed", "expired", "revoked"
	TokenID         *string   `gorm:"type:varchar(255);index" json:"token_id"`          // Foreign key to oauth_tokens.ID (set after callback)
	ServerURL            string  `gorm:"type:text" json:"server_url"`                      // MCP server URL for OAuth discovery
	UseDiscovery         bool    `gorm:"default:false" json:"use_discovery"`               // Flag to enable OAuth discovery
	MCPClientConfigJSON  *string `gorm:"type:text" json:"-"`                               // JSON serialized MCPClientConfig for multi-instance support (pending MCP client waiting for OAuth completion)
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`
	ExpiresAt        time.Time `gorm:"index;not null" json:"expires_at"` // State expiry (15 min)
}

// TableName sets the table name
func (TableOauthConfig) TableName() string {
	return "oauth_configs"
}

// BeforeSave hook
func (c *TableOauthConfig) BeforeSave(tx *gorm.DB) error {
	// Ensure status is valid
	if c.Status == "" {
		c.Status = "pending"
	}

	// Encrypt sensitive fields
	if encrypt.IsEnabled() {
		encrypted := false
		if c.ClientSecret != "" {
			if err := encryptString(&c.ClientSecret); err != nil {
				return fmt.Errorf("failed to encrypt oauth client secret: %w", err)
			}
			encrypted = true
		}
		if c.CodeVerifier != "" {
			if err := encryptString(&c.CodeVerifier); err != nil {
				return fmt.Errorf("failed to encrypt oauth code verifier: %w", err)
			}
			encrypted = true
		}
		if encrypted {
			c.EncryptionStatus = EncryptionStatusEncrypted
		}
	}
	return nil
}

// AfterFind hook to decrypt sensitive fields
func (c *TableOauthConfig) AfterFind(tx *gorm.DB) error {
	if c.EncryptionStatus == EncryptionStatusEncrypted {
		if err := decryptString(&c.ClientSecret); err != nil {
			return fmt.Errorf("failed to decrypt oauth client secret: %w", err)
		}
		if err := decryptString(&c.CodeVerifier); err != nil {
			return fmt.Errorf("failed to decrypt oauth code verifier: %w", err)
		}
	}
	return nil
}

// TableOauthToken represents an OAuth token in the database
// This stores the actual access and refresh tokens
type TableOauthToken struct {
	ID              string     `gorm:"type:varchar(255);primaryKey" json:"id"`            // UUID
	AccessToken     string     `gorm:"type:text;not null" json:"-"`                       // Encrypted access token
	RefreshToken    string     `gorm:"type:text" json:"-"`                                // Encrypted refresh token (optional)
	TokenType       string     `gorm:"type:varchar(50);not null" json:"token_type"`       // "Bearer"
	ExpiresAt       time.Time  `gorm:"index;not null" json:"expires_at"`                  // Token expiration
	Scopes          string     `gorm:"type:text" json:"scopes"`                           // JSON array of granted scopes
	LastRefreshedAt  *time.Time `gorm:"index" json:"last_refreshed_at,omitempty"` // Track when token was last refreshed
	EncryptionStatus string     `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time  `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name
func (TableOauthToken) TableName() string {
	return "oauth_tokens"
}

// BeforeSave hook
func (t *TableOauthToken) BeforeSave(tx *gorm.DB) error {
	// Ensure token type is set
	if t.TokenType == "" {
		t.TokenType = "Bearer"
	}

	// Encrypt sensitive fields
	if encrypt.IsEnabled() {
		if err := encryptString(&t.AccessToken); err != nil {
			return fmt.Errorf("failed to encrypt oauth access token: %w", err)
		}
		if err := encryptString(&t.RefreshToken); err != nil {
			return fmt.Errorf("failed to encrypt oauth refresh token: %w", err)
		}
		t.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind hook to decrypt sensitive fields
func (t *TableOauthToken) AfterFind(tx *gorm.DB) error {
	if t.EncryptionStatus == EncryptionStatusEncrypted {
		if err := decryptString(&t.AccessToken); err != nil {
			return fmt.Errorf("failed to decrypt oauth access token: %w", err)
		}
		if err := decryptString(&t.RefreshToken); err != nil {
			return fmt.Errorf("failed to decrypt oauth refresh token: %w", err)
		}
	}
	return nil
}
