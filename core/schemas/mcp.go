//go:build !tinygo && !wasm

// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/server"
)

// OAuth-related errors
var (
	ErrOAuth2ConfigNotFound       = errors.New("oauth2 config not found")
	ErrOAuth2ProviderNotAvailable = errors.New("oauth2 provider not available")
	ErrOAuth2TokenExpired         = errors.New("oauth2 token expired")
	ErrOAuth2TokenInvalid         = errors.New("oauth2 token invalid")
	ErrOAuth2RefreshFailed        = errors.New("oauth2 token refresh failed")
	ErrOAuth2NotPerUserSession    = errors.New("state does not match a per-user oauth session")
	ErrOAuth2TokenNotFound        = errors.New("per-user oauth token not found for this identity and mcp server")
	ErrOAuth2FlowNotPending       = errors.New("oauth flow is not in pending state")
	ErrOAuth2FlowExpired          = errors.New("oauth flow has expired")
	// ErrTokenExchangeUnavailable means delegated token exchange cannot run:
	// no identity-provider integration with an exchange client is configured
	// (nil TokenExchangeIdPResolver, or Available() == false).
	ErrTokenExchangeUnavailable = errors.New("delegated token exchange is not available: identity-provider integration with an exchange client is not configured")
	// ErrExchangeSubjectTokenMissing means the request carried no caller
	// identity-provider token to use as the exchange subject.
	ErrExchangeSubjectTokenMissing = errors.New("no caller identity-provider token available to exchange")
	// ErrMCPReconnectNotApplicable signals that the reconnect operation is not
	// meaningful for this client type — e.g. per-user OAuth clients, where
	// each user manages their own auth and there is no shared upstream
	// connection to "reconnect". Distinct from "not implemented".
	ErrMCPReconnectNotApplicable = errors.New("reconnect is not applicable for this client type")
)

// MCPAuthRequiredKind discriminates the kind of inline-401 auth flow surfaced
// to the caller. The value lands in MCPAuthRequiredError.Kind and on the wire
// under extra_fields.mcp_auth_required.kind.
const (
	MCPAuthRequiredKindOAuth    = "oauth"
	MCPAuthRequiredKindHeaders  = "headers"
	MCPAuthRequiredKindExchange = "exchange"
)

// MCPAuthRequiredError is returned when a per-user MCP credential is missing
// and the caller must complete an inline auth flow (OAuth dance or headers
// submission) before tool execution can proceed.
//
// Kind discriminates which set of fields is populated:
//   - "oauth":    AuthorizeURL, SessionID
//   - "headers":  SubmitURL, SessionID, RequiredHeaderKeys, AdminHeaderKeys
//   - "exchange": SubjectTokenMissing, ExchangeError (no interactive flow:
//     the caller fixes the request credential and retries; there is no URL
//     to visit and no flow row to track)
//
// SessionID is shared by both Kinds: for "oauth" it is the
// mcp_per_user_oauth_flows row ID, for "headers" the
// mcp_per_user_header_flows row ID. Either way it lets the caller
// reference the pending flow row without parsing the URL fragment.
//
// Common fields (MCPClientID, MCPClientName, Message) are always set.
type MCPAuthRequiredError struct {
	Kind          string `json:"kind"`
	MCPClientID   string `json:"mcp_client_id"`
	MCPClientName string `json:"mcp_client_name"`
	Message       string `json:"message"`

	// OAuth-specific fields (populated when Kind == "oauth"). SessionID is
	// also populated for Kind == "headers" — see the type-level comment.
	AuthorizeURL string `json:"authorize_url,omitempty"`
	SessionID    string `json:"session_id,omitempty"`

	// Headers-specific fields (populated when Kind == "headers"). SubmitURL is
	// the workspace landing page where the user provides values for
	// RequiredHeaderKeys; AdminHeaderKeys lists the admin-set static headers
	// (names only, no values) for context display.
	SubmitURL          string   `json:"submit_url,omitempty"`
	RequiredHeaderKeys []string `json:"required_header_keys,omitempty"`
	AdminHeaderKeys    []string `json:"admin_header_keys,omitempty"`

	// Exchange-specific fields (populated when Kind == "exchange").
	// SubjectTokenMissing is true when the request carried no caller token to
	// exchange; false means a token was present but the identity provider
	// rejected the exchange, with ExchangeError carrying the provider's
	// error/error_description for display.
	SubjectTokenMissing bool   `json:"subject_token_missing,omitempty"`
	ExchangeError       string `json:"exchange_error,omitempty"`
}

func (e *MCPAuthRequiredError) Error() string {
	return e.Message
}

// MCPAuthTempTokenReminder is appended to MCPAuthRequiredError.Message when the
// auth URL carries a `#t=<token>` temp-token fragment (see
// MCPAuthURLHasTempTokenFragment). The fragment is deliberately never sent to
// the server (unlike a query param, it's not logged or forwarded as a
// Referer), but that also makes it easy for an LLM relaying the link to a
// human to mistake it for a non-essential anchor and drop it, breaking the
// link. Spelling this out in the message itself is the cheapest way to stop
// that from happening.
const MCPAuthTempTokenReminder = " IMPORTANT: this link includes a required fragment after the '#' character (a one-time auth token). Copy and share the ENTIRE URL exactly as given, including everything after the '#' — the link will not work without it."

