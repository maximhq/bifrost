package tables

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
)

// MCPServerAuthMode controls how Bifrost's /mcp endpoint authenticates inbound
// MCP clients. It does not affect how Bifrost authenticates to upstream MCP
// servers (governed by MCPClientConfig.AuthType).
type MCPServerAuthMode string

// AllowAnyRedirectURI as an allowed_redirect_uris entry approves every remote
// callback that passes the redirect URI shape checks.
const AllowAnyRedirectURI = "*"

const (
	DefaultAuthCodeTTL    = 300 // 5 minutes
	MaxAuthCodeTTL        = 900 // 15 minutes — hard ceiling; enforced at config save and at code issuance
	DefaultAccessTokenTTL = 600 // 10 minutes
)

const (
	// MCPServerAuthModeHeaders accepts header credentials only: x-bf-vk,
	// Authorization: Bearer <vk>, x-api-key, x-bf-mcp-session-id.
	// Discovery endpoints return 404. Default — today's behavior.
	MCPServerAuthModeHeaders MCPServerAuthMode = "headers"

	// MCPServerAuthModeBoth accepts both header credentials and Bifrost-issued
	// JWTs. Discovery endpoints are live; existing header-credential clients
	// that never receive a 401 are unaffected.
	MCPServerAuthModeBoth MCPServerAuthMode = "both"

	// MCPServerAuthModeOAuth accepts Bifrost-issued JWTs only. Header
	// credentials (VK / api-key / session) are rejected on /mcp.
	// WARNING: existing virtual-key MCP integrations will stop working.
	MCPServerAuthModeOAuth MCPServerAuthMode = "oauth"
)

// OAuth2ServerConfig holds the OAuth2 authorization-server settings serialized
// as JSON into config_client.oauth2_server_config_json. Only meaningful when
// MCPServerAuthMode is MCPServerAuthModeBoth or MCPServerAuthModeOAuth.
// Not a table of its own.
type OAuth2ServerConfig struct {
	AllowedRedirectURIs []string `json:"allowed_redirect_uris,omitempty"` // Exact administrator-approved remote callbacks, or AllowAnyRedirectURI.
	// IssuerURL is Bifrost's OAuth authorization-server identity — it appears
	// as the `issuer` in discovery documents and as the `iss` claim in every
	// issued JWT. Supports env var syntax ("env.MY_VAR"). Required whenever MCP
	// OAuth discovery is enabled (mcp_server_auth_mode is "oauth" or "both") -
	// both the config-save handler and load-time validation reject an unset
	// value in that case. Left unset with discovery disabled,
	// BuildBaseURL(request) is used as a per-request fallback for
	// header-mode-only deployments where no OAuth issuer identity is needed.
	IssuerURL *schemas.SecretVar `json:"issuer_url,omitempty"`

	// BrowserBaseURL is where users' browsers reach Bifrost when MCP clients
	// reach it on another hostname. The authorization endpoint, the consent
	// redirect and the device sign-in pages use it; the issuer and every
	// server-to-server endpoint stay on IssuerURL. Unset means IssuerURL.
	BrowserBaseURL *schemas.SecretVar `json:"browser_base_url,omitempty"`

	// AdditionalIssuerURLs are further issuers for hostnames that cannot reach
	// IssuerURL, such as a private tunnel. A request whose host matches one is
	// served that issuer; any other request gets IssuerURL, which stays the primary.
	AdditionalIssuerURLs []*schemas.SecretVar `json:"additional_issuer_urls,omitempty"`

	// AuthCodeTTL is the lifetime of the one-time authorization code issued by
	// /oauth2/authorize and exchanged at /oauth2/token (seconds, default 300).
	// The code is single-use — it is invalidated the moment it is exchanged or
	// expires. Short TTL is intentional: if the window lapses the user simply
	// re-authenticates. Capped at MaxAuthCodeTTL (900s); larger stored values
	// are rejected at config save and clamped to the cap at issuance.
	AuthCodeTTL int `json:"auth_code_ttl"`

	// AccessTokenTTL is the lifetime of the issued JWT Bearer token (seconds,
	// default 600 = 10 min). When the token expires the client uses its refresh
	// token to silently obtain a new one without any user interaction.
	AccessTokenTTL int `json:"access_token_ttl"`

	// DisableVKIdentity removes virtual-key identity from the OAuth consent flow:
	// vk is neither offered on the consent page nor accepted if submitted, and
	// existing vk-mode grants are rejected immediately at request time and cannot
	// refresh. Anonymous session identity is unaffected — that is governed
	// separately by EnforceAuthOnInference. Honored only when an identity provider
	// is configured, so it can never strip the consent flow of every identity
	// option. Only meaningful when MCPServerAuthMode is oauth.
	DisableVKIdentity bool `json:"disable_vk_identity,omitempty"`

	Gateways *ClientGatewaysConfig `json:"gateways,omitempty"` // client gateways under /gateway/<client>; each needs IssuerURL

	// Refresh tokens have no hard expiry — they are invalidated only by:
	//   - rotation on use (each /oauth2/token refresh call issues a new token
	//     and immediately invalidates the previous one)
	//   - bf_sub liveness check on refresh (VK or user deleted / deactivated →
	//     invalid_grant, forcing re-authentication)
	//   - explicit revocation via the OAuth Grants UI
	//   - DisableVKIdentity enabled (vk-mode grants denied on refresh)
	// No RefreshTokenTTL field exists by design — there is no timer, only
	// explicit invalidation paths.
}

