package tables

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableMCPPerUserHeaderFlow tracks pending per-user-headers submission
// flows. Mirrors TableOauthUserSession structurally so the per-user-auth
// surfaces (OAuth + headers) have identical lifecycles: an inline-401
// from the resolver creates a flow row, the auth-page URL carries the
// flow's ID (with a temp-token in the URL fragment for unauthenticated
// callers), and the submission endpoint completes / deletes the row.
//
// Unlike OAuth, there is no PKCE state to round-trip — the only durable
// state this row carries is (mcp_client_id, identity) so the submission
// endpoint can scope the upsert. No state column either: the row exists
// only while the submission is pending; submit completes by deleting it.
type TableMCPPerUserHeaderFlow struct {
	ID           string    `gorm:"type:varchar(255);primaryKey" json:"id"`                  // Flow UUID
	MCPClientID  string    `gorm:"type:varchar(255);not null;index" json:"mcp_client_id"`   // Which MCP server this submission is for
	SessionID    string    `gorm:"type:varchar(255);index" json:"session_id,omitempty"`     // Session-mode identity: client-asserted x-bf-mcp-session-id. Empty for vk/user mode rows.
	VirtualKeyID *string   `gorm:"type:varchar(255);index" json:"virtual_key_id"`           // VK identity (vk-mode rows)
	UserID       *string   `gorm:"type:varchar(255);index" json:"user_id"`                  // User identity (user-mode rows)
	FlowMode     string    `gorm:"type:varchar(20);not null;default:'vk'" json:"flow_mode"` // 'user' | 'vk' | 'session' — mirrors the credential row's AuthMode; immutable after creation
	Status       string    `gorm:"type:varchar(50);not null;index" json:"status"`           // "pending", "completed", "expired"
	ExpiresAt    time.Time `gorm:"index;not null" json:"expires_at"`                        // Flow expiration (15 min default)
	CreatedAt    time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt    time.Time `gorm:"index;not null" json:"updated_at"`

	// Display-only relations (no DB-level FK constraint; preloaded for sessions UI).
	MCPClient  *TableMCPClient  `gorm:"foreignKey:MCPClientID;references:ClientID" json:"-"`
	VirtualKey *TableVirtualKey `gorm:"foreignKey:VirtualKeyID;references:ID" json:"-"`

	// User mirrors TableOauthUserSession.User — populated post-fetch by the
	// enterprise configstore wrapper for the sessions UI. OSS leaves it nil.
	User *OauthUserSummary `gorm:"-" json:"-"`
}

// TableName sets the table name.
func (TableMCPPerUserHeaderFlow) TableName() string {
	return "mcp_per_user_header_flows"
}

// BeforeSave defaults Status to 'pending' when unset.
func (f *TableMCPPerUserHeaderFlow) BeforeSave(tx *gorm.DB) error {
	if f.Status == "" {
		f.Status = "pending"
	}
	return nil
}