// MCPAuthURLHasTempTokenFragment reports whether authURL carries the `#t=`
// temp-token fragment minted by InitiateUserOAuthFlow /
// InitiateUserSubmissionFlow. Callers use this to decide whether
// MCPAuthTempTokenReminder applies — the mint is best-effort (see those
// functions' docs), so the fragment isn't always present even when
// MCPEnableTempTokenAuth is on.
func MCPAuthURLHasTempTokenFragment(authURL string) bool {
	return strings.Contains(authURL, "#t=")
}

// MCPUserOAuthRequiredError is an alias retained for backward compatibility
// with callers that referenced the OAuth-only error type before headers auth
// was added. New code should use MCPAuthRequiredError directly.
//
// Deprecated: use MCPAuthRequiredError.
type MCPUserOAuthRequiredError = MCPAuthRequiredError

// MCPCredentialStore is the single source of truth for MCP credential resolution.
// It exposes three predicates that MCPManager consumes uniformly:
//
//   - ConnectionHeaders         — headers attached to opening an upstream transport
//   - RequestHeaders            — per-message headers on an already-open transport
//   - RequiresPerCallConnection — whether each call needs an ephemeral transport
//
// Storage lifecycle (orphaning on VK reassignment, cascade on client delete)
// is NOT part of this interface — those concerns stay in the configstore
// layer where transactional atomicity is preserved.
type MCPCredentialStore interface {
	// ConnectionHeaders returns the headers to attach when opening an upstream
	// transport. Called from two sites:
	//
	//  1. At AddClient / Reconnect / UpdateClientCredentials for shared-
	//     connection auth types (none, headers, server_oauth). The caller
	//     wraps the Bifrost lifecycle context into a synthetic BifrostContext
	//     with no identity, so the resolver returns admin-level headers
	//     (static config + admin Bearer for server_oauth).
	//
	//  2. Per call inside the ephemeral-transport path for per-user auth
	//     types. The caller passes the real request BifrostContext, and the
	//     resolver returns the caller's full set (static + filtered
	//     context-extras + per-user auth).
	//
	// May return *MCPAuthRequiredError when a per-user credential is missing
	// and the caller must complete an inline auth flow (OAuth dance or
	// headers submission) before retrying.
	ConnectionHeaders(ctx *BifrostContext, config *MCPClientConfig) (http.Header, error)

	// RequestHeaders returns the per-message headers attached to each
	// CallTool / ListTools / Ping that flows over an already-open
	// transport — currently just the filtered context-extras
	// (BifrostContextKeyMCPExtraHeaders, scoped by config.AllowedExtraHeaders).
	//
	// Only meaningful when the connection is shared
	// (RequiresPerCallConnection is false). Per-user types embed all
	// caller-specific headers in the ephemeral transport itself via
	// ConnectionHeaders; the caller skips RequestHeaders in that path.
	RequestHeaders(ctx *BifrostContext, config *MCPClientConfig) (http.Header, error)

	// RequiresPerCallConnection reports whether each tool invocation needs a
	// freshly-built ephemeral upstream connection (rather than reusing a
	// shared persistent one). True for per-user auth types; false for
	// shared (none, headers, oauth-server-level).
	RequiresPerCallConnection(config *MCPClientConfig) bool

	// ForceRefresh unconditionally refreshes the credential backing config,
	// bypassing whatever lazy expiry gate ConnectionHeaders' normal
	// resolution path applies. Called when a live upstream tool call was
	// rejected despite Bifrost's own bookkeeping saying the credential was
	// still good — the premise being that the local bookkeeping is what's
	// stale, not necessarily the credential. A no-op returning nil for auth
	// types with nothing to refresh (none, static headers, per-user headers).
	ForceRefresh(ctx *BifrostContext, config *MCPClientConfig) error

	// AdminConnectionHeaders resolves the retained admin bootstrap-verification
	// credential's connection headers, for periodic tool-discovery refresh
	// (the tool syncer's per-user path — see ClientToolSyncer.performSync)
	// rather than a real caller's per-call credential (that's ConnectionHeaders'
	// job, which raises an interactive *MCPAuthRequiredError on a miss since
	// there's a real caller to redirect). The syncer has no caller to redirect,
	// so a miss here is just a plain error the syncer logs and retries next
	// cycle. Meaningful only for per_user_oauth/per_user_headers resolvers,
	// which keep this credential alive specifically for this purpose. Every
	// other resolver type errors: there's no separate "admin" credential
	// distinct from the shared one for those auth types.
	AdminConnectionHeaders(ctx context.Context, config *MCPClientConfig) (http.Header, error)
}