// ClientGatewaysConfig holds one block per client gateway served under
// /gateway/<client>. A client gateway lets a coding client sign in to Bifrost
// with its own /login flow (an RFC 8628 device grant approved on the OAuth
// consent page) instead of a hand-configured virtual key. Gateways reuse the
// OAuth2 authorization server's signing key, consent page and refresh-token
// rotation, but not the /mcp auth mode: each is served whenever it is enabled
// and IssuerURL is set, even while /mcp stays on header auth. A nil block, or
// Enabled=false, leaves that gateway's routes answering 404.
type ClientGatewaysConfig struct {
	ClaudeCode *ClaudeCodeGatewayConfig `json:"claude_code,omitempty"` // /gateway/claude-code
}

// ClaudeCodeGatewayConfig configures the Claude Code gateway: the device sign-in
// Claude Code's /login (Cloud gateway) runs, Anthropic Messages inference,
// managed settings and OTLP telemetry. ManagedSettings is the
// managed-settings.json document served at /gateway/claude-code/managed/settings
// to every signed-in client; nil means no managed policy (the endpoint answers
// 404), while an empty object is an empty policy.
type ClaudeCodeGatewayConfig struct {
	Enabled         bool                          `json:"enabled"`
	ManagedSettings map[string]any                `json:"managed_settings"` // no omitempty: {} (empty policy) must survive a round trip
	Telemetry       *ClientGatewayTelemetryConfig `json:"telemetry,omitempty"`
}

// ClientGatewayTelemetryConfig keeps the OTLP telemetry a client gateway's
// clients send (metrics, logs and traces) instead of discarding it. Each export
// is stamped with the Bifrost identity the client signed in as and forwarded
// asynchronously to every destination. DropAttributes lists attribute keys
// removed at every level before forwarding (for example "prompt", when clients
// are configured to log prompt text).
type ClientGatewayTelemetryConfig struct {
	Enabled        bool                                `json:"enabled"`
	Destinations   []ClientGatewayTelemetryDestination `json:"destinations,omitempty"`
	DropAttributes []string                            `json:"drop_attributes,omitempty"`
}

// ClientGatewayTelemetryDestination is one OTLP/HTTP collector. Endpoint is the
// collector's base URL; each signal is posted as protobuf to
// {endpoint}/v1/{metrics|logs|traces}, as OTEL_EXPORTER_OTLP_ENDPOINT works.
// Header values are plain values or env./vault. references, like provider keys;
// the config API redacts plain values.
type ClientGatewayTelemetryDestination struct {
	Endpoint *schemas.SecretVar            `json:"endpoint"`
	Headers  map[string]*schemas.SecretVar `json:"headers,omitempty"`
}

