package schemas

import (
	"slices"
	"time"
)

type AcceptedBifrostCredential struct {
	Header string
	Value  string
}

// AgentGatewayAuthPolicy lets the core Agent Gateway describe downstream
// authentication in generated gateway cards without importing transport or
// configuration packages. The transport layer supplies it at manager
// construction so card generation stays consistent with the live HTTP
// authentication behavior. The generated card always advertises the
// bifrostVirtualKey scheme because the gateway always accepts a Bifrost virtual
// key; the policy only controls whether that credential is required.
type AgentGatewayAuthPolicy struct {
	// EnforceAuthentication is read at card-generation time rather than
	// captured once, because enforce_auth_on_inference is reconfigurable at
	// runtime. A nil func means authentication is optional, so the virtual-key
	// scheme is advertised but not required.
	EnforceAuthentication func() bool
}

// UpstreamAuth configures authentication and TLS for one upstream endpoint.
// Agent Gateway supports only shared, noninteractive credentials.
type UpstreamAuth struct {
	Type            MCPAuthType          `json:"type"`
	Headers         map[string]SecretVar `json:"headers,omitempty"`
	OAuth           *UpstreamOAuthConfig `json:"oauth,omitempty"`
	SecuritySchemes []string             `json:"security_schemes,omitempty"`
	Advanced        bool                 `json:"advanced,omitempty"`
	TLS             *UpstreamTLSConfig   `json:"tls,omitempty"`
}

// UpstreamOAuthConfig configures a shared OAuth client-credentials grant.
type UpstreamOAuthConfig struct {
	ProviderURL  string     `json:"provider_url,omitempty"`
	DiscoveryURL string     `json:"discovery_url,omitempty"`
	TokenURL     string     `json:"token_url,omitempty"`
	ClientID     *SecretVar `json:"client_id"`
	ClientSecret *SecretVar `json:"client_secret"`
	Scopes       []string   `json:"scopes,omitempty"`
	Resource     string     `json:"resource,omitempty"`
}

// UpstreamTLSConfig applies to HTTP and gRPC upstream connections.
type UpstreamTLSConfig struct {
	InsecureSkipVerify bool       `json:"insecure_skip_verify,omitempty"`
	CACertPEM          *SecretVar `json:"ca_cert_pem,omitempty"`
	ClientCertPEM      *SecretVar `json:"client_cert_pem,omitempty"`
	ClientKeyPEM       *SecretVar `json:"client_key_pem,omitempty"`
}

func (a *UpstreamAuth) Redacted() *UpstreamAuth {
	if a == nil {
		return nil
	}
	copy := *a
	copy.Headers = make(map[string]SecretVar, len(a.Headers))
	for name, value := range a.Headers {
		copy.Headers[name] = *value.FullyRedacted()
	}
	copy.SecuritySchemes = slices.Clone(a.SecuritySchemes)
	if a.OAuth != nil {
		oauth := *a.OAuth
		oauth.ClientID = a.OAuth.ClientID.FullyRedacted()
		oauth.ClientSecret = a.OAuth.ClientSecret.FullyRedacted()
		oauth.Scopes = slices.Clone(a.OAuth.Scopes)
		copy.OAuth = &oauth
	}
	if a.TLS != nil {
		tlsConfig := *a.TLS
		tlsConfig.CACertPEM = a.TLS.CACertPEM.FullyRedacted()
		tlsConfig.ClientCertPEM = a.TLS.ClientCertPEM.FullyRedacted()
		tlsConfig.ClientKeyPEM = a.TLS.ClientKeyPEM.FullyRedacted()
		copy.TLS = &tlsConfig
	}
	return &copy
}