// MCPConfig represents the configuration for MCP integration in Bifrost.
// It enables tool auto-discovery and execution from local and external MCP servers.
type MCPConfig struct {
	ClientConfigs     []*MCPClientConfig    `json:"client_configs,omitempty"`      // Per-client execution configurations
	ToolManagerConfig *MCPToolManagerConfig `json:"tool_manager_config,omitempty"` // MCP tool manager configuration
	ToolSyncInterval  time.Duration         `json:"tool_sync_interval,omitempty"`  // Global default interval for syncing tools from MCP servers (0 = use default 10 min)

	// Function to fetch a new request ID for each tool call result message in agent mode,
	// this is used to ensure that the tool call result messages are unique and can be tracked in plugins or by the user.
	// This id is attached to ctx.Value(schemas.BifrostContextKeyRequestID) in the agent mode.
	// If not provider, same request ID is used for all tool call result messages without any overrides.
	FetchNewRequestIDFunc func(ctx *BifrostContext) string `json:"-"`

	// PluginPipelineProvider returns a plugin pipeline for running MCP plugin hooks.
	// Used when executeCode tool calls nested MCP tools to ensure plugins run for them.
	// The plugin pipeline should be released back to the pool using ReleasePluginPipeline.
	PluginPipelineProvider func() interface{} `json:"-"`

	// ReleasePluginPipeline releases a plugin pipeline back to the pool.
	// This should be called after the plugin pipeline is no longer needed.
	ReleasePluginPipeline func(pipeline interface{}) `json:"-"`
}

