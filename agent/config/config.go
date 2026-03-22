// Package config defines the agent configuration types and defaults.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"sync"
	"time"
)

// AgentConfig is the top-level configuration for the Bifrost agent.
// It is fetched from the Bifrost management API and cached locally.
type AgentConfig struct {
	// GatewayURL is the base URL of the Bifrost gateway (e.g. "https://gateway.example.com").
	GatewayURL string `json:"gateway_url"`

	// VirtualKey is the virtual key to inject into intercepted requests (x-bf-vk header).
	VirtualKey string `json:"virtual_key"`

	// CACertPEM is the PEM-encoded CA certificate used for TLS MITM.
	CACertPEM string `json:"ca_cert_pem"`

	// CAKeyPEM is the PEM-encoded CA private key used for signing per-domain certs.
	CAKeyPEM string `json:"ca_key_pem"`

	// Domains is the list of AI provider domains to intercept.
	Domains []DomainRule `json:"domains"`

	// ConfigVersion is an opaque version identifier for change detection.
	ConfigVersion int64 `json:"config_version"`

	// PollInterval controls how often the agent syncs config from the management API.
	PollInterval Duration `json:"poll_interval"`

	// AgentToken is the authentication token for the management API.
	// Stored locally, not part of the server response.
	AgentToken string `json:"agent_token,omitempty"`

	// ManagementURL is the URL of the management API endpoint.
	// Stored locally, not part of the server response.
	ManagementURL string `json:"management_url,omitempty"`
}

// DomainRule maps an AI provider domain to a Bifrost gateway integration path.
type DomainRule struct {
	// Hostname is the AI provider domain to intercept (e.g. "api.openai.com").
	Hostname string `json:"hostname"`

	// IntegrationPrefix is the gateway path prefix (e.g. "/openai", "/anthropic").
	// The original request path is appended after this prefix.
	IntegrationPrefix string `json:"integration_prefix"`

	// PreservePath controls whether the original URL path is appended after the prefix.
	// Almost always true.
	PreservePath bool `json:"preserve_path"`

	// Passthrough indicates whether to use the passthrough endpoint instead.
	// When true, IntegrationPrefix should be e.g. "/openai_passthrough".
	Passthrough bool `json:"passthrough,omitempty"`

	// ProxyPathPrefixes restricts which request paths are sent to the Bifrost gateway.
	// If empty, ALL paths are proxied. If set, only paths matching one of these
	// prefixes go through Bifrost — all other paths are relayed directly to the
	// origin server (no gateway roundtrip).
	// Example: ["/backend-api/"] for chatgpt.com to only proxy API calls.
	ProxyPathPrefixes []string `json:"proxy_path_prefixes,omitempty"`
}

// ShouldProxyPath returns true if the given request path should be sent to
// the Bifrost gateway. If ProxyPathPrefixes is empty, all paths are proxied.
func (r *DomainRule) ShouldProxyPath(path string) bool {
	if len(r.ProxyPathPrefixes) == 0 {
		return true // no filter = proxy everything
	}
	for _, prefix := range r.ProxyPathPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Duration wraps time.Duration for JSON marshaling as seconds.
type Duration struct {
	time.Duration
}

// MarshalJSON encodes the duration as seconds.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Duration.String() + `"`), nil
}

// UnmarshalJSON decodes a duration from seconds (number) or a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := string(b)
	// Try as a number (seconds)
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		var secs float64
		for i, c := range s {
			if c == '.' {
				// Parse float
				_ = i // handled by parseDuration fallback
				break
			}
		}
		// Fall through to ParseDuration with "s" suffix
		dur, err := time.ParseDuration(s + "s")
		if err == nil {
			d.Duration = dur
			return nil
		}
		_ = secs
	}
	// Try as a quoted duration string like "60s" or "1m30s"
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

// DefaultConfig returns a config with sensible defaults for local development/testing.
func DefaultConfig() *AgentConfig {
	return &AgentConfig{
		GatewayURL: "http://localhost:8080",
		PollInterval: Duration{
			Duration: 60 * time.Second,
		},
		Domains: DefaultDomainRules(),
	}
}

