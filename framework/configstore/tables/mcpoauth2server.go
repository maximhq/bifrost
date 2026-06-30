package tables

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
)

// MCPServerAuthMode controls how Bifrost's /mcp endpoint authenticates inbound
// MCP clients. It does not affect how Bifrost authenticates to upstream MCP
// servers (governed by MCPClientConfig.AuthType).
type MCPServerAuthMode string

const (
	DefaultAuthCodeTTL    = 600 // 10 minutes
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
	// IssuerURL is Bifrost's OAuth authorization-server identity — it appears
	// as the `issuer` in discovery documents and as the `iss` claim in every
	// issued JWT. Supports env var syntax ("env.MY_VAR"). When empty,
	// BuildBaseURL(request) is used as a per-request fallback, which works for
	// single-host / dev deployments. Multi-host or reverse-proxy deployments
	// MUST set a stable value; token verification fails when the Host header
	// differs across nodes.
	IssuerURL *schemas.SecretVar `json:"issuer_url,omitempty"`

	// AuthCodeTTL is the lifetime of the one-time authorization code issued by
	// /oauth2/authorize and exchanged at /oauth2/token (seconds, default 600).
	// The code is single-use — it is invalidated the moment it is exchanged or
	// expires. Short TTL is intentional: if the window lapses the user simply
	// re-authenticates.
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