// UnmarshalJSON supports Go duration strings (e.g. "10m") for tool_sync_interval.
// Numeric values remain supported for backward compatibility (treated as raw nanoseconds).
func (c *MCPConfig) UnmarshalJSON(data []byte) error {
	type alias MCPConfig
	aux := &struct {
		ToolSyncInterval *json.Number `json:"tool_sync_interval,omitempty"`
		*alias
	}{alias: (*alias)(c)}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(aux); err == nil {
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("trailing JSON data")
		}
		if aux.ToolSyncInterval == nil {
			return nil
		}
		dur, parseErr := parseFlexibleDurationField(*aux.ToolSyncInterval, "tool_sync_interval")
		if parseErr != nil {
			return parseErr
		}
		c.ToolSyncInterval = dur
		return nil
	}

	// Allow Go duration strings while keeping numeric tokens as json.Number.
	auxStr := &struct {
		ToolSyncInterval *string `json:"tool_sync_interval,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, auxStr); err != nil {
		return err
	}
	if auxStr.ToolSyncInterval == nil {
		return nil
	}
	dur, err := parseFlexibleDurationField(*auxStr.ToolSyncInterval, "tool_sync_interval")
	if err != nil {
		return err
	}
	c.ToolSyncInterval = dur
	return nil
}

type MCPToolManagerConfig struct {
	// ToolExecutionTimeout accepts a Go duration string (e.g. "30s", "2m") or a
	// bare integer treated as seconds (e.g. 30 → 30s). This intentionally differs
	// from schemas.Duration, which treats bare integers as nanoseconds.
	ToolExecutionTimeout  Duration             `json:"tool_execution_timeout"`
	MaxAgentDepth         int                  `json:"max_agent_depth"`
	CodeModeBindingLevel  CodeModeBindingLevel `json:"code_mode_binding_level,omitempty"`  // How tools are exposed in VFS: "server" or "tool"
	DisableAutoToolInject bool                 `json:"disable_auto_tool_inject,omitempty"` // When true, MCP tools are not injected into requests by default
}

// UnmarshalJSON implements json.Unmarshaler so that tool_execution_timeout treats
// bare integers as seconds (matching the schema description and user expectation)
// rather than the nanosecond interpretation used by the underlying Duration type.
func (c *MCPToolManagerConfig) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid infinite recursion, then fix up ToolExecutionTimeout.
	type alias MCPToolManagerConfig
	aux := &struct {
		ToolExecutionTimeout *json.RawMessage `json:"tool_execution_timeout,omitempty"`
		*alias
	}{alias: (*alias)(c)}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if aux.ToolExecutionTimeout == nil {
		return nil
	}

	raw := *aux.ToolExecutionTimeout
	// If it's a quoted string, delegate to the normal Duration parser ("30s", "2m", etc.)
	if len(raw) > 0 && raw[0] == '"' {
		return json.Unmarshal(raw, &c.ToolExecutionTimeout)
	}

	// Bare integer: treat as seconds (not nanoseconds).
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return fmt.Errorf("invalid tool_execution_timeout: expected a duration string (e.g. \"30s\") or integer seconds: %w", err)
	}
	c.ToolExecutionTimeout = Duration(time.Duration(n) * time.Second)
	return nil
}

const (
	DefaultMaxAgentDepth        = 10
	DefaultToolExecutionTimeout = 30 * time.Second
)

// CodeModeBindingLevel defines how tools are exposed in the VFS for code execution
type CodeModeBindingLevel string

const (
	CodeModeBindingLevelServer CodeModeBindingLevel = "server"
	CodeModeBindingLevelTool   CodeModeBindingLevel = "tool"
)

// MCPAuthType defines the authentication type for MCP connections
type MCPAuthType string

const (
	MCPAuthTypeNone           MCPAuthType = "none"             // No authentication
	MCPAuthTypeHeaders        MCPAuthType = "headers"          // Header-based authentication (API keys, etc.)
	MCPAuthTypeOauth          MCPAuthType = "oauth"            // OAuth 2.0 authentication (server-level, admin authenticates once)
	MCPAuthTypePerUserOauth   MCPAuthType = "per_user_oauth"   // Per-user OAuth 2.0 authentication (each user authenticates individually)
	MCPAuthTypePerUserHeaders MCPAuthType = "per_user_headers" // Per-user header authentication (each user submits API keys / signed tokens; admin declares the required key names via PerUserHeaderKeys)
	MCPAuthTypeTokenExchange  MCPAuthType = "token_exchange"   // Delegated token exchange: the caller's identity-provider token is exchanged for a short-lived upstream token scoped to this server's audience; requires user-identity authentication to be available
)

// MCPTokenExchangeConfig configures delegated token exchange for one MCP
// client (AuthType == token_exchange): which resource the exchanged token
// must be scoped to, and the identity-provider application authorized to
// perform the exchange for that resource. The endpoint and grant shape are
// not configured here — they come from the deployment's identity-provider
// integration at exchange time.
type MCPTokenExchangeConfig struct {
	// Audience is the resource identifier the identity provider scopes the
	// exchanged token to (e.g. "api://jira-mcp"). Required.
	Audience string `json:"audience"`
	// ClientID identifies the identity-provider application authorized to
	// perform exchanges for this audience — typically a dedicated
	// registration carrying the token-exchange (or on-behalf-of) grant,
	// separate from the SSO login application. Required unless
	// UseIdPCredentials is true, in which case this is ignored and the
	// SSO login application's own client ID is used instead. Supports
	// env./vault. references.
	ClientID *SecretVar `json:"client_id,omitempty"`
	// ClientSecret authenticates the exchange application. Omit for public
	// clients. Ignored when UseIdPCredentials is true. Supports env./vault.
	// references.
	ClientSecret *SecretVar `json:"client_secret,omitempty"`
	// UseIdPCredentials, when true, performs the exchange using the SSO
	// login application's own client ID/secret (from the deployment's
	// configured identity-provider integration) instead of ClientID /
	// ClientSecret above, which are then ignored. Some providers require
	// this: Microsoft Entra ID's on-behalf-of grant only accepts an
	// assertion whose audience matches the exchanging application, and
	// Bifrost's own SSO login flow always requests a token self-audienced
	// to the SSO application — so for Entra, a dedicated exchange
	// application distinct from the SSO login app can never receive a
	// usable assertion, and this must be true. Providers with a
	// standalone RFC 8693 token-exchange grant (Okta, Auth0, Keycloak)
	// aren't bound by that constraint; a dedicated exchange application is
	// recommended for those, so this defaults to false.
	UseIdPCredentials bool `json:"use_idp_credentials,omitempty"`
	// Scopes optionally narrows the exchanged token; joined into the OAuth
	// scope parameter. Include "offline_access" (where the identity provider
	// supports it) to have exchanges issue refresh tokens, which keeps the
	// retained admin discovery credential self-renewing instead of flipping
	// to needs_reauth when it expires.
	Scopes []string `json:"scopes,omitempty"`
	// AuthorizationServerURL optionally overrides where the exchange request
	// is sent, for identity providers that bind an audience to a specific
	// authorization server distinct from the one used for SSO login (e.g.
	// Okta's per-resource Custom Authorization Servers). When empty, the
	// exchange is sent to the deployment's SSO login issuer, which is
	// correct for providers with a single tenant-wide token endpoint (Entra,
	// Auth0).
	AuthorizationServerURL *string `json:"authorization_server_url,omitempty"`
}

// DiffersFrom reports whether resolved's fields differ from c's, comparing
// audience, client_id, client_secret, authorization_server_url, and scopes
// (order-insensitive). Callers pass a fully-resolved value for resolved —
// e.g. the update-MCP-client API's redacted-value-preserve merge, or a
// config.json sync's "file value if declared, else keep stored" merge —
// not a raw partial declaration. A nil receiver always differs from a
// non-nil resolved value (first token_exchange config ever set on this
// client); resolved == nil never differs (nothing to compare against).
func (c *MCPTokenExchangeConfig) DiffersFrom(resolved *MCPTokenExchangeConfig) bool {
	if resolved == nil {
		return false
	}
	if c == nil {
		return true
	}
	if c.Audience != resolved.Audience {
		return true
	}
	if c.UseIdPCredentials != resolved.UseIdPCredentials {
		return true
	}
	if !c.ClientID.Equals(resolved.ClientID) {
		return true
	}
	if !c.ClientSecret.Equals(resolved.ClientSecret) {
		return true
	}
	existingURL, resolvedURL := "", ""
	if c.AuthorizationServerURL != nil {
		existingURL = *c.AuthorizationServerURL
	}
	if resolved.AuthorizationServerURL != nil {
		resolvedURL = *resolved.AuthorizationServerURL
	}
	if existingURL != resolvedURL {
		return true
	}
	existingScopes := slices.Clone(c.Scopes)
	resolvedScopes := slices.Clone(resolved.Scopes)
	slices.Sort(existingScopes)
	slices.Sort(resolvedScopes)
	return !slices.Equal(existingScopes, resolvedScopes)
}

// MCPClientConfig defines tool filtering for an MCP client.
type MCPClientConfig struct {
	ID                string            `json:"client_id"`                     // Client ID
	Name              string            `json:"name"`                          // Client name
	IsCodeModeClient  bool              `json:"is_code_mode_client"`           // Whether the client is a code mode client
	ConnectionType    MCPConnectionType `json:"connection_type"`               // How to connect (HTTP, STDIO, SSE, or InProcess)
	ConnectionString  *SecretVar        `json:"connection_string,omitempty"`   // HTTP or SSE URL (required for HTTP or SSE connections)
	StdioConfig       *MCPStdioConfig   `json:"stdio_config,omitempty"`        // STDIO configuration (required for STDIO connections)
	TLSConfig         *MCPTLSConfig     `json:"tls_config,omitempty"`          // TLS configuration for HTTP/SSE connections
	AuthType          MCPAuthType       `json:"auth_type"`                     // Authentication type (none, headers, or oauth)
	OauthConfigID     *string           `json:"oauth_config_id,omitempty"`     // OAuth config ID (references oauth_configs table)
	OauthClientID     *SecretVar        `json:"oauth_client_id,omitempty"`     // Redacted OAuth client ID (populated on GET, not stored here)
	OauthClientSecret *SecretVar        `json:"oauth_client_secret,omitempty"` // Redacted OAuth client secret (populated on GET, not stored here)
	// The remaining OAuth fields are populated on GET (not stored here) so the
	// full oauth_config, not just client_id/client_secret, can be reviewed and
	// edited without a separate lookup.
	OauthAuthorizeURL    string               `json:"oauth_authorize_url,omitempty"`
	OauthTokenURL        string               `json:"oauth_token_url,omitempty"`
	OauthRegistrationURL string               `json:"oauth_registration_url,omitempty"`
	OauthScopes          []string             `json:"oauth_scopes,omitempty"`
	OauthResource        string               `json:"oauth_resource,omitempty"`
	State                string               `json:"state,omitempty"`   // Connection state (connected, disconnected, error)
	Headers              map[string]SecretVar `json:"headers,omitempty"` // Headers to send with the request (for headers auth type)
	// PerUserHeaderKeys lists the header *names* each caller must supply for
	// MCPAuthTypePerUserHeaders clients. Admin-declared schema only — the
	// values live per-user in the mcp_per_user_header_credentials table and
	// are resolved at call time. Names in this list are stripped from
	// utils.StaticConfigHeaders so admin-set values in `Headers` with the
	// same name cannot leak through the plugin gate. Required (non-empty)
	// when AuthType == per_user_headers; ignored otherwise.
	PerUserHeaderKeys []string `json:"per_user_header_keys,omitempty"`
	// TokenExchange scopes delegated token exchange. Required (with a
	// non-empty Audience) when AuthType == token_exchange; ignored otherwise.
	TokenExchange       *MCPTokenExchangeConfig `json:"token_exchange,omitempty"`
	AllowedExtraHeaders WhiteList               `json:"allowed_extra_headers,omitempty"` // Allowlist of request-level headers that callers may forward to this MCP server at execution time
	InProcessServer     *server.MCPServer       `json:"-"`                               // MCP server instance for in-process connections (Go package only)
	ToolsToExecute      WhiteList               `json:"tools_to_execute,omitempty"`      // Include-only list.
	// ToolsToExecute semantics:
	// - ["*"] => all tools are included
	// - []    => no tools are included (deny-by-default)
	// - nil/omitted => treated as [] (no tools)
	// - ["tool1", "tool2"] => include only the specified tools
	ToolsToAutoExecute WhiteList `json:"tools_to_auto_execute,omitempty"` // Auto-execute list.
	// ToolsToAutoExecute semantics:
	// - ["*"] => all tools are auto-executed
	// - []    => no tools are auto-executed (deny-by-default)
	// - nil/omitted => treated as [] (no tools)
	// - ["tool1", "tool2"] => auto-execute only the specified tools
	// Note: If a tool is in ToolsToAutoExecute but not in ToolsToExecute, it will be skipped.
	IsPingAvailable *bool `json:"is_ping_available,omitempty"` // Whether the MCP server supports ping for health checks (nil/true = ping; false = listTools). Defaults to true.
	// NeedsSessionStickiness controls whether this shared client (auth_type
	// oauth/headers) maintains one persistent connection reused across every
	// caller (true) or connects fresh per call, same model as the per-user
	// auth types, with no persistent connection or background connection
	// checker at all (nil/false, the default for newly created clients).
	// Every client that existed before this field was introduced is
	// explicitly backfilled to true, preserving its exact prior behavior;
	// nil/false only applies to clients created afterward. Only meaningful
	// for ConnectionType == http: SSE has no stateless mode and STDIO needs
	// a persistent subprocess, so both always behave as sticky regardless
	// of this field, and an explicit false is rejected for either at write
	// time. Ignored for per-user auth types (already always per-call
	// regardless).
	NeedsSessionStickiness *bool              `json:"needs_session_stickiness,omitempty"`
	ToolSyncInterval       time.Duration      `json:"tool_sync_interval,omitempty"`     // Per-client override for tool sync interval (0 = use global, negative = disabled)
	ToolExecutionTimeout   time.Duration      `json:"tool_execution_timeout,omitempty"` // Per-client override for tool execution timeout (0 = use global from tool_manager_config)
	ToolPricing            map[string]float64 `json:"tool_pricing,omitempty"`           // Tool pricing for each tool (cost per execution)
	Disabled               bool               `json:"disabled"`                         // Whether the client is intentionally disabled (stops connection and workers)
	ConfigHash             string             `json:"-"`                                // Config hash for reconciliation (not serialized)
	AllowOnAllVirtualKeys  bool               `json:"allow_on_all_virtual_keys"`        // Whether to allow the MCP client to run on all virtual keys

	// Discovered tools for per-user OAuth clients (persisted so they survive restart)
	DiscoveredTools           map[string]ChatTool `json:"-"` // Discovered tool schemas keyed by prefixed name
	DiscoveredToolNameMapping map[string]string   `json:"-"` // Mapping from sanitized tool names to original MCP names

	// PendingOAuthConfig holds the inline `oauth_config` block declared in
	// config.json for shared-OAuth MCP clients (auth_type == "oauth").
	//
	// Lifecycle: populated by the config.json loader when an entry has
	// AuthType==oauth without an OauthConfigID; persisted on the DB row as
	// TableMCPClient.PendingOAuthConfigJSON; consumed at admin-click time by
	// the initiate-verification endpoint to call InitiateOAuthFlow; cleared
	// by the OAuth callback once oauth_configs.status='authorized'.
	//
	// Mirrors the UI Create-MCP-Client form's `oauth_config` block on the
	// wire — same field set, same optionality (all inner fields can be
	// omitted; discovery + dynamic registration fill them in at admin-click
	// time). Nil for clients whose OAuth has already been authorized.
	// Stored values are plaintext; env-var-reference resolution is not
	// applied to fields inside this block.
	PendingOAuthConfig *OAuth2Config `json:"oauth_config,omitempty"`
}

// UnmarshalJSON supports Go duration strings (e.g. "10m") for tool_sync_interval and
// tool_execution_timeout. Numeric values are treated as raw nanoseconds for tool_sync_interval
// and as seconds for tool_execution_timeout (matching tool_manager_config behaviour).
func (c *MCPClientConfig) UnmarshalJSON(data []byte) error {
	type alias MCPClientConfig
	aux := &struct {
		ToolSyncInterval     *json.Number     `json:"tool_sync_interval,omitempty"`
		ToolExecutionTimeout *json.RawMessage `json:"tool_execution_timeout,omitempty"`
		*alias
	}{alias: (*alias)(c)}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(aux); err == nil {
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("trailing JSON data")
		}
		if aux.ToolSyncInterval != nil {
			dur, parseErr := parseFlexibleDurationField(*aux.ToolSyncInterval, "tool_sync_interval")
			if parseErr != nil {
				return parseErr
			}
			c.ToolSyncInterval = dur
		}
		if aux.ToolExecutionTimeout != nil {
			dur, err := parseToolExecutionTimeoutField(*aux.ToolExecutionTimeout)
			if err != nil {
				return err
			}
			c.ToolExecutionTimeout = dur
		}
		return nil
	}

	// Allow Go duration strings while keeping numeric tokens as json.Number.
	// ToolExecutionTimeout uses *json.RawMessage (not *string) so that integer
	// values like 60 remain valid even when tool_sync_interval is a string.
	auxStr := &struct {
		ToolSyncInterval     *string          `json:"tool_sync_interval,omitempty"`
		ToolExecutionTimeout *json.RawMessage `json:"tool_execution_timeout,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, auxStr); err != nil {
		return err
	}
	if auxStr.ToolSyncInterval != nil {
		dur, err := parseFlexibleDurationField(*auxStr.ToolSyncInterval, "tool_sync_interval")
		if err != nil {
			return err
		}
		c.ToolSyncInterval = dur
	}
	if auxStr.ToolExecutionTimeout != nil {
		dur, err := parseToolExecutionTimeoutField(*auxStr.ToolExecutionTimeout)
		if err != nil {
			return err
		}
		c.ToolExecutionTimeout = dur
	}
	return nil
}

