//go:build !tinygo && !wasm

package schemas

import (
	"context"
	"errors"
	"time"
)

// Per-user-headers errors. Mirrors the OAuth sentinels at the top of mcp.go;
// kept in this file so the headers feature surface is self-contained.
var (
	// ErrHeadersCredentialProviderNotAvailable signals that the headers
	// provider isn't wired up — typically a misconfiguration (per_user_headers
	// auth type used while running without a configstore-backed provider).
	ErrHeadersCredentialProviderNotAvailable = errors.New("per-user headers credential provider not available")

	// ErrHeadersCredentialNotFound is the sentinel returned by
	// MCPHeadersProvider.GetCredentialByMode when no row exists for the
	// (mode, identity, mcp_client) triple. The resolver fans this out into an
	// inline MCPAuthRequiredError so the caller can complete the submission
	// flow.
	ErrHeadersCredentialNotFound = errors.New("per-user headers credential not found for this identity and mcp client")

	// ErrHeadersCredentialNeedsUpdate signals that the stored credential is
	// stale relative to the current MCPClientConfig.PerUserHeaderKeys schema
	// (e.g. admin added a new required key). The resolver treats this like
	// "not found" for inline-401 purposes but the row is preserved so the UI
	// can prefill known values.
	ErrHeadersCredentialNeedsUpdate = errors.New("per-user headers credential is missing keys required by the current schema")
)

// MCPHeadersUserCredentialStatus mirrors the lifecycle states tracked on the
// mcp_per_user_header_credentials table. Storage-layer concerns; the resolver
// only cares about "is this row usable right now".
type MCPHeadersUserCredentialStatus string

const (
	MCPHeadersUserCredentialStatusActive      MCPHeadersUserCredentialStatus = "active"       // Row matches the current schema and may be used
	MCPHeadersUserCredentialStatusNeedsUpdate MCPHeadersUserCredentialStatus = "needs_update" // Schema (PerUserHeaderKeys) changed; user must resubmit
	MCPHeadersUserCredentialStatusOrphaned    MCPHeadersUserCredentialStatus = "orphaned"     // Owner (VK / user) was deleted or detached; awaiting cleanup
)

// MCPHeadersUserCredential is the in-memory view of a single per-user header
// credential row. The transport between core and framework treats Headers as
// plaintext — encryption at rest is the configstore's responsibility.
type MCPHeadersUserCredential struct {
	ID           string
	MCPClientID  string
	AuthMode     MCPAuthMode
	UserID       *string
	VirtualKeyID *string
	SessionID    *string
	Headers      map[string]string // Decrypted header values
	Status       MCPHeadersUserCredentialStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// MCPHeadersFlowInitiation is the response returned by InitiateUserSubmissionFlow.
// Mirrors OAuth2FlowInitiation structurally so the resolver-side handling on
// the two per-user-auth surfaces stays uniform: a UUID, an auth-page URL,
// and an expiry. The "state" field is unused (no PKCE for headers); kept
// off the struct.
type MCPHeadersFlowInitiation struct {
	FlowID      string    // Flow row primary key
	FrontendURL string    // {base}/workspace/mcp-sessions/auth?flow={id}#t={temp_token}
	ExpiresAt   time.Time // Flow expiration (15 min default; matches OAuth)
}

// MCPHeadersProvider is the contract between the per-user-headers
// CredentialStore resolver and the configstore-backed implementation. Mirrors
// OAuth2Provider's per-user methods structurally so future provider
// implementations stay consistent.
type MCPHeadersProvider interface {
	// GetCredentialByMode returns the persisted credential for a single
	// identity dimension determined by mode. No fallback chain — exactly one
	// identity column is queried. Returns ErrHeadersCredentialNotFound when
	// the row is absent. Both 'active' and 'needs_update' rows are returned;
	// orphaned rows are filtered out at the store layer. The runtime
	// resolver's missing-keys check distinguishes usable from re-submission-
	// required rows, so the caller doesn't need to inspect Status itself.
	GetCredentialByMode(ctx context.Context, mode MCPAuthMode, identity, mcpClientID string) (*MCPHeadersUserCredential, error)

	// UpsertCredential persists a user-submitted set of header values for the
	// (mode, identity, mcp_client_id) triple after a successful verify. The
	// caller is expected to have run VerifyHeadersConnection before invoking
	// this — the provider does not re-test the upstream connection.
	UpsertCredential(ctx context.Context, cred *MCPHeadersUserCredential) error

	// DeleteCredential removes a credential row by its primary-key ID.
	DeleteCredential(ctx context.Context, id string) error

	// InitiateUserSubmissionFlow creates a pending mcp_per_user_header_flows
	// row keyed by (mode, identity, mcp_client_id), mints a mcp_headers_auth
	// temp-token bound to the new row's ID, and returns the auth-page URL
	// with the token embedded as a `#t=<token>` fragment. Mirrors
	// OAuth2Provider.InitiateUserOAuthFlow's role: the resolver calls this
	// when an inline-401 fires, then puts the returned FrontendURL on the
	// MCPAuthRequiredError so the caller can drive the submission flow.
	//
	// baseURL is the bifrost dashboard origin (e.g. "https://host") — the
	// resolver pulls it from BifrostContextKeyMCPCallbackBaseURL and passes
	// it in so the provider can construct the frontend URL without
	// reaching into the BifrostContext itself.
	InitiateUserSubmissionFlow(ctx context.Context, mode MCPAuthMode, identity, mcpClientID, baseURL string) (*MCPHeadersFlowInitiation, error)
}