// TelemetryEnabled reports whether this gateway forwards telemetry.
func (c *ClaudeCodeGatewayConfig) TelemetryEnabled() bool {
	return c != nil && c.Telemetry != nil && c.Telemetry.Enabled
}

// Validate checks a telemetry block that is switched on: at least one
// destination, every endpoint an http(s) URL once resolved, and every header with a value.
// A collector listed twice is refused either way; otherwise a switched-off block is not checked, so an
// operator can keep destinations around while forwarding is paused.
func (c *ClientGatewayTelemetryConfig) Validate(path string) error {
	if c == nil {
		return nil
	}
	// A collector listed twice is refused even while forwarding is paused: its redacted headers
	// could not be told apart on the next save, which would then fail.
	seen := make(map[string]int, len(c.Destinations))
	for i, d := range c.Destinations {
		key, ok := d.endpointKey()
		if !ok {
			continue
		}
		if first, dup := seen[key]; dup {
			return fmt.Errorf("%s.destinations[%d].endpoint repeats destinations[%d]; list each collector once", path, i, first)
		}
		seen[key] = i
	}
	if !c.Enabled {
		return nil
	}
	if len(c.Destinations) == 0 {
		return fmt.Errorf("%s.destinations must list at least one collector when telemetry is enabled", path)
	}
	for i, d := range c.Destinations {
		if d.Endpoint == nil || !d.Endpoint.IsSet() {
			return fmt.Errorf("%s.destinations[%d].endpoint is required", path, i)
		}
		// A reference is checked by what it resolves to, as the issuer URL is: unresolved is an
		// error, and the error names the reference, never the resolved value.
		endpoint := strings.TrimSpace(d.Endpoint.GetValue())
		if endpoint == "" && d.Endpoint.IsFromSecret() {
			return fmt.Errorf("%s.destinations[%d].endpoint references %s, which resolves to an empty value", path, i, d.Endpoint.GetRawRef())
		}
		if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			if d.Endpoint.IsFromSecret() {
				return fmt.Errorf("%s.destinations[%d].endpoint references %s, which does not resolve to an http or https URL", path, i, d.Endpoint.GetRawRef())
			}
			return fmt.Errorf("%s.destinations[%d].endpoint must be an http or https URL", path, i)
		}
		for name, v := range d.Headers {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("%s.destinations[%d].headers has an empty header name", path, i)
			}
			if v == nil || (!v.IsFromSecret() && strings.TrimSpace(v.GetValue()) == "") {
				return fmt.Errorf("%s.destinations[%d].headers.%s needs a value", path, i, name)
			}
		}
	}
	for _, key := range c.DropAttributes {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s.drop_attributes has an empty attribute key", path)
		}
	}
	return nil
}

// DropDuplicateDestinations removes every collector whose endpoint is listed more than once and
// returns the indexes removed. Startup uses it so a config.json listing a collector twice still
// boots, where Validate would refuse the whole block; no entry is kept, since which one's headers
// are meant cannot be told (as duplicate agents are skipped).
func (c *ClientGatewayTelemetryConfig) DropDuplicateDestinations() []int {
	if c == nil {
		return nil
	}
	counts := make(map[string]int, len(c.Destinations))
	for _, d := range c.Destinations {
		if key, ok := d.endpointKey(); ok {
			counts[key]++
		}
	}
	var dropped []int
	kept := make([]ClientGatewayTelemetryDestination, 0, len(c.Destinations))
	for i, d := range c.Destinations {
		if key, ok := d.endpointKey(); ok && counts[key] > 1 {
			dropped = append(dropped, i)
			continue
		}
		kept = append(kept, d)
	}
	if len(dropped) > 0 {
		c.Destinations = kept
	}
	return dropped
}