// parseToolExecutionTimeoutField parses a tool_execution_timeout JSON value.
// Accepts a Go duration string (e.g. "30s") or a bare integer treated as seconds.
// Rejects negative values and integers that would overflow time.Duration.
func parseToolExecutionTimeoutField(raw json.RawMessage) (time.Duration, error) {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, fmt.Errorf("invalid tool_execution_timeout: %w", err)
		}
		dur, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid tool_execution_timeout %q: %w", s, err)
		}
		if dur < 0 {
			return 0, fmt.Errorf("invalid tool_execution_timeout: value must be >= 0, got %v", dur)
		}
		return dur, nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("invalid tool_execution_timeout: expected a duration string (e.g. \"30s\") or integer seconds: %w", err)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid tool_execution_timeout: value must be >= 0, got %d", n)
	}
	const maxTimeoutSeconds = math.MaxInt64 / int64(time.Second)
	if n > maxTimeoutSeconds {
		return 0, fmt.Errorf("invalid tool_execution_timeout: value %d seconds overflows duration (max %d)", n, maxTimeoutSeconds)
	}
	return time.Duration(n) * time.Second, nil
}

// MarshalJSON emits tool_execution_timeout as a duration string so it round-trips
// correctly — default time.Duration marshaling emits nanoseconds, but UnmarshalJSON
// treats bare integers as seconds.
func (c MCPClientConfig) MarshalJSON() ([]byte, error) {
	type alias MCPClientConfig
	type shadow struct {
		ToolExecutionTimeout string `json:"tool_execution_timeout,omitempty"`
		*alias
	}
	s := shadow{alias: (*alias)(&c)}
	if c.ToolExecutionTimeout > 0 {
		s.ToolExecutionTimeout = c.ToolExecutionTimeout.String()
	}
	return json.Marshal(s)
}

