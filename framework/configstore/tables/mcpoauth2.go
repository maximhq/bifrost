package tables

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableOauthConfig represents an OAuth configuration in the database
// This stores the OAuth client configuration and flow state
type TableOauthConfig struct {
	ID                  string             `gorm:"type:varchar(255);primaryKey" json:"id"`          // UUID
	ClientID            *schemas.SecretVar `gorm:"type:varchar(512)" json:"client_id"`              // OAuth provider's client ID (optional for public clients)
	ClientSecret        *schemas.SecretVar `gorm:"type:text" json:"-"`                              // Encrypted OAuth client secret (optional for public clients)
	AuthorizeURL        string             `gorm:"type:text" json:"authorize_url"`                  // Provider's authorization endpoint (optional, can be discovered)
	TokenURL            string             `gorm:"type:text" json:"token_url"`                      // Provider's token endpoint (optional, can be discovered)
	RegistrationURL     *string            `gorm:"type:text" json:"registration_url,omitempty"`     // Provider's dynamic registration endpoint (optional, can be discovered)
	RedirectURI         string             `gorm:"type:text;not null" json:"redirect_uri"`          // Callback URL
	Scopes              string             `gorm:"type:text" json:"scopes"`                         // JSON array of scopes (optional, can be discovered)
	State               string             `gorm:"type:varchar(255);uniqueIndex;not null" json:"-"` // CSRF state token
	CodeVerifier        string             `gorm:"type:text" json:"-"`                              // PKCE code verifier (generated, kept secret)
	CodeChallenge       string             `gorm:"type:varchar(255)" json:"code_challenge"`         // PKCE code challenge (sent to provider)
	Status              string             `gorm:"type:varchar(50);not null;index" json:"status"`   // "pending", "authorized", "failed", "expired", "revoked"
	TokenID             *string            `gorm:"type:varchar(255);index" json:"token_id"`         // Foreign key to oauth_tokens.ID (set after callback)
	ServerURL           string             `gorm:"type:text" json:"server_url"`                     // MCP server URL for OAuth discovery
	Resource            string             `gorm:"type:text" json:"resource,omitempty"`             // OAuth resource indicator (RFC 8707), typically the MCP server URL
	UseDiscovery        bool               `gorm:"default:false" json:"use_discovery"`              // Flag to enable OAuth discovery
	MCPClientConfigJSON *string            `gorm:"type:text" json:"-"`                              // JSON serialized MCPClientConfig for multi-instance support (pending MCP client waiting for OAuth completion)
	EncryptionStatus    string             `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt           time.Time          `gorm:"index;not null" json:"created_at"`
	UpdatedAt           time.Time          `gorm:"index;not null" json:"updated_at"`
	ExpiresAt           time.Time          `gorm:"index;not null" json:"expires_at"` // State expiry (15 min)
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

	if encrypt.IsEnabled() {
		encrypted := false
		if c.ClientSecret != nil && !c.ClientSecret.IsFromSecret() && c.ClientSecret.Val != "" {
			if err := encryptString(&c.ClientSecret.Val); err != nil {
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
	switch c.EncryptionStatus {
	case EncryptionStatusEncrypted:
		if c.ClientSecret != nil && !c.ClientSecret.IsFromSecret() && c.ClientSecret.Val != "" {
			if err := decryptString(&c.ClientSecret.Val); err != nil {
				return fmt.Errorf("failed to decrypt oauth client secret: %w", err)
			}
		}
		if err := decryptString(&c.CodeVerifier); err != nil {
			return fmt.Errorf("failed to decrypt oauth code verifier: %w", err)
		}
	}
	return nil
}

// VaultPathKey implements schemas.VaultPathKeyer so the global GORM vault
// callback can compute the vault base path for this model automatically.
func (c *TableOauthConfig) VaultPathKey() string { return c.ID }

// GetResolvedClientID returns the resolved ClientID value, expanding env var references at runtime.
func (c *TableOauthConfig) GetResolvedClientID() string {
	return c.ClientID.GetValue()
}

// GetResolvedClientSecret returns the resolved ClientSecret value, expanding env var references at runtime.
func (c *TableOauthConfig) GetResolvedClientSecret() string {
	return c.ClientSecret.GetValue()
}

// GetClientSecretAsSecretVar returns ClientSecret as an SecretVar (preserves env var reference metadata).
func (c *TableOauthConfig) GetClientSecretAsSecretVar() *schemas.SecretVar {
	return c.ClientSecret
}

// TableOauthToken represents an OAuth token in the database
// This stores the actual access and refresh tokens
type TableOauthToken struct {
	ID               string     `gorm:"type:varchar(255);primaryKey" json:"id"`      // UUID
	AccessToken      string     `gorm:"type:text;not null" json:"-"`                 // Encrypted access token
	RefreshToken     string     `gorm:"type:text" json:"-"`                          // Encrypted refresh token (optional)
	TokenType        string     `gorm:"type:varchar(50);not null" json:"token_type"` // "Bearer"
	ExpiresAt        *time.Time `gorm:"index" json:"expires_at,omitempty"`           // Token expiration (nil means unknown/non-expiring)
	Scopes           string     `gorm:"type:text" json:"scopes"`                     // JSON array of granted scopes
	LastRefreshedAt  *time.Time `gorm:"index" json:"last_refreshed_at,omitempty"`    // Track when token was last refreshed
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

// ---------- Per-User OAuth Tables ----------

// TableOauthUserSession tracks pending per-user OAuth flows.
// Each record maps an OAuth state token to a specific MCP client, allowing
// the callback to associate the resulting tokens with the correct user session.
type TableOauthUserSession struct {
	ID               string    `gorm:"type:varchar(255);primaryKey" json:"id"`                  // Session UUID
	MCPClientID      string    `gorm:"type:varchar(255);not null;index" json:"mcp_client_id"`   // Which MCP server this auth is for
	OauthConfigID    string    `gorm:"type:varchar(255);not null;index" json:"oauth_config_id"` // Template OAuth config (holds client_id, token_url, etc.)
	State            string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"-"`         // CSRF state token sent to OAuth provider
	RedirectURI      string    `gorm:"type:text" json:"-"`                                      // Per-request redirect URI used in authorize step
	CodeVerifier     string    `gorm:"type:text" json:"-"`                                      // PKCE code verifier (kept secret)
	SessionID        string    `gorm:"type:varchar(255);index" json:"session_id,omitempty"`     // Session-mode identity: client-asserted x-bf-mcp-session-id. Empty for vk/user mode rows. Stored plaintext (not a bearer credential; same trust model as a VK value).
	VirtualKeyID     *string   `gorm:"type:varchar(255);index" json:"virtual_key_id"`           // VK identity (propagated to oauth_user_tokens)
	UserID           *string   `gorm:"type:varchar(255);index" json:"user_id"`                  // User identity (propagated to oauth_user_tokens); populated only for user-mode rows, nil for vk/session-mode
	FlowMode         string    `gorm:"type:varchar(20);not null;default:'vk'" json:"flow_mode"` // 'user' | 'vk' | 'session' — mirrors the token row's AuthMode; immutable after creation
	Status           string    `gorm:"type:varchar(50);not null;index" json:"status"`           // "pending", "authorized", "failed", "expired"
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	ExpiresAt        time.Time `gorm:"index;not null" json:"expires_at"` // Flow expiration (15 min)
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`

	// Display-only relations (no DB-level FK constraint; preloaded for sessions UI).
	MCPClient  *TableMCPClient  `gorm:"foreignKey:MCPClientID;references:ClientID" json:"-"`
	VirtualKey *TableVirtualKey `gorm:"foreignKey:VirtualKeyID;references:ID" json:"-"`

	// User is a non-DB, enterprise-only annotation populated after fetch on
	// user-keyed flow rows so the sessions UI can render name/email instead
	// of a raw user_id. OSS has no users table; OSS leaves it nil.
	User *OauthUserSummary `gorm:"-" json:"-"`
}

// OauthUserSummary is the minimal user view embedded on user-keyed oauth rows
// for display purposes. Populated post-fetch by the enterprise configstore
// wrapper (it carries the SCIM user table data into OSS without OSS knowing
// the enterprise type).
type OauthUserSummary struct {
	ID   string
	Name string
}

func (TableOauthUserSession) TableName() string {
	return "oauth_user_sessions"
}

func (s *TableOauthUserSession) BeforeSave(tx *gorm.DB) error {
	if s.Status == "" {
		s.Status = "pending"
	}
	if encrypt.IsEnabled() {
		if s.CodeVerifier != "" {
			if err := encryptString(&s.CodeVerifier); err != nil {
				return fmt.Errorf("failed to encrypt oauth user session code verifier: %w", err)
			}
		}
		s.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

func (s *TableOauthUserSession) AfterFind(tx *gorm.DB) error {
	if s.EncryptionStatus == EncryptionStatusEncrypted && s.CodeVerifier != "" {
		if err := decryptString(&s.CodeVerifier); err != nil {
			return fmt.Errorf("failed to decrypt oauth user session code verifier: %w", err)
		}
	}
	return nil
}

// TableOauthUserToken stores per-user OAuth credentials.
// Each record holds the access/refresh tokens for a specific identity × MCP client pair.
// Exactly one identity column (UserID, VirtualKeyID, or SessionID) is populated
// per row; AuthMode records which one.
type TableOauthUserToken struct {
	ID               string     `gorm:"type:varchar(255);primaryKey" json:"id"`                   // Token UUID
	SessionID        string     `gorm:"type:varchar(255);index" json:"session_id,omitempty"`      // Session-mode identity: client-asserted x-bf-mcp-session-id. Empty for vk/user mode rows.
	VirtualKeyID     *string    `gorm:"type:varchar(255);index" json:"virtual_key_id"`            // VK identity (vk-mode rows)
	UserID           *string    `gorm:"type:varchar(255);index" json:"user_id"`                   // User identity (user-mode rows; populated by enterprise middleware/governance)
	MCPClientID      string     `gorm:"type:varchar(255);not null;index" json:"mcp_client_id"`    // Which MCP server
	AuthMode         string     `gorm:"type:varchar(20);not null" json:"auth_mode"`               // 'user' | 'vk' | 'session' — which identity column keys this row
	Status           string     `gorm:"type:varchar(20);not null;default:'active'" json:"status"` // 'active' | 'orphaned' | 'needs_reauth' — only 'active' satisfies a runtime lookup; the others are surfaced in the UI with distinct copy
	OauthConfigID    string     `gorm:"type:varchar(255);not null;index" json:"oauth_config_id"`  // Template OAuth config
	AccessToken      string     `gorm:"type:text;not null" json:"-"`                              // Encrypted user's OAuth access token
	RefreshToken     string     `gorm:"type:text" json:"-"`                                       // Encrypted user's OAuth refresh token
	TokenType        string     `gorm:"type:varchar(50);not null" json:"token_type"`              // "Bearer"
	ExpiresAt        *time.Time `gorm:"index" json:"expires_at,omitempty"`                        // Token expiry (nil means unknown/non-expiring)
	Scopes           string     `gorm:"type:text" json:"scopes"`                                  // JSON array of granted scopes
	LastRefreshedAt  *time.Time `gorm:"index" json:"last_refreshed_at,omitempty"`                 // Last refresh time
	EncryptionStatus string     `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time  `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"index;not null" json:"updated_at"`

	// Display-only relations (no DB-level FK constraint; preloaded for sessions UI).
	MCPClient  *TableMCPClient  `gorm:"foreignKey:MCPClientID;references:ClientID" json:"-"`
	VirtualKey *TableVirtualKey `gorm:"foreignKey:VirtualKeyID;references:ID" json:"-"`

	// User mirrors TableOauthUserSession.User — see OauthUserSummary above.
	User *OauthUserSummary `gorm:"-" json:"-"`
}

func (TableOauthUserToken) TableName() string {
	return "oauth_user_tokens"
}

func (t *TableOauthUserToken) BeforeSave(tx *gorm.DB) error {
	if t.TokenType == "" {
		t.TokenType = "Bearer"
	}
	if encrypt.IsEnabled() {
		if err := encryptString(&t.AccessToken); err != nil {
			return fmt.Errorf("failed to encrypt oauth user access token: %w", err)
		}
		if err := encryptString(&t.RefreshToken); err != nil {
			return fmt.Errorf("failed to encrypt oauth user refresh token: %w", err)
		}
		t.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

func (t *TableOauthUserToken) AfterFind(tx *gorm.DB) error {
	if t.EncryptionStatus == EncryptionStatusEncrypted {
		if err := decryptString(&t.AccessToken); err != nil {
			return fmt.Errorf("failed to decrypt oauth user access token: %w", err)
		}
		if err := decryptString(&t.RefreshToken); err != nil {
			return fmt.Errorf("failed to decrypt oauth user refresh token: %w", err)
		}
	}
	return nil
}