// TableMCPPerUserHeaderCredential stores per-user header credentials for
// MCPAuthTypePerUserHeaders MCP clients. Each row holds the encrypted header
// values for a specific identity × MCP client pair. Exactly one identity
// column (UserID, VirtualKeyID, or SessionID) is populated per row; AuthMode
// records which one. Mirrors TableOauthUserToken structurally so cascade /
// orphan-sweep logic stays parallel between the two per-user auth surfaces.
//
// HeadersJSON holds a JSON-encoded map[string]string of header_name → value,
// encrypted at rest via the shared encrypt package (same key as
// oauth_user_tokens). Schema (i.e. the set of allowed header names) lives on
// TableMCPClient.PerUserHeaderKeysJSON; this table holds the values only.
type TableMCPPerUserHeaderCredential struct {
	ID               string    `gorm:"type:varchar(255);primaryKey" json:"id"`                   // UUID
	SessionID        string    `gorm:"type:varchar(255);index" json:"session_id,omitempty"`      // Session-mode identity: client-asserted x-bf-mcp-session-id. Empty for vk/user mode rows.
	VirtualKeyID     *string   `gorm:"type:varchar(255);index" json:"virtual_key_id"`            // VK identity (vk-mode rows)
	UserID           *string   `gorm:"type:varchar(255);index" json:"user_id"`                   // User identity (user-mode rows)
	MCPClientID      string    `gorm:"type:varchar(255);not null;index" json:"mcp_client_id"`    // Which MCP server
	AuthMode         string    `gorm:"type:varchar(20);not null" json:"auth_mode"`               // 'user' | 'vk' | 'session' — which identity column keys this row
	Status           string    `gorm:"type:varchar(20);not null;default:'active'" json:"status"` // 'active' | 'orphaned' | 'needs_update'
	HeadersJSON      string    `gorm:"type:text;not null" json:"-"`                              // Encrypted JSON map[string]string of user-supplied header values
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`

	// Display-only relations (no DB-level FK constraint; preloaded for sessions UI).
	MCPClient  *TableMCPClient  `gorm:"foreignKey:MCPClientID;references:ClientID" json:"-"`
	VirtualKey *TableVirtualKey `gorm:"foreignKey:VirtualKeyID;references:ID" json:"-"`

	// User mirrors TableOauthUserToken.User — populated post-fetch by enterprise
	// configstore wrapper for the sessions UI. OSS leaves it nil.
	User *OauthUserSummary `gorm:"-" json:"-"`
}

func (TableMCPPerUserHeaderCredential) TableName() string {
	return "mcp_per_user_header_credentials"
}

// BeforeSave encrypts HeadersJSON when encryption is enabled. The JSON
// serialization is the caller's responsibility (see SetHeaders). When
// encryption is not configured (no BIFROST_ENCRYPTION_KEY), the field
// is stored as plaintext and EncryptionStatus stays "plain_text" — same
// convention as TableOauthUserToken.
func (c *TableMCPPerUserHeaderCredential) BeforeSave(tx *gorm.DB) error {
	if c.Status == "" {
		c.Status = "active"
	}
	if c.HeadersJSON == "" {
		c.HeadersJSON = "{}"
	}
	if encrypt.IsEnabled() {
		if err := encryptString(&c.HeadersJSON); err != nil {
			return fmt.Errorf("failed to encrypt mcp per-user header credential headers: %w", err)
		}
		c.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind decrypts HeadersJSON when the row is marked encrypted.
func (c *TableMCPPerUserHeaderCredential) AfterFind(tx *gorm.DB) error {
	if c.EncryptionStatus == EncryptionStatusEncrypted {
		if err := decryptString(&c.HeadersJSON); err != nil {
			return fmt.Errorf("failed to decrypt mcp per-user header credential headers: %w", err)
		}
	}
	return nil
}

// SetHeaders serializes the caller-supplied header map into HeadersJSON.
// Callers must use this rather than assigning HeadersJSON directly so the
// JSON shape stays consistent.
func (c *TableMCPPerUserHeaderCredential) SetHeaders(headers map[string]string) error {
	if headers == nil {
		headers = map[string]string{}
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return fmt.Errorf("failed to serialize mcp per-user header credential headers: %w", err)
	}
	c.HeadersJSON = string(data)
	return nil
}

// GetHeaders deserializes HeadersJSON into a header map. Returns an empty map
// for the zero JSON (`{}` or empty string) so callers do not need to nil-check.
func (c *TableMCPPerUserHeaderCredential) GetHeaders() (map[string]string, error) {
	headers := map[string]string{}
	if c.HeadersJSON == "" || c.HeadersJSON == "{}" {
		return headers, nil
	}
	if err := json.Unmarshal([]byte(c.HeadersJSON), &headers); err != nil {
		return nil, fmt.Errorf("failed to deserialize mcp per-user header credential headers: %w", err)
	}
	return headers, nil
}