func parseFlexibleDurationField(v any, fieldName string) (time.Duration, error) {
	switch t := v.(type) {
	case string:
		d, err := time.ParseDuration(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("invalid %s duration %q: %w", fieldName, t, err)
		}
		return d, nil
	case json.Number:
		raw := strings.TrimSpace(t.String())
		if raw == "" {
			return 0, fmt.Errorf("invalid %s: empty numeric value", fieldName)
		}
		if strings.Contains(raw, ".") {
			return 0, fmt.Errorf("invalid %s value %q: fractional numeric values are not allowed; use an integer nanosecond value or a duration string like \"10m\"", fieldName, raw)
		}

		// Keep parity with JavaScript-safe integer range for config interchange.
		const maxSafeJSONInt int64 = 9007199254740991
		const minSafeJSONInt int64 = -9007199254740991

		var ns int64
		if strings.ContainsAny(raw, "eE") {
			rat := new(big.Rat)
			if _, ok := rat.SetString(raw); !ok {
				return 0, fmt.Errorf("invalid %s value %q: expected an integer nanosecond value", fieldName, raw)
			}
			if rat.Denom().Cmp(big.NewInt(1)) != 0 {
				return 0, fmt.Errorf("invalid %s value %q: fractional numeric values are not allowed; use an integer nanosecond value or a duration string like \"10m\"", fieldName, raw)
			}
			if !rat.Num().IsInt64() {
				return 0, fmt.Errorf("invalid %s value %q: out of int64 range for nanoseconds", fieldName, raw)
			}
			ns = rat.Num().Int64()
		} else {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid %s value %q: expected an integer nanosecond value", fieldName, raw)
			}
			ns = parsed
		}

		if ns < minSafeJSONInt || ns > maxSafeJSONInt {
			return 0, fmt.Errorf("invalid %s value %q: exceeds safe integer range", fieldName, raw)
		}
		return time.Duration(ns), nil
	default:
		return 0, fmt.Errorf("invalid %s type %T: expected duration string (e.g. \"10m\") or number", fieldName, v)
	}
}

