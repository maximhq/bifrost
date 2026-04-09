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
	ID                  string    `gorm:"type:varchar(255);primaryKey" json:"id"`          // UUID
	ClientID            string    `gorm:"type:varchar(512)" json:"client_id"`              // OAuth provider's client ID (optional for public clients)
	ClientSecret        string    `gorm:"type:text" json:"-"`                              // Encrypted OAuth client secret (optional for public clients)
	AuthorizeURL        string    `gorm:"type:text" json:"authorize_url"`                  // Provider's authorization endpoint (optional, can be discovered)
	TokenURL            string    `gorm:"type:text" json:"token_url"`                      // Provider's token endpoint (optional, can be discovered)
	RegistrationURL     *string   `gorm:"type:text" json:"registration_url,omitempty"`     // Provider's dynamic registration endpoint (optional, can be discovered)
	RedirectURI         string    `gorm:"type:text;not null" json:"redirect_uri"`          // Callback URL
	Scopes              string    `gorm:"type:text" json:"scopes"`                         // JSON array of scopes (optional, can be discovered)
	State               string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"-"` // CSRF state token
	CodeVerifier        string    `gorm:"type:text" json:"-"`                              // PKCE code verifier (generated, kept secret)
	CodeChallenge       string    `gorm:"type:varchar(255)" json:"code_challenge"`         // PKCE code challenge (sent to provider)
	Status              string    `gorm:"type:varchar(50);not null;index" json:"status"`   // "pending", "authorized", "failed", "expired", "revoked"
	TokenID             *string   `gorm:"type:varchar(255);index" json:"token_id"`         // Foreign key to oauth_tokens.ID (set after callback)
	ServerURL           string    `gorm:"type:text" json:"server_url"`                     // MCP server URL for OAuth discovery
	UseDiscovery        bool      `gorm:"default:false" json:"use_discovery"`              // Flag to enable OAuth discovery
	MCPClientConfigJSON *string   `gorm:"type:text" json:"-"`                              // JSON serialized MCPClientConfig for multi-instance support (pending MCP client waiting for OAuth completion)
	EncryptionStatus    string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt           time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt           time.Time `gorm:"index;not null" json:"updated_at"`
	ExpiresAt           time.Time `gorm:"index;not null" json:"expires_at"` // State expiry (15 min)
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
	ID               string     `gorm:"type:varchar(255);primaryKey" json:"id"`      // UUID
	AccessToken      string     `gorm:"type:text;not null" json:"-"`                 // Encrypted access token
	RefreshToken     string     `gorm:"type:text" json:"-"`                          // Encrypted refresh token (optional)
	TokenType        string     `gorm:"type:varchar(50);not null" json:"token_type"` // "Bearer"
	ExpiresAt        time.Time  `gorm:"index;not null" json:"expires_at"`            // Token expiration
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
	SessionToken     string    `gorm:"type:varchar(255)" json:"-"`                              // Bifrost session ID (links to oauth_per_user_sessions)
	SessionTokenHash string    `gorm:"type:varchar(64);uniqueIndex" json:"-"`                   // SHA-256 hash of SessionToken for secure lookups
	GatewaySessionID string    `gorm:"type:varchar(255);index" json:"-"`                        // Bifrost MCP gateway session ID (separate from SessionToken)
	VirtualKeyID     *string   `gorm:"type:varchar(255);index" json:"virtual_key_id"`           // VK identity (propagated to oauth_user_tokens)
	UserID           *string   `gorm:"type:varchar(255);index" json:"user_id"`                  // Enterprise user identity (propagated to oauth_user_tokens)
	Status           string    `gorm:"type:varchar(50);not null;index" json:"status"`           // "pending", "authorized", "failed", "expired"
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	ExpiresAt        time.Time `gorm:"index;not null" json:"expires_at"` // Flow expiration (15 min)
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`
}

func (TableOauthUserSession) TableName() string {
	return "oauth_user_sessions"
}

