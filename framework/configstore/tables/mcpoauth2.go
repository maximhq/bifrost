package tables

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableOauthConfig represents an OAuth configuration in the database. Past
// the bootstrap flow, this is a pure registration/credential template row —
// client_id/client_secret/endpoints/scopes plus the bootstrap lifecycle
// Status ("pending"/"authorized"/"failed" — a completed bootstrap never
// regresses this to anything else; ongoing credential health lives entirely
// on the token row's own Status instead, see TableMCPOauthToken). CSRF state
// and PKCE fields (state, code_verifier, code_challenge) and the flow's own
// expiry live on TableMCPOauthFlow instead — one config row is a durable,
// reusable credential template, while a flow row is scoped to a single
// authorize attempt and is disposed of once that attempt completes. There is
// likewise no FK shortcut onto the token row here (a retired TokenID field
// used to serve that purpose for the shared-mode holder only) — every
// holder's token, shared or per-identity alike, is reached by querying
// mcp_oauth_tokens on (oauth_config_id, auth_mode) instead. The lookup
// contract differs by holder type: a shared lookup filters on
// (oauth_config_id, auth_mode = "shared") alone, which is not unique by
// itself — see the shared-mode uniqueness index below for what actually
// constrains it to one row. A per-identity lookup additionally requires
// mcp_client_id and the matching user_id, virtual_key_id, or session_id,
// plus status = "active". A refresh, once a token row is already in hand,
// looks it up directly by its own ID instead of re-deriving either of the
// above. FlowMode = "admin" on the originating TableMCPOauthFlow is what
// produces a token row with AuthMode = "shared".
type TableOauthConfig struct {
	ID                  string             `gorm:"type:varchar(255);primaryKey" json:"id"`        // UUID
	ClientID            *schemas.SecretVar `gorm:"type:varchar(512)" json:"client_id"`            // OAuth provider's client ID (optional for public clients)
	ClientSecret        *schemas.SecretVar `gorm:"type:text" json:"-"`                            // Encrypted OAuth client secret (optional for public clients)
	AuthorizeURL        string             `gorm:"type:text" json:"authorize_url"`                // Provider's authorization endpoint (optional, can be discovered)
	TokenURL            string             `gorm:"type:text" json:"token_url"`                    // Provider's token endpoint (optional, can be discovered)
	RegistrationURL     *string            `gorm:"type:text" json:"registration_url,omitempty"`   // Provider's dynamic registration endpoint (optional, can be discovered)
	RedirectURI         string             `gorm:"type:text;not null" json:"redirect_uri"`        // Callback URL
	Scopes              string             `gorm:"type:text" json:"scopes"`                       // JSON array of scopes (optional, can be discovered)
	Status              string             `gorm:"type:varchar(50);not null;index" json:"status"` // "pending", "authorized", "failed" — the one-time bootstrap lifecycle only
	ServerURL           string             `gorm:"type:text" json:"server_url"`                   // MCP server URL for OAuth discovery
	Resource            string             `gorm:"type:text" json:"resource,omitempty"`           // OAuth resource indicator (RFC 8707), typically the MCP server URL
	UseDiscovery        bool               `gorm:"default:false" json:"use_discovery"`            // Flag to enable OAuth discovery
	MCPClientConfigJSON *string            `gorm:"type:text" json:"-"`                            // JSON serialized MCPClientConfig for multi-instance support (pending MCP client waiting for OAuth completion)
	EncryptionStatus    string             `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt           time.Time          `gorm:"index;not null" json:"created_at"`
	UpdatedAt           time.Time          `gorm:"index;not null" json:"updated_at"`
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
		if c.ClientSecret != nil && !c.ClientSecret.IsFromSecret() && c.ClientSecret.Val != "" {
			if err := encryptString(&c.ClientSecret.Val); err != nil {
				return fmt.Errorf("failed to encrypt oauth client secret: %w", err)
			}
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

// TableOauthToken represents the OAuth token record for the single shared
// MCP OAuth client credential — one row per shared-mode connection.
//
// Deprecated: no longer read or written by any application code as of the
// TableMCPOauthToken merge; this type exists only so the migration that
// introduced TableMCPOauthToken can reference its table (oauth_tokens) via
// normal GORM model access. Both this type and its backing table will be
// removed in the next major version.
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

// TableMCPOauthToken represents an OAuth token in the database. This stores
// the actual access and refresh tokens for every holder of an MCP OAuth
// credential — the single shared client credential ('shared' mode) as well
// as per-identity credentials ('user' | 'vk' | 'session' mode). Exactly one
// of MCPClientID+OauthConfigID's owning row shape applies regardless of mode;
// for per-identity rows, exactly one of VirtualKeyID/UserID/SessionID is
// populated per row, and AuthMode records which one (or 'shared', which
// populates none of them).
//
// Mode nuance for per-user OAuth clients: 'admin' is their retained
// client-level discovery credential, and a 'shared' row on such a client is
// NOT a shared-client credential but a transient staging state. The OAuth
// callback (CompleteOAuthFlow) writes every admin-consent token as 'shared'
// because at that point the token is unverified and, during initial
// creation, the MCP client row may not exist yet; the complete-oauth step
// then either promotes it to 'admin' (PromoteSharedOauthTokenToAdmin) or
// revokes it. Outside that seconds-long window, per-user clients hold no
// 'shared' rows.
//
// Lives in mcp_oauth_tokens, a table created fresh by the migration that
// introduced this struct — it does not alter or extend either of the two
// tables ('oauth_tokens', 'oauth_user_tokens') this type's predecessors used
// to map to. See that migration for why both older tables are left in place
// unused.
type TableMCPOauthToken struct {
	ID       string `gorm:"type:varchar(255);primaryKey" json:"id"`     // UUID
	AuthMode string `gorm:"type:varchar(20);not null" json:"auth_mode"` // 'shared' | 'user' | 'vk' | 'session' | 'admin' — no DB-level CHECK constraint; validated at the application layer only, so adding a new mode later is a pure data change, not a migration
	// MCPClientID and OauthConfigID are not DB-level NOT NULL: rows written by
	// current code always populate both, but a pre-existing row whose owning
	// oauth_config was deleted before the orphan-cleanup fix (see
	// DeleteMCPClientConfig) may have no derivable client/config and is left
	// with the empty string rather than rejected by a migration.
	MCPClientID      string     `gorm:"type:varchar(255);index" json:"mcp_client_id"`             // Which MCP server
	OauthConfigID    string     `gorm:"type:varchar(255);index" json:"oauth_config_id"`           // Template OAuth config (holds client_id, token_url, etc.)
	SessionID        string     `gorm:"type:varchar(255);index" json:"session_id,omitempty"`      // Session-mode identity: client-asserted x-bf-mcp-session-id. Empty for shared/vk/user mode rows.
	VirtualKeyID     *string    `gorm:"type:varchar(255);index" json:"virtual_key_id"`            // VK identity (vk-mode rows)
	UserID           *string    `gorm:"type:varchar(255);index" json:"user_id"`                   // User identity (user-mode rows; populated by enterprise middleware/governance)
	Status           string     `gorm:"type:varchar(20);not null;default:'active'" json:"status"` // 'active' | 'orphaned' | 'needs_reauth' — only 'active' satisfies a runtime lookup; the others are surfaced in the UI with distinct copy
	AccessToken      string     `gorm:"type:text;not null" json:"-"`                              // Encrypted access token
	RefreshToken     string     `gorm:"type:text" json:"-"`                                       // Encrypted refresh token (optional)
	TokenType        string     `gorm:"type:varchar(50);not null" json:"token_type"`              // "Bearer"
	ExpiresAt        *time.Time `gorm:"index" json:"expires_at,omitempty"`                        // Token expiration (nil means unknown/non-expiring)
	Scopes           string     `gorm:"type:text" json:"scopes"`                                  // JSON array of granted scopes
	LastRefreshedAt  *time.Time `gorm:"index" json:"last_refreshed_at,omitempty"`                 // Track when token was last refreshed
	EncryptionStatus string     `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time  `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"index;not null" json:"updated_at"`

	// Display-only relations (no DB-level FK constraint — "-:migration" skips
	// both constraint creation and ordinary column migration for these two
	// fields; the actual FK values live in MCPClientID/VirtualKeyID above,
	// which are ordinary migrated columns). Preloaded for the sessions UI.
	// "-:migration" sidesteps a real constraint-violation hazard on
	// MCPClientID specifically: a row backfilled from a shared oauth_tokens
	// row whose oauth_config was deleted before the DeleteMCPClientConfig
	// orphan-cleanup fix has no derivable client (see the MCPClientID field
	// comment above) and is left with MCPClientID = "" rather than NULL — a
	// real FK constraint against config_mcp_clients.client_id would reject
	// that insert. Table-creation order is not the concern here:
	// mcp_oauth_tokens is created by migrationMergeOauthTokenTables, the
	// last migration registered in this file, long after config_mcp_clients
	// and governance_virtual_keys already exist.
	MCPClient  *TableMCPClient  `gorm:"-:migration;foreignKey:MCPClientID;references:ClientID" json:"-"`
	VirtualKey *TableVirtualKey `gorm:"-:migration;foreignKey:VirtualKeyID;references:ID" json:"-"`

	// User is a non-DB, enterprise-only annotation populated after fetch on
	// user-keyed rows so the sessions UI can render name/email instead of a
	// raw user_id. OSS has no users table; OSS leaves it nil.
	User *OauthUserSummary `gorm:"-" json:"-"`
}