// NewMCPClientConfigFromMap creates a new MCP client config from a map[string]any.
func NewMCPClientConfigFromMap(configMap map[string]any) *MCPClientConfig {
	var config MCPClientConfig
	data, err := MarshalSorted(configMap)
	if err != nil {
		return nil
	}
	if err := Unmarshal(data, &config); err != nil {
		return nil
	}
	return &config
}

// MCPConnectionType defines the communication protocol for MCP connections
type MCPConnectionType string

const (
	MCPConnectionTypeHTTP      MCPConnectionType = "http"      // HTTP-based connection
	MCPConnectionTypeSTDIO     MCPConnectionType = "stdio"     // STDIO-based connection
	MCPConnectionTypeSSE       MCPConnectionType = "sse"       // Server-Sent Events connection
	MCPConnectionTypeInProcess MCPConnectionType = "inprocess" // In-process (in-memory) connection
)

// OTelNetworkTransport returns the OTel semconv network.transport value: stdio→"pipe",
// http/sse→"tcp". InProcess has none, so it returns "" and callers omit the attribute.
func (c MCPConnectionType) OTelNetworkTransport() string {
	switch c {
	case MCPConnectionTypeSTDIO:
		return "pipe"
	case MCPConnectionTypeHTTP, MCPConnectionTypeSSE:
		return "tcp"
	default:
		return ""
	}
}

// MCPStdioConfig defines how to launch a STDIO-based MCP server.
type MCPStdioConfig struct {
	Command string   `json:"command"` // Executable command to run
	Args    []string `json:"args"`    // Command line arguments
	Envs    []string `json:"envs"`    // Environment variables required
}

// MCPTLSConfig holds TLS options for HTTP and SSE MCP connections.
// InsecureSkipVerify takes priority over CACertPEM when both are set.
type MCPTLSConfig struct {
	InsecureSkipVerify bool       `json:"insecure_skip_verify,omitempty"` // Disable TLS certificate verification (development only)
	CACertPEM          *SecretVar `json:"ca_cert_pem,omitempty"`          // PEM-encoded CA certificate to trust (supports env.*)
}

// MarshalForStorage serializes MCPTLSConfig for DB persistence.
// ca_cert_pem is stored as a plain string ("env.VAR_NAME" or literal PEM).
// For HTTP API responses use json.Marshal so clients receive the full SecretVar object.
func (t *MCPTLSConfig) MarshalForStorage() ([]byte, error) {
	if t == nil {
		return []byte("null"), nil
	}
	type tlsConfigStorage struct {
		InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
		CACertPEM          string `json:"ca_cert_pem,omitempty"`
	}
	a := tlsConfigStorage{InsecureSkipVerify: t.InsecureSkipVerify}
	if t.CACertPEM != nil {
		a.CACertPEM = SecretVarAsString(t.CACertPEM)
	}
	return json.Marshal(a)
}

type MCPConnectionState string