// endpointKey identifies a collector: its env or vault reference, or its trimmed URL.
func (d ClientGatewayTelemetryDestination) endpointKey() (string, bool) {
	if d.Endpoint == nil || !d.Endpoint.IsSet() {
		return "", false
	}
	if d.Endpoint.IsFromSecret() {
		return d.Endpoint.GetRawRef(), true
	}
	return strings.TrimSpace(d.Endpoint.GetValue()), true
}

// StorageJSON is the oauth2_server_config_json written for c. Its secret fields (the issuer,
// browser and additional issuer URLs, and each telemetry endpoint and header value) keep only an env. or vault. reference, never its
// resolved value, as MCP client headers are stored; the reference is resolved again on load.
// Everything else, managed settings included, is written as is.
func (c *OAuth2ServerConfig) StorageJSON() (string, error) {
	if c == nil {
		return "", nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return "", err
	}
	stripResolvedSecretValue(doc["issuer_url"])
	stripResolvedSecretValue(doc["browser_base_url"])
	issuers, _ := doc["additional_issuer_urls"].([]any)
	for _, issuer := range issuers {
		stripResolvedSecretValue(issuer)
	}
	for _, d := range telemetryDestinationsOf(doc) {
		dest, _ := d.(map[string]any)
		stripResolvedSecretValue(dest["endpoint"])
		headers, _ := dest["headers"].(map[string]any)
		for _, v := range headers {
			stripResolvedSecretValue(v)
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// stripResolvedSecretValue blanks the value of one serialized SecretVar that carries an env. or
// vault. reference.
func stripResolvedSecretValue(v any) {
	secret, ok := v.(map[string]any)
	if !ok {
		return
	}
	if ref, _ := secret["ref"].(string); strings.HasPrefix(ref, "env.") || strings.HasPrefix(ref, "vault.") {
		if _, has := secret["value"]; has {
			secret["value"] = ""
		}
	}
}

// telemetryDestinationsOf returns the serialized Claude Code telemetry destinations in doc.
func telemetryDestinationsOf(doc map[string]any) []any {
	gateways, _ := doc["gateways"].(map[string]any)
	claudeCode, _ := gateways["claude_code"].(map[string]any)
	telemetry, _ := claudeCode["telemetry"].(map[string]any)
	destinations, _ := telemetry["destinations"].([]any)
	return destinations
}

// ClaudeCodeGateway returns the Claude Code gateway block, or nil when it is unset.
func (c *OAuth2ServerConfig) ClaudeCodeGateway() *ClaudeCodeGatewayConfig {
	if c == nil || c.Gateways == nil {
		return nil
	}
	return c.Gateways.ClaudeCode
}

// IsClaudeCodeGatewayEnabled reports whether the Claude Code gateway toggle is
// on. It does not check IssuerURL, which the gateway also requires.
func (c *OAuth2ServerConfig) IsClaudeCodeGatewayEnabled() bool {
	cc := c.ClaudeCodeGateway()
	return cc != nil && cc.Enabled
}

// AnyClientGatewayEnabled reports whether at least one client gateway is on, and
// so whether IssuerURL is required on that account.
func (c *OAuth2ServerConfig) AnyClientGatewayEnabled() bool {
	return c.IsClaudeCodeGatewayEnabled()
}

// Redacted returns c with every client gateway telemetry credential fully
// masked: header values, and an endpoint that comes from env or vault. The
// references stay visible, so the dashboard can show and save them unchanged
// (an env or vault value is re-resolved from its reference when read back).
// The copy shares nothing mutable with c; c is returned as is when it holds no
// telemetry.
func (c *OAuth2ServerConfig) Redacted() *OAuth2ServerConfig {
	cc := c.ClaudeCodeGateway()
	if cc == nil || cc.Telemetry == nil {
		return c
	}
	telemetry := *cc.Telemetry
	telemetry.Destinations = make([]ClientGatewayTelemetryDestination, len(cc.Telemetry.Destinations))
	for i, d := range cc.Telemetry.Destinations {
		redacted := ClientGatewayTelemetryDestination{Endpoint: d.Endpoint}
		if d.Endpoint.IsFromSecret() {
			redacted.Endpoint = d.Endpoint.FullyRedacted()
		}
		if d.Headers != nil {
			redacted.Headers = make(map[string]*schemas.SecretVar, len(d.Headers))
			for name, v := range d.Headers {
				redacted.Headers[name] = v.FullyRedacted()
			}
		}
		telemetry.Destinations[i] = redacted
	}
	claudeCode := *cc
	claudeCode.Telemetry = &telemetry
	gateways := *c.Gateways
	gateways.ClaudeCode = &claudeCode
	out := *c
	out.Gateways = &gateways
	return &out
}

// ValidateClientGateways checks every client gateway block. IssuerURL is checked
// by the callers, which word that error for their own surface.
func (c *OAuth2ServerConfig) ValidateClientGateways() error {
	if cc := c.ClaudeCodeGateway(); cc != nil {
		if err := cc.Telemetry.Validate("oauth2_server_config.gateways.claude_code.telemetry"); err != nil {
			return err
		}
	}
	return nil
}

// DefaultOAuth2ServerConfig returns sensible defaults for the AS-specific settings.
func DefaultOAuth2ServerConfig() *OAuth2ServerConfig {
	return &OAuth2ServerConfig{
		AuthCodeTTL:    DefaultAuthCodeTTL,
		AccessTokenTTL: DefaultAccessTokenTTL,
	}
}

// OAuth2SigningKey holds the single RS256 keypair used to sign Bifrost-issued
// JWTs. Stored as JSON in governance_config under GovernanceConfigKeyOAuth2SigningKey.
// The private key PEM is encrypted via framework/encrypt before storage.
type OAuth2SigningKey struct {
	KID              string `json:"kid"`                         // key ID embedded in JWT headers
	PrivateKeyPEM    string `json:"private_key_pem"`             // encrypted at rest via framework/encrypt when EncryptionStatus is "encrypted"
	PublicKeyPEM     string `json:"public_key_pem"`              // plaintext; public key is not sensitive
	EncryptionStatus string `json:"encryption_status,omitempty"` // EncryptionStatusPlainText or EncryptionStatusEncrypted — records whether PrivateKeyPEM was encrypted at write time so reads do not depend on the current encrypt.IsEnabled() state
}

// Encrypt encrypts PrivateKeyPEM in place and stamps EncryptionStatus, mirroring
// the BeforeSave hooks on secret-bearing tables. Because the signing key is
// persisted as a JSON blob inside governance_config (not its own GORM row) it
// cannot rely on GORM hooks, so callers invoke this before marshaling/storing.
func (k *OAuth2SigningKey) Encrypt() error {
	if encrypt.IsEnabled() && k.PrivateKeyPEM != "" {
		if err := encryptString(&k.PrivateKeyPEM); err != nil {
			return fmt.Errorf("failed to encrypt oauth2 signing key: %w", err)
		}
		k.EncryptionStatus = EncryptionStatusEncrypted
	} else {
		k.EncryptionStatus = EncryptionStatusPlainText
	}
	return nil
}

// Decrypt decrypts PrivateKeyPEM in place based on the stored EncryptionStatus
// marker, mirroring the AfterFind hooks on secret-bearing tables. Keys written
// before encryption was enabled are marked plain_text and returned as-is.
// Records written before this marker existed carry an empty status and fall back
// to the historical encrypt.IsEnabled() behavior.
func (k *OAuth2SigningKey) Decrypt() error {
	if k.PrivateKeyPEM == "" {
		return nil
	}
	shouldDecrypt := k.EncryptionStatus == EncryptionStatusEncrypted ||
		(k.EncryptionStatus == "" && encrypt.IsEnabled())
	if shouldDecrypt {
		if err := decryptString(&k.PrivateKeyPEM); err != nil {
			return fmt.Errorf("failed to decrypt oauth2 signing key: %w", err)
		}
	}
	return nil
}

// GovernanceConfigKeyOAuth2SigningKey is the governance_config key under which
// the OAuth2 signing keypair is stored.
const GovernanceConfigKeyOAuth2SigningKey = "oauth2_signing_key"