func (s *TableOauthUserSession) BeforeSave(tx *gorm.DB) error {
	if s.Status == "" {
		s.Status = "pending"
	}
	if s.SessionToken != "" {
		s.SessionTokenHash = encrypt.HashSHA256(s.SessionToken)
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
// Each record holds the access/refresh tokens for a specific user session + MCP client pair.
// Lookup is by SessionToken.
type TableOauthUserToken struct {
	ID               string     `gorm:"type:varchar(255);primaryKey" json:"id"`                                              // Token UUID
	SessionToken     string     `gorm:"type:varchar(255)" json:"-"`                                                          // Maps to Bifrost session (fallback for anonymous users)
	SessionTokenHash string     `gorm:"type:varchar(64);index" json:"-"`                                                     // SHA-256 hash of SessionToken for secure lookups
	VirtualKeyID     *string    `gorm:"type:varchar(255);index:idx_vk_mcp" json:"virtual_key_id"`                            // VK identity (persistent across sessions)
	UserID           *string    `gorm:"type:varchar(255);index:idx_user_mcp" json:"user_id"`                                 // Enterprise user identity (persistent across sessions)
	MCPClientID      string     `gorm:"type:varchar(255);not null;index:idx_vk_mcp;index:idx_user_mcp" json:"mcp_client_id"` // Which MCP server
	OauthConfigID    string     `gorm:"type:varchar(255);not null;index" json:"oauth_config_id"`                             // Template OAuth config
	AccessToken      string     `gorm:"type:text;not null" json:"-"`                                                         // Encrypted user's OAuth access token
	RefreshToken     string     `gorm:"type:text" json:"-"`                                                                  // Encrypted user's OAuth refresh token
	TokenType        string     `gorm:"type:varchar(50);not null" json:"token_type"`                                         // "Bearer"
	ExpiresAt        time.Time  `gorm:"index;not null" json:"expires_at"`                                                    // Token expiry
	Scopes           string     `gorm:"type:text" json:"scopes"`                                                             // JSON array of granted scopes
	LastRefreshedAt  *time.Time `gorm:"index" json:"last_refreshed_at,omitempty"`                                            // Last refresh time
	EncryptionStatus string     `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time  `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"index;not null" json:"updated_at"`
}

func (TableOauthUserToken) TableName() string {
	return "oauth_user_tokens"
}

func (t *TableOauthUserToken) BeforeSave(tx *gorm.DB) error {
	if t.TokenType == "" {
		t.TokenType = "Bearer"
	}
	if t.SessionToken != "" {
		t.SessionTokenHash = encrypt.HashSHA256(t.SessionToken)
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

// ---------- Per-User OAuth Authorization Server Tables ----------

// TablePerUserOAuthClient stores dynamically registered OAuth clients (RFC 7591).
// MCP clients (like Claude Code) register themselves with Bifrost's OAuth
// authorization server to obtain a client_id for the authorization code flow.
type TablePerUserOAuthClient struct {
	ID           string    `gorm:"type:varchar(255);primaryKey" json:"id"`
	ClientID     string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"client_id"`
	ClientName   string    `gorm:"type:varchar(255)" json:"client_name"`
	RedirectURIs string    `gorm:"type:text;not null" json:"redirect_uris"` // JSON array of allowed redirect URIs
	GrantTypes   string    `gorm:"type:text" json:"grant_types"`            // JSON array of grant types
	CreatedAt    time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt    time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName returns the table name for per-user OAuth clients.
func (TablePerUserOAuthClient) TableName() string {
	return "oauth_per_user_clients"
}

// TablePerUserOAuthSession stores Bifrost-issued access tokens for authenticated
// MCP connections. When a user authenticates via Bifrost's OAuth flow, a session
// is created. The access token is included in all subsequent MCP requests.
// Upstream provider tokens are linked via the oauth_user_tokens table.
type TablePerUserOAuthSession struct {
	ID               string           `gorm:"type:varchar(255);primaryKey" json:"id"`
	AccessToken      string           `gorm:"type:text;not null" json:"-"`                       // Bifrost-issued access token (encrypted)
	AccessTokenHash  string           `gorm:"type:varchar(64);uniqueIndex" json:"-"`             // SHA-256 hash for secure lookups
	RefreshToken     string           `gorm:"type:text" json:"-"`                                // Bifrost-issued refresh token (encrypted, optional)
	RefreshTokenHash string           `gorm:"type:varchar(64);index" json:"-"`                   // SHA-256 hash for secure lookups (not unique — refresh tokens are optional)
	ClientID         string           `gorm:"type:varchar(255);not null;index" json:"client_id"` // Which OAuth client registered this session
	VirtualKeyID     *string          `gorm:"type:varchar(255);index" json:"virtual_key_id"`     // Linked VK identity (set when VK is present during auth)
	VirtualKey       *TableVirtualKey `gorm:"foreignKey:VirtualKeyID" json:"-"` // Linked VK identity (server-only, not serialized)
	UserID           *string          `gorm:"type:varchar(255);index" json:"user_id"`            // Linked enterprise user identity (set when user ID is present)
	ExpiresAt        time.Time        `gorm:"index;not null" json:"expires_at"`
	EncryptionStatus string           `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time        `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time        `gorm:"index;not null" json:"updated_at"`
}

// TableName returns the table name for per-user OAuth sessions.
func (TablePerUserOAuthSession) TableName() string {
	return "oauth_per_user_sessions"
}

// BeforeSave encrypts sensitive fields.
func (s *TablePerUserOAuthSession) BeforeSave(tx *gorm.DB) error {
	if s.AccessToken != "" {
		s.AccessTokenHash = encrypt.HashSHA256(s.AccessToken)
	}
	if s.RefreshToken != "" {
		s.RefreshTokenHash = encrypt.HashSHA256(s.RefreshToken)
	}
	if encrypt.IsEnabled() {
		if err := encryptString(&s.AccessToken); err != nil {
			return fmt.Errorf("failed to encrypt per-user oauth access token: %w", err)
		}
		if s.RefreshToken != "" {
			if err := encryptString(&s.RefreshToken); err != nil {
				return fmt.Errorf("failed to encrypt per-user oauth refresh token: %w", err)
			}
		}
		s.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind decrypts sensitive fields.
func (s *TablePerUserOAuthSession) AfterFind(tx *gorm.DB) error {
	if s.EncryptionStatus == EncryptionStatusEncrypted {
		if err := decryptString(&s.AccessToken); err != nil {
			return fmt.Errorf("failed to decrypt per-user oauth access token: %w", err)
		}
		if s.RefreshToken != "" {
			if err := decryptString(&s.RefreshToken); err != nil {
				return fmt.Errorf("failed to decrypt per-user oauth refresh token: %w", err)
			}
		}
	}
	return nil
}

// TablePerUserOAuthCode stores authorization codes during the OAuth flow.
// Codes are short-lived (5 minutes) and single-use.
type TablePerUserOAuthCode struct {
	ID            string    `gorm:"type:varchar(255);primaryKey" json:"id"`
	Code          string    `gorm:"type:text;not null" json:"-"`           // Authorization code
	CodeHash      string    `gorm:"type:varchar(64);uniqueIndex" json:"-"` // SHA-256 hash for secure lookups
	ClientID      string    `gorm:"type:varchar(255);not null;index" json:"client_id"`
	RedirectURI   string    `gorm:"type:text;not null" json:"redirect_uri"`
	CodeChallenge string    `gorm:"type:varchar(255);not null" json:"-"` // PKCE S256 challenge
	Scopes        string    `gorm:"type:text" json:"scopes"`             // JSON array of requested scopes
	SessionID     string    `gorm:"type:varchar(255);index" json:"-"`    // Links to the TablePerUserOAuthSession created during consent submit
	ExpiresAt     time.Time `gorm:"index;not null" json:"expires_at"`    // 5 min TTL
	Used          bool      `gorm:"default:false;not null" json:"used"`  // Single-use flag
	CreatedAt     time.Time `gorm:"index;not null" json:"created_at"`
}

// BeforeSave hashes the code for secure lookups.
func (c *TablePerUserOAuthCode) BeforeSave(tx *gorm.DB) error {
	if c.Code != "" {
		c.CodeHash = encrypt.HashSHA256(c.Code)
	}
	return nil
}

// TableName returns the table name for per-user OAuth authorization codes.
func (TablePerUserOAuthCode) TableName() string {
	return "oauth_per_user_codes"
}

// TablePerUserOAuthPendingFlow stores OAuth parameters between the authorize step
// and the final code issuance. It carries state through the multi-step consent
// screen (VK entry + per-MCP upstream auth) before a real authorization code is issued.
type TablePerUserOAuthPendingFlow struct {
	ID                string    `gorm:"type:varchar(255);primaryKey" json:"id"`
	ClientID          string    `gorm:"type:varchar(255);not null;index" json:"client_id"` // Registered OAuth client (from authorize request)
	RedirectURI       string    `gorm:"type:text;not null" json:"redirect_uri"`            // Client's callback URL
	CodeChallenge     string    `gorm:"type:varchar(255);not null" json:"-"`               // PKCE S256 challenge (echoed into the final code)
	State             string    `gorm:"type:text;not null" json:"-"`                       // Original OAuth state (echoed back on final redirect)
	VirtualKeyID      *string   `gorm:"type:varchar(255);index" json:"virtual_key_id"`     // Set if user chose VK identity
	UserID            *string   `gorm:"type:varchar(255);index" json:"user_id"`            // Set if user chose User ID identity
	BrowserSecretHash string    `gorm:"type:varchar(255)" json:"-"`                        // SHA-256 hash of browser-binding cookie secret
	ExpiresAt         time.Time `gorm:"index;not null" json:"expires_at"`                  // 15-min TTL
	CreatedAt         time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt         time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName returns the table name for per-user OAuth pending flows.
func (TablePerUserOAuthPendingFlow) TableName() string {
	return "oauth_per_user_pending_flows"
}