const (
	// MCPConnectionStateHealthy means Bifrost's own periodic connection check
	// (heartbeat/list_tools for sticky clients, list_tools for per-call ones)
	// most recently succeeded. Replaces the old "connected" state — renamed
	// because that name implied exactly one shared TCP connection, which
	// doesn't fit per-call auth types cleanly.
	MCPConnectionStateHealthy MCPConnectionState = "healthy"
	// MCPConnectionStateUnstable means Bifrost's own periodic connection check
	// most recently failed with a transient-classified error. Purely
	// informational — unlike MCPConnectionStateNeedsReauth, this never gates
	// execution; tool calls are still attempted normally. Reflects only
	// Bifrost's own connection checks to the server, never the outcome of
	// real tool calls made through it, in either direction. Self-heals to
	// Healthy on the next successful check, no human action implied. Replaces
	// the old "disconnected" state.
	MCPConnectionStateUnstable MCPConnectionState = "unstable"
	// MCPConnectionStateError is a data-consistency fallback used only by the
	// HTTP client-list handler when a client is registered in the config
	// store but missing from the runtime manager entirely — a different,
	// deeper anomaly than anything in the normal connect/health lifecycle
	// below, which never assigns this value.
	MCPConnectionStateError               MCPConnectionState = "error"                // Client is in an error state, and cannot be used
	MCPConnectionStatePendingVerification MCPConnectionState = "pending_verification" // Declared (typically via config.json) but the one-time auth/test flow has not been completed by an admin yet
	MCPConnectionStateDisabled            MCPConnectionState = "disabled"             // Client is intentionally disabled by the user
	// MCPConnectionStateNeedsReauth means this client was previously authorized and
	// connected at least once, but a credential an admin is responsible for can no
	// longer be used and needs a human to repair it. This is distinct from
	// MCPConnectionStatePendingVerification, which means the one-time initial setup was
	// never completed in the first place; NeedsReauth means setup succeeded once and the
	// credential died later. What "the credential" is depends on the auth type:
	//   - Shared-connection auth types: the connection credential itself died; the
	//     upstream OAuth token was rejected/expired with no way to silently recover
	//     (see ErrOAuth2TokenExpired) and the connection cannot be re-established
	//     until an admin reauthorizes. The runtime manager holds this state, and
	//     unlike Unstable, it IS a hard gate: prepareToolExecution refuses tool
	//     calls outright rather than attempting them against a known-dead
	//     credential, and the periodic checker goes quiet on this client until
	//     an explicit reauthorize — no auto-heal.
	//   - Per-user auth types: a response-only projection computed when listing the
	//     registry, never stored in the runtime manager (whose state stays
	//     healthy). It means the retained admin credential used for periodic
	//     tool-list discovery needs repair; end-user credentials and tool calls
	//     keep working, only the tool list stops refreshing until an admin repairs
	//     the discovery credential.
	MCPConnectionStateNeedsReauth MCPConnectionState = "needs_reauth"
	// MCPConnectionStateDegraded is a read-time aggregate value, never a
	// node's own local State: it means multiple instances of a distributed
	// deployment each hold a different self-reported state for the same
	// client (e.g. one instance's periodic check currently sees Healthy
	// while another's currently sees Unstable). Only meaningful for states
	// that can genuinely vary per instance in the first place (Healthy,
	// Unstable, PendingVerification); NeedsReauth/Disabled are config-
	// sourced facts expected to already agree everywhere, so disagreement
	// on those is a propagation problem, not something this value covers.
	// A single-instance deployment never produces this value.
	MCPConnectionStateDegraded MCPConnectionState = "degraded"
)

// MCPClientState represents a connected MCP client with its configuration and tools.
// It is used internally by the MCP manager to track the state of a connected MCP client.
type MCPClientState struct {
	Name            string                   // Unique name for this client
	Conn            *client.Client           // Active MCP client connection
	ExecutionConfig *MCPClientConfig         // Tool filtering settings
	ToolMap         map[string]ChatTool      // Available tools mapped by name
	ToolNameMapping map[string]string        // Maps sanitized_name -> original_mcp_name (e.g., "notion_search" -> "notion-search")
	ConnectionInfo  *MCPClientConnectionInfo `json:"connection_info"` // Connection metadata for management
	CancelFunc      context.CancelFunc       `json:"-"`               // Cancel function for SSE connections (not serialized)
	State           MCPConnectionState       // Connection state (healthy, unstable, needs_reauth, ...)
	ConnGeneration  uint64                   `json:"-"` // Counts connection swaps; late writers bound to an older Conn compare against it to detect staleness (not serialized)
	LastToolsHash   string                   `json:"-"` // Content hash of the last ToolMap/ToolNameMapping the tools-change callback fired for; gates the funnel to genuine changes only (not serialized)
}

// MCPClientConnectionInfo stores metadata about how a client is connected.
type MCPClientConnectionInfo struct {
	Type               MCPConnectionType `json:"type"`                           // Connection type (HTTP, STDIO, SSE, or InProcess)
	ConnectionURL      *string           `json:"connection_url,omitempty"`       // HTTP/SSE endpoint URL (for HTTP/SSE connections)
	StdioCommandString *string           `json:"stdio_command_string,omitempty"` // Command string for display (for STDIO connections)
}

// MCPClient represents a connected MCP client with its configuration and tools,
// and connection information, after it has been initialized.
// It is returned by GetMCPClients() method in bifrost.
type MCPClient struct {
	Config *MCPClientConfig   `json:"config"` // Tool filtering settings
	Tools  []ChatToolFunction `json:"tools"`  // Available tools
	State  MCPConnectionState `json:"state"`  // Connection state
}
