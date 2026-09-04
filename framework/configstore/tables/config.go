package tables

import "github.com/maximhq/bifrost/core/network"

const (
	ConfigAdminUsernameKey = "admin_username"
	ConfigAdminPasswordKey = "admin_password"
	ConfigIsAuthEnabledKey = "is_auth_enabled"
	ConfigProxyKey         = "proxy_config"
	// ConfigComplexityAnalyzerConfigKey stores the persisted analyzer config JSON.
	//
	// This row is also the rollback-compatibility surface: it is written in a
	// shape that a pre-semantic Bifrost can still read, validate, and run from,
	// because that shape shipped and older binaries write this same key. Nothing
	// that only the semantic router understands may live here — see
	// ConfigComplexitySemanticConfigKey.
	ConfigComplexityAnalyzerConfigKey = "complexity_analyzer_config"
	// ConfigComplexitySemanticConfigKey stores the semantic classifier config:
	// its settings and the per-tier exemplars it embeds.
	//
	// It is deliberately a separate row rather than a section inside
	// ConfigComplexityAnalyzerConfigKey. A Bifrost old enough to predate the
	// semantic router has no field for this config, so it would drop the section
	// the first time it rewrote the analyzer row — permanently, and without a
	// way to recover it by rolling forward again. Older binaries only ever read
	// and write the keys they know, and governance_config is only ever written
	// per-key (see RDBConfigStore.UpdateConfig), so a key they do not know is
	// inert to them.
	//
	// The exemplars live here rather than in the analyzer row because they are
	// not lexical keywords wearing a different name -- they are the reference
	// phrases one classifier embeds, and the two classifiers no longer share a
	// list.
	ConfigComplexitySemanticConfigKey = "complexity_semantic_config"
	// ConfigComplexitySemanticDimensionsKey stores the embedding width each
	// provider/model pair has been observed to return.
	//
	// It is observed runtime state, not configuration, so it deliberately does
	// not live in ConfigComplexitySemanticConfigKey: that row is what an
	// operator edits and what the management API returns, and a saved edit would
	// drop anything the client did not send back. Its own row also keeps it
	// inert to older binaries, for the same reason the semantic config has one.
	//
	// The width is only knowable by embedding something, so without it every
	// boot pays a batch of embeddings purely to re-learn a number that has not
	// changed — even when the generation it identifies is already complete in
	// the vector store. Remembering it lets a restart adopt that generation
	// having called no provider at all.
	ConfigComplexitySemanticDimensionsKey = "complexity_semantic_dimensions"
	// ConfigComplexitySemanticGenerationsKey stores which exemplar generation
	// each node is currently using, so retired ones can be reclaimed.
	//
	// A generation is only safe to delete once no node is serving it, and no
	// node can observe another's state directly — OSS has no cluster transport
	// at all, so a node that never learned about a configuration change keeps
	// serving an older generation indefinitely. Deleting on a timer would pull
	// those vectors out from under it. Registration inverts that: a node in use
	// says so, and anything unclaimed is genuinely unreachable.
	//
	// Written under a distributed lock because every node updates the same row.
	ConfigComplexitySemanticGenerationsKey = "complexity_semantic_generations"
	ConfigRestartRequiredKey              = "restart_required"
	ConfigHeaderFilterKey                 = "header_filter_config"
)

// Keys for the ClientConfig.MetadataJSON blob.
// These live inside the metadata JSON map on config_client, not as governance_config rows.
const (
	MetadataKeyOnboardingDismissed = "onboarding_dismissed"
)

// RestartRequiredConfig represents the restart required configuration
// This is set when a config change requires a server restart to take effect
type RestartRequiredConfig struct {
	Required bool   `json:"required"`
	Reason   string `json:"reason,omitempty"`
}

// GlobalProxyConfig represents the global proxy configuration
type GlobalProxyConfig struct {
	Enabled       bool                    `json:"enabled"`
	Type          network.GlobalProxyType `json:"type"`                      // "http", "socks5", "tcp"
	URL           string                  `json:"url"`                       // Proxy URL (e.g., http://proxy.example.com:8080)
	Username      string                  `json:"username,omitempty"`        // Optional authentication username
	Password      string                  `json:"password,omitempty"`        // Optional authentication password
	NoProxy       string                  `json:"no_proxy,omitempty"`        // Comma-separated list of hosts to bypass proxy
	Timeout       int                     `json:"timeout"`                   // Connection timeout in seconds
	SkipTLSVerify bool                    `json:"skip_tls_verify,omitempty"` // Skip TLS certificate verification
	// Entity enablement flags
	EnableForSCIM      bool `json:"enable_for_scim"`      // Enable proxy for SCIM requests (enterprise only)
	EnableForInference bool `json:"enable_for_inference"` // Enable proxy for inference requests
	EnableForAPI       bool `json:"enable_for_api"`       // Enable proxy for API requests
}

// GlobalHeaderFilterConfig represents global header filtering configuration
// for headers forwarded to LLM providers via the x-bf-eh-* prefix.
// Filter logic:
// - If allowlist is non-empty, only headers in the allowlist are forwarded
// - If denylist is non-empty, headers in the denylist are dropped
// - If both are non-empty, allowlist takes precedence first, then denylist filters the result
type GlobalHeaderFilterConfig struct {
	Allowlist []string `json:"allowlist,omitempty"` // If non-empty, only these headers are allowed
	Denylist  []string `json:"denylist,omitempty"`  // Headers to always block
}

// TableGovernanceConfig represents generic configuration key-value pairs
type TableGovernanceConfig struct {
	Key   string `gorm:"primaryKey;type:varchar(255)" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// TableName sets the table name for each model
func (TableGovernanceConfig) TableName() string { return "governance_config" }
