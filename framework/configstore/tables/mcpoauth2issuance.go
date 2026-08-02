package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// TableOAuth2Client holds a registered OAuth2 client created via Dynamic Client
// Registration (RFC 7591). Bifrost only supports public clients
// (token_endpoint_auth_method=none) — no client secrets.
type TableOAuth2Client struct {
	ID               string    `gorm:"type:varchar(255);primaryKey" json:"id"`
	ClientID         string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"client_id"`
	ClientName       string    `gorm:"type:varchar(255)" json:"client_name"`
	RedirectURIsJSON string    `gorm:"type:text;not null" json:"-"` // JSON []string
	GrantTypesJSON   string    `gorm:"type:text;not null" json:"-"` // JSON []string
	Scope            string    `gorm:"type:varchar(255)" json:"scope"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`

	// Virtual fields
	RedirectURIs []string `gorm:"-" json:"redirect_uris"`
	GrantTypes   []string `gorm:"-" json:"grant_types"`
}

func (TableOAuth2Client) TableName() string { return "oauth2_clients" }

func (c *TableOAuth2Client) BeforeSave(tx *gorm.DB) error {
	if c.RedirectURIs != nil {
		data, err := json.Marshal(c.RedirectURIs)
		if err != nil {
			return err
		}
		c.RedirectURIsJSON = string(data)
	}
	if c.GrantTypes != nil {
		data, err := json.Marshal(c.GrantTypes)
		if err != nil {
			return err
		}
		c.GrantTypesJSON = string(data)
	}
	return nil
}

func (c *TableOAuth2Client) AfterFind(tx *gorm.DB) error {
	if c.RedirectURIsJSON != "" {
		if err := json.Unmarshal([]byte(c.RedirectURIsJSON), &c.RedirectURIs); err != nil {
			return err
		}
	}
	if c.GrantTypesJSON != "" {
		if err := json.Unmarshal([]byte(c.GrantTypesJSON), &c.GrantTypes); err != nil {
			return err
		}
	}
	return nil
}

// OAuth2AuthorizeRequestStatus is the status of a downstream authorize request.
type OAuth2AuthorizeRequestStatus string

const (
	OAuth2AuthorizeRequestStatusPending    OAuth2AuthorizeRequestStatus = "pending"    // waiting for consent
	OAuth2AuthorizeRequestStatusConsented  OAuth2AuthorizeRequestStatus = "consented"  // identity resolved, code minted
	OAuth2AuthorizeRequestStatusCodeIssued OAuth2AuthorizeRequestStatus = "code_issued" // token exchanged, one-time consumed
)

// TableOAuth2AuthorizeRequest tracks a pending downstream OAuth2 authorization
// request from creation at /oauth2/authorize through consent to token exchange
// at /oauth2/token.
//
// State transitions:
//   - pending    — request created; browser redirected to consent page
//   - consented  — user approved; identity resolved; auth code minted (CodeHash set)
//   - code_issued — auth code exchanged at /oauth2/token; row is consumed (single-use)
type TableOAuth2AuthorizeRequest struct {
	ID                  string                       `gorm:"type:varchar(255);primaryKey" json:"id"`
	ClientID            string                       `gorm:"type:varchar(255);not null;index" json:"client_id"`
	RedirectURI         string                       `gorm:"type:text;not null" json:"-"`
	State               string                       `gorm:"type:varchar(512);not null" json:"-"` // CSRF; returned in redirect
	Scope               string                       `gorm:"type:varchar(255)" json:"scope"`
	Resource            string                       `gorm:"type:text;not null" json:"-"`         // RFC 8707 resource indicator
	CodeChallenge       string                       `gorm:"type:varchar(512);not null" json:"-"` // PKCE S256 challenge
	CodeChallengeMethod string                       `gorm:"type:varchar(10);not null" json:"-"`  // always "S256"
	Status              OAuth2AuthorizeRequestStatus `gorm:"type:varchar(20);not null;index" json:"status"`
	// Set by the consent flow once the user approves:
	BfMode   string `gorm:"type:varchar(20)" json:"bf_mode,omitempty"` // user|vk|session
	BfSub    string `gorm:"type:varchar(255)" json:"bf_sub,omitempty"` // resolved identity
	// nil while pending; set to SHA256(auth_code) at consent. A pointer so unset
	// rows store SQL NULL — NULLs are distinct under the unique index, letting many
	// requests stay pending at once while still enforcing uniqueness for real hashes.
	CodeHash *string `gorm:"type:varchar(255);uniqueIndex" json:"-"`
	// TTL:
	ExpiresAt time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (TableOAuth2AuthorizeRequest) TableName() string { return "oauth2_authorize_requests" }

// TableOAuth2RefreshToken stores a hashed rotating refresh token. The plaintext
// token is only returned to the client once at issuance; only the SHA256 hash
// is persisted. Invalidation paths:
//   - rotation on use: old token revoked atomically when a new one is issued
//   - bf_sub liveness: VK deleted or user deactivated → invalid_grant on next refresh
//   - explicit revocation via the Connected Clients UI
//
// FamilyID links all tokens descended from the same authorization grant (set to
// the authorize request ID at first issuance, propagated on every rotation).
// When a revoked token is presented — indicating the token was stolen and used
// after the legitimate client already rotated — all tokens sharing the FamilyID
// are revoked immediately, per the OAuth 2.0 Security BCP (RFC 9700 §2.2.2).
type TableOAuth2RefreshToken struct {
	ID         string     `gorm:"type:varchar(255);primaryKey" json:"id"`
	TokenHash  string     `gorm:"type:varchar(255);uniqueIndex;not null" json:"-"` // SHA256 hex
	FamilyID   string     `gorm:"type:varchar(255);not null;index" json:"family_id"` // authorize request ID
	ClientID   string     `gorm:"type:varchar(255);not null;index" json:"client_id"`
	BfMode     string     `gorm:"type:varchar(20);not null" json:"bf_mode"` // user|vk|session
	BfSub      string     `gorm:"type:varchar(255);not null" json:"bf_sub"` // resolved identity
	Scope      string     `gorm:"type:varchar(255)" json:"scope"`
	Resource   string     `gorm:"type:text;not null" json:"-"` // RFC 8707 resource indicator; preserved across rotations for the JWT aud claim
	RevokedAt  *time.Time `gorm:"index" json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `gorm:"index" json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`
}

func (TableOAuth2RefreshToken) TableName() string { return "oauth2_refresh_tokens" }