// TableName sets the table name
func (TableMCPOauthToken) TableName() string {
	return "mcp_oauth_tokens"
}

// BeforeSave hook
func (t *TableMCPOauthToken) BeforeSave(tx *gorm.DB) error {
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
func (t *TableMCPOauthToken) AfterFind(tx *gorm.DB) error {
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

// TableMCPOauthFlow tracks a single in-flight OAuth authorize attempt — the
// CSRF state token, PKCE verifier, originating identity, and expiry for the
// duration of that attempt. Covers every flow kind that lands on the
// /api/oauth/callback redirect_uri: per-identity flows ('user' | 'vk' |
// 'session', formerly the entire contents of this table's predecessor) and
// 'admin' flows (the one-time shared-client production authorize, and a
// per-user client's admin bootstrap-test authorize — indistinguishable at
// this layer; the keep-vs-discard-token decision downstream branches on the
// MCP client's AuthType, not on anything here). A config row
// (TableOauthConfig) is a durable, reusable credential template; a flow row
// is scoped to one authorize attempt and is deleted once that attempt
// reaches a terminal state.
//
// Lives in mcp_oauth_flows, a table created fresh by the migration that
// introduced this struct — it does not alter or extend oauth_user_sessions,
// this type's predecessor's table. See that migration for why the old table
// is left in place unused.
type TableMCPOauthFlow struct {
	ID            string `gorm:"type:varchar(255);primaryKey" json:"id"`                  // Flow UUID
	MCPClientID   string `gorm:"type:varchar(255);not null;index" json:"mcp_client_id"`   // Which MCP server this auth is for
	OauthConfigID string `gorm:"type:varchar(255);not null;index" json:"oauth_config_id"` // Template OAuth config (holds client_id, token_url, etc.)
	// State carries a plain (non-unique) index in the struct tag rather than
	// uniqueIndex: the migration that creates this table needs to run its
	// oauth_user_sessions backfill before a unique index on state exists (a
	// gorm-tag-driven uniqueIndex would instead be created as part of
	// CreateTable, ahead of the backfill) — see that migration for the
	// ordering rationale. The real uniqueness is enforced there via a raw
	// CREATE UNIQUE INDEX issued after the backfill completes.
	State            string    `gorm:"type:varchar(255);index;not null" json:"-"`               // CSRF state token sent to OAuth provider
	RedirectURI      string    `gorm:"type:text" json:"-"`                                      // Per-request redirect URI used in authorize step
	CodeVerifier     string    `gorm:"type:text" json:"-"`                                      // PKCE code verifier (kept secret)
	SessionID        string    `gorm:"type:varchar(255);index" json:"session_id,omitempty"`     // Session-mode identity: client-asserted x-bf-mcp-session-id. Empty for admin/vk/user mode rows. Stored plaintext (not a bearer credential; same trust model as a VK value).
	VirtualKeyID     *string   `gorm:"type:varchar(255);index" json:"virtual_key_id"`           // VK identity (propagated to mcp_oauth_tokens)
	UserID           *string   `gorm:"type:varchar(255);index" json:"user_id"`                  // User identity (propagated to mcp_oauth_tokens); populated only for user-mode rows, nil otherwise
	FlowMode         string    `gorm:"type:varchar(20);not null;default:'vk'" json:"flow_mode"` // 'user' | 'vk' | 'session' | 'admin' — mirrors the eventual token row's AuthMode; immutable after creation; no DB-level CHECK constraint, validated at the application layer only (same reasoning as TableMCPOauthToken.AuthMode)
	Status           string    `gorm:"type:varchar(50);not null;index" json:"status"`           // "pending", "authorized", "failed", "expired" (plus a transient "claiming" between an atomic claim-by-state and the token exchange it guards)
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	ExpiresAt        time.Time `gorm:"index;not null" json:"expires_at"` // Flow expiration (15 min)
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`

	// Display-only relations (no DB-level FK constraint; preloaded for
	// sessions/admin UI). "-:migration" is load-bearing, not decorative — see
	// the matching comment on TableMCPOauthToken.MCPClient/VirtualKey above
	// for the full reasoning: a flow row is written before its MCP client row
	// exists (the client is only persisted once the admin completes OAuth
	// consent), and the oauth_user_sessions backfill this table's migration
	// runs can carry the same orphaned-client-id hazard, so a real FK here
	// would reject inserts a plain "constraint:-" doesn't reliably prevent.
	MCPClient  *TableMCPClient  `gorm:"-:migration;foreignKey:MCPClientID;references:ClientID" json:"-"`
	VirtualKey *TableVirtualKey `gorm:"-:migration;foreignKey:VirtualKeyID;references:ID" json:"-"`

	// User mirrors TableOauthUserSession.User — see OauthUserSummary below.
	User *OauthUserSummary `gorm:"-" json:"-"`
}

// TableName sets the table name
func (TableMCPOauthFlow) TableName() string {
	return "mcp_oauth_flows"
}

func (s *TableMCPOauthFlow) BeforeSave(tx *gorm.DB) error {
	if s.Status == "" {
		s.Status = "pending"
	}
	if encrypt.IsEnabled() {
		if s.CodeVerifier != "" {
			if err := encryptString(&s.CodeVerifier); err != nil {
				return fmt.Errorf("failed to encrypt oauth flow code verifier: %w", err)
			}
		}
		s.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

func (s *TableMCPOauthFlow) AfterFind(tx *gorm.DB) error {
	if s.EncryptionStatus == EncryptionStatusEncrypted && s.CodeVerifier != "" {
		if err := decryptString(&s.CodeVerifier); err != nil {
			return fmt.Errorf("failed to decrypt oauth flow code verifier: %w", err)
		}
	}
	return nil
}

// ---------- Per-User OAuth Tables (deprecated) ----------

// TableOauthUserSession tracks pending per-user OAuth flows.
// Each record maps an OAuth state token to a specific MCP client, allowing
// the callback to associate the resulting tokens with the correct user session.
//
// Deprecated: no longer read or written by any application code as of the
// TableMCPOauthFlow merge; this type exists only so the migration that
// introduced TableMCPOauthFlow can reference its table (oauth_user_sessions)
// via normal GORM model access. Both this type and its backing table will be
// removed in the next major version.
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

// TableOauthUserToken stores per-user OAuth credentials — one row per
// identity (user, VK, or session) × MCP client pair. Kept alongside
// TableOauthToken so the historical schema-evolution migrations that
// already shipped against oauth_user_tokens (column adds, index changes,
// etc.) keep compiling and keep producing the exact DDL they always have.
//
// Deprecated: no longer read or written by any application code as of the
// TableMCPOauthToken merge; this type exists only so the migration that
// introduced TableMCPOauthToken can reference its table (oauth_user_tokens)
// via normal GORM model access. Both this type and its backing table will be
// removed in the next major version.
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
