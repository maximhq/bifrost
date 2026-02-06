package tables

import (
	"time"

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
	CreatedAt            time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"index;not null" json:"updated_at"`
	ExpiresAt       time.Time `gorm:"index;not null" json:"expires_at"`                 // State expiry (15 min)
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
	LastRefreshedAt *time.Time `gorm:"index" json:"last_refreshed_at,omitempty"`          // Track when token was last refreshed
	CreatedAt       time.Time  `gorm:"index;not null" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"index;not null" json:"updated_at"`
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
	return nil
}