// AgentRegistration is the durable Agent Gateway registration model. It is the
// single source of truth an administrator controls: which upstream agent exists,
// how Bifrost discovers and authenticates to it, and which virtual keys may
// reach it. The runtime rebuilds its in-memory state from these rows, so a
// restart restores identical routing and authorization.
//
// Name is the stable identifier used in gateway URLs, grant rows, and logs.
// AgentCardURL is the upstream discovery URL, fetched on every card and message
// request rather than cached as truth. Enabled false keeps the durable row but
// removes the agent from the runtime, so protocol traffic no longer resolves it.
// DiscoveryAuth authenticates the card fetch and RuntimeAuth the proxied calls.
// AllowByDefault authorizes any valid virtual key without an explicit
// grant row; VirtualKeyIDs otherwise lists the exact permitted keys and is kept
// sorted so authorization can binary-search it on the hot path.
type AgentRegistration struct {
	Name                                   string        `json:"name"`
	AgentCardURL                           string        `json:"agent_card_url"`
	Tenant                                 string        `json:"tenant,omitempty"`
	Enabled                                bool          `json:"enabled"`
	AllowByDefault                         bool          `json:"allow_by_default"`
	ForwardAcceptedCredential              bool          `json:"forward_accepted_credential"`
	ForwardAcceptedCredentialOverridesAuth bool          `json:"forward_accepted_credential_overrides_auth"`
	DiscoveryAuth                          *UpstreamAuth `json:"discovery_auth,omitempty"`
	RuntimeAuth                            *UpstreamAuth `json:"runtime_auth,omitempty"`
	ExtensionURIs                          []string      `json:"extension_uris,omitempty"`
	VirtualKeyIDs                          []string      `json:"virtual_key_ids,omitempty"`
	// ConfigHash checkpoints the last config.json-driven write so startup
	// reconciliation can detect file edits. It is set only by the file sync,
	// never by API writes, and is excluded from all API responses.
	ConfigHash string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// AgentRegistrationView is safe for management API reads. Credential metadata
// is retained, but secret values are fully redacted.
type AgentRegistrationView struct {
	Name                                   string        `json:"name"`
	AgentCardURL                           string        `json:"agent_card_url"`
	Tenant                                 string        `json:"tenant,omitempty"`
	Enabled                                bool          `json:"enabled"`
	AllowByDefault                         bool          `json:"allow_by_default"`
	ForwardAcceptedCredential              bool          `json:"forward_accepted_credential"`
	ForwardAcceptedCredentialOverridesAuth bool          `json:"forward_accepted_credential_overrides_auth"`
	DiscoveryAuth                          *UpstreamAuth `json:"discovery_auth,omitempty"`
	RuntimeAuth                            *UpstreamAuth `json:"runtime_auth,omitempty"`
	ExtensionURIs                          []string      `json:"extension_uris,omitempty"`
	VirtualKeyIDs                          []string      `json:"virtual_key_ids,omitempty"`
	CreatedAt                              time.Time     `json:"created_at"`
	UpdatedAt                              time.Time     `json:"updated_at"`
}

// Redacted projects a registration for management reads. Grant IDs are copied
// so a response can never alias runtime state, and both credentials are masked
// so no read path can leak an upstream secret.
func (r AgentRegistration) Redacted() AgentRegistrationView {
	return AgentRegistrationView{
		Name:                                   r.Name,
		AgentCardURL:                           r.AgentCardURL,
		Tenant:                                 r.Tenant,
		Enabled:                                r.Enabled,
		AllowByDefault:                         r.AllowByDefault,
		ForwardAcceptedCredential:              r.ForwardAcceptedCredential,
		ForwardAcceptedCredentialOverridesAuth: r.ForwardAcceptedCredentialOverridesAuth,
		DiscoveryAuth:                          r.DiscoveryAuth.Redacted(),
		RuntimeAuth:                            r.RuntimeAuth.Redacted(),
		ExtensionURIs:                          slices.Clone(r.ExtensionURIs),
		VirtualKeyIDs:                          slices.Clone(r.VirtualKeyIDs),
		CreatedAt:                              r.CreatedAt,
		UpdatedAt:                              r.UpdatedAt,
	}
}

// AgentPushConfig is the durable record of one downstream push callback
// registration and the independent credential the upstream agent must present
// on Bifrost's push ingress. Downstream secrets are write-only: they are used
// only when Bifrost re-originates delivery and are never returned by any read.
// The ingress credential is stored as a hash only, so no read path can recover it.
type AgentPushConfig struct {
	AgentName string `json:"agent_name"`
	TaskID    string `json:"task_id"`
	ConfigID  string `json:"config_id"`
	// Tenant is the downstream client's logical Agent-interface tenant. It is
	// preserved for local reads; the selected upstream SDK transport supplies its
	// own interface tenant independently.
	Tenant string `json:"tenant,omitempty"`
	// URL is the downstream client's callback endpoint.
	URL string `json:"url"`
	// Token is the downstream client's own notification token, replayed verbatim
	// on re-originated deliveries.
	Token *SecretVar `json:"token,omitempty"`
	// AuthScheme and AuthCredentials mirror the A2A PushAuthInfo the downstream
	// client registered for its callback endpoint.
	AuthScheme      string     `json:"auth_scheme,omitempty"`
	AuthCredentials *SecretVar `json:"auth_credentials,omitempty"`
	// IngressTokenHash is the SHA-256 hex digest of the gateway-minted token the
	// upstream agent presents when pushing to Bifrost.
	IngressTokenHash string    `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type AgentPushConfigView struct {
	AgentName string    `json:"agent_name"`
	TaskID    string    `json:"task_id"`
	ConfigID  string    `json:"config_id"`
	Tenant    string    `json:"tenant,omitempty"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentPushConfigQuery struct {
	AgentNames []string
	TaskID     string
	ConfigID   string
	URL        string
	Limit      int
	Offset     int
}

func (c AgentPushConfig) Redacted() AgentPushConfigView {
	return AgentPushConfigView{
		AgentName: c.AgentName,
		TaskID:    c.TaskID,
		ConfigID:  c.ConfigID,
		Tenant:    c.Tenant,
		URL:       c.URL,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// Push delivery lifecycle states for AgentPushDelivery.Status.
const (
	AgentPushDeliveryStatusPending   = "pending"
	AgentPushDeliveryStatusDelivered = "delivered"
	AgentPushDeliveryStatusDead      = "dead"
)

// AgentPushDelivery is one durable outbox row: a verified upstream push queued
// for re-originated delivery to the downstream callback. ID is a content digest
// so an upstream retransmission of the same event deduplicates instead of
// producing a second delivery. Rows carry no secrets.
type AgentPushDelivery struct {
	ID            string    `json:"id"`
	AgentName     string    `json:"agent_name"`
	TaskID        string    `json:"task_id"`
	ConfigID      string    `json:"config_id"`
	Payload       string    `json:"payload,omitempty"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     string    `json:"last_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