// DefaultDomainRules returns the default set of AI provider domains and their
// corresponding Bifrost gateway integration prefixes.
func DefaultDomainRules() []DomainRule {
	return []DomainRule{
		// Developer API domains — all traffic is proxied
		{Hostname: "api.openai.com", IntegrationPrefix: "/openai", PreservePath: true},
		// {Hostname: "api.anthropic.com", IntegrationPrefix: "/anthropic", PreservePath: true},

		// ChatGPT webapp — only proxy conversation endpoints, relay everything else directly
		{Hostname: "chatgpt.com", IntegrationPrefix: "/chatgpt", PreservePath: true, ProxyPathPrefixes: []string{"/backend-api/f/conversation", "/backend-anon/f/conversation"}},
		{Hostname: "ab.chatgpt.com", IntegrationPrefix: "/chatgpt", PreservePath: true, ProxyPathPrefixes: []string{"/backend-api/f/conversation", "/backend-anon/f/conversation"}},

		// Google — Gemini API
		{Hostname: "generativelanguage.googleapis.com", IntegrationPrefix: "/genai", PreservePath: true},

		// Cohere
		{Hostname: "api.cohere.com", IntegrationPrefix: "/cohere", PreservePath: true},

		// OpenAI-compatible providers
		{Hostname: "api.mistral.ai", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "api.groq.com", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "api.cerebras.ai", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "api.perplexity.ai", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "api.x.ai", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "openrouter.ai", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "api.fireworks.ai", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "api.together.xyz", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "api.deepseek.com", IntegrationPrefix: "/openai", PreservePath: true},
	}
}

// RuntimeConfig holds the parsed/derived runtime state from AgentConfig.
// It is rebuilt whenever the config changes.
type RuntimeConfig struct {
	mu sync.RWMutex

	// Config is the current agent config.
	Config *AgentConfig

	// CACert is the parsed CA certificate.
	CACert *x509.Certificate

	// CATLSCert is the CA cert+key as a tls.Certificate for signing.
	CATLSCert *tls.Certificate

	// DomainMap maps hostname → DomainRule for fast lookup.
	DomainMap map[string]*DomainRule
}

// NewRuntimeConfig creates a RuntimeConfig from an AgentConfig.
func NewRuntimeConfig(cfg *AgentConfig) (*RuntimeConfig, error) {
	rc := &RuntimeConfig{
		Config:    cfg,
		DomainMap: make(map[string]*DomainRule, len(cfg.Domains)),
	}

	// Build domain lookup map
	for i := range cfg.Domains {
		rc.DomainMap[cfg.Domains[i].Hostname] = &cfg.Domains[i]
	}

	// Parse CA cert and key if present
	if cfg.CACertPEM != "" && cfg.CAKeyPEM != "" {
		tlsCert, err := tls.X509KeyPair([]byte(cfg.CACertPEM), []byte(cfg.CAKeyPEM))
		if err != nil {
			return nil, err
		}
		rc.CATLSCert = &tlsCert

		x509Cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
		if err != nil {
			return nil, err
		}
		rc.CACert = x509Cert
	}

	return rc, nil
}

// IsInterceptedDomain checks if a hostname should be intercepted.
func (rc *RuntimeConfig) IsInterceptedDomain(hostname string) bool {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	_, ok := rc.DomainMap[hostname]
	return ok
}

// GetDomainRule returns the DomainRule for a hostname, or nil if not intercepted.
func (rc *RuntimeConfig) GetDomainRule(hostname string) *DomainRule {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.DomainMap[hostname]
}

// UpdateDomains rebuilds the domain map with new domains from the server.
// Returns the lists of added and removed hostnames.
func (rc *RuntimeConfig) UpdateDomains(domains []DomainRule) (added, removed []string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	newMap := make(map[string]*DomainRule, len(domains))
	for i := range domains {
		newMap[domains[i].Hostname] = &domains[i]
	}

	// Find removed
	for hostname := range rc.DomainMap {
		if _, exists := newMap[hostname]; !exists {
			removed = append(removed, hostname)
		}
	}

	// Find added
	for hostname := range newMap {
		if _, exists := rc.DomainMap[hostname]; !exists {
			added = append(added, hostname)
		}
	}

	rc.DomainMap = newMap
	return added, removed
}
