// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	mcputils "github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/envutils"
	"github.com/maximhq/bifrost/framework/featureflags"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcp_headers"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/framework/objectstore"
	plugins "github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/compat"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/plugins/otel"
	"github.com/maximhq/bifrost/plugins/prompts"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/plugins/telemetry"
	"gorm.io/gorm"
)

// StreamChunkInterceptor intercepts streaming chunks before they're sent to clients.
// Implementations can modify, filter, or observe chunks in real-time.
// This interface enables proper dependency injection for streaming handlers.
type StreamChunkInterceptor interface {
	// InterceptChunk processes a chunk before it's written to the client.
	// Returns the (potentially modified) chunk, or nil to skip the chunk entirely.
	InterceptChunk(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error)
}

// HandlerStore provides access to runtime configuration values for handlers.
// This interface allows handlers to access only the configuration they need
// without depending on the entire ConfigStore, improving testability and decoupling.
type HandlerStore interface {
	// GetHeaderMatcher returns the precompiled header matcher for header filtering
	GetHeaderMatcher() *HeaderMatcher
	// GetStreamChunkInterceptor returns the interceptor for streaming chunks.
	// Returns nil if no plugins are loaded or streaming interception is not needed.
	GetStreamChunkInterceptor() StreamChunkInterceptor
	// GetAsyncJobExecutor returns the cached async job executor.
	// Returns nil if LogsStore or governance plugin is not configured.
	GetAsyncJobExecutor() *logstore.AsyncJobExecutor
	// GetAsyncJobResultTTL returns the default TTL for async job results in seconds.
	GetAsyncJobResultTTL() int
	// GetKVStore returns the shared in-memory kvstore instance.
	// Returns nil if not initialized.
	GetKVStore() *kvstore.Store
	// GetMCPHeaderCombinedAllowlist returns the combined allowlist for MCP headers
	GetMCPHeaderCombinedAllowlist() schemas.WhiteList
	// ShouldAllowPerRequestStorageOverride returns whether per-request overrides for content storage are permitted
	ShouldAllowPerRequestStorageOverride() bool
	// ShouldAllowPerRequestRawOverride returns whether per-request overrides for raw request/response visibility are permitted
	ShouldAllowPerRequestRawOverride() bool
	// ShouldAllowDirectKeys returns whether callers may bypass the registered key pool via x-bf-direct-key header
	ShouldAllowDirectKeys() bool
	// GetMCPExternalClientURL returns the configured external base URL Bifrost uses as the
	// redirect_uri when acting as an OAuth client to upstream MCP servers, or empty string
	// if not configured (falls back to dynamic Host-header-based URL).
	GetMCPExternalClientURL() string
}

// Retry backoff constants for validation
const (
	MinRetryBackoff = 100 * time.Millisecond     // Minimum retry backoff: 100ms
	MaxRetryBackoff = 1000000 * time.Millisecond // Maximum retry backoff: 1000000ms (1000 seconds)
)

const (
	DBLookupMaxRetries = 5
	DBLookupDelay      = 1 * time.Second
)

const (
	// SourceOfTruthSplit preserves the current DB/config.json merge behavior.
	SourceOfTruthSplit = "split"
	// SourceOfTruthConfigJSON makes present config.json sections authoritative during startup sync.
	SourceOfTruthConfigJSON = "config.json"
)

// getWeight safely dereferences a *float64 weight pointer, returning 1.0 as default if nil.
// This allows distinguishing between "not set" (nil -> 1.0) and "explicitly set to 0" (0.0).
func getWeight(w *float64) float64 {
	if w == nil {
		return 1.0
	}
	return *w
}

// BuiltinPluginNames is the canonical list of built-in plugin names.
// It is the single source of truth — update here when adding or removing a built-in plugin.
var builtinPluginNames = []string{
	telemetry.PluginName,
	prompts.PluginName,
	logging.PluginName,
	governance.PluginName,
	otel.PluginName,
	semanticcache.PluginName,
	compat.PluginName,
	maxim.PluginName,
}

func GetBuiltinPluginNames() []string {
	return slices.Clone(builtinPluginNames)
}

// IsBuiltinPlugin checks if a plugin is a built-in plugin
func IsBuiltinPlugin(name string) bool {
	return slices.Contains(builtinPluginNames, name)
}

// pluginOrderInfo stores ordering metadata for a plugin.
type pluginOrderInfo struct {
	Placement schemas.PluginPlacement
	Order     int
}

type ServerConfig struct {
	ReadBufferSize int `json:"read_buffer_size,omitempty"`
}

// ConfigData represents the configuration data for the Bifrost HTTP transport.
// It contains the client configuration, provider configurations, MCP configuration,
// vector store configuration, config store configuration, and logs store configuration.
type ConfigData struct {
	// Version controls how empty arrays in allow-list fields are interpreted when loading
	// from config.json. Omitting this field or setting it to 2 uses v1.5.0+ semantics:
	// empty = deny all, ["*"] = allow all. Setting it to 1 restores v1.4.x semantics:
	// empty = allow all (equivalent to ["*"]).
	Version       int                       `json:"version,omitempty"`
	EnvLabel      string                    `json:"env_label,omitempty"`
	Server        *ServerConfig             `json:"server,omitempty"`
	SourceOfTruth string                    `json:"source_of_truth,omitempty"`
	Client        *configstore.ClientConfig `json:"client"`
	EncryptionKey *schemas.SecretVar        `json:"encryption_key"`
	// Deprecated: Use GovernanceConfig.AuthConfig instead
	AuthConfig        *configstore.AuthConfig               `json:"auth_config,omitempty"`
	Providers         map[string]configstore.ProviderConfig `json:"providers"`
	FrameworkConfig   *framework.FrameworkConfig            `json:"framework,omitempty"`
	MCP               *schemas.MCPConfig                    `json:"mcp,omitempty"`
	Webhooks          []*WebhookEndpointConfig              `json:"webhooks,omitempty"`
	Governance        *configstore.GovernanceConfig         `json:"governance,omitempty"`
	VectorStoreConfig *vectorstore.Config                   `json:"vector_store,omitempty"`
	ConfigStoreConfig *configstore.Config                   `json:"config_store,omitempty"`
	LogsStoreConfig   *logstore.Config                      `json:"logs_store,omitempty"`
	Plugins           []*schemas.PluginConfig               `json:"plugins,omitempty"`
	WebSocket         *schemas.WebSocketConfig              `json:"websocket,omitempty"`
	FeatureFlags      *FeatureFlagsFileConfig               `json:"feature_flags,omitempty"`

	presentSections           map[string]bool
	presentGovernanceSections map[string]bool
	SkillsRegistry            *SkillsRegistryConfig `json:"skills_registry,omitempty"`
}

// SkillsRegistryConfig defines declarative skill definitions in config.json.
type SkillsRegistryConfig struct {
	Enabled *bool                 `json:"enabled,omitempty"`
	Skills  []SkillsRegistryEntry `json:"skills,omitempty"`
}

// SkillsRegistryEntry describes a single skill to reconcile from config.json.
type SkillsRegistryEntry struct {
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	License          string                 `json:"license,omitempty"`
	Compatibility    string                 `json:"compatibility,omitempty"`
	AllowedTools     string                 `json:"allowed_tools,omitempty"`
	ExtraFrontmatter map[string]interface{} `json:"extra_frontmatter,omitempty"`
	Metadata         map[string]string      `json:"metadata,omitempty"`
	SkillMDBody      string                 `json:"skill_md_body"`
	Version          string                 `json:"version"`
	Files            []SkillsRegistryFile   `json:"files,omitempty"`
}

// SkillsRegistryFile describes a file attached to a config-defined skill.
type SkillsRegistryFile struct {
	Path       string `json:"path"`
	SourceType string `json:"source_type"`
	// Source-type-specific fields
	URL     string `json:"url,omitempty"`     // for source_type "url"
	Content string `json:"content,omitempty"` // for source_type "text"
	DataURL string `json:"dataurl,omitempty"` // for source_type "dataurl"
}

// FeatureFlagsFileConfig is the config.json / Helm shape for feature flag
// boot overrides. Values declared here win over DB overrides and are
// rendered as "locked" in the UI so operators must edit config.json (or
// re-deploy Helm) to change them.
type FeatureFlagsFileConfig struct {
	Flags map[string]FeatureFlagFileValue `json:"flags"`
}

// FeatureFlagFileValue accepts either a JSON literal bool or a string. The
// string form supports the same "env.NAME" indirection used elsewhere in
// config.json (encryption_key, provider creds), so Helm can flip flags via
// container env vars without re-templating the JSON. Recognized truthy
// string values are "true", "1", "yes", "on" (case-insensitive); anything
// else parses as false.
type FeatureFlagFileValue struct {
	Enabled bool `json:"enabled"`
}

// UnmarshalJSON accepts {"enabled": true} (literal bool), {"enabled": "true"}
// (string literal), or {"enabled": "env.BIFROST_FOO"} (env-var indirection).
// The string form is critical for Helm because chart values are stringly
// typed when sourced from env vars.
func (v *FeatureFlagFileValue) UnmarshalJSON(data []byte) error {
	var raw struct {
		Enabled json.RawMessage `json:"enabled"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.Enabled) == 0 {
		return nil
	}
	// Literal bool path.
	var asBool bool
	if err := json.Unmarshal(raw.Enabled, &asBool); err == nil {
		v.Enabled = asBool
		return nil
	}
	// String path: literal "true"/"false" or "env.X" indirection.
	var asStr string
	if err := json.Unmarshal(raw.Enabled, &asStr); err != nil {
		return fmt.Errorf("feature flag enabled: must be bool or string, got %s", string(raw.Enabled))
	}
	resolved := asStr
	if envKey, ok := strings.CutPrefix(asStr, "env."); ok {
		resolved = os.Getenv(envKey)
	}
	switch strings.ToLower(strings.TrimSpace(resolved)) {
	case "true", "1", "yes", "on":
		v.Enabled = true
	default:
		v.Enabled = false
	}
	return nil
}

// WebhookEndpointConfig declares one webhook endpoint in config.json.
// The secret is optional and best supplied as an env reference; when absent
// one is generated at creation time. The tuning knobs are optional; zero
// means "use the delivery worker's default".
type WebhookEndpointConfig struct {
	ID                  string                           `json:"id,omitempty"`
	Name                string                           `json:"name"`
	URL                 string                           `json:"url"`
	Secret              *schemas.SecretVar               `json:"secret,omitempty"`
	Events              []configstoreTables.WebhookEvent `json:"events"`
	Headers             map[string]schemas.SecretVar     `json:"headers,omitempty"`
	IncludeResponse     bool                             `json:"include_response,omitempty"`
	AllowPrivateNetwork bool                             `json:"allow_private_network,omitempty"`
	Disabled            bool                             `json:"disabled,omitempty"`

	MaxRetries                 int `json:"max_retries,omitempty"`
	RetryBackoffInitialSeconds int `json:"retry_backoff_initial_seconds,omitempty"`
	RetryBackoffMaxSeconds     int `json:"retry_backoff_max_seconds,omitempty"`
	AttemptTimeoutSeconds      int `json:"attempt_timeout_seconds,omitempty"`
	MaxResponsePayloadKBs      int `json:"max_response_payload_kbs,omitempty"`
	MaxConcurrentDeliveries    int `json:"max_concurrent_deliveries,omitempty"`
}

// toTable converts a file declaration into the table model.
func (w *WebhookEndpointConfig) toTable() *configstoreTables.TableWebhookEndpoint {
	return &configstoreTables.TableWebhookEndpoint{
		ID:                         w.ID,
		Name:                       w.Name,
		URL:                        w.URL,
		Secret:                     w.Secret,
		Events:                     w.Events,
		Headers:                    w.Headers,
		IncludeResponse:            w.IncludeResponse,
		AllowPrivateNetwork:        w.AllowPrivateNetwork,
		Disabled:                   w.Disabled,
		MaxRetries:                 w.MaxRetries,
		RetryBackoffInitialSeconds: w.RetryBackoffInitialSeconds,
		RetryBackoffMaxSeconds:     w.RetryBackoffMaxSeconds,
		AttemptTimeoutSeconds:      w.AttemptTimeoutSeconds,
		MaxResponsePayloadKBs:      w.MaxResponsePayloadKBs,
		MaxConcurrentDeliveries:    w.MaxConcurrentDeliveries,
	}
}

// normalizeSourceOfTruth returns the configured source-of-truth mode, defaulting to split.
func normalizeSourceOfTruth(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", SourceOfTruthSplit:
		return SourceOfTruthSplit
	case SourceOfTruthConfigJSON:
		return SourceOfTruthConfigJSON
	default:
		// Unknown values fall back to split rather than persisting an invalid
		// mode. Schema validation rejects unknown values upstream; this guards
		// any path that bypasses it so reconciliation never acts on garbage.
		logger.Warn("unknown source_of_truth %q, defaulting to %q", value, SourceOfTruthSplit)
		return SourceOfTruthSplit
	}
}

// isConfigJSONSourceOfTruth reports whether present config.json sections own reconciliation.
func (cd *ConfigData) isConfigJSONSourceOfTruth() bool {
	if cd == nil {
		return false
	}
	return normalizeSourceOfTruth(cd.SourceOfTruth) == SourceOfTruthConfigJSON
}

// sectionPresent reports whether a top-level config.json section was explicitly provided.
func (cd *ConfigData) sectionPresent(name string) bool {
	if cd == nil {
		return false
	}
	if cd.presentSections != nil {
		return cd.presentSections[name]
	}
	switch name {
	case "client":
		return cd.Client != nil
	case "providers":
		return cd.Providers != nil
	case "mcp":
		return cd.MCP != nil
	case "governance":
		return cd.Governance != nil
	case "plugins":
		return cd.Plugins != nil
	case "framework":
		return cd.FrameworkConfig != nil
	case "vector_store":
		return cd.VectorStoreConfig != nil
	case "logs_store":
		return cd.LogsStoreConfig != nil
	case "config_store":
		return cd.ConfigStoreConfig != nil
	default:
		return false
	}
}

// governanceSectionPresent reports whether a governance collection was explicitly provided.
func (cd *ConfigData) governanceSectionPresent(name string) bool {
	if cd == nil || cd.Governance == nil {
		return false
	}
	if cd.presentGovernanceSections != nil {
		return cd.presentGovernanceSections[name]
	}
	switch name {
	case "virtual_keys":
		return cd.Governance.VirtualKeys != nil
	case "teams":
		return cd.Governance.Teams != nil
	case "customers":
		return cd.Governance.Customers != nil
	case "budgets":
		return cd.Governance.Budgets != nil
	case "rate_limits":
		return cd.Governance.RateLimits != nil
	case "model_configs":
		return cd.Governance.ModelConfigs != nil
	case "providers":
		return cd.Governance.Providers != nil
	case "routing_rules":
		return cd.Governance.RoutingRules != nil
	case "pricing_overrides":
		return cd.Governance.PricingOverrides != nil
	default:
		return false
	}
}

// UnmarshalJSON unmarshals the ConfigData from JSON using internal unmarshallers
// for VectorStoreConfig, ConfigStoreConfig, and LogsStoreConfig to ensure proper
// type safety and configuration parsing.
func (cd *ConfigData) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("failed to inspect config data: %w", err)
	}
	cd.presentSections = make(map[string]bool, len(raw))
	for key := range raw {
		cd.presentSections[key] = true
	}

	// First, unmarshal into a temporary struct to get all fields except the complex configs
	type TempConfigData struct {
		Version           int                                   `json:"version,omitempty"`
		EnvLabel          string                                `json:"env_label,omitempty"`
		SourceOfTruth     string                                `json:"source_of_truth,omitempty"`
		FrameworkConfig   json.RawMessage                       `json:"framework,omitempty"`
		Server            *ServerConfig                         `json:"server,omitempty"`
		Client            *configstore.ClientConfig             `json:"client"`
		EncryptionKey     *schemas.SecretVar                    `json:"encryption_key"`
		AuthConfig        *configstore.AuthConfig               `json:"auth_config,omitempty"`
		Providers         map[string]configstore.ProviderConfig `json:"providers"`
		MCP               *schemas.MCPConfig                    `json:"mcp,omitempty"`
		Webhooks          []*WebhookEndpointConfig              `json:"webhooks,omitempty"`
		Governance        *configstore.GovernanceConfig         `json:"governance,omitempty"`
		VectorStoreConfig json.RawMessage                       `json:"vector_store,omitempty"`
		ConfigStoreConfig json.RawMessage                       `json:"config_store,omitempty"`
		LogsStoreConfig   json.RawMessage                       `json:"logs_store,omitempty"`
		Plugins           []*schemas.PluginConfig               `json:"plugins,omitempty"`
		WebSocket         *schemas.WebSocketConfig              `json:"websocket,omitempty"`
		FeatureFlags      *FeatureFlagsFileConfig               `json:"feature_flags,omitempty"`
		SkillsRegistry    *SkillsRegistryConfig                 `json:"skills_registry,omitempty"`
	}

	var temp TempConfigData
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal config data: %w", err)
	}

	// Set simple fields
	cd.Version = temp.Version
	cd.EnvLabel = temp.EnvLabel
	cd.SourceOfTruth = normalizeSourceOfTruth(temp.SourceOfTruth)
	cd.Client = temp.Client
	cd.Server = temp.Server
	cd.EncryptionKey = temp.EncryptionKey
	cd.AuthConfig = temp.AuthConfig
	cd.Providers = temp.Providers
	cd.MCP = temp.MCP
	cd.Webhooks = temp.Webhooks
	cd.Governance = temp.Governance
	cd.Plugins = temp.Plugins
	cd.WebSocket = temp.WebSocket
	cd.FeatureFlags = temp.FeatureFlags
	cd.presentGovernanceSections = nil
	if rawGovernance, ok := raw["governance"]; ok && len(rawGovernance) > 0 {
		var rawGovernanceFields map[string]json.RawMessage
		if err := json.Unmarshal(rawGovernance, &rawGovernanceFields); err == nil {
			cd.presentGovernanceSections = make(map[string]bool, len(rawGovernanceFields))
			for key := range rawGovernanceFields {
				cd.presentGovernanceSections[key] = true
			}
		}
	}
	cd.SkillsRegistry = temp.SkillsRegistry
	// Initialize providers map if nil
	if cd.Providers == nil {
		cd.Providers = make(map[string]configstore.ProviderConfig)
	}

	// Parse VectorStoreConfig using its internal unmarshaler
	if len(temp.VectorStoreConfig) > 0 {
		var vectorStoreConfig vectorstore.Config
		if err := json.Unmarshal(temp.VectorStoreConfig, &vectorStoreConfig); err != nil {
			return fmt.Errorf("failed to unmarshal vector store config: %w", err)
		}
		cd.VectorStoreConfig = &vectorStoreConfig
	}

	// Parse FrameworkConfig using its internal unmarshaler
	if len(temp.FrameworkConfig) > 0 {
		var frameworkConfig framework.FrameworkConfig
		if err := json.Unmarshal(temp.FrameworkConfig, &frameworkConfig); err != nil {
			return fmt.Errorf("failed to unmarshal framework config: %w", err)
		}
		cd.FrameworkConfig = &frameworkConfig
	}

	// Parse ConfigStoreConfig using its internal unmarshaler
	if len(temp.ConfigStoreConfig) > 0 {
		var configStoreConfig configstore.Config
		if err := json.Unmarshal(temp.ConfigStoreConfig, &configStoreConfig); err != nil {
			return fmt.Errorf("failed to unmarshal config store config: %w", err)
		}
		cd.ConfigStoreConfig = &configStoreConfig
	}

	// Parse LogsStoreConfig using its internal unmarshaler
	if len(temp.LogsStoreConfig) > 0 {
		var logsStoreConfig logstore.Config
		if err := json.Unmarshal(temp.LogsStoreConfig, &logsStoreConfig); err != nil {
			return fmt.Errorf("failed to unmarshal logs store config: %w", err)
		}
		cd.LogsStoreConfig = &logsStoreConfig
	}
	return nil
}

// Config represents a high-performance in-memory configuration store for Bifrost.
// It provides thread-safe access to provider configurations with database persistence.
//
// Features:
//   - Pure in-memory storage for ultra-fast access
//   - Environment variable processing for API keys and key-level configurations
//   - Thread-safe operations with read-write mutexes
//   - Real-time configuration updates via HTTP API
//   - Automatic database persistence for all changes
//   - Support for provider-specific key configurations (Azure, Vertex, Bedrock)
//   - Lock-free plugin reads via atomic.Pointer for minimal hot-path latency
type Config struct {
	Mu         sync.RWMutex // Exported for direct access from handlers (governance plugin)
	muMCP      sync.RWMutex
	muWebhooks sync.RWMutex
	client     *bifrost.Bifrost

	configPath string

	// Stores
	ConfigStore configstore.ConfigStore
	VectorStore vectorstore.VectorStore
	LogsStore   logstore.LogStore
	// LogsStoreConfig is the effective logs store configuration used to create LogsStore.
	LogsStoreConfig *logstore.Config
	ObjectStore     objectstore.ObjectStore

	// oauth2SigningKey caches the immutable OAuth2 signing key used to sign and
	// verify Bifrost-issued /mcp JWTs. The key is created once via an idempotent
	// insert and never rotated, so it is identical across nodes and immutable for
	// the process lifetime. Caching it here lets the JWKS, token-issuance, and
	// JWT-verify paths share a single load — sparing each a DB read + private-key
	// decrypt per request — through one invalidation point. See
	// GetOAuth2SigningKey.
	oauth2SigningKey atomic.Pointer[configstoreTables.OAuth2SigningKey]

	// In-memory storage
	ServerConfig     *ServerConfig
	ClientConfig     *configstore.ClientConfig
	Providers        map[schemas.ModelProvider]configstore.ProviderConfig
	MCPConfig        *schemas.MCPConfig
	GovernanceConfig *configstore.GovernanceConfig
	FrameworkConfig  *framework.FrameworkConfig
	ProxyConfig      *configstoreTables.GlobalProxyConfig

	// webhookEndpoints serves webhook endpoint config to the request path and
	// the delivery worker without database reads. Guarded by muWebhooks;
	// handlers mutate it in lockstep with every database write. Operational
	// counters are deliberately not mirrored here — reads that need them go
	// to the store.
	webhookEndpoints       map[string]*configstoreTables.TableWebhookEndpoint // keyed by ID
	webhookEndpointsByName map[string]string                                  // unique name -> ID

	// Plugin Storage (SINGLE SOURCE OF TRUTH)
	// All plugins are stored in BasePlugins. Interface-specific caches are
	// derived views rebuilt automatically on any plugin change.
	// Lock-free reads via atomic.Pointer for hot-path performance.
	pluginsMu            sync.Mutex                                                // Protects structural changes to BasePlugins
	pluginOrderMap       map[string]pluginOrderInfo                                // Plugin ordering metadata (protected by pluginsMu)
	BasePlugins          atomic.Pointer[[]schemas.BasePlugin]                      // Master list of all plugins
	LLMPlugins           atomic.Pointer[[]schemas.LLMPlugin]                       // Derived cache (auto-rebuilt)
	MCPPlugins           atomic.Pointer[[]schemas.MCPPlugin]                       // Derived cache (auto-rebuilt)
	HTTPTransportPlugins atomic.Pointer[[]schemas.HTTPTransportPlugin]             // Derived cache (auto-rebuilt)
	ConfigMarshallers    atomic.Pointer[map[string]schemas.ConfigMarshallerPlugin] // Derived cache (auto-rebuilt)
	PluginLoader         plugins.PluginLoader

	// Plugin metadata from config file/database
	PluginConfigs []*schemas.PluginConfig

	// Plugin status tracking (co-located with plugin instances)
	pluginStatusMu sync.RWMutex
	pluginStatus   map[string]schemas.PluginStatus // name -> status

	OAuthProvider      *oauth2.OAuth2Provider
	TokenRefreshWorker *oauth2.TokenRefreshWorker
	OAuthSweepWorker   *oauth2.PerUserOAuthSweepWorker

	// MCPHeadersProvider backs MCPAuthTypePerUserHeaders credential storage.
	// Constructed alongside OAuthProvider and passed into the Bifrost core
	// init so the per-user-headers resolver can resolve / persist values
	// scoped by (auth_mode, identity, mcp_client).
	MCPHeadersProvider    *mcp_headers.Provider
	MCPHeadersSweepWorker *mcp_headers.CredentialSweepWorker

	// Async job executor (initialized during setup if LogsStore + governance are available)
	AsyncJobExecutor *logstore.AsyncJobExecutor
	// Shared in-memory kvstore for transport-level protocol coordination.
	KVStore *kvstore.Store

	// Process-wide feature flag store. Flags are code-declared via
	// featureflags.Register; this struct holds the effective state with
	// layered overrides (DB then file). May be wired with a SyncDelegate
	// by enterprise for cluster-wide gossip.
	FeatureFlags *featureflags.Store

	// Catalog managers
	ModelCatalog *modelcatalog.ModelCatalog
	MCPCatalog   *mcpcatalog.MCPCatalog

	// Optional event broadcaster for real-time updates (e.g., WebSocket).
	// Set by HTTP server at startup; may be nil in non-HTTP usage.
	EventBroadcaster schemas.EventBroadcaster

	// EnvLabel is a short label (max 10 chars) displayed in the UI sidebar to identify the
	// environment (e.g. "staging", "prod"). Set via config.json env_label or BIFROST_ENV_LABEL env var.
	EnvLabel string

	// StreamingDecompressThreshold overrides the default threshold (10MB) for
	// switching from buffered to streaming request decompression. Set by
	// enterprise from LargePayloadConfig.RequestThresholdBytes. Zero means
	// use schemas.DefaultLargePayloadRequestThresholdBytes.
	StreamingDecompressThreshold int64
	// WebSocket configuration for WS gateway features (Responses WS mode, Realtime API).
	WebSocketConfig *schemas.WebSocketConfig

	// Precompiled header matcher for header filtering. Rebuilt on config change.
	headerMatcher atomic.Pointer[HeaderMatcher]
}

// DefaultClientConfig is the default client config used when no config is provided.
var DefaultClientConfig = configstore.ClientConfig{
	DropExcessRequests:              false,
	PrometheusLabels:                []string{},
	InitialPoolSize:                 schemas.DefaultInitialPoolSize,
	EnableLogging:                   new(true),
	DisableContentLogging:           false,
	RetainContentInObjectStorage:    false,
	EnforceAuthOnInference:          false,
	AllowedOrigins:                  []string{"*"},
	AllowedHeaders:                  []string{},
	WhitelistedRoutes:               []string{},
	MaxRequestBodySizeMB:            100,
	MCPAgentDepth:                   10,
	MCPToolExecutionTimeout:         30,
	MCPCodeModeBindingLevel:         string(schemas.CodeModeBindingLevelServer),
	MCPEnableTempTokenAuth:          false,
	HideDeletedVirtualKeysInFilters: false,
	RoutingChainMaxDepth:            governance.DefaultRoutingChainMaxDepth,
}

// applyV1Compat normalizes ConfigData to restore v1.4.x allow-list semantics.
// In v1.4.x, empty arrays in allow-list fields meant "allow all". In v1.5.0+ they mean
// "deny all". When config.json sets version: 1, this function converts empty arrays to
// the explicit wildcard ["*"] (or sets AllowAllKeys=true) before any further processing,
// so the rest of the stack sees v1.5.0-compatible data throughout.
//
// Affected fields:
//   - Provider key Models: nil/[] → ["*"]
//   - VK ProviderConfigs empty list → backfill all configured providers with AllowedModels: ["*"], AllowAllKeys: true
//   - VK ProviderConfig AllowedModels: [] → ["*"]
//   - VK ProviderConfig key_ids empty (AllowAllKeys=false, no Keys) → AllowAllKeys=true
//   - VK MCPConfigs empty list → backfill all configured MCP clients with ToolsToExecute: ["*"]
//
// Note: tools_to_execute within a VK MCP config entry is NOT normalized — an empty
// tools_to_execute already meant "skip this client" in v1.4.x, so the behavior is unchanged.
func applyV1Compat(configData *ConfigData) {
	// 1. Provider key models
	for providerName, providerCfg := range configData.Providers {
		changed := false
		for i := range providerCfg.Keys {
			if len(providerCfg.Keys[i].Models) == 0 {
				providerCfg.Keys[i].Models = schemas.WhiteList{"*"}
				changed = true
			}
		}
		if changed {
			configData.Providers[providerName] = providerCfg
		}
	}

	if configData.Governance == nil {
		return
	}

	// 2. VK-level allow-list fields
	for i := range configData.Governance.VirtualKeys {
		vk := &configData.Governance.VirtualKeys[i]

		// Provider configs: empty list → backfill all configured providers
		if len(vk.ProviderConfigs) == 0 {
			providerNames := make([]string, 0, len(configData.Providers))
			for providerName := range configData.Providers {
				providerNames = append(providerNames, strings.ToLower(providerName))
			}
			sort.Strings(providerNames)
			for _, providerName := range providerNames {
				vk.ProviderConfigs = append(vk.ProviderConfigs, configstoreTables.TableVirtualKeyProviderConfig{
					Provider:      providerName,
					AllowedModels: schemas.WhiteList{"*"},
					AllowAllKeys:  true,
				})
			}
		} else {
			for j := range vk.ProviderConfigs {
				pc := &vk.ProviderConfigs[j]
				if len(pc.AllowedModels) == 0 {
					pc.AllowedModels = schemas.WhiteList{"*"}
				}
				if !pc.AllowAllKeys && len(pc.Keys) == 0 {
					pc.AllowAllKeys = true
				}
			}
		}

		// MCP configs: empty list → backfill all configured MCP clients
		if len(vk.MCPConfigs) == 0 && configData.MCP != nil {
			for _, mcpClient := range configData.MCP.ClientConfigs {
				if mcpClient == nil {
					continue
				}
				vk.MCPConfigs = append(vk.MCPConfigs, configstoreTables.TableVirtualKeyMCPConfig{
					MCPClientName:  mcpClient.Name,
					ToolsToExecute: schemas.WhiteList{"*"},
				})
			}
		}
	}
}

// promoteDeprecatedCalendarAligned lifts the legacy per-budget / per-rate-limit
// calendar_aligned input to the owning VK or Team. Owner wins if already true;
// otherwise OR across descendants (own budgets/rate-limit + every provider
// config's budgets/rate-limit). Inner pointers are always cleared. Mirrors the
// enterprise promoteDeprecatedAccessProfileCalendarAligned at the access
// profile level. Runs on every load regardless of config version
func promoteDeprecatedCalendarAligned(configData *ConfigData) {
	if configData == nil || configData.Governance == nil {
		return
	}
	// Build ID-keyed lookup maps for the global budget/rate-limit sections so
	// customer entries (which reference by ID, not inline) can promote legacy
	// calendar_aligned flags from the referenced rows.
	budgetsByID := make(map[string]*configstoreTables.TableBudget, len(configData.Governance.Budgets))
	for i := range configData.Governance.Budgets {
		b := &configData.Governance.Budgets[i]
		budgetsByID[b.ID] = b
	}
	rateLimitsByID := make(map[string]*configstoreTables.TableRateLimit, len(configData.Governance.RateLimits))
	for i := range configData.Governance.RateLimits {
		rl := &configData.Governance.RateLimits[i]
		rateLimitsByID[rl.ID] = rl
	}
	for i := range configData.Governance.VirtualKeys {
		vk := &configData.Governance.VirtualKeys[i]
		promoteCalendarAligned(&vk.CalendarAligned, vk.Budgets, vk.RateLimit)
		for j := range vk.ProviderConfigs {
			pc := &vk.ProviderConfigs[j]
			promoteCalendarAligned(&vk.CalendarAligned, pc.Budgets, pc.RateLimit)
		}
	}
	for i := range configData.Governance.Teams {
		team := &configData.Governance.Teams[i]
		promoteCalendarAligned(&team.CalendarAligned, team.Budgets, team.RateLimit)
	}
	for i := range configData.Governance.Customers {
		customer := &configData.Governance.Customers[i]
		// Inline budgets (new multi-budget format): promote directly.
		promoteCalendarAligned(&customer.CalendarAligned, customer.Budgets, nil)
		// Legacy budget_id reference: look up the referenced row.
		if customer.BudgetID != nil {
			if b := budgetsByID[*customer.BudgetID]; b != nil {
				if b.CalendarAlignedInput != nil && *b.CalendarAlignedInput {
					customer.CalendarAligned = true
				}
				b.CalendarAlignedInput = nil
			}
		}
		if customer.RateLimitID != nil {
			if rl := rateLimitsByID[*customer.RateLimitID]; rl != nil {
				if rl.CalendarAlignedInput != nil && *rl.CalendarAlignedInput {
					customer.CalendarAligned = true
				}
				rl.CalendarAlignedInput = nil
			}
		}
	}
}

// promoteCalendarAligned ORs each child's legacy calendar_aligned input into
// the owner's flag and clears the child field. Treats a nil child pointer as
// "not set" — only explicit true contributes.
func promoteCalendarAligned(owner *bool, budgets []configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) {
	for i := range budgets {
		if budgets[i].CalendarAlignedInput != nil && *budgets[i].CalendarAlignedInput {
			*owner = true
		}
		budgets[i].CalendarAlignedInput = nil
	}
	if rateLimit != nil && rateLimit.CalendarAlignedInput != nil {
		if *rateLimit.CalendarAlignedInput {
			*owner = true
		}
		rateLimit.CalendarAlignedInput = nil
	}
}

// registerFeatureFlags registers feature flags from the config store into the global flag registry.
func registerFeatureFlags(_ context.Context) error {
	// No feature flags to register
	return nil
}

// LoadConfig loads initial configuration from a JSON config file into memory
// with full preprocessing including environment variable resolution and key config parsing.
// All processing is done upfront to ensure zero latency when retrieving data.
//
// If the config file doesn't exist, the system starts with default configuration
// and users can add providers dynamically via the HTTP API.
//
// This method handles:
//   - JSON config file parsing
//   - Environment variable substitution for API keys (env.VARIABLE_NAME)
//   - Key-level config processing for Azure, Vertex, and Bedrock (Endpoint, APIVersion, ProjectID, Region, AuthCredentials)
//   - Case conversion for provider names (e.g., "OpenAI" -> "openai")
//   - In-memory storage for ultra-fast access during request processing
//   - Graceful handling of missing config files
func LoadConfig(ctx context.Context, configDirPath string) (*Config, error) {
	configFilePath := filepath.Join(configDirPath, "config.json")
	configDBPath := filepath.Join(configDirPath, "config.db")
	logsDBPath := filepath.Join(configDirPath, "logs.db")
	// Initialize config
	config := &Config{
		configPath: configFilePath,
		Providers:  make(map[schemas.ModelProvider]configstore.ProviderConfig),
		LLMPlugins: atomic.Pointer[[]schemas.LLMPlugin]{},
	}
	// Register feature flags before any file/DB-driven init so the
	// registry is populated even when config.json is absent. initFeatureFlags
	// (called below) hydrates DB overrides and applies file overrides; both
	// depend on the registry being populated to surface flags correctly.
	if err := registerFeatureFlags(ctx); err != nil {
		logger.Error("failed to register feature flags: %v", err)
	}
	absConfigFilePath, err := filepath.Abs(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for config file: %w", err)
	}
	// Parse config file if it exists; otherwise use empty ConfigData (defaults will apply)
	var configData ConfigData
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		// No config file — configData stays zero-value, defaults will apply
		logger.Info("config file not found at path: %s, initializing with default values", absConfigFilePath)
	} else {
		// Schema warning check
		var schema map[string]any
		if err := json.Unmarshal(data, &schema); err != nil {
			return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
		}
		if schemaURL, ok := schema["$schema"].(string); !ok || strings.TrimSpace(schemaURL) == "" {
			yellowColor := "\033[33m"
			resetColor := "\033[0m"
			message := fmt.Sprintf("config file %s does not include a \"$schema\" location. Set it to %q or your mirrored schema location to enable IDE validation.", absConfigFilePath, DefaultConfigSchemaURL)
			boxWidth := 100
			contentWidth := boxWidth - 4
			words := strings.Fields(message)
			var lines []string
			currentLine := ""
			for _, word := range words {
				if currentLine == "" {
					currentLine = word
				} else if len(currentLine)+1+len(word) <= contentWidth {
					currentLine += " " + word
				} else {
					lines = append(lines, currentLine)
					currentLine = word
				}
			}
			if currentLine != "" {
				lines = append(lines, currentLine)
			}
			fmt.Printf("%s╔%s╗%s\n", yellowColor, strings.Repeat("═", boxWidth-2), resetColor)
			for _, l := range lines {
				padding := contentWidth - len(l)
				if padding < 0 {
					padding = 0
				}
				fmt.Printf("%s║ %s%s ║%s\n", yellowColor, l, strings.Repeat(" ", padding), resetColor)
			}
			fmt.Printf("%s╚%s╝%s\n", yellowColor, strings.Repeat("═", boxWidth-2), resetColor)
			fmt.Println("")
			logger.Warn("config file %s does not include a \"$schema\" location", absConfigFilePath)
		}
		// Parse config data
		if err := json.Unmarshal(data, &configData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
		logger.Info("loading configuration from: %s", absConfigFilePath)
		// Promote deprecated per-budget / per-rate-limit calendar_aligned to the
		// owning VK / Team. Independent of config version — the deprecation
		// predates the v1/v2 allow-list split.
		promoteDeprecatedCalendarAligned(&configData)
		// If version is 1, apply v1.4.x compatibility: empty allow-list arrays mean "allow all"
		if configData.Version == 1 {
			logger.Info("config version 1 detected, applying v1.4.x compatibility semantics (empty arrays = allow all)")
			applyV1Compat(&configData)
		}
	}

	// 1. Encryption (before stores so BeforeSave hooks work correctly)
	if err := initEncryption(&configData); err != nil {
		return nil, err
	}
	// 1a. Vault config acknowledgement (initialization handled by enterprise layer)
	initVault(&configData)
	// 2. Stores (config, logs, vector) — creates defaults for absent configs
	if err := initStores(ctx, config, &configData, configDBPath, logsDBPath); err != nil {
		return nil, err
	}
	// 3. KV store
	if err := initKVStore(config); err != nil {
		return nil, err
	}
	// 3a. Feature flags (after ConfigStore from initStores, before handlers)
	if err := initFeatureFlags(ctx, config, &configData); err != nil {
		return nil, err
	}
	// 4. Client config (store → file → defaults)
	loadClientConfig(ctx, config, &configData)
	// Reject an out-of-range client config (e.g. auth_code_ttl above the cap)
	// loudly at startup instead of silently correcting it, so in-memory, core,
	// and DB state cannot diverge.
	if err := validateClientConfig(config.ClientConfig); err != nil {
		return nil, err
	}
	config.SetHeaderMatcher(NewHeaderMatcher(config.ClientConfig.HeaderFilterConfig))
	// 5. Providers (store → file → auto-detect)
	if err := loadProviders(ctx, config, &configData); err != nil {
		return nil, err
	}
	// 6. MCP config
	loadMCPConfig(ctx, config, &configData)
	// 7. Webhook endpoints
	loadWebhooksConfig(ctx, config, &configData)
	// 8. Governance config
	loadGovernanceConfig(ctx, config, &configData)
	// 9. Auth config
	loadAuthConfig(ctx, config, &configData)
	// 10. Plugins
	loadPlugins(ctx, config, &configData)
	// 11. Skills registry (after plugins, before framework)
	loadSkillsRegistry(ctx, config, &configData)
	// 12. Framework config and pricing manager
	initFrameworkConfig(ctx, config, &configData)
	// 13. Encryption sync
	syncEncryption(ctx, config)
	// 14. Env label (config.json takes precedence over BIFROST_ENV_LABEL env var)
	truncateLabel := func(s string) string {
		r := []rune(s)
		if len(r) > 14 {
			return string(r[:14])
		}
		return s
	}
	if label := strings.TrimSpace(configData.EnvLabel); label != "" {
		config.EnvLabel = truncateLabel(label)
	} else if label := strings.TrimSpace(os.Getenv("BIFROST_ENV_LABEL")); label != "" {
		config.EnvLabel = truncateLabel(label)
	}
	// 15. WebSocket defaults
	if configData.WebSocket != nil {
		configData.WebSocket.CheckAndSetDefaults()
		config.WebSocketConfig = configData.WebSocket
	} else {
		wsConfig := &schemas.WebSocketConfig{}
		wsConfig.CheckAndSetDefaults()
		config.WebSocketConfig = wsConfig
	}
	// 16. Server config
	if configData.Server != nil {
		config.ServerConfig = configData.Server
	} else {
		config.ServerConfig = &ServerConfig{
			ReadBufferSize: 1024 * 64,
		}
	}
	return config, nil
}

// initStores initializes config, logs, and vector stores.
// When config data sections are absent (nil), creates default SQLite stores for persistence.
func initStores(ctx context.Context, config *Config, configData *ConfigData, configDBPath, logsDBPath string) error {
	var err error
	// Initialize config store
	if configData.ConfigStoreConfig != nil && configData.ConfigStoreConfig.Enabled {
		// Explicit config store configuration from config.json
		config.ConfigStore, err = configstore.NewConfigStore(ctx, configData.ConfigStoreConfig, logger)
		if err != nil {
			return err
		}
		logger.Info("config store initialized")
	} else if configData.ConfigStoreConfig == nil {
		// No config store section — create default SQLite store for persistence
		config.ConfigStore, err = configstore.NewConfigStore(ctx, &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config: &configstore.SQLiteConfig{
				Path: configDBPath,
			},
		}, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize default config store: %w", err)
		}
		logger.Info("config store initialized (default SQLite)")
	}
	// else: ConfigStoreConfig is present but Enabled == false — leave ConfigStore nil

	// Clear restart required flag on server startup
	if config.ConfigStore != nil {
		if err = config.ConfigStore.ClearRestartRequiredConfig(ctx); err != nil {
			logger.Warn("failed to clear restart required config: %v", err)
		}
	}

	// Initialize log store
	if configData.LogsStoreConfig != nil && configData.LogsStoreConfig.Enabled {
		// Explicit logs store configuration from config.json
		config.LogsStore, err = logstore.NewLogStore(ctx, configData.LogsStoreConfig, logger)
		if err != nil {
			return err
		}
		config.LogsStoreConfig = configData.LogsStoreConfig
		if err := initSkillsObjectStore(ctx, config, configData.LogsStoreConfig); err != nil {
			return err
		}
		logger.Info("logs store initialized")
	} else if configData.LogsStoreConfig == nil {
		// No logs store section — check DB for stored config (if available), then fall back to default SQLite
		var logStoreConfig *logstore.Config
		if config.ConfigStore != nil {
			var dbErr error
			logStoreConfig, dbErr = config.ConfigStore.GetLogsStoreConfig(ctx)
			if dbErr != nil {
				return fmt.Errorf("failed to get logs store config: %w", dbErr)
			}
		}
		if logStoreConfig == nil {
			logStoreConfig = &logstore.Config{
				Enabled: true,
				Type:    logstore.LogStoreTypeSQLite,
				Config: &logstore.SQLiteConfig{
					Path: logsDBPath,
				},
			}
		}
		config.LogsStore, err = logstore.NewLogStore(ctx, logStoreConfig, logger)
		if err != nil {
			// Handle case where stored path doesn't exist, create new at default path
			if logStoreConfig.Type == logstore.LogStoreTypeSQLite && errors.Is(err, os.ErrNotExist) {
				storedPath := ""
				if sqliteConfig, ok := logStoreConfig.Config.(*logstore.SQLiteConfig); ok {
					storedPath = sqliteConfig.Path
				}
				if storedPath != logsDBPath {
					logger.Warn("failed to locate logstore file at path: %s: %v. Creating new one at path: %s", storedPath, err, logsDBPath)
					logStoreConfig = &logstore.Config{
						Enabled: true,
						Type:    logstore.LogStoreTypeSQLite,
						Config: &logstore.SQLiteConfig{
							Path: logsDBPath,
						},
					}
					config.LogsStore, err = logstore.NewLogStore(ctx, logStoreConfig, logger)
					if err != nil {
						return fmt.Errorf("failed to initialize logs store: %v", err)
					}
					config.LogsStoreConfig = logStoreConfig
				} else {
					return fmt.Errorf("failed to initialize logs store: %v", err)
				}
			} else {
				return fmt.Errorf("failed to initialize logs store: %v", err)
			}
		}
		if config.LogsStoreConfig == nil {
			config.LogsStoreConfig = logStoreConfig
		}
		logger.Info("logs store initialized")
		if err := initSkillsObjectStore(ctx, config, logStoreConfig); err != nil {
			return err
		}
		if config.ConfigStore != nil {
			if err = config.ConfigStore.UpdateLogsStoreConfig(ctx, logStoreConfig); err != nil {
				return fmt.Errorf("failed to update logs store config: %w", err)
			}
		}
	}

	// Initialize vector store (only if explicitly configured)
	if configData.VectorStoreConfig != nil && configData.VectorStoreConfig.Enabled {
		logger.Info("connecting to vectorstore")
		config.VectorStore, err = vectorstore.NewVectorStore(ctx, configData.VectorStoreConfig, logger)
		if err != nil {
			logger.Fatal("failed to connect to vector store: %v", err)
		}
		if config.ConfigStore != nil {
			if err = config.ConfigStore.UpdateVectorStoreConfig(ctx, configData.VectorStoreConfig); err != nil {
				logger.Warn("failed to update vector store config: %v", err)
			}
		}
	}
	return nil
}

// applyClientConfigDefaults fills in default values for zero-value fields in a ClientConfig.
// This ensures partial configs (from file or DB) get sensible defaults for unset fields.
func applyClientConfigDefaults(cc *configstore.ClientConfig) {
	if cc.InitialPoolSize == 0 {
		cc.InitialPoolSize = DefaultClientConfig.InitialPoolSize
	}
	if cc.MaxRequestBodySizeMB == 0 {
		cc.MaxRequestBodySizeMB = DefaultClientConfig.MaxRequestBodySizeMB
	}
	if cc.MCPAgentDepth == 0 {
		cc.MCPAgentDepth = DefaultClientConfig.MCPAgentDepth
	}
	if cc.RoutingChainMaxDepth == 0 {
		cc.RoutingChainMaxDepth = DefaultClientConfig.RoutingChainMaxDepth
	}
	if cc.MCPToolExecutionTimeout == 0 {
		cc.MCPToolExecutionTimeout = DefaultClientConfig.MCPToolExecutionTimeout
	}
	if cc.MCPCodeModeBindingLevel == "" {
		cc.MCPCodeModeBindingLevel = DefaultClientConfig.MCPCodeModeBindingLevel
	}
	if cc.AllowedOrigins == nil {
		cc.AllowedOrigins = DefaultClientConfig.AllowedOrigins
	}
	if cc.AllowedHeaders == nil {
		cc.AllowedHeaders = DefaultClientConfig.AllowedHeaders
	}
	if cc.EnableLogging == nil {
		cc.EnableLogging = new(true)
	}
}

// validateClientConfig checks invariants on a fully-merged client config that
// must hold regardless of the source (config.json, DB, or defaults). It returns
// an error rather than silently correcting a value so an out-of-range setting
// fails loudly at startup instead of diverging in-memory state from what the
// operator wrote — and stays wrong (re-warned, unpersisted) on every restart.
func validateClientConfig(cc *configstore.ClientConfig) error {
	// The /api/config handler rejects an over-max auth_code_ttl, but config.json
	// and any DB row written before the cap existed bypass that path. Fail fast
	// here, the point where every config source converges, so a leaked one-time
	// code can never be minted with a lifetime above the ceiling. A zero/omitted
	// value is valid — it resolves to the default at issuance.
	if oc := cc.OAuth2ServerConfig; oc != nil && oc.AuthCodeTTL > configstoreTables.MaxAuthCodeTTL {
		return fmt.Errorf("oauth2_server_config.auth_code_ttl %d exceeds the maximum of %d seconds (15 minutes)", oc.AuthCodeTTL, configstoreTables.MaxAuthCodeTTL)
	}
	return nil
}

// sanitizeMCPExternalOAuthURLs validates the MCP external OAuth URL overrides
// on a ClientConfig and clears any invalid override so it cannot leak into
// OAuth URL generation. The warning intentionally omits the offending value:
// these fields support env-var references (`env.MY_VAR`), and echoing the
// resolved value would let a misconfigured deployment surface env contents
// in logs.
func sanitizeMCPExternalOAuthURLs(client *configstore.ClientConfig) {
	if client == nil {
		return
	}
	if err := ValidateBaseURL(client.MCPExternalClientURL.GetValue()); err != nil {
		logger.Warn("mcp_external_client_url %v; override will be ignored and OAuth URLs will fall back to the request Host header", err)
		client.MCPExternalClientURL = nil
	}
}

// loadClientConfig loads and merges client config from file with store using hash-based reconciliation.
// The hash covers both the client section and mcp.tool_manager_config so that UI changes to either
// survive restarts when the file is unchanged.
func loadClientConfig(ctx context.Context, config *Config, configData *ConfigData) {
	var clientConfig *configstore.ClientConfig
	var err error
	if config.ConfigStore != nil {
		clientConfig, err = config.ConfigStore.GetClientConfig(ctx)
		if err != nil {
			logger.Warn("failed to get client config from store: %v", err)
		}
	}

	// toolManagerFromFile returns the mcp.tool_manager_config section of the file, or nil.
	var toolManagerFromFile *schemas.MCPToolManagerConfig
	if configData.MCP != nil {
		toolManagerFromFile = configData.MCP.ToolManagerConfig
	}

	// Case 1: No config in DB - use file config (or defaults)
	if clientConfig == nil {
		logger.Debug("client config not found in store, using config file")
		if configData.Client != nil {
			sanitizeMCPExternalOAuthURLs(configData.Client)
			config.ClientConfig = configData.Client
			applyClientConfigDefaults(config.ClientConfig)
			applyToolManagerToClientConfig(config.ClientConfig, toolManagerFromFile)
			fileHash, hashErr := configData.Client.GenerateClientConfigHashWithToolManager(toolManagerFromFile)
			if hashErr != nil {
				logger.Warn("failed to generate client config hash: %v", hashErr)
			} else {
				config.ClientConfig.ConfigHash = fileHash
			}
		} else {
			config.ClientConfig = new(DefaultClientConfig)
			applyToolManagerToClientConfig(config.ClientConfig, toolManagerFromFile)
			defaultHash, hashErr := config.ClientConfig.GenerateClientConfigHashWithToolManager(toolManagerFromFile)
			if hashErr != nil {
				logger.Warn("failed to generate default client config hash: %v", hashErr)
			} else {
				config.ClientConfig.ConfigHash = defaultHash
			}
		}
		if config.ConfigStore != nil {
			logger.Debug("updating client config in store")
			if err = config.ConfigStore.UpdateClientConfig(ctx, config.ClientConfig); err != nil {
				logger.Warn("failed to update client config: %v", err)
			}
		}
		return
	}
	// Case 2: Config exists in DB
	config.ClientConfig = clientConfig
	applyClientConfigDefaults(config.ClientConfig)
	// Case 2a: No file config - use DB config as-is
	if configData.Client == nil {
		logger.Debug("no client config in file, using DB config")
		return
	}
	// Case 2b: Both DB and file config exist - use hash-based reconciliation.
	// The hash covers both the client section and mcp.tool_manager_config so a change
	// in either section triggers a file-wins sync.
	fileHash, hashErr := configData.Client.GenerateClientConfigHashWithToolManager(toolManagerFromFile)
	if hashErr != nil {
		logger.Warn("failed to generate client config hash from file: %v", hashErr)
		return
	}
	// When config.json owns this section, the file always wins regardless of the
	// stored hash: UI/API edits do not bump ConfigHash, so a hash match cannot prove
	// the DB row is unchanged. Forcing the sync reverts UI drift back to file values.
	forceClientSync := configData.isConfigJSONSourceOfTruth() && configData.sectionPresent("client")
	if !forceClientSync && clientConfig.ConfigHash == fileHash {
		// Hash matches - keep DB config (preserves UI changes to both client and tool manager settings)
		logger.Debug("client config hash matches, keeping DB config")
	} else if baseHash, baseErr := configData.Client.GenerateClientConfigHash(); !forceClientSync && baseErr == nil &&
		clientConfig.ConfigHash == baseHash && toolManagerFromFile != nil {
		// Legacy hash match (pre-upgrade): the stored hash covers only the client section and
		// matches the file, meaning the client section is unchanged. Only apply the tool manager
		// settings from the file so that client-section UI changes survive the upgrade.
		logger.Info("upgrading config hash to include mcp.tool_manager_config; applying tool manager settings from file, client config preserved from DB")
		applyToolManagerToClientConfig(config.ClientConfig, toolManagerFromFile)
		config.ClientConfig.ConfigHash = fileHash
		if config.ConfigStore != nil {
			if err = config.ConfigStore.UpdateClientConfig(ctx, config.ClientConfig); err != nil {
				logger.Warn("failed to update client config: %v", err)
			}
		}
	} else {
		// Full hash mismatch - file changed, sync from file (file takes precedence)
		logger.Info("client config was updated in config.json, syncing. Note that: file config takes precedence.")
		sanitizeMCPExternalOAuthURLs(configData.Client)
		config.ClientConfig = configData.Client
		config.ClientConfig.ConfigHash = fileHash
		applyClientConfigDefaults(config.ClientConfig)
		applyToolManagerToClientConfig(config.ClientConfig, toolManagerFromFile)
		if config.ConfigStore != nil {
			logger.Debug("updating client config in store from file")
			if err = config.ConfigStore.UpdateClientConfig(ctx, config.ClientConfig); err != nil {
				logger.Warn("failed to update client config: %v", err)
			}
		}
	}
}

// applyToolManagerToClientConfig copies tool manager settings from the file into ClientConfig.
// Only called when the file has changed (hash mismatch) or on first startup.
// Zero/empty file values reset the field to its default so users can remove a setting
// from the file and have it revert to the default rather than being silently preserved.
func applyToolManagerToClientConfig(cc *configstore.ClientConfig, tm *schemas.MCPToolManagerConfig) {
	if tm == nil {
		return
	}
	if tm.MaxAgentDepth > 0 {
		cc.MCPAgentDepth = tm.MaxAgentDepth
	} else {
		cc.MCPAgentDepth = DefaultClientConfig.MCPAgentDepth
	}
	if d := tm.ToolExecutionTimeout.D(); d > 0 {
		cc.MCPToolExecutionTimeout = int(math.Ceil(d.Seconds()))
	} else {
		cc.MCPToolExecutionTimeout = DefaultClientConfig.MCPToolExecutionTimeout
	}
	if tm.CodeModeBindingLevel != "" {
		cc.MCPCodeModeBindingLevel = string(tm.CodeModeBindingLevel)
	} else {
		cc.MCPCodeModeBindingLevel = DefaultClientConfig.MCPCodeModeBindingLevel
	}
	cc.MCPDisableAutoToolInject = tm.DisableAutoToolInject
}

// loadProviders loads and merges providers from file with store using hash reconciliation
func loadProviders(ctx context.Context, config *Config, configData *ConfigData) error {
	var providersInConfigStore map[schemas.ModelProvider]configstore.ProviderConfig
	var err error
	if config.ConfigStore != nil {
		logger.Debug("getting providers config from store")
		providersInConfigStore, err = config.ConfigStore.GetProvidersConfig(ctx)
		if err != nil {
			logger.Warn("failed to get providers config from store: %v", err)
		}
	}
	if providersInConfigStore == nil {
		logger.Debug("no providers config found in store, processing from config file")
		providersInConfigStore = make(map[schemas.ModelProvider]configstore.ProviderConfig)
	}
	existingProvidersForPrune := providersInConfigStore
	providersSectionPresent := configData.sectionPresent("providers")
	if configData.isConfigJSONSourceOfTruth() && providersSectionPresent {
		logger.Debug("source_of_truth=config.json: syncing providers exactly from config file")
		authoritativeProviders := make(map[schemas.ModelProvider]configstore.ProviderConfig, len(configData.Providers))
		for providerName, providerCfgInFile := range configData.Providers {
			provider := schemas.ModelProvider(strings.ToLower(providerName))
			existingCfg, exists := providersInConfigStore[provider]
			processAuthoritativeProvider(providerName, providerCfgInFile, existingCfg, exists, authoritativeProviders)
		}
		providersInConfigStore = authoritativeProviders
	} else {
		// Process provider configurations from file
		if len(configData.Providers) > 0 {
			for providerName, providerCfgInFile := range configData.Providers {
				if err = processProvider(config, providerName, providerCfgInFile, providersInConfigStore); err != nil {
					logger.Warn("failed to process provider %s: %v", providerName, err)
				}
			}
		} else if len(providersInConfigStore) == 0 && (!configData.isConfigJSONSourceOfTruth() || providersSectionPresent) {
			// No providers in file and none in DB — auto-detect from environment
			config.autoDetectProviders(ctx)
			maps.Copy(providersInConfigStore, config.Providers)
		}
	}
	// Update store and config
	if config.ConfigStore != nil {
		logger.Debug("updating providers config in store")
		if configData.isConfigJSONSourceOfTruth() && providersSectionPresent {
			err = syncAuthoritativeProvidersInStore(ctx, config.ConfigStore, existingProvidersForPrune, providersInConfigStore)
		} else {
			err = config.ConfigStore.UpdateProvidersConfig(ctx, providersInConfigStore)
		}
		if err != nil {
			logger.Fatal("failed to update providers config: %v", err)
		}
	}
	config.Providers = providersInConfigStore
	return nil
}

// syncAuthoritativeProvidersInStore persists providers and deletes DB-only providers and keys.
func syncAuthoritativeProvidersInStore(
	ctx context.Context,
	store configstore.ConfigStore,
	existingProviders map[schemas.ModelProvider]configstore.ProviderConfig,
	authoritativeProviders map[schemas.ModelProvider]configstore.ProviderConfig,
) error {
	return store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		for provider, existingConfig := range existingProviders {
			newConfig, keepProvider := authoritativeProviders[provider]
			if !keepProvider {
				if err := store.DeleteProvider(ctx, provider, tx); err != nil {
					return err
				}
				continue
			}
			keepKeys := make(map[string]bool, len(newConfig.Keys))
			for _, key := range newConfig.Keys {
				if key.ID != "" {
					keepKeys[key.ID] = true
				}
			}
			for _, key := range existingConfig.Keys {
				if key.ID != "" && !keepKeys[key.ID] {
					if err := store.DeleteProviderKey(ctx, provider, key.ID, tx); err != nil {
						return err
					}
				}
			}
		}
		return store.UpdateProvidersConfig(ctx, authoritativeProviders, tx)
	})
}

// processProvider processes a single provider configuration from config file
func processProvider(
	_ *Config,
	providerName string,
	providerCfgInFile configstore.ProviderConfig,
	providersInConfigStore map[schemas.ModelProvider]configstore.ProviderConfig,
) error {
	provider := schemas.ModelProvider(strings.ToLower(providerName))

	if err := ValidateCustomProvider(providerCfgInFile, provider); err != nil {
		return err
	}

	baseProvider := provider
	if providerCfgInFile.CustomProviderConfig != nil && providerCfgInFile.CustomProviderConfig.BaseProviderType != "" {
		baseProvider = providerCfgInFile.CustomProviderConfig.BaseProviderType
	}

	// Process environment variables in keys (including key-level configs)
	for i, providerKeyInFile := range providerCfgInFile.Keys {
		if providerKeyInFile.ID == "" {
			providerCfgInFile.Keys[i].ID = uuid.NewString()
		}
		if err := providerKeyInFile.Aliases.Validate(baseProvider); err != nil {
			return fmt.Errorf("invalid aliases for key %q in provider %s: %w", providerKeyInFile.Name, provider, err)
		}
	}
	// Generate hash from config.json provider config
	fileProviderConfigHash, err := providerCfgInFile.GenerateConfigHash(string(provider))
	if err != nil {
		logger.Warn("failed to generate config hash for %s: %v", provider, err)
	}
	providerCfgInFile.ConfigHash = fileProviderConfigHash
	// Merge with existing config using hash-based reconciliation
	mergeProviderWithHash(provider, providerCfgInFile, providersInConfigStore)
	return nil
}

// processAuthoritativeProvider adds one config.json provider to the authoritative provider set.
func processAuthoritativeProvider(
	providerName string,
	providerCfgInFile configstore.ProviderConfig,
	existingCfg configstore.ProviderConfig,
	exists bool,
	providers map[schemas.ModelProvider]configstore.ProviderConfig,
) {
	provider := schemas.ModelProvider(strings.ToLower(providerName))
	if err := ValidateCustomProvider(providerCfgInFile, provider); err != nil {
		logger.Warn("invalid custom provider config for %s (writing through): %v", provider, err)
	}
	baseProvider := provider
	if providerCfgInFile.CustomProviderConfig != nil && providerCfgInFile.CustomProviderConfig.BaseProviderType != "" {
		baseProvider = providerCfgInFile.CustomProviderConfig.BaseProviderType
	}
	for i, providerKeyInFile := range providerCfgInFile.Keys {
		if providerKeyInFile.ID == "" {
			providerCfgInFile.Keys[i].ID = uuid.NewString()
		}
		if err := providerKeyInFile.Aliases.Validate(baseProvider); err != nil {
			logger.Warn("invalid aliases for key %q in provider %s (writing through): %v", providerKeyInFile.Name, provider, err)
		}
	}
	fileProviderConfigHash, err := providerCfgInFile.GenerateConfigHash(string(provider))
	if err != nil {
		logger.Warn("failed to generate config hash for %s: %v", provider, err)
	}
	providerCfgInFile.ConfigHash = fileProviderConfigHash
	if exists {
		providerCfgInFile.Keys = mergeProviderKeys(provider, providerCfgInFile.Keys, existingCfg.Keys)
		providerCfgInFile.Status = existingCfg.Status
		providerCfgInFile.Description = existingCfg.Description
	}
	providers[provider] = providerCfgInFile
}

// mergeProviderWithHash merges provider config using hash-based reconciliation
func mergeProviderWithHash(
	provider schemas.ModelProvider,
	providerCfgInFile configstore.ProviderConfig,
	providersInConfigStore map[schemas.ModelProvider]configstore.ProviderConfig,
) {
	existingCfg, exists := providersInConfigStore[provider]
	if !exists {
		// New provider - add from config.json
		providersInConfigStore[provider] = providerCfgInFile
		return
	}
	// Provider exists in DB - compare hashes
	if existingCfg.ConfigHash != providerCfgInFile.ConfigHash {
		// Hash mismatch - config.json was changed, sync from file
		logger.Debug("config hash mismatch for provider %s, syncing from config file", provider)
		mergedKeys := mergeProviderKeys(provider, providerCfgInFile.Keys, existingCfg.Keys)
		providerCfgInFile.Keys = mergedKeys
		providersInConfigStore[provider] = providerCfgInFile
	} else {
		// Provider hash matches - but still check individual keys
		logger.Debug("config hash matches for provider %s, checking individual keys", provider)
		mergedKeys := reconcileProviderKeys(provider, providerCfgInFile.Keys, existingCfg.Keys)
		existingCfg.Keys = mergedKeys
		providersInConfigStore[provider] = existingCfg
	}
}

// mergeProviderKeys syncs keys when provider hash has changed (file is source of truth).
// Keys in file are kept, keys only in DB are removed.
func mergeProviderKeys(provider schemas.ModelProvider, fileKeys, dbKeys []schemas.Key) []schemas.Key {
	mergedKeys := fileKeys
	for _, dbKey := range dbKeys {
		found := false
		for i, fileKey := range fileKeys {
			// Compare by hash to detect changes
			fileKeyHash, err := configstore.GenerateKeyHash(fileKey)
			if err != nil {
				logger.Warn("failed to generate key hash for file key %s (%s): %v, falling back to name comparison", fileKey.Name, provider, err)
				if fileKey.Name == dbKey.Name {
					fileKeys[i].ID = dbKey.ID
					fileKeys[i].Status = dbKey.Status
					fileKeys[i].Description = dbKey.Description
					found = true
					break
				}
				continue
			}
			// Assign ConfigHash to file key (marks it as from config.json)
			fileKeys[i].ConfigHash = fileKeyHash
			// Use stored ConfigHash for comparison if available
			if dbKey.ConfigHash != "" {
				if fileKeyHash == dbKey.ConfigHash || fileKey.Name == dbKey.Name {
					fileKeys[i].ID = dbKey.ID
					fileKeys[i].Status = dbKey.Status
					fileKeys[i].Description = dbKey.Description
					found = true
					break
				}
			} else {
				// No stored hash (legacy) - fall back to generating fresh hash
				dbKeyHash, err := configstore.GenerateKeyHash(schemas.Key{
					Name:                   dbKey.Name,
					Value:                  dbKey.Value,
					Models:                 dbKey.Models,
					BlacklistedModels:      dbKey.BlacklistedModels,
					Weight:                 dbKey.Weight,
					AzureKeyConfig:         dbKey.AzureKeyConfig,
					VertexKeyConfig:        dbKey.VertexKeyConfig,
					BedrockKeyConfig:       dbKey.BedrockKeyConfig,
					BedrockMantleKeyConfig: dbKey.BedrockMantleKeyConfig,
					ReplicateKeyConfig:     dbKey.ReplicateKeyConfig,
					Aliases:                dbKey.Aliases,
					VLLMKeyConfig:          dbKey.VLLMKeyConfig,
					OllamaKeyConfig:        dbKey.OllamaKeyConfig,
					SGLKeyConfig:           dbKey.SGLKeyConfig,
					Enabled:                dbKey.Enabled,
					UseForBatchAPI:         dbKey.UseForBatchAPI,
					UseAnthropicEndpoints:  dbKey.UseAnthropicEndpoints,
				})
				if err != nil {
					logger.Warn("failed to generate key hash for db key %s (%s): %v, falling back to name comparison", dbKey.Name, provider, err)
					if fileKey.Name == dbKey.Name {
						fileKeys[i].ID = dbKey.ID
						fileKeys[i].Status = dbKey.Status
						fileKeys[i].Description = dbKey.Description
						found = true
						break
					}
					continue
				}
				if fileKeyHash == dbKeyHash || fileKey.Name == dbKey.Name {
					fileKeys[i].ID = dbKey.ID
					fileKeys[i].Status = dbKey.Status
					fileKeys[i].Description = dbKey.Description
					found = true
					break
				}
			}
		}
		if !found {
			// Key exists in DB but not in file - skip it (file is source of truth when hash changed)
			logger.Debug("key %s exists in DB but not in file for provider %s, removing", dbKey.Name, provider)
		}
	}
	return mergedKeys
}

// reconcileProviderKeys reconciles keys when provider hash matches
func reconcileProviderKeys(provider schemas.ModelProvider, fileKeys, dbKeys []schemas.Key) []schemas.Key {
	mergedKeys := make([]schemas.Key, 0)
	fileKeysByName := make(map[string]int) // name -> index in file keys
	for i, fileKey := range fileKeys {
		fileKeysByName[fileKey.Name] = i
	}
	// Process DB keys - check if they exist in file and compare hashes
	for _, dbKey := range dbKeys {
		if fileIdx, exists := fileKeysByName[dbKey.Name]; exists {
			fileKey := fileKeys[fileIdx]
			fileKeyHash, err := configstore.GenerateKeyHash(fileKey)
			if err != nil {
				logger.Warn("failed to generate key hash for file key %s (%s): %v", fileKey.Name, provider, err)
				mergedKeys = append(mergedKeys, dbKey)
				delete(fileKeysByName, dbKey.Name)
				continue
			}

			// Compare file hash against STORED config hash (not fresh hash from DB values)
			// This ensures DB updates are preserved when config.json hasn't changed
			if dbKey.ConfigHash != "" {
				if fileKeyHash == dbKey.ConfigHash {
					// File unchanged - keep DB version (preserves user updates)
					mergedKeys = append(mergedKeys, dbKey)
				} else {
					// File changed - use file version but preserve ID and set ConfigHash
					logger.Debug("key %s changed in config file for provider %s, updating", fileKey.Name, provider)
					fileKey.ID = dbKey.ID
					fileKey.ConfigHash = fileKeyHash
					fileKey.Status = dbKey.Status
					fileKey.Description = dbKey.Description
					mergedKeys = append(mergedKeys, fileKey)
				}
			} else {
				// No stored hash (legacy) - fall back to generating fresh hash for comparison
				dbKeyHash, err := configstore.GenerateKeyHash(schemas.Key{
					Name:                   dbKey.Name,
					Value:                  dbKey.Value,
					Models:                 dbKey.Models,
					BlacklistedModels:      dbKey.BlacklistedModels,
					Weight:                 dbKey.Weight,
					AzureKeyConfig:         dbKey.AzureKeyConfig,
					VertexKeyConfig:        dbKey.VertexKeyConfig,
					BedrockKeyConfig:       dbKey.BedrockKeyConfig,
					BedrockMantleKeyConfig: dbKey.BedrockMantleKeyConfig,
					ReplicateKeyConfig:     dbKey.ReplicateKeyConfig,
					Aliases:                dbKey.Aliases,
					VLLMKeyConfig:          dbKey.VLLMKeyConfig,
					OllamaKeyConfig:        dbKey.OllamaKeyConfig,
					SGLKeyConfig:           dbKey.SGLKeyConfig,
					Enabled:                dbKey.Enabled,
					UseForBatchAPI:         dbKey.UseForBatchAPI,
					UseAnthropicEndpoints:  dbKey.UseAnthropicEndpoints,
				})
				if err != nil {
					logger.Warn("failed to generate key hash for db key %s (%s): %v", dbKey.Name, provider, err)
					mergedKeys = append(mergedKeys, dbKey)
					delete(fileKeysByName, dbKey.Name)
					continue
				}
				if fileKeyHash != dbKeyHash {
					// Key changed in file - use file version but preserve ID and set ConfigHash
					logger.Debug("key %s changed in config file for provider %s, updating", fileKey.Name, provider)
					fileKey.ID = dbKey.ID
					fileKey.ConfigHash = fileKeyHash
					fileKey.Status = dbKey.Status
					fileKey.Description = dbKey.Description
					mergedKeys = append(mergedKeys, fileKey)
				} else {
					// Key unchanged - keep DB version
					mergedKeys = append(mergedKeys, dbKey)
				}
			}
			delete(fileKeysByName, dbKey.Name) // Mark as processed
		} else {
			// Key only in DB - preserve it (added via dashboard)
			mergedKeys = append(mergedKeys, dbKey)
		}
	}
	// Add keys only in file (new keys from config.json)
	for _, idx := range fileKeysByName {
		fileKey := fileKeys[idx]
		// Generate and assign ConfigHash for new keys from config.json
		fileKeyHash, err := configstore.GenerateKeyHash(fileKey)
		if err != nil {
			logger.Warn("failed to generate key hash for new file key %s (%s): %v", fileKey.Name, provider, err)
		} else {
			fileKey.ConfigHash = fileKeyHash
		}
		mergedKeys = append(mergedKeys, fileKey)
	}
	return mergedKeys
}

// loadMCPConfig loads and merges MCP config from file
func loadMCPConfig(ctx context.Context, config *Config, configData *ConfigData) {
	if config.ConfigStore == nil {
		if configData.MCP != nil && len(configData.MCP.ClientConfigs) > 0 {
			logger.Warn("config store is disabled - MCP manager will not be initialized. MCP clients require config store for persistence.")
		}
		return
	}
	// Validate MCP client names from config file before processing
	if configData.MCP != nil && len(configData.MCP.ClientConfigs) > 0 {
		valid := make([]*schemas.MCPClientConfig, 0, len(configData.MCP.ClientConfigs))
		for _, c := range configData.MCP.ClientConfigs {
			if c == nil {
				continue
			}
			if err := mcp.ValidateMCPClientName(c.Name); err != nil {
				logger.Warn("skipping MCP client config %q from config file: %v", c.Name, err)
				continue
			}
			// oauth_config_id is server-managed state — a credentials-row
			// reference produced by admin authorization — not a declarative
			// input. A file-supplied value is at best redundant (the sync
			// preserves the server-side link) and at worst repoints the
			// client at a stale or foreign-deployment row. Ignore it; the
			// synthesis below then parks unauthorized clients in
			// pending_verification, where the admin flow can mint a real one.
			if c.OauthConfigID != nil {
				logger.Warn("ignoring oauth_config_id on MCP client %q from config file: this field is managed by Bifrost and cannot be set via config.json", c.Name)
				c.OauthConfigID = nil
			}
			// OAuth-based auth types with no inline `oauth_config` still
			// need the bootstrap-pending marker so the client lands in
			// pending_verification and the initiate-verification endpoint
			// can run discovery + dynamic client registration off the
			// connection_string at admin-click time. Synthesize an empty
			// OAuth2Config so the runtime gate and the handler see the same
			// shape they do when the block was provided.
			if (c.AuthType == schemas.MCPAuthTypeOauth || c.AuthType == schemas.MCPAuthTypePerUserOauth) &&
				c.PendingOAuthConfig == nil {
				c.PendingOAuthConfig = &schemas.OAuth2Config{}
			}
			valid = append(valid, c)
		}
		configData.MCP.ClientConfigs = valid
	}

	if config.ConfigStore != nil {
		logger.Debug("getting MCP config from store")
		tableMCPConfig, err := config.ConfigStore.GetMCPConfig(ctx)
		if err != nil {
			logger.Warn("failed to get MCP config from store: %v", err)
		} else if tableMCPConfig != nil {
			config.MCPConfig = tableMCPConfig
		}
	}

	if config.MCPConfig != nil {
		// Merge with config file if present
		if configData.MCP != nil {
			if configData.isConfigJSONSourceOfTruth() && configData.sectionPresent("mcp") {
				syncMCPConfigFromFile(ctx, config, configData, config.MCPConfig)
			} else {
				mergeMCPConfig(ctx, config, configData, config.MCPConfig)
			}
		}
	} else if configData.MCP != nil {
		// MCP config not in store, use config file
		logger.Debug("no MCP config found in store, processing from config file")
		config.MCPConfig = configData.MCP
		if config.ConfigStore != nil && config.MCPConfig != nil {
			logger.Debug("updating MCP config in store")
			for _, clientConfig := range config.MCPConfig.ClientConfigs {
				if clientConfig != nil {
					if clientConfig.ID == "" {
						clientConfig.ID = uuid.NewString()
					}
					if err := config.ConfigStore.CreateMCPClientConfig(ctx, clientConfig); err != nil {
						logger.Warn("failed to create MCP client config: %v", err)
					}
				}
			}
		}
	}
	applyMCPGlobalSettingsToClientConfig(ctx, config, configData.MCP)
}

// pinMCPClientImmutableFields rewrites a file-declared client so that fields
// which are immutable after creation keep their stored values, mirroring the
// update API: MCPClientUpdateRequest accepts none of these fields, so an API
// caller sending them has them silently dropped, and the file sync applies
// the same rule. It also carries server-side verification state that
// config.json cannot express (it is produced by admin verification at
// runtime):
//   - OauthConfigID links the oauth_configs row (token / provider config)
//     created by the admin's browser flow.
//   - DiscoveredTools / DiscoveredToolNameMapping are populated by the
//     one-time admin verification for per-user auth types.
//   - PendingOAuthConfig stays whatever the store holds: the stash declared
//     at creation while verification is pending, nil once the OAuth callback
//     has cleared it.
//
// The returned list names the immutable fields whose file values differ from
// the stored ones, for the caller's advisory log. authorizedOauth is the
// oauth_configs row backing an already-authorized client (nil when the client
// is unauthorized or the row is unavailable); it is only read, and only to
// detect oauth_config drift.
func pinMCPClientImmutableFields(fileClient, existing *schemas.MCPClientConfig, authorizedOauth *configstoreTables.TableOauthConfig) []string {
	if fileClient == nil || existing == nil {
		return nil
	}
	var changed []string

	fileAuth := fileClient.AuthType
	if fileAuth == "" {
		fileAuth = schemas.MCPAuthTypeHeaders
	}
	existingAuth := existing.AuthType
	if existingAuth == "" {
		existingAuth = schemas.MCPAuthTypeHeaders
	}
	if fileAuth != existingAuth {
		changed = append(changed, "auth_type")
	}
	fileClient.AuthType = existingAuth

	if fileClient.ConnectionType != existing.ConnectionType {
		changed = append(changed, "connection_type")
	}
	fileClient.ConnectionType = existing.ConnectionType

	if !fileClient.ConnectionString.Equals(existing.ConnectionString) {
		changed = append(changed, "connection_string")
	}
	fileClient.ConnectionString = existing.ConnectionString

	if !reflect.DeepEqual(fileClient.StdioConfig, existing.StdioConfig) {
		changed = append(changed, "stdio_config")
	}
	fileClient.StdioConfig = existing.StdioConfig

	if mcpOauthBlockChanged(fileClient.PendingOAuthConfig, existing.PendingOAuthConfig, authorizedOauth) {
		changed = append(changed, "oauth_config")
	}
	fileClient.PendingOAuthConfig = existing.PendingOAuthConfig

	// Not immutable, but a per_user_headers client must keep a non-empty key
	// schema — the update API rejects emptying it for the same reason.
	if fileClient.AuthType == schemas.MCPAuthTypePerUserHeaders &&
		len(fileClient.PerUserHeaderKeys) == 0 && len(existing.PerUserHeaderKeys) > 0 {
		logger.Warn("per_user_header_keys cannot be emptied for MCP client %q (auth_type 'per_user_headers'); keeping the stored keys", existing.Name)
		fileClient.PerUserHeaderKeys = existing.PerUserHeaderKeys
	}

	fileClient.OauthConfigID = existing.OauthConfigID
	if len(fileClient.DiscoveredTools) == 0 {
		fileClient.DiscoveredTools = existing.DiscoveredTools
		fileClient.DiscoveredToolNameMapping = existing.DiscoveredToolNameMapping
	}
	return changed
}

// mcpOauthBlockChanged reports whether the file's inline oauth_config block
// drifts from the OAuth configuration the client actually runs on. Only
// fields the file explicitly sets are compared: absent fields are filled in
// by discovery / dynamic client registration at authorization time, so a
// stored value with no file counterpart is not drift. The reference is the
// stored pending stash while verification is pending, and the authorized
// oauth_configs row afterwards; with neither available there is nothing to
// compare against.
func mcpOauthBlockChanged(fileBlock, storedStash *schemas.OAuth2Config, authorizedOauth *configstoreTables.TableOauthConfig) bool {
	if fileBlock == nil {
		return false
	}
	var refClientID, refClientSecret, refAuthorizeURL, refTokenURL string
	var refScopes []string
	switch {
	case storedStash != nil:
		refClientID = storedStash.ClientID.GetValue()
		refClientSecret = storedStash.ClientSecret.GetValue()
		refAuthorizeURL = storedStash.AuthorizeURL
		refTokenURL = storedStash.TokenURL
		refScopes = storedStash.Scopes
	case authorizedOauth != nil:
		refClientID = authorizedOauth.GetResolvedClientID()
		refClientSecret = authorizedOauth.GetResolvedClientSecret()
		refAuthorizeURL = authorizedOauth.AuthorizeURL
		refTokenURL = authorizedOauth.TokenURL
		if authorizedOauth.Scopes != "" {
			_ = json.Unmarshal([]byte(authorizedOauth.Scopes), &refScopes)
		}
	default:
		return false
	}
	// Credentials compare by resolved value: an env./vault. reference that
	// resolves to the stored credential is the same effective config.
	if fileBlock.ClientID.IsSet() && fileBlock.ClientID.GetValue() != refClientID {
		return true
	}
	if fileBlock.ClientSecret.IsSet() && fileBlock.ClientSecret.GetValue() != refClientSecret {
		return true
	}
	if fileBlock.AuthorizeURL != "" && fileBlock.AuthorizeURL != refAuthorizeURL {
		return true
	}
	if fileBlock.TokenURL != "" && fileBlock.TokenURL != refTokenURL {
		return true
	}
	if len(fileBlock.Scopes) > 0 {
		fileScopes := slices.Clone(fileBlock.Scopes)
		storedScopes := slices.Clone(refScopes)
		slices.Sort(fileScopes)
		slices.Sort(storedScopes)
		if !slices.Equal(fileScopes, storedScopes) {
			return true
		}
	}
	return false
}

// authorizedOauthRowForComparison loads the oauth_configs row backing an
// already-authorized client so an edited file oauth_config block can be
// detected by pinMCPClientImmutableFields. Returns nil whenever there is
// nothing to compare (no file block, verification still pending, no store)
// or the lookup fails — pinning still applies either way, only the drift
// warning loses the oauth_config detail.
func authorizedOauthRowForComparison(ctx context.Context, store configstore.ConfigStore, fileClient, existing *schemas.MCPClientConfig) *configstoreTables.TableOauthConfig {
	if store == nil || fileClient == nil || existing == nil {
		return nil
	}
	if fileClient.PendingOAuthConfig == nil || existing.PendingOAuthConfig != nil || existing.OauthConfigID == nil {
		return nil
	}
	row, err := store.GetOauthConfigByID(ctx, *existing.OauthConfigID)
	if err != nil {
		logger.Debug("failed to load oauth config %s for MCP client %q immutable-field check: %v", *existing.OauthConfigID, existing.Name, err)
		return nil
	}
	return row
}

// warnIgnoredImmutableMCPFields emits the advisory line for a config.json
// edit that touched fields which cannot change after creation. The update
// API drops the same fields silently (MCPClientUpdateRequest does not accept
// them); the file path gets this log line because a file editor has no
// response channel.
func warnIgnoredImmutableMCPFields(clientName string, fields []string) {
	if len(fields) == 0 {
		return
	}
	logger.Warn("ignoring changes to immutable fields [%s] on MCP client %q from config file: these cannot be changed after creation; delete and recreate the client to change them", strings.Join(fields, ", "), clientName)
}

// applyMCPClientPinnedStateToRow mirrors the pinned immutable fields and
// preserved server-side state onto the table row that will be persisted,
// keeping the row and the in-memory config consistent (the row was built
// from the raw file entry before pinning). The row's ConfigHash
// intentionally stays the raw-file hash: the stored hash always reflects
// the file entry as last synced, so the immutable-field warning fires once
// per file edit instead of on every boot.
func applyMCPClientPinnedStateToRow(row *configstoreTables.TableMCPClient, clientConfig *schemas.MCPClientConfig) {
	authType := string(clientConfig.AuthType)
	if authType == "" {
		authType = string(schemas.MCPAuthTypeHeaders)
	}
	row.AuthType = authType
	row.ConnectionType = string(clientConfig.ConnectionType)
	row.ConnectionString = clientConfig.ConnectionString
	row.StdioConfig = clientConfig.StdioConfig
	row.PerUserHeaderKeys = mcputils.CanonicalizeHeaderKeys(clientConfig.PerUserHeaderKeys)
	row.OauthConfigID = clientConfig.OauthConfigID
	row.DiscoveredTools = clientConfig.DiscoveredTools
	row.DiscoveredToolNameMapping = clientConfig.DiscoveredToolNameMapping
	row.PendingOAuthConfig = clientConfig.PendingOAuthConfig
}

// mergeMCPConfig merges MCP config from file with store
func mergeMCPConfig(ctx context.Context, config *Config, configData *ConfigData, mcpConfig *schemas.MCPConfig) {
	logger.Debug("merging MCP config from config file with store")

	if configData.MCP == nil {
		return
	}
	tempMCPConfig := configData.MCP
	config.MCPConfig = tempMCPConfig
	// Merge client configs by name with hash-based reconciliation.
	clientConfigsToAdd := make([]*schemas.MCPClientConfig, 0)
	clientConfigsToUpdate := make([]configstoreTables.TableMCPClient, 0)
	for _, newClientConfig := range tempMCPConfig.ClientConfigs {
		if newClientConfig == nil {
			continue
		}
		if newClientConfig.ID == "" {
			newClientConfig.ID = uuid.NewString()
		}
		fileClientRow, err := mcpClientConfigToTable(newClientConfig)
		if err != nil {
			logger.Warn("invalid MCP client config for %q: %v", newClientConfig.Name, err)
			continue
		}
		fileHash, err := configstore.GenerateMCPClientHash(fileClientRow)
		if err != nil {
			logger.Warn("failed to generate MCP client hash for %q: %v", newClientConfig.Name, err)
			continue
		}
		newClientConfig.ConfigHash = fileHash
		fileClientRow.ConfigHash = fileHash

		found := false
		for i, existingClientConfig := range mcpConfig.ClientConfigs {
			if existingClientConfig == nil {
				continue
			}
			if newClientConfig.Name != "" && existingClientConfig.Name == newClientConfig.Name {
				found = true
				if existingClientConfig.ConfigHash != fileHash {
					logger.Debug("config hash mismatch for MCP client %q, syncing from config file", newClientConfig.Name)
					newClientConfig.ID = existingClientConfig.ID
					newClientConfig.ConfigHash = fileHash
					// Pin immutable fields to their stored values and carry
					// server-side verification state (authorized OAuth link,
					// discovered tools) the file cannot express, so a config
					// edit does not reset a verified client.
					authorizedOauth := authorizedOauthRowForComparison(ctx, config.ConfigStore, newClientConfig, existingClientConfig)
					warnIgnoredImmutableMCPFields(existingClientConfig.Name, pinMCPClientImmutableFields(newClientConfig, existingClientConfig, authorizedOauth))
					applyMCPClientPinnedStateToRow(&fileClientRow, newClientConfig)
					fileClientRow.ClientID = existingClientConfig.ID
					fileClientRow.ConfigHash = fileHash
					clientConfigsToUpdate = append(clientConfigsToUpdate, fileClientRow)
					mcpConfig.ClientConfigs[i] = newClientConfig
				} else {
					logger.Debug("config hash matches for MCP client %q, keeping DB config", newClientConfig.Name)
				}
				break
			}
		}
		if !found {
			clientConfigsToAdd = append(clientConfigsToAdd, newClientConfig)
		}
	}
	// Add new client configs to existing ones.
	config.MCPConfig.ClientConfigs = append(mcpConfig.ClientConfigs, clientConfigsToAdd...)
	// Persist additions and config-driven updates.
	if config.ConfigStore != nil && (len(clientConfigsToAdd) > 0 || len(clientConfigsToUpdate) > 0) {
		logger.Debug("updating MCP config in store with %d new clients and %d updated clients", len(clientConfigsToAdd), len(clientConfigsToUpdate))
		for _, clientConfig := range clientConfigsToAdd {
			if clientConfig != nil {
				if err := config.ConfigStore.CreateMCPClientConfig(ctx, clientConfig); err != nil {
					logger.Warn("failed to create MCP client config: %v", err)
				}
			}
		}
		for i := range clientConfigsToUpdate {
			update := clientConfigsToUpdate[i]
			if err := config.ConfigStore.UpdateMCPClientConfig(ctx, update.ClientID, &update); err != nil {
				logger.Warn("failed to update MCP client config %q: %v", update.Name, err)
			}
		}
	}
}

func applyMCPGlobalSettingsToClientConfig(ctx context.Context, config *Config, mcpCfg *schemas.MCPConfig) {
	if config == nil || config.ClientConfig == nil || mcpCfg == nil {
		return
	}

	// Backfill MCPConfig.ToolManagerConfig from ClientConfig so bifrost.Init always receives
	// the authoritative values (which may be DB values preserved by hash reconciliation in
	// loadClientConfig, or file values applied there on hash mismatch).
	// Allocate if absent so bifrost.Init never falls back to hardcoded defaults.
	if mcpCfg.ToolManagerConfig == nil {
		mcpCfg.ToolManagerConfig = &schemas.MCPToolManagerConfig{}
	}
	mcpCfg.ToolManagerConfig.MaxAgentDepth = config.ClientConfig.MCPAgentDepth
	mcpCfg.ToolManagerConfig.ToolExecutionTimeout = schemas.Duration(
		time.Duration(config.ClientConfig.MCPToolExecutionTimeout) * time.Second,
	)
	mcpCfg.ToolManagerConfig.CodeModeBindingLevel = schemas.CodeModeBindingLevel(
		config.ClientConfig.MCPCodeModeBindingLevel,
	)
	mcpCfg.ToolManagerConfig.DisableAutoToolInject = config.ClientConfig.MCPDisableAutoToolInject

	// ToolSyncInterval lives only in MCPConfig (not a ClientConfig field), so reconcile separately.
	changed := false
	if mcpCfg.ToolSyncInterval == 0 {
		if config.ClientConfig.MCPToolSyncInterval != 0 {
			config.ClientConfig.MCPToolSyncInterval = 0
			changed = true
		}
	} else if mcpCfg.ToolSyncInterval > 0 {
		if mcpCfg.ToolSyncInterval%time.Second != 0 {
			logger.Warn(
				"ignoring mcp.tool_sync_interval %q: must be a whole number of seconds",
				mcpCfg.ToolSyncInterval.String(),
			)
		} else {
			syncSeconds := int(mcpCfg.ToolSyncInterval / time.Second)
			if config.ClientConfig.MCPToolSyncInterval != syncSeconds {
				config.ClientConfig.MCPToolSyncInterval = syncSeconds
				changed = true
			}
		}
	}

	if changed && config.ConfigStore != nil {
		if err := config.ConfigStore.UpdateClientConfig(ctx, config.ClientConfig); err != nil {
			logger.Warn("failed to update client config with MCP global settings: %v", err)
		}
	}
}

func mcpClientConfigToTable(clientConfig *schemas.MCPClientConfig) (configstoreTables.TableMCPClient, error) {
	if clientConfig == nil {
		return configstoreTables.TableMCPClient{}, nil
	}
	if clientConfig.ToolSyncInterval%time.Second != 0 {
		return configstoreTables.TableMCPClient{}, fmt.Errorf(
			"tool_sync_interval must be a whole number of seconds, got %q",
			clientConfig.ToolSyncInterval.String(),
		)
	}
	if clientConfig.ToolExecutionTimeout < 0 {
		return configstoreTables.TableMCPClient{}, fmt.Errorf(
			"tool_execution_timeout must be >= 0, got %q",
			clientConfig.ToolExecutionTimeout.String(),
		)
	}
	authType := string(clientConfig.AuthType)
	if authType == "" {
		authType = string(schemas.MCPAuthTypeHeaders)
	}
	return configstoreTables.TableMCPClient{
		ClientID:                  clientConfig.ID,
		Name:                      clientConfig.Name,
		IsCodeModeClient:          clientConfig.IsCodeModeClient,
		ConnectionType:            string(clientConfig.ConnectionType),
		ConnectionString:          clientConfig.ConnectionString,
		StdioConfig:               clientConfig.StdioConfig,
		AuthType:                  authType,
		OauthConfigID:             clientConfig.OauthConfigID,
		ToolsToExecute:            clientConfig.ToolsToExecute,
		ToolsToAutoExecute:        clientConfig.ToolsToAutoExecute,
		Headers:                   clientConfig.Headers,
		AllowedExtraHeaders:       clientConfig.AllowedExtraHeaders,
		IsPingAvailable:           clientConfig.IsPingAvailable,
		ToolSyncInterval:          int(clientConfig.ToolSyncInterval / time.Second),
		ToolExecutionTimeout:      int(math.Ceil(clientConfig.ToolExecutionTimeout.Seconds())),
		ToolPricing:               clientConfig.ToolPricing,
		AllowOnAllVirtualKeys:     clientConfig.AllowOnAllVirtualKeys,
		Disabled:                  clientConfig.Disabled,
		DiscoveredTools:           clientConfig.DiscoveredTools,
		DiscoveredToolNameMapping: clientConfig.DiscoveredToolNameMapping,
		PerUserHeaderKeys:         mcputils.CanonicalizeHeaderKeys(clientConfig.PerUserHeaderKeys),
		PendingOAuthConfig:        clientConfig.PendingOAuthConfig,
		ConfigHash:                clientConfig.ConfigHash,
	}, nil
}

// syncMCPConfigFromFile replaces stored MCP clients with the clients declared in config.json.
//
// Unlike the provider/plugin/governance syncs, this reconciliation is intentionally
// best-effort rather than transactional: the MCP store methods (Create/Update/Delete
// MCPClientConfig) do not accept an enclosing tx, and each client is reconciled
// independently so a single malformed client only logs a warning instead of aborting
// startup. An all-or-nothing transaction is incompatible with that warn-and-continue
// behavior, and blocking boot on one bad MCP entry is the worse failure mode here.
func syncMCPConfigFromFile(ctx context.Context, config *Config, configData *ConfigData, mcpConfig *schemas.MCPConfig) {
	logger.Debug("source_of_truth=config.json: syncing MCP config exactly from config file")
	if configData.MCP == nil {
		return
	}
	fileMCPConfig := configData.MCP
	existingByName := make(map[string]*schemas.MCPClientConfig, len(mcpConfig.ClientConfigs))
	existingByID := make(map[string]*schemas.MCPClientConfig, len(mcpConfig.ClientConfigs))
	for _, existing := range mcpConfig.ClientConfigs {
		if existing == nil {
			continue
		}
		if existing.Name != "" {
			existingByName[existing.Name] = existing
		}
		if existing.ID != "" {
			existingByID[existing.ID] = existing
		}
	}

	keepIDs := make(map[string]bool, len(fileMCPConfig.ClientConfigs))
	updates := make([]configstoreTables.TableMCPClient, 0)
	adds := make([]*schemas.MCPClientConfig, 0)
	for _, fileClient := range fileMCPConfig.ClientConfigs {
		if fileClient == nil {
			continue
		}
		existing := existingByName[fileClient.Name]
		if existing == nil && fileClient.ID != "" {
			existing = existingByID[fileClient.ID]
		}
		// Mark the matched existing client as kept up-front so a later validation
		// failure (which `continue`s) does not cause the prune loop to delete it.
		if existing != nil && existing.ID != "" {
			keepIDs[existing.ID] = true
		}
		if fileClient.ID == "" {
			if existing != nil && existing.ID != "" {
				fileClient.ID = existing.ID
			} else {
				fileClient.ID = uuid.NewString()
			}
		}
		fileRow, err := mcpClientConfigToTable(fileClient)
		if err != nil {
			logger.Warn("invalid MCP client config for %q: %v", fileClient.Name, err)
			continue
		}
		fileHash, err := configstore.GenerateMCPClientHash(fileRow)
		if err != nil {
			logger.Warn("failed to generate MCP client hash for %q: %v", fileClient.Name, err)
			continue
		}
		fileClient.ConfigHash = fileHash
		fileRow.ConfigHash = fileHash
		keepIDs[fileClient.ID] = true
		if existing == nil {
			adds = append(adds, fileClient)
		} else {
			// Pin immutable fields to their stored values and carry
			// server-side verification state (authorized OAuth link,
			// discovered tools) the file cannot express, so the every-boot
			// overwrite does not reset a verified client. The advisory log
			// is gated on the stored hash so it fires once per file edit,
			// not on every boot.
			var authorizedOauth *configstoreTables.TableOauthConfig
			fileEdited := existing.ConfigHash != fileHash
			if fileEdited {
				authorizedOauth = authorizedOauthRowForComparison(ctx, config.ConfigStore, fileClient, existing)
			}
			changedImmutable := pinMCPClientImmutableFields(fileClient, existing, authorizedOauth)
			if fileEdited {
				warnIgnoredImmutableMCPFields(existing.Name, changedImmutable)
			}
			applyMCPClientPinnedStateToRow(&fileRow, fileClient)
			fileRow.ClientID = existing.ID
			updates = append(updates, fileRow)
		}
	}

	if config.ConfigStore != nil {
		for _, existing := range mcpConfig.ClientConfigs {
			if existing == nil || existing.ID == "" || keepIDs[existing.ID] {
				continue
			}
			if err := config.ConfigStore.DeleteMCPClientConfig(ctx, existing.ID); err != nil {
				logger.Warn("failed to delete MCP client config %q: %v", existing.Name, err)
			}
		}
		for _, add := range adds {
			if err := config.ConfigStore.CreateMCPClientConfig(ctx, add); err != nil {
				logger.Warn("failed to create MCP client config: %v", err)
			}
		}
		for i := range updates {
			update := updates[i]
			if err := config.ConfigStore.UpdateMCPClientConfig(ctx, update.ClientID, &update); err != nil {
				logger.Warn("failed to update MCP client config %q: %v", update.Name, err)
			}
		}
	}
	config.MCPConfig = fileMCPConfig
}

// loadWebhooksConfig loads webhook endpoints into memory and reconciles
// config.json declarations against the database, mirroring the MCP config
// flow: declarations are validated with a warn-and-skip policy, matched
// against database rows by name using the config hash for change detection,
// and pruned only when config.json is the source of truth and the section is
// physically present.
func loadWebhooksConfig(ctx context.Context, config *Config, configData *ConfigData) {
	fileEndpoints := make([]*configstoreTables.TableWebhookEndpoint, 0)
	// Every declared name, valid or not: an entry that fails validation must
	// still protect its existing database row from the source-of-truth prune —
	// a typo in one field must never delete a working endpoint.
	declaredNames := make(map[string]bool)
	if configData.Webhooks != nil {
		for _, declared := range configData.Webhooks {
			if declared == nil {
				continue
			}
			if declared.Name != "" {
				declaredNames[declared.Name] = true
			}
			endpoint := declared.toTable()
			if err := endpoint.Validate(); err != nil {
				logger.Warn("skipping webhook endpoint %q from config file: %v", declared.Name, err)
				continue
			}
			fileEndpoints = append(fileEndpoints, endpoint)
		}
	}

	if config.ConfigStore == nil {
		if len(fileEndpoints) > 0 {
			logger.Warn("config store is disabled - webhook endpoints from config file will not be available")
		}
		return
	}

	existing, err := config.ConfigStore.GetWebhookEndpoints(ctx)
	if err != nil {
		logger.Warn("failed to load webhook endpoints: %v", err)
		return
	}

	if configData.isConfigJSONSourceOfTruth() && configData.sectionPresent("webhooks") {
		syncWebhookEndpointsFromFile(ctx, config, fileEndpoints, declaredNames, existing)
	} else {
		mergeWebhookEndpoints(ctx, config, fileEndpoints, existing)
	}

	// Memory serves the final database state, whichever path produced it.
	final, err := config.ConfigStore.GetWebhookEndpoints(ctx)
	if err != nil {
		logger.Warn("failed to reload webhook endpoints: %v", err)
		return
	}
	config.replaceWebhookEndpoints(final)
}

// mergeWebhookEndpoints reconciles file declarations additively: new names
// are created, changed ones (by config hash) are updated, and database
// endpoints absent from the file are left alone. Best-effort with per-item
// warnings, matching the other config-section loaders.
func mergeWebhookEndpoints(ctx context.Context, config *Config, fileEndpoints []*configstoreTables.TableWebhookEndpoint, existing []configstoreTables.TableWebhookEndpoint) {
	existingByName := make(map[string]*configstoreTables.TableWebhookEndpoint, len(existing))
	for i := range existing {
		existingByName[existing[i].Name] = &existing[i]
	}
	for _, endpoint := range fileEndpoints {
		fileHash, err := configstore.GenerateWebhookEndpointHash(endpoint)
		if err != nil {
			logger.Warn("failed to hash webhook endpoint %q: %v", endpoint.Name, err)
			continue
		}
		endpoint.ConfigHash = fileHash
		if match, ok := existingByName[endpoint.Name]; ok {
			if match.ConfigHash == fileHash {
				continue
			}
			endpoint.ID = match.ID
			if err := config.ConfigStore.UpdateWebhookEndpoint(ctx, endpoint); err != nil {
				logger.Warn("failed to update webhook endpoint %q: %v", endpoint.Name, err)
			}
			continue
		}
		if endpoint.ID == "" {
			endpoint.ID = uuid.NewString()
		}
		if err := config.ConfigStore.CreateWebhookEndpoint(ctx, endpoint); err != nil {
			logger.Warn("failed to create webhook endpoint %q: %v", endpoint.Name, err)
		}
	}
}

// syncWebhookEndpointsFromFile makes the database mirror the config file:
// declarations are created or updated as in the merge path, and database
// endpoints not present in the file are deleted. Best-effort, non-
// transactional — each step warns and continues, and a later load converges.
// The prune fails safe: a row is only deleted when its name appears in
// NEITHER the applied set nor the declared set, so a declaration that failed
// validation or hashing keeps its existing endpoint instead of removing it.
func syncWebhookEndpointsFromFile(ctx context.Context, config *Config, fileEndpoints []*configstoreTables.TableWebhookEndpoint, declaredNames map[string]bool, existing []configstoreTables.TableWebhookEndpoint) {
	existingByName := make(map[string]*configstoreTables.TableWebhookEndpoint, len(existing))
	existingByID := make(map[string]*configstoreTables.TableWebhookEndpoint, len(existing))
	for i := range existing {
		existingByName[existing[i].Name] = &existing[i]
		existingByID[existing[i].ID] = &existing[i]
	}

	keepIDs := make(map[string]bool, len(fileEndpoints))
	for _, endpoint := range fileEndpoints {
		// Resolve the match before anything that can fail, so failures keep
		// the existing row rather than exposing it to the prune below.
		match := existingByName[endpoint.Name]
		if match == nil && endpoint.ID != "" {
			match = existingByID[endpoint.ID]
		}
		if match != nil {
			keepIDs[match.ID] = true
		}

		fileHash, err := configstore.GenerateWebhookEndpointHash(endpoint)
		if err != nil {
			logger.Warn("failed to hash webhook endpoint %q: %v", endpoint.Name, err)
			continue
		}
		endpoint.ConfigHash = fileHash

		if match != nil {
			if match.ConfigHash == fileHash {
				continue
			}
			endpoint.ID = match.ID
			if err := config.ConfigStore.UpdateWebhookEndpoint(ctx, endpoint); err != nil {
				logger.Warn("failed to update webhook endpoint %q: %v", endpoint.Name, err)
			}
			continue
		}
		if endpoint.ID == "" {
			endpoint.ID = uuid.NewString()
		}
		if err := config.ConfigStore.CreateWebhookEndpoint(ctx, endpoint); err != nil {
			logger.Warn("failed to create webhook endpoint %q: %v", endpoint.Name, err)
			continue
		}
		keepIDs[endpoint.ID] = true
	}

	for i := range existing {
		if keepIDs[existing[i].ID] || declaredNames[existing[i].Name] {
			continue
		}
		if err := config.ConfigStore.DeleteWebhookEndpoint(ctx, existing[i].ID); err != nil {
			logger.Warn("failed to delete webhook endpoint %q: %v", existing[i].Name, err)
		}
	}
}

// loadGovernanceConfig loads and merges governance config from file
func loadGovernanceConfig(ctx context.Context, config *Config, configData *ConfigData) {
	if configData.Governance != nil {
		if err := resolveGovernanceKeyReferences(ctx, config, configData.Governance); err != nil {
			logger.Fatal("failed to resolve governance key references: %v", err)
		}
	}

	var governanceConfig *configstore.GovernanceConfig
	var err error
	// Checking from the store
	if config.ConfigStore != nil {
		logger.Debug("getting governance config from store")
		governanceConfig, err = config.ConfigStore.GetGovernanceConfig(ctx)
		if err != nil {
			logger.Warn("failed to get governance config from store: %v", err)
		}
	} else {
		logger.Debug("config.ConfigStore is nil, skipping store lookup")
	}
	// Merging config
	if governanceConfig != nil {
		config.GovernanceConfig = governanceConfig
	} else if configData.Governance != nil {
		// No governance config in store, merge config file against an empty snapshot
		// so first import follows the same ID normalization and persistence path.
		logger.Debug("no governance config found in store, processing from config file")
		governanceConfig = &configstore.GovernanceConfig{
			AuthConfig: configData.Governance.AuthConfig,
		}
		config.GovernanceConfig = governanceConfig
		// Pricing overrides are loaded into ModelCatalog after initFrameworkConfig,
		// once ModelCatalog is initialized.
	} else {
		logger.Debug("no governance config in store or config file")
	}

	if governanceConfig != nil && configData.Governance != nil {
		mergeGovernanceConfig(ctx, config, configData, governanceConfig)
	}
}

func resolveGovernanceKeyReferences(ctx context.Context, config *Config, governanceConfig *configstore.GovernanceConfig) error {
	if governanceConfig == nil {
		return nil
	}

	usesNameRefs := false
	for i := range governanceConfig.RoutingRules {
		for j := range governanceConfig.RoutingRules[i].Targets {
			target := &governanceConfig.RoutingRules[i].Targets[j]
			if target.ProviderKeyName != nil && strings.TrimSpace(*target.ProviderKeyName) != "" {
				usesNameRefs = true
				break
			}
		}
		if usesNameRefs {
			break
		}
	}
	if !usesNameRefs {
		for i := range governanceConfig.PricingOverrides {
			override := &governanceConfig.PricingOverrides[i]
			if override.ProviderKeyName != nil && strings.TrimSpace(*override.ProviderKeyName) != "" {
				usesNameRefs = true
				break
			}
		}
	}
	if !usesNameRefs {
		return nil
	}
	if config.ConfigStore == nil {
		return fmt.Errorf("provider_key_name references require config store for key lookup")
	}

	return config.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		resolveProviderKeyIDByProviderAndName := func(provider string, keyName string) (string, error) {
			var key configstoreTables.TableKey
			err := tx.WithContext(ctx).
				Model(&configstoreTables.TableKey{}).
				Select("key_id").
				Where("LOWER(provider) = LOWER(?) AND name = ?", provider, keyName).
				First(&key).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", fmt.Errorf("provider key not found for provider=%q name=%q", provider, keyName)
			}
			if err != nil {
				return "", err
			}
			return key.KeyID, nil
		}

		resolveProviderKeyIDByName := func(keyName string) (string, error) {
			var key configstoreTables.TableKey
			err := tx.WithContext(ctx).
				Model(&configstoreTables.TableKey{}).
				Select("key_id").
				Where("name = ?", keyName).
				First(&key).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", fmt.Errorf("provider key not found for name=%q", keyName)
			}
			if err != nil {
				return "", err
			}
			return key.KeyID, nil
		}

		for i := range governanceConfig.RoutingRules {
			for j := range governanceConfig.RoutingRules[i].Targets {
				target := &governanceConfig.RoutingRules[i].Targets[j]
				keyName := ""
				if target.ProviderKeyName != nil {
					keyName = strings.TrimSpace(*target.ProviderKeyName)
				}
				if keyName == "" {
					target.ProviderKeyName = nil
					continue
				}
				if target.KeyID != nil && strings.TrimSpace(*target.KeyID) != "" {
					return fmt.Errorf("routing rule %q target cannot set key_id together with provider_key_name", governanceConfig.RoutingRules[i].ID)
				}
				if target.Provider == nil || strings.TrimSpace(*target.Provider) == "" {
					return fmt.Errorf("routing rule %q target provider_key_name requires provider to be set", governanceConfig.RoutingRules[i].ID)
				}

				keyID, err := resolveProviderKeyIDByProviderAndName(*target.Provider, keyName)
				if err != nil {
					return fmt.Errorf("routing rule %q target provider_key_name resolution failed: %w", governanceConfig.RoutingRules[i].ID, err)
				}
				target.KeyID = bifrost.Ptr(keyID)
				target.ProviderKeyName = nil
			}
		}

		for i := range governanceConfig.PricingOverrides {
			override := &governanceConfig.PricingOverrides[i]
			if override.ProviderKeyName == nil {
				continue
			}

			keyName := strings.TrimSpace(*override.ProviderKeyName)
			if keyName == "" {
				override.ProviderKeyName = nil
				continue
			}
			if override.ProviderKeyID != nil && strings.TrimSpace(*override.ProviderKeyID) != "" {
				return fmt.Errorf("pricing override %q cannot set both provider_key_id and provider_key_name", override.ID)
			}

			keyID, err := resolveProviderKeyIDByName(keyName)
			if err != nil {
				return fmt.Errorf("pricing override %q provider_key_name resolution failed: %w", override.ID, err)
			}
			override.ProviderKeyID = bifrost.Ptr(keyID)
			override.ProviderKeyName = nil
		}

		return nil
	})
}

// entityIDSet builds a lookup set of IDs from an already-synced slice plus a
// slice of entries still pending insertion, so newly-added-in-this-file entities
// are valid scope_id targets even before they're persisted.
func entityIDSet[T any](existing []T, id func(T) string, toAdd []T) map[string]bool {
	set := make(map[string]bool, len(existing)+len(toAdd))
	for _, e := range existing {
		set[id(e)] = true
	}
	for _, e := range toAdd {
		set[id(e)] = true
	}
	return set
}

// mergeGovernanceConfig merges governance config from file with store
func mergeGovernanceConfig(ctx context.Context, config *Config, configData *ConfigData, governanceConfig *configstore.GovernanceConfig) {
	logger.Debug("merging governance config from config file with store")
	// When config.json is the source of truth, file-present entities must be
	// re-synced even when the stored ConfigHash still matches the file. The stored
	// hash is not updated on UI/API edits, so a hash match cannot prove the DB row
	// is unchanged; forcing the sync reverts UI drift back to the file values.
	forceFileSync := configData.isConfigJSONSourceOfTruth()
	// Merge Budgets by ID with hash comparison
	budgetsToAdd := make([]configstoreTables.TableBudget, 0)
	budgetsToUpdate := make([]configstoreTables.TableBudget, 0)
	for i, newBudget := range configData.Governance.Budgets {
		fileBudgetHash, err := configstore.GenerateBudgetHash(newBudget)
		if err != nil {
			logger.Warn("failed to generate budget hash for %s: %v", newBudget.ID, err)
			continue
		}
		configData.Governance.Budgets[i].ConfigHash = fileBudgetHash
		// Replacing budgets
		found := false
		for j, existingBudget := range governanceConfig.Budgets {
			if existingBudget.ID == newBudget.ID {
				found = true
				if forceFileSync || existingBudget.ConfigHash != fileBudgetHash {
					logger.Debug("config hash mismatch for budget %s, syncing from config file", newBudget.ID)
					configData.Governance.Budgets[i].ConfigHash = fileBudgetHash
					budgetsToUpdate = append(budgetsToUpdate, configData.Governance.Budgets[i])
					governanceConfig.Budgets[j] = configData.Governance.Budgets[i]
				} else {
					logger.Debug("config hash matches for budget %s, keeping DB config", newBudget.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.Budgets[i].ConfigHash = fileBudgetHash
			budgetsToAdd = append(budgetsToAdd, configData.Governance.Budgets[i])
		}
	}
	// Merge RateLimits by ID with hash comparison
	rateLimitsToAdd := make([]configstoreTables.TableRateLimit, 0)
	rateLimitsToUpdate := make([]configstoreTables.TableRateLimit, 0)
	for i, newRateLimit := range configData.Governance.RateLimits {
		fileRLHash, err := configstore.GenerateRateLimitHash(newRateLimit)
		if err != nil {
			logger.Warn("failed to generate rate limit hash for %s: %v", newRateLimit.ID, err)
			continue
		}
		configData.Governance.RateLimits[i].ConfigHash = fileRLHash

		found := false
		for j, existingRateLimit := range governanceConfig.RateLimits {
			if existingRateLimit.ID == newRateLimit.ID {
				found = true
				if forceFileSync || existingRateLimit.ConfigHash != fileRLHash {
					logger.Debug("config hash mismatch for rate limit %s, syncing from config file", newRateLimit.ID)
					configData.Governance.RateLimits[i].ConfigHash = fileRLHash
					rateLimitsToUpdate = append(rateLimitsToUpdate, configData.Governance.RateLimits[i])
					governanceConfig.RateLimits[j] = configData.Governance.RateLimits[i]
				} else {
					logger.Debug("config hash matches for rate limit %s, keeping DB config", newRateLimit.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.RateLimits[i].ConfigHash = fileRLHash
			rateLimitsToAdd = append(rateLimitsToAdd, configData.Governance.RateLimits[i])
		}
	}
	// Merge Customers by ID with hash comparison
	customersToAdd := make([]configstoreTables.TableCustomer, 0)
	customersToUpdate := make([]configstoreTables.TableCustomer, 0)
	for i, newCustomer := range configData.Governance.Customers {
		fileCustomerHash, err := configstore.GenerateCustomerHash(newCustomer)
		if err != nil {
			logger.Warn("failed to generate customer hash for %s: %v", newCustomer.ID, err)
			continue
		}
		configData.Governance.Customers[i].ConfigHash = fileCustomerHash

		found := false
		for j, existingCustomer := range governanceConfig.Customers {
			idMatch := existingCustomer.ID == newCustomer.ID
			nameMatch := newCustomer.ID == "" && existingCustomer.Name == newCustomer.Name
			if idMatch || nameMatch {
				if nameMatch {
					// Config file has no ID; adopt the DB record's ID so updates use the right primary key.
					configData.Governance.Customers[i].ID = existingCustomer.ID
				}
				found = true
				if forceFileSync || existingCustomer.ConfigHash != fileCustomerHash {
					logger.Debug("config hash mismatch for customer %s, syncing from config file", newCustomer.ID)
					configData.Governance.Customers[i].ConfigHash = fileCustomerHash
					customersToUpdate = append(customersToUpdate, configData.Governance.Customers[i])
					governanceConfig.Customers[j] = configData.Governance.Customers[i]
				} else {
					logger.Debug("config hash matches for customer %s, keeping DB config", newCustomer.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.Customers[i].ConfigHash = fileCustomerHash
			if configData.Governance.Customers[i].ID == "" {
				configData.Governance.Customers[i].ID = uuid.NewString()
			}
			customersToAdd = append(customersToAdd, configData.Governance.Customers[i])
		}
	}
	// Merge Teams by ID with hash comparison
	teamsToAdd := make([]configstoreTables.TableTeam, 0)
	teamsToUpdate := make([]configstoreTables.TableTeam, 0)
	for i, newTeam := range configData.Governance.Teams {
		fileTeamHash, err := configstore.GenerateTeamHash(newTeam)
		if err != nil {
			logger.Warn("failed to generate team hash for %s: %v", newTeam.ID, err)
			continue
		}
		configData.Governance.Teams[i].ConfigHash = fileTeamHash

		found := false
		for j, existingTeam := range governanceConfig.Teams {
			idMatch := existingTeam.ID == newTeam.ID
			nameMatch := newTeam.ID == "" && existingTeam.Name == newTeam.Name
			if idMatch || nameMatch {
				if nameMatch {
					// Config file has no ID; adopt the DB record's ID so updates use the right primary key.
					configData.Governance.Teams[i].ID = existingTeam.ID
				}
				found = true
				if forceFileSync || existingTeam.ConfigHash != fileTeamHash {
					logger.Debug("config hash mismatch for team %s, syncing from config file", newTeam.ID)
					configData.Governance.Teams[i].ConfigHash = fileTeamHash
					teamsToUpdate = append(teamsToUpdate, configData.Governance.Teams[i])
					governanceConfig.Teams[j] = configData.Governance.Teams[i]
				} else {
					logger.Debug("config hash matches for team %s, keeping DB config", newTeam.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.Teams[i].ConfigHash = fileTeamHash
			if configData.Governance.Teams[i].ID == "" {
				configData.Governance.Teams[i].ID = uuid.NewString()
			}
			teamsToAdd = append(teamsToAdd, configData.Governance.Teams[i])
		}
	}
	// Merge VirtualKeys by ID with hash comparison
	virtualKeysToAdd := make([]configstoreTables.TableVirtualKey, 0)
	virtualKeysToUpdate := make([]configstoreTables.TableVirtualKey, 0)
	// skippedNewVirtualKeyIDs tracks brand-new VKs whose secret-backed Value failed
	// to resolve: an ID is assigned before that check runs, but the entry is never
	// added to virtualKeysToAdd, so it never gets a DB row. Existing VKs whose
	// update was skipped for the same reason aren't tracked here — their prior DB
	// row is untouched and still valid, so their ID stays a legitimate reference.
	skippedNewVirtualKeyIDs := make(map[string]bool)
	for i, newVirtualKey := range configData.Governance.VirtualKeys {
		fileVKHash, err := configstore.GenerateVirtualKeyHash(newVirtualKey)
		if err != nil {
			logger.Warn("failed to generate virtual key hash for %s: %v", newVirtualKey.ID, err)
			continue
		}
		configData.Governance.VirtualKeys[i].ConfigHash = fileVKHash
		// Preparing hash
		found := false
		for j, existingVirtualKey := range governanceConfig.VirtualKeys {
			idMatch := existingVirtualKey.ID == newVirtualKey.ID
			nameMatch := newVirtualKey.ID == "" && existingVirtualKey.Name == newVirtualKey.Name
			if idMatch || nameMatch {
				if nameMatch {
					// Config file has no ID; adopt the DB record's ID so updates use the right primary key.
					configData.Governance.VirtualKeys[i].ID = existingVirtualKey.ID
				}
				found = true
				if forceFileSync || existingVirtualKey.ConfigHash != fileVKHash {
					logger.Debug("config hash mismatch for virtual key %s, syncing from config file", existingVirtualKey.ID)
					configData.Governance.VirtualKeys[i].ConfigHash = fileVKHash
					// Preserve stored value when config doesn't supply one
					if configData.Governance.VirtualKeys[i].Value.ShouldPreserveStored() && existingVirtualKey.Value.IsSet() {
						configData.Governance.VirtualKeys[i].Value = existingVirtualKey.Value
					}
					resolvedVal := configData.Governance.VirtualKeys[i].Value.GetValue()
					if resolvedVal == "" && configData.Governance.VirtualKeys[i].Value.IsFromSecret() {
						logger.Warn("virtual key %s: env/vault ref %q could not be resolved, skipping update", newVirtualKey.ID, configData.Governance.VirtualKeys[i].Value.GetRawRef())
						break
					}
					if !strings.HasPrefix(resolvedVal, governance.VirtualKeyPrefix) {
						logger.Warn("virtual key %s has a value in the config file that does not have %s prefix. We are generating a new one for you.", newVirtualKey.ID, governance.VirtualKeyPrefix)
						configData.Governance.VirtualKeys[i].Value = *schemas.NewSecretVar(governance.GenerateVirtualKey())
					}
					// Resolve MCP client names to IDs for config file mcp_configs
					configData.Governance.VirtualKeys[i].MCPConfigs = resolveMCPConfigClientIDs(
						ctx, config.ConfigStore, configData.Governance.VirtualKeys[i].MCPConfigs, newVirtualKey.ID)
					virtualKeysToUpdate = append(virtualKeysToUpdate, configData.Governance.VirtualKeys[i])
					governanceConfig.VirtualKeys[j] = configData.Governance.VirtualKeys[i]
				} else {
					logger.Debug("config hash matches for virtual key %s, keeping DB config", newVirtualKey.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.VirtualKeys[i].ConfigHash = fileVKHash
			if configData.Governance.VirtualKeys[i].ID == "" {
				configData.Governance.VirtualKeys[i].ID = uuid.NewString()
			}
			resolvedVal := configData.Governance.VirtualKeys[i].Value.GetValue()
			if resolvedVal == "" && configData.Governance.VirtualKeys[i].Value.IsFromSecret() {
				logger.Warn("virtual key %s: env/vault ref %q could not be resolved, skipping", newVirtualKey.ID, configData.Governance.VirtualKeys[i].Value.GetRawRef())
				skippedNewVirtualKeyIDs[configData.Governance.VirtualKeys[i].ID] = true
				continue
			}
			if !strings.HasPrefix(resolvedVal, governance.VirtualKeyPrefix) {
				logger.Warn("virtual key %s has a value in the config file that does not have %s prefix. We are generating a new one for you.", newVirtualKey.ID, governance.VirtualKeyPrefix)
				configData.Governance.VirtualKeys[i].Value = *schemas.NewSecretVar(governance.GenerateVirtualKey())
			}
			// Resolve MCP client names to IDs for config file mcp_configs
			configData.Governance.VirtualKeys[i].MCPConfigs = resolveMCPConfigClientIDs(
				ctx, config.ConfigStore, configData.Governance.VirtualKeys[i].MCPConfigs, newVirtualKey.ID)
			virtualKeysToAdd = append(virtualKeysToAdd, configData.Governance.VirtualKeys[i])
		}
	}
	// Build the set of entity IDs each non-global scope may reference, so a routing
	// rule whose scope_id doesn't resolve (e.g. a name typed in place of an ID) is
	// rejected here instead of silently matching zero requests at eval time.
	//
	// Under source_of_truth=config.json, entities absent from the file are deleted
	// by the later prune step, so a currently-persisted entity that isn't in the
	// file is NOT a valid target — only file-declared IDs survive. Otherwise
	// (default merge mode, or the section is simply absent from this file) anything
	// already persisted, or being added by this same file, remains valid.
	teamScopeIDs := entityIDSet(governanceConfig.Teams, func(t configstoreTables.TableTeam) string { return t.ID }, teamsToAdd)
	if configData.isConfigJSONSourceOfTruth() && configData.governanceSectionPresent("teams") {
		teamScopeIDs = entityIDSet(configData.Governance.Teams, func(t configstoreTables.TableTeam) string { return t.ID }, nil)
	}
	customerScopeIDs := entityIDSet(governanceConfig.Customers, func(c configstoreTables.TableCustomer) string { return c.ID }, customersToAdd)
	if configData.isConfigJSONSourceOfTruth() && configData.governanceSectionPresent("customers") {
		customerScopeIDs = entityIDSet(configData.Governance.Customers, func(c configstoreTables.TableCustomer) string { return c.ID }, nil)
	}
	vkScopeIDs := entityIDSet(governanceConfig.VirtualKeys, func(vk configstoreTables.TableVirtualKey) string { return vk.ID }, virtualKeysToAdd)
	if configData.isConfigJSONSourceOfTruth() && configData.governanceSectionPresent("virtual_keys") {
		// Exclude brand-new VKs that were skipped from persistence (unresolved
		// secret ref): their ID is present in the raw file slice but they never
		// got a DB row, so counting them here would let a routing rule pass
		// sanitization while pointing at a virtual key that doesn't exist.
		vkScopeIDs = make(map[string]bool, len(configData.Governance.VirtualKeys))
		for _, vk := range configData.Governance.VirtualKeys {
			if skippedNewVirtualKeyIDs[vk.ID] {
				continue
			}
			vkScopeIDs[vk.ID] = true
		}
	}
	validRoutingScopeIDs := map[string]map[string]bool{
		"team":        teamScopeIDs,
		"customer":    customerScopeIDs,
		"virtual_key": vkScopeIDs,
	}

	// Normalize an omitted/empty scope to the canonical "global" value, matching
	// the HTTP API's write-time behavior (empty scope defaults to global there).
	// Without this, a config.json rule persists with Scope="" verbatim: the
	// routing engine's cache key is built from the literal scope string, and only
	// the literal "global" is ever looked up, so an unnormalized "" rule would
	// silently match zero requests — the same failure mode this validation exists
	// to prevent, via a different field. Also clear any stray scope_id once scope
	// is (or becomes) global, so it can never carry a dangling reference.
	for i := range configData.Governance.RoutingRules {
		if configData.Governance.RoutingRules[i].Scope == "" {
			configData.Governance.RoutingRules[i].Scope = "global"
		}
		if configData.Governance.RoutingRules[i].Scope == "global" {
			configData.Governance.RoutingRules[i].ScopeID = nil
		}
	}

	// Sanitize routing rules with an unresolvable scope_id BEFORE the merge loop
	// and before pruneGovernanceConfigToFile treats configData.Governance.RoutingRules
	// as the new authoritative snapshot (it both computes the prune keep-set from it
	// and assigns it verbatim to config.GovernanceConfig.RoutingRules). A brand-new
	// invalid rule is removed outright; an invalid edit to an existing rule falls back
	// to the last persisted version ONLY if that version's own scope target also
	// survives this sync — otherwise it's removed too, so the in-memory snapshot and
	// the prune keep-set never reflect a rule that was rejected, or one that would be
	// left dangling by the very entity deletions this same sync is about to perform.
	// routingScopeResolves reports whether rule's own scope target will still exist
	// after this sync (used both to reject an incoming file rule and to make sure
	// a fallback-to-persisted candidate isn't itself pointing at an about-to-be-pruned
	// entity — restoring a rule whose own scope_id won't survive just reproduces the
	// same dangling-rule bug one level down).
	routingScopeResolves := func(rule configstoreTables.TableRoutingRule) bool {
		if rule.Scope == "" || rule.Scope == "global" {
			return true
		}
		if rule.ScopeID == nil || *rule.ScopeID == "" {
			return false
		}
		return validRoutingScopeIDs[rule.Scope][*rule.ScopeID]
	}

	existingRoutingRulesByID := make(map[string]configstoreTables.TableRoutingRule, len(governanceConfig.RoutingRules))
	for _, r := range governanceConfig.RoutingRules {
		existingRoutingRulesByID[r.ID] = r
	}
	sanitizedRoutingRules := make([]configstoreTables.TableRoutingRule, 0, len(configData.Governance.RoutingRules))
	for _, rule := range configData.Governance.RoutingRules {
		if !routingScopeResolves(rule) {
			scopeID := ""
			if rule.ScopeID != nil {
				scopeID = *rule.ScopeID
			}
			if existing, ok := existingRoutingRulesByID[rule.ID]; ok && routingScopeResolves(existing) {
				logger.Warn("routing rule %s: scope_id %q does not resolve to an existing %s (use the entity's id, not its name); keeping last persisted version", rule.ID, scopeID, rule.Scope)
				sanitizedRoutingRules = append(sanitizedRoutingRules, existing)
			} else {
				logger.Warn("routing rule %s: scope_id %q does not resolve to an existing %s (use the entity's id, not its name); removing rule", rule.ID, scopeID, rule.Scope)
			}
			continue
		}
		sanitizedRoutingRules = append(sanitizedRoutingRules, rule)
	}
	configData.Governance.RoutingRules = sanitizedRoutingRules

	// Merge RoutingRules by ID with hash comparison
	routingRulesToAdd := make([]configstoreTables.TableRoutingRule, 0)
	routingRulesToUpdate := make([]configstoreTables.TableRoutingRule, 0)
	for i, newRoutingRule := range configData.Governance.RoutingRules {
		fileRoutingRuleHash, err := configstore.GenerateRoutingRuleHash(newRoutingRule)
		if err != nil {
			logger.Warn("failed to generate routing rule hash for %s: %v", newRoutingRule.ID, err)
			continue
		}
		configData.Governance.RoutingRules[i].ConfigHash = fileRoutingRuleHash

		found := false
		for j, existingRoutingRule := range governanceConfig.RoutingRules {
			if existingRoutingRule.ID == newRoutingRule.ID {
				found = true
				if forceFileSync || existingRoutingRule.ConfigHash != fileRoutingRuleHash {
					logger.Debug("config hash mismatch for routing rule %s, syncing from config file", newRoutingRule.ID)
					configData.Governance.RoutingRules[i].ConfigHash = fileRoutingRuleHash
					routingRulesToUpdate = append(routingRulesToUpdate, configData.Governance.RoutingRules[i])
					governanceConfig.RoutingRules[j] = configData.Governance.RoutingRules[i]
				} else {
					logger.Debug("config hash matches for routing rule %s, keeping DB config", newRoutingRule.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.RoutingRules[i].ConfigHash = fileRoutingRuleHash
			routingRulesToAdd = append(routingRulesToAdd, configData.Governance.RoutingRules[i])
		}
	}
	// Merge PricingOverrides by ID with hash comparison
	pricingOverridesToAdd := make([]configstoreTables.TablePricingOverride, 0)
	pricingOverridesToUpdate := make([]configstoreTables.TablePricingOverride, 0)
	for i, newOverride := range configData.Governance.PricingOverrides {
		if len(newOverride.RequestTypes) > 0 {
			b, err := json.Marshal(newOverride.RequestTypes)
			if err != nil {
				logger.Warn("failed to serialize request_types for pricing override %s: %v", newOverride.ID, err)
				continue
			}
			configData.Governance.PricingOverrides[i].RequestTypesJSON = string(b)
		} else {
			configData.Governance.PricingOverrides[i].RequestTypesJSON = "[]"
		}
		fileHash, err := configstore.GeneratePricingOverrideHash(configData.Governance.PricingOverrides[i])
		if err != nil {
			logger.Warn("failed to generate pricing override hash for %s: %v", newOverride.ID, err)
			continue
		}
		configData.Governance.PricingOverrides[i].ConfigHash = fileHash

		found := false
		for j, existing := range governanceConfig.PricingOverrides {
			if existing.ID == newOverride.ID {
				found = true
				if forceFileSync || existing.ConfigHash != fileHash {
					logger.Debug("config hash mismatch for pricing override %s, syncing from config file", newOverride.ID)
					pricingOverridesToUpdate = append(pricingOverridesToUpdate, configData.Governance.PricingOverrides[i])
					governanceConfig.PricingOverrides[j] = configData.Governance.PricingOverrides[i]
				} else {
					logger.Debug("config hash matches for pricing override %s, keeping DB config", newOverride.ID)
				}
				break
			}
		}
		if !found {
			pricingOverridesToAdd = append(pricingOverridesToAdd, configData.Governance.PricingOverrides[i])
		}
	}
	// Merge ModelConfigs by ID (governance model-level budget/rate-limit bindings)
	modelConfigsToAdd := make([]configstoreTables.TableModelConfig, 0)
	modelConfigsToUpdate := make([]configstoreTables.TableModelConfig, 0)
	for i, newModelConfig := range configData.Governance.ModelConfigs {
		fileModelConfigHash, err := configstore.GenerateModelConfigHash(newModelConfig)
		if err != nil {
			logger.Warn("failed to generate model config hash for %s: %v", newModelConfig.ID, err)
			continue
		}
		configData.Governance.ModelConfigs[i].ConfigHash = fileModelConfigHash

		found := false
		for j, existingModelConfig := range governanceConfig.ModelConfigs {
			if existingModelConfig.ID == newModelConfig.ID {
				found = true
				if forceFileSync || existingModelConfig.ConfigHash != fileModelConfigHash {
					logger.Debug("config hash mismatch for model config %s, syncing from config file", newModelConfig.ID)
					modelConfigsToUpdate = append(modelConfigsToUpdate, configData.Governance.ModelConfigs[i])
					governanceConfig.ModelConfigs[j] = configData.Governance.ModelConfigs[i]
				} else {
					logger.Debug("config hash matches for model config %s, keeping DB config", newModelConfig.ID)
				}
				break
			}
		}
		if !found {
			modelConfigsToAdd = append(modelConfigsToAdd, configData.Governance.ModelConfigs[i])
		}
	}
	// Merge provider governance bindings by provider name.
	providersToAdd := make([]configstoreTables.TableProvider, 0)
	providersToUpdate := make([]configstoreTables.TableProvider, 0)
	for i, newProvider := range configData.Governance.Providers {
		fileProviderGovHash, err := configstore.GenerateProviderGovernanceHash(newProvider)
		if err != nil {
			logger.Warn("failed to generate provider governance hash for %s: %v", newProvider.Name, err)
			continue
		}
		found := false
		for j, existingProvider := range governanceConfig.Providers {
			if existingProvider.Name == newProvider.Name {
				found = true
				existingProviderGovHash, err := configstore.GenerateProviderGovernanceHash(existingProvider)
				if err != nil {
					logger.Warn("failed to generate existing provider governance hash for %s: %v", existingProvider.Name, err)
					existingProviderGovHash = ""
				}
				if existingProviderGovHash != fileProviderGovHash {
					logger.Debug("config hash mismatch for provider governance %s, syncing from config file", newProvider.Name)
					providersToUpdate = append(providersToUpdate, configData.Governance.Providers[i])
					governanceConfig.Providers[j] = configData.Governance.Providers[i]
				} else {
					logger.Debug("config hash matches for provider governance %s, keeping DB config", newProvider.Name)
				}
				break
			}
		}
		if !found {
			providersToAdd = append(providersToAdd, configData.Governance.Providers[i])
		}
	}
	// Add merged items to config
	config.GovernanceConfig.Budgets = append(governanceConfig.Budgets, budgetsToAdd...)
	config.GovernanceConfig.RateLimits = append(governanceConfig.RateLimits, rateLimitsToAdd...)
	config.GovernanceConfig.Customers = append(governanceConfig.Customers, customersToAdd...)
	config.GovernanceConfig.Teams = append(governanceConfig.Teams, teamsToAdd...)
	config.GovernanceConfig.VirtualKeys = append(governanceConfig.VirtualKeys, virtualKeysToAdd...)
	config.GovernanceConfig.RoutingRules = append(governanceConfig.RoutingRules, routingRulesToAdd...)
	config.GovernanceConfig.PricingOverrides = append(governanceConfig.PricingOverrides, pricingOverridesToAdd...)
	config.GovernanceConfig.ModelConfigs = append(governanceConfig.ModelConfigs, modelConfigsToAdd...)
	config.GovernanceConfig.Providers = append(governanceConfig.Providers, providersToAdd...)
	complexityAnalyzerConfigToUpdate := planComplexityAnalyzerConfigUpdate(config, configData)
	// Update store with merged config items
	hasChanges := len(budgetsToAdd) > 0 || len(budgetsToUpdate) > 0 ||
		len(rateLimitsToAdd) > 0 || len(rateLimitsToUpdate) > 0 ||
		len(customersToAdd) > 0 || len(customersToUpdate) > 0 ||
		len(teamsToAdd) > 0 || len(teamsToUpdate) > 0 ||
		len(virtualKeysToAdd) > 0 || len(virtualKeysToUpdate) > 0 ||
		len(routingRulesToAdd) > 0 || len(routingRulesToUpdate) > 0 ||
		len(pricingOverridesToAdd) > 0 || len(pricingOverridesToUpdate) > 0 ||
		len(modelConfigsToAdd) > 0 || len(modelConfigsToUpdate) > 0 ||
		len(providersToAdd) > 0 || len(providersToUpdate) > 0 ||
		complexityAnalyzerConfigToUpdate != nil
	if config.ConfigStore == nil && complexityAnalyzerConfigToUpdate != nil {
		config.GovernanceConfig.ComplexityAnalyzerConfig = complexityAnalyzerConfigToUpdate
	}
	if config.ConfigStore != nil && hasChanges {
		err := updateGovernanceConfigInStore(ctx, config,
			budgetsToAdd, budgetsToUpdate,
			rateLimitsToAdd, rateLimitsToUpdate,
			customersToAdd, customersToUpdate,
			teamsToAdd, teamsToUpdate,
			virtualKeysToAdd, virtualKeysToUpdate,
			routingRulesToAdd, routingRulesToUpdate,
			pricingOverridesToAdd, pricingOverridesToUpdate,
			modelConfigsToAdd, modelConfigsToUpdate,
			providersToAdd, providersToUpdate,
			complexityAnalyzerConfigToUpdate)
		if err != nil {
			logger.Fatal("failed to sync governance config: %v", err)
		}
	}

	// Sync pricing overrides into the model catalog in one batch to avoid
	// rebuilding the lookup map on every iteration.
	if config.ModelCatalog != nil {
		rows := make([]*configstoreTables.TablePricingOverride, 0, len(pricingOverridesToAdd)+len(pricingOverridesToUpdate))
		for i := range pricingOverridesToAdd {
			rows = append(rows, &pricingOverridesToAdd[i])
		}
		for i := range pricingOverridesToUpdate {
			rows = append(rows, &pricingOverridesToUpdate[i])
		}
		if len(rows) > 0 {
			if err := config.ModelCatalog.UpsertPricingOverrides(rows...); err != nil {
				logger.Error("failed to upsert pricing overrides into model catalog: %v", err)
			}
		}
	}
	if configData.isConfigJSONSourceOfTruth() {
		pruneGovernanceConfigToFile(ctx, config, configData)
	}
}

// planComplexityAnalyzerConfigUpdate applies config.json complexity analyzer
// reconciliation rules and returns the next singleton config when it should be
// persisted. Store writes happen in updateGovernanceConfigInStore with the rest
// of the planned governance updates.
//
// In split mode, unchanged section hashes preserve runtime UI/API edits. When a
// section changes in config.json, tier boundaries are replaced and keyword lists
// are merged additively with the stored runtime lists.
func planComplexityAnalyzerConfigUpdate(config *Config, configData *ConfigData) *configstore.ComplexityAnalyzerConfig {
	if config == nil || configData == nil || configData.Governance == nil || configData.Governance.ComplexityAnalyzerConfig == nil {
		return nil
	}
	if config.GovernanceConfig == nil {
		config.GovernanceConfig = &configstore.GovernanceConfig{}
	}

	fileConfig, fileHashes, ok := complexityAnalyzerConfigFromFile(configData)
	if !ok {
		return nil
	}
	current := config.GovernanceConfig.ComplexityAnalyzerConfig
	if configData.isConfigJSONSourceOfTruth() {
		if current != nil && reflect.DeepEqual(current, fileConfig) {
			return nil
		}
		return fileConfig
	}
	if current != nil && current.ConfigHashes.Equal(fileHashes) {
		logger.Debug("complexity analyzer config section hashes match, keeping DB config")
		return nil
	}

	merged, err := mergeComplexityAnalyzerConfigFromFile(current, fileConfig)
	if err != nil {
		logger.Warn("failed to merge complexity analyzer config from config file: %v", err)
		return nil
	}
	if current != nil && reflect.DeepEqual(current, merged) {
		return nil
	}
	return merged
}

// complexityAnalyzerConfigFromFile validates the file-backed analyzer config and
// returns the canonical section hashes used to detect whether config.json changed.
func complexityAnalyzerConfigFromFile(configData *ConfigData) (*configstore.ComplexityAnalyzerConfig, configstore.ComplexityAnalyzerConfigHashes, bool) {
	fileConfig, err := complexity.ValidateAndNormalize(configData.Governance.ComplexityAnalyzerConfig)
	if err != nil {
		logger.Error("invalid complexity analyzer config in config file: %v", err)
		return nil, configstore.ComplexityAnalyzerConfigHashes{}, false
	}
	fileHashes, err := configstore.GenerateComplexityAnalyzerConfigHashes(fileConfig)
	if err != nil {
		logger.Warn("failed to generate complexity analyzer config hashes: %v", err)
		return nil, configstore.ComplexityAnalyzerConfigHashes{}, false
	}
	fileConfig.ConfigHashes = fileHashes
	return fileConfig, fileHashes, true
}

// mergeComplexityAnalyzerConfigFromFile uses defaults as the first split-mode
// base so config.json seeds do not erase built-in keyword coverage.
func mergeComplexityAnalyzerConfigFromFile(current, fileConfig *configstore.ComplexityAnalyzerConfig) (*configstore.ComplexityAnalyzerConfig, error) {
	base := current
	if base == nil {
		defaults := complexity.DefaultAnalyzerConfig()
		base = &defaults
	}
	return configstore.MergeComplexityAnalyzerConfigByHashes(base, fileConfig)
}

// pruneGovernanceConfigToFile removes DB-only governance rows for file-present collections.
func pruneGovernanceConfigToFile(ctx context.Context, config *Config, configData *ConfigData) {
	if config.ConfigStore == nil || config.GovernanceConfig == nil || configData.Governance == nil {
		return
	}
	logger.Debug("source_of_truth=config.json: pruning governance rows not present in config file")
	err := config.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		if configData.governanceSectionPresent("virtual_keys") {
			keep := make(map[string]bool, len(configData.Governance.VirtualKeys))
			for i := range configData.Governance.VirtualKeys {
				vk := &configData.Governance.VirtualKeys[i]
				keep[vk.ID] = true
				// Unchanged VKs never went through resolveMCPConfigClientIDs in
				// mergeGovernanceConfig, so their MCPConfigs may still carry
				// mcp_client_name with MCPClientID==0. Resolve before reconciling
				// to avoid creating/deleting client-id 0 associations.
				vk.MCPConfigs = resolveMCPConfigClientIDs(ctx, config.ConfigStore, vk.MCPConfigs, vk.ID)
				if err := reconcileVirtualKeyAssociations(ctx, config.ConfigStore, tx, vk.ID, vk.ProviderConfigs, vk.MCPConfigs); err != nil {
					return fmt.Errorf("failed to reconcile associations for virtual key %s: %w", vk.ID, err)
				}
			}
			for _, existing := range config.GovernanceConfig.VirtualKeys {
				if existing.ID != "" && !keep[existing.ID] {
					if err := config.ConfigStore.DeleteVirtualKey(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete virtual key %s: %w", existing.ID, err)
					}
				}
			}
			config.GovernanceConfig.VirtualKeys = configData.Governance.VirtualKeys
		}
		if configData.governanceSectionPresent("routing_rules") {
			keep := make(map[string]bool, len(configData.Governance.RoutingRules))
			for _, row := range configData.Governance.RoutingRules {
				keep[row.ID] = true
			}
			for _, existing := range config.GovernanceConfig.RoutingRules {
				if existing.ID != "" && !keep[existing.ID] {
					if err := config.ConfigStore.DeleteRoutingRule(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete routing rule %s: %w", existing.ID, err)
					}
				}
			}
			config.GovernanceConfig.RoutingRules = configData.Governance.RoutingRules
		}
		if configData.governanceSectionPresent("pricing_overrides") {
			keep := make(map[string]bool, len(configData.Governance.PricingOverrides))
			for _, row := range configData.Governance.PricingOverrides {
				keep[row.ID] = true
			}
			for _, existing := range config.GovernanceConfig.PricingOverrides {
				if existing.ID != "" && !keep[existing.ID] {
					if err := config.ConfigStore.DeletePricingOverride(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete pricing override %s: %w", existing.ID, err)
					}
				}
			}
			config.GovernanceConfig.PricingOverrides = configData.Governance.PricingOverrides
		}
		if configData.governanceSectionPresent("model_configs") {
			keep := make(map[string]bool, len(configData.Governance.ModelConfigs))
			for _, row := range configData.Governance.ModelConfigs {
				keep[row.ID] = true
			}
			for _, existing := range config.GovernanceConfig.ModelConfigs {
				if existing.ID != "" && !keep[existing.ID] {
					if err := config.ConfigStore.DeleteModelConfig(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete model config %s: %w", existing.ID, err)
					}
				}
			}
			config.GovernanceConfig.ModelConfigs = configData.Governance.ModelConfigs
		}
		if configData.governanceSectionPresent("teams") {
			keep := make(map[string]bool, len(configData.Governance.Teams))
			for _, row := range configData.Governance.Teams {
				keep[row.ID] = true
			}
			for _, existing := range config.GovernanceConfig.Teams {
				if existing.ID != "" && !keep[existing.ID] {
					if err := config.ConfigStore.DeleteTeam(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete team %s: %w", existing.ID, err)
					}
				}
			}
			config.GovernanceConfig.Teams = configData.Governance.Teams
		}
		if configData.governanceSectionPresent("customers") {
			keep := make(map[string]bool, len(configData.Governance.Customers))
			for _, row := range configData.Governance.Customers {
				keep[row.ID] = true
			}
			for _, existing := range config.GovernanceConfig.Customers {
				if existing.ID != "" && !keep[existing.ID] {
					if err := config.ConfigStore.DeleteCustomer(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete customer %s: %w", existing.ID, err)
					}
				}
			}
			config.GovernanceConfig.Customers = configData.Governance.Customers
		}
		if configData.governanceSectionPresent("providers") {
			keep := make(map[string]configstoreTables.TableProvider, len(configData.Governance.Providers))
			for _, row := range configData.Governance.Providers {
				keep[row.Name] = row
			}
			for _, existing := range config.GovernanceConfig.Providers {
				if existing.Name == "" || keep[existing.Name].Name != "" {
					continue
				}
				if err := tx.Model(&configstoreTables.TableProvider{}).
					Where("name = ?", existing.Name).
					Select("budget_id", "rate_limit_id").
					Updates(map[string]interface{}{"budget_id": nil, "rate_limit_id": nil}).Error; err != nil {
					return fmt.Errorf("failed to clear provider governance mapping for %s: %w", existing.Name, err)
				}
			}
			config.GovernanceConfig.Providers = configData.Governance.Providers
		}
		if configData.governanceSectionPresent("budgets") {
			keep := make(map[string]bool, len(configData.Governance.Budgets))
			for _, row := range configData.Governance.Budgets {
				keep[row.ID] = true
			}
			for _, existing := range config.GovernanceConfig.Budgets {
				if existing.ID != "" && !keep[existing.ID] {
					if err := config.ConfigStore.DeleteBudget(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete budget %s: %w", existing.ID, err)
					}
				}
			}
			config.GovernanceConfig.Budgets = configData.Governance.Budgets
		}
		if configData.governanceSectionPresent("rate_limits") {
			keep := make(map[string]bool, len(configData.Governance.RateLimits))
			for _, row := range configData.Governance.RateLimits {
				keep[row.ID] = true
			}
			for _, existing := range config.GovernanceConfig.RateLimits {
				if existing.ID != "" && !keep[existing.ID] {
					if err := config.ConfigStore.DeleteRateLimit(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete rate limit %s: %w", existing.ID, err)
					}
				}
			}
			config.GovernanceConfig.RateLimits = configData.Governance.RateLimits
		}
		return nil
	})
	if err != nil {
		logger.Fatal("failed to prune governance config: %v", err)
	}
}

// updateGovernanceConfigInStore updates governance config items in the store
func updateGovernanceConfigInStore(
	ctx context.Context,
	config *Config,
	budgetsToAdd []configstoreTables.TableBudget,
	budgetsToUpdate []configstoreTables.TableBudget,
	rateLimitsToAdd []configstoreTables.TableRateLimit,
	rateLimitsToUpdate []configstoreTables.TableRateLimit,
	customersToAdd []configstoreTables.TableCustomer,
	customersToUpdate []configstoreTables.TableCustomer,
	teamsToAdd []configstoreTables.TableTeam,
	teamsToUpdate []configstoreTables.TableTeam,
	virtualKeysToAdd []configstoreTables.TableVirtualKey,
	virtualKeysToUpdate []configstoreTables.TableVirtualKey,
	routingRulesToAdd []configstoreTables.TableRoutingRule,
	routingRulesToUpdate []configstoreTables.TableRoutingRule,
	pricingOverridesToAdd []configstoreTables.TablePricingOverride,
	pricingOverridesToUpdate []configstoreTables.TablePricingOverride,
	modelConfigsToAdd []configstoreTables.TableModelConfig,
	modelConfigsToUpdate []configstoreTables.TableModelConfig,
	providersToAdd []configstoreTables.TableProvider,
	providersToUpdate []configstoreTables.TableProvider,
	complexityAnalyzerConfigToUpdate *configstore.ComplexityAnalyzerConfig,
) error {
	logger.Debug("updating governance config in store with merged items")
	return config.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Owner-scoped budgets require owner rows to exist first:
		// - team_id -> governance_teams
		// - virtual_key_id -> governance_virtual_keys
		// - provider_config_id -> governance_virtual_key_provider_configs
		// - customer_id -> governance_customers
		pendingTeamBudgetsToAdd := make([]configstoreTables.TableBudget, 0)
		pendingVirtualKeyBudgetsToAdd := make([]configstoreTables.TableBudget, 0)
		pendingProviderConfigBudgetsToAdd := make([]configstoreTables.TableBudget, 0)
		pendingCustomerBudgetsToAdd := make([]configstoreTables.TableBudget, 0)
		pendingTeamBudgetsToUpdate := make([]configstoreTables.TableBudget, 0)
		pendingVirtualKeyBudgetsToUpdate := make([]configstoreTables.TableBudget, 0)
		pendingProviderConfigBudgetsToUpdate := make([]configstoreTables.TableBudget, 0)
		pendingCustomerBudgetsToUpdate := make([]configstoreTables.TableBudget, 0)

		// Create budgets
		for _, budget := range budgetsToAdd {
			if budget.TeamID != nil {
				pendingTeamBudgetsToAdd = append(pendingTeamBudgetsToAdd, budget)
				continue
			}
			if budget.VirtualKeyID != nil {
				pendingVirtualKeyBudgetsToAdd = append(pendingVirtualKeyBudgetsToAdd, budget)
				continue
			}
			if budget.ProviderConfigID != nil {
				pendingProviderConfigBudgetsToAdd = append(pendingProviderConfigBudgetsToAdd, budget)
				continue
			}
			if budget.CustomerID != nil {
				pendingCustomerBudgetsToAdd = append(pendingCustomerBudgetsToAdd, budget)
				continue
			}
			if err := config.ConfigStore.CreateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
			}
		}

		// Update budgets (config.json changed)
		for _, budget := range budgetsToUpdate {
			if budget.TeamID != nil {
				pendingTeamBudgetsToUpdate = append(pendingTeamBudgetsToUpdate, budget)
				continue
			}
			if budget.VirtualKeyID != nil {
				pendingVirtualKeyBudgetsToUpdate = append(pendingVirtualKeyBudgetsToUpdate, budget)
				continue
			}
			if budget.ProviderConfigID != nil {
				pendingProviderConfigBudgetsToUpdate = append(pendingProviderConfigBudgetsToUpdate, budget)
				continue
			}
			if budget.CustomerID != nil {
				pendingCustomerBudgetsToUpdate = append(pendingCustomerBudgetsToUpdate, budget)
				continue
			}
			if err := config.ConfigStore.UpdateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to update budget %s: %w", budget.ID, err)
			}
		}

		// Create rate limits
		for _, rateLimit := range rateLimitsToAdd {
			if err := config.ConfigStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
				return fmt.Errorf("failed to create rate limit %s: %w", rateLimit.ID, err)
			}
		}

		// Update rate limits (config.json changed)
		for _, rateLimit := range rateLimitsToUpdate {
			if err := config.ConfigStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
				return fmt.Errorf("failed to update rate limit %s: %w", rateLimit.ID, err)
			}
		}

		// Create customers — strip inline Budgets first; created explicitly after the row exists.
		for i := range customersToAdd {
			customer := &customersToAdd[i]
			for j := range customer.Budgets {
				cid := customer.ID
				customer.Budgets[j].CustomerID = &cid
				pendingCustomerBudgetsToAdd = append(pendingCustomerBudgetsToAdd, customer.Budgets[j])
			}
			customer.Budgets = nil
			if err := config.ConfigStore.CreateCustomer(ctx, customer, tx); err != nil {
				return fmt.Errorf("failed to create customer %s: %w", customer.ID, err)
			}
		}

		// Update customers (config.json changed)
		for i := range customersToUpdate {
			customer := &customersToUpdate[i]
			if customer.Budgets != nil {
				// Fetch existing budget IDs for this customer so we can route
				// to add vs update — avoids INSERT conflicts on second sync.
				var existingIDs []string
				if err := tx.Model(&configstoreTables.TableBudget{}).
					Where("customer_id = ?", customer.ID).
					Pluck("id", &existingIDs).Error; err != nil {
					return fmt.Errorf("failed to query existing budgets for customer %s: %w", customer.ID, err)
				}
				existingSet := make(map[string]bool, len(existingIDs))
				for _, id := range existingIDs {
					existingSet[id] = true
				}
				desiredSet := make(map[string]bool, len(customer.Budgets))
				for j := range customer.Budgets {
					cid := customer.ID
					customer.Budgets[j].CustomerID = &cid
					desiredSet[customer.Budgets[j].ID] = true
					if existingSet[customer.Budgets[j].ID] {
						pendingCustomerBudgetsToUpdate = append(pendingCustomerBudgetsToUpdate, customer.Budgets[j])
					} else {
						pendingCustomerBudgetsToAdd = append(pendingCustomerBudgetsToAdd, customer.Budgets[j])
					}
				}
				// Delete stale budgets one by one — mirrors the team/VK reconcile pattern.
				for _, existingID := range existingIDs {
					if !desiredSet[existingID] {
						if err := config.ConfigStore.DeleteBudget(ctx, existingID, tx); err != nil {
							return fmt.Errorf("failed to delete stale budget %s for customer %s: %w", existingID, customer.ID, err)
						}
					}
				}
			}
			customer.Budgets = nil
			if err := config.ConfigStore.UpdateCustomer(ctx, customer, tx); err != nil {
				return fmt.Errorf("failed to update customer %s: %w", customer.ID, err)
			}
		}

		// Link budget_id references: validate ownership and set customer_id.
		// For adds: verify the budget is unowned before taking it.
		// For updates: also clear any stale link from the old budget_id.
		for _, customer := range customersToAdd {
			if customer.BudgetID == nil {
				continue
			}
			if err := linkCustomerBudgetID(tx, customer.ID, *customer.BudgetID, false); err != nil {
				return fmt.Errorf("failed to link budget %s to customer %s: %w", *customer.BudgetID, customer.ID, err)
			}
		}
		for _, customer := range customersToUpdate {
			if customer.BudgetID == nil {
				// budget_id removed — unlink any budget previously owned via this path.
				if err := tx.Model(&configstoreTables.TableBudget{}).
					Where("customer_id = ?", customer.ID).
					UpdateColumn("customer_id", nil).Error; err != nil {
					return fmt.Errorf("failed to unlink stale budgets from customer %s: %w", customer.ID, err)
				}
				continue
			}
			if err := linkCustomerBudgetID(tx, customer.ID, *customer.BudgetID, true); err != nil {
				return fmt.Errorf("failed to link budget %s to customer %s: %w", *customer.BudgetID, customer.ID, err)
			}
		}

		// Create teams
		for _, team := range teamsToAdd {
			if err := config.ConfigStore.CreateTeam(ctx, &team, tx); err != nil {
				return fmt.Errorf("failed to create team %s: %w", team.ID, err)
			}
		}

		// Update teams (config.json changed)
		for _, team := range teamsToUpdate {
			if err := config.ConfigStore.UpdateTeam(ctx, &team, tx); err != nil {
				return fmt.Errorf("failed to update team %s: %w", team.ID, err)
			}
		}

		// Create team-owned budgets after teams exist.
		for _, budget := range pendingTeamBudgetsToAdd {
			if err := config.ConfigStore.CreateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
			}
		}

		// Update team-owned budgets after teams exist.
		for _, budget := range pendingTeamBudgetsToUpdate {
			if err := config.ConfigStore.UpdateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to update budget %s: %w", budget.ID, err)
			}
		}

		// Create customer-owned budgets after customers exist (inline budgets + top-level with customer_id).
		for _, budget := range pendingCustomerBudgetsToAdd {
			if err := config.ConfigStore.CreateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
			}
		}

		// Update customer-owned budgets declared in top-level governance.budgets.
		for _, budget := range pendingCustomerBudgetsToUpdate {
			if err := config.ConfigStore.UpdateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to update budget %s: %w", budget.ID, err)
			}
		}

		// Create virtual keys with explicit association handling
		for i := range virtualKeysToAdd {
			virtualKey := &virtualKeysToAdd[i]
			providerConfigs := virtualKey.ProviderConfigs
			mcpConfigs := virtualKey.MCPConfigs
			virtualKey.ProviderConfigs = nil
			virtualKey.MCPConfigs = nil
			// Here we wll filter provider / keys that are not available
			if err := config.ConfigStore.CreateVirtualKey(ctx, virtualKey, tx); err != nil {
				return fmt.Errorf("failed to create virtual key %s: %w", virtualKey.ID, err)
			}
			for j := range providerConfigs {
				providerConfigs[j].VirtualKeyID = virtualKey.ID
				if err := config.ConfigStore.CreateVirtualKeyProviderConfig(ctx, &providerConfigs[j], tx); err != nil {
					return fmt.Errorf("failed to create provider config for virtual key %s: %w", virtualKey.ID, err)
				}
			}
			for j := range mcpConfigs {
				mcpConfigs[j].VirtualKeyID = virtualKey.ID
				if err := config.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &mcpConfigs[j], tx); err != nil {
					return fmt.Errorf("failed to create MCP config for virtual key %s: %w", virtualKey.ID, err)
				}
			}

			virtualKey.ProviderConfigs = providerConfigs
			virtualKey.MCPConfigs = mcpConfigs
		}

		// Update virtual keys (config.json changed)
		for _, virtualKey := range virtualKeysToUpdate {
			if err := reconcileVirtualKeyAssociations(ctx, config.ConfigStore, tx, virtualKey.ID, virtualKey.ProviderConfigs, virtualKey.MCPConfigs); err != nil {
				return fmt.Errorf("failed to reconcile associations for virtual key %s: %w", virtualKey.ID, err)
			}
			if err := config.ConfigStore.UpdateVirtualKey(ctx, &virtualKey, tx); err != nil {
				return fmt.Errorf("failed to update virtual key %s: %w", virtualKey.ID, err)
			}
		}

		// Create virtual-key-owned budgets after virtual keys exist.
		for _, budget := range pendingVirtualKeyBudgetsToAdd {
			if err := config.ConfigStore.CreateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
			}
		}

		// Update virtual-key-owned budgets after virtual keys exist.
		for _, budget := range pendingVirtualKeyBudgetsToUpdate {
			if err := config.ConfigStore.UpdateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to update budget %s: %w", budget.ID, err)
			}
		}

		// Create provider-config-owned budgets after virtual key provider configs exist.
		for _, budget := range pendingProviderConfigBudgetsToAdd {
			if err := config.ConfigStore.CreateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
			}
		}

		// Update provider-config-owned budgets after virtual key provider configs exist.
		for _, budget := range pendingProviderConfigBudgetsToUpdate {
			if err := config.ConfigStore.UpdateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to update budget %s: %w", budget.ID, err)
			}
		}

		// Create routing rules (new from config.json)
		for _, rule := range routingRulesToAdd {
			if err := config.ConfigStore.CreateRoutingRule(ctx, &rule, tx); err != nil {
				return fmt.Errorf("failed to create routing rule %s: %w", rule.ID, err)
			}
		}

		// Update routing rules (config.json changed)
		for _, rule := range routingRulesToUpdate {
			if err := config.ConfigStore.UpdateRoutingRule(ctx, &rule, tx); err != nil {
				return fmt.Errorf("failed to update routing rule %s: %w", rule.ID, err)
			}
		}

		// Create pricing overrides (new from config.json)
		for _, override := range pricingOverridesToAdd {
			if err := config.ConfigStore.CreatePricingOverride(ctx, &override, tx); err != nil {
				return fmt.Errorf("failed to create pricing override %s: %w", override.ID, err)
			}
		}

		// Update pricing overrides (config.json changed)
		for _, override := range pricingOverridesToUpdate {
			if err := config.ConfigStore.UpdatePricingOverride(ctx, &override, tx); err != nil {
				return fmt.Errorf("failed to update pricing override %s: %w", override.ID, err)
			}
		}
		// Create model configs (new from config.json)
		for _, modelConfig := range modelConfigsToAdd {
			if err := validateModelConfigGovernanceOwnership(tx, modelConfig); err != nil {
				return err
			}
			if err := config.ConfigStore.CreateModelConfig(ctx, &modelConfig, tx); err != nil {
				return fmt.Errorf("failed to create model config %s: %w", modelConfig.ID, err)
			}
			if len(modelConfig.BudgetIDs) > 0 {
				if err := linkModelConfigBudgets(tx, modelConfig.ID, modelConfig.BudgetIDs); err != nil {
					return err
				}
			}
		}

		// Update model configs (config.json changed)
		for _, modelConfig := range modelConfigsToUpdate {
			if err := validateModelConfigGovernanceOwnership(tx, modelConfig); err != nil {
				return err
			}
			if err := config.ConfigStore.UpdateModelConfig(ctx, &modelConfig, tx); err != nil {
				return fmt.Errorf("failed to update model config %s: %w", modelConfig.ID, err)
			}
			if len(modelConfig.BudgetIDs) > 0 {
				if err := linkModelConfigBudgets(tx, modelConfig.ID, modelConfig.BudgetIDs); err != nil {
					return err
				}
			}
		}

		// Upsert provider governance links (budget_id/rate_limit_id) for newly added mappings.
		for _, provider := range providersToAdd {
			if provider.Name == "" {
				continue
			}
			if err := validateProviderGovernanceOwnership(tx, provider); err != nil {
				return err
			}
			updates := map[string]interface{}{
				"budget_id":     provider.BudgetID,
				"rate_limit_id": provider.RateLimitID,
			}
			result := tx.Model(&configstoreTables.TableProvider{}).
				Where("name = ?", provider.Name).
				Select("budget_id", "rate_limit_id").
				Updates(updates)
			if result.Error != nil {
				return fmt.Errorf("failed to create provider governance mapping for %s: %w", provider.Name, result.Error)
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf(
					"failed to create provider governance mapping for %s: no provider row found (budget_id=%v, rate_limit_id=%v)",
					provider.Name,
					provider.BudgetID,
					provider.RateLimitID,
				)
			}
		}

		// Update provider governance links when config file values changed.
		for _, provider := range providersToUpdate {
			if provider.Name == "" {
				continue
			}
			if err := validateProviderGovernanceOwnership(tx, provider); err != nil {
				return err
			}
			updates := map[string]interface{}{
				"budget_id":     provider.BudgetID,
				"rate_limit_id": provider.RateLimitID,
			}
			result := tx.Model(&configstoreTables.TableProvider{}).
				Where("name = ?", provider.Name).
				Select("budget_id", "rate_limit_id").
				Updates(updates)
			if result.Error != nil {
				return fmt.Errorf("failed to update provider governance mapping for %s: %w", provider.Name, result.Error)
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf(
					"failed to update provider governance mapping for %s: no provider row found (budget_id=%v, rate_limit_id=%v)",
					provider.Name,
					provider.BudgetID,
					provider.RateLimitID,
				)
			}
		}

		if complexityAnalyzerConfigToUpdate != nil {
			if err := config.ConfigStore.UpdateComplexityAnalyzerConfig(ctx, complexityAnalyzerConfigToUpdate, tx); err != nil {
				logger.Warn("failed to sync complexity analyzer config from config file: %v", err)
			} else {
				config.GovernanceConfig.ComplexityAnalyzerConfig = complexityAnalyzerConfigToUpdate
			}
		}

		return nil
	})
}

func validateModelConfigGovernanceOwnership(tx *gorm.DB, modelConfig configstoreTables.TableModelConfig) error {
	if err := validateBudgetLinkOwnership(tx, modelConfig.BudgetID, "model config", modelConfig.ID); err != nil {
		return err
	}
	if err := validateRateLimitLinkOwnership(tx, modelConfig.RateLimitID, "model config", modelConfig.ID); err != nil {
		return err
	}
	for _, budgetID := range modelConfig.BudgetIDs {
		id := budgetID
		if err := validateBudgetLinkOwnership(tx, &id, "model config", modelConfig.ID); err != nil {
			return err
		}
	}
	return nil
}

// linkCustomerBudgetID sets customer_id on the referenced budget row, verifying that the budget
// is either unowned or already owned by this customer. When clearStale is true it also
// unlinks any other budget previously owned by the customer (handles budget_id changes).
func linkCustomerBudgetID(tx *gorm.DB, customerID, budgetID string, clearStale bool) error {
	var existing configstoreTables.TableBudget
	if err := tx.Select("id", "customer_id", "team_id", "virtual_key_id", "provider_config_id", "model_config_id").
		First(&existing, "id = ?", budgetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("budget %s not found", budgetID)
		}
		return fmt.Errorf("failed to check budget ownership: %w", err)
	}
	if (existing.CustomerID != nil && *existing.CustomerID != customerID) ||
		existing.TeamID != nil || existing.VirtualKeyID != nil ||
		existing.ProviderConfigID != nil || existing.ModelConfigID != nil {
		return fmt.Errorf("budget %s is already owned by another entity", budgetID)
	}
	if clearStale {
		if err := tx.Model(&configstoreTables.TableBudget{}).
			Where("customer_id = ? AND id != ?", customerID, budgetID).
			UpdateColumn("customer_id", nil).Error; err != nil {
			return fmt.Errorf("failed to unlink stale budget from customer %s: %w", customerID, err)
		}
	}
	result := tx.Model(&configstoreTables.TableBudget{}).
		Where("id = ?", budgetID).
		UpdateColumn("customer_id", customerID)
	if result.Error != nil {
		return fmt.Errorf("failed to link budget %s to customer %s: %w", budgetID, customerID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("failed to link budget %s to customer %s: no row updated", budgetID, customerID)
	}
	return nil
}

// linkModelConfigBudgets sets model_config_id on each budget in budgetIDs, and clears it from
// any budgets previously owned by mcID that are no longer in the list.
func linkModelConfigBudgets(tx *gorm.DB, mcID string, budgetIDs []string) error {
	// Normalize: trim whitespace and deduplicate.
	seen := make(map[string]struct{}, len(budgetIDs))
	normalized := make([]string, 0, len(budgetIDs))
	for _, raw := range budgetIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}

	// Clear ownership from budgets that are no longer referenced.
	unlinkQ := tx.Model(&configstoreTables.TableBudget{}).
		Where("model_config_id = ?", mcID)
	if len(normalized) > 0 {
		unlinkQ = unlinkQ.Where("id NOT IN ?", normalized)
	}
	if err := unlinkQ.UpdateColumn("model_config_id", nil).Error; err != nil {
		return fmt.Errorf("failed to unlink stale budgets from model config %q: %w", mcID, err)
	}
	// Link the declared budgets.
	for _, id := range normalized {
		if err := tx.Model(&configstoreTables.TableBudget{}).
			Where("id = ?", id).
			UpdateColumn("model_config_id", mcID).Error; err != nil {
			return fmt.Errorf("failed to link budget %q to model config %q: %w", id, mcID, err)
		}
	}
	return nil
}

func validateProviderGovernanceOwnership(tx *gorm.DB, provider configstoreTables.TableProvider) error {
	if err := validateBudgetLinkOwnership(tx, provider.BudgetID, "provider", provider.Name); err != nil {
		return err
	}
	if err := validateRateLimitLinkOwnership(tx, provider.RateLimitID, "provider", provider.Name); err != nil {
		return err
	}
	return nil
}

func validateBudgetLinkOwnership(tx *gorm.DB, budgetID *string, ownerType, ownerID string) error {
	if budgetID == nil {
		return nil
	}
	id := strings.TrimSpace(*budgetID)
	if id == "" {
		return nil
	}

	var budget configstoreTables.TableBudget
	if err := tx.Select("id", "team_id", "virtual_key_id", "provider_config_id").Where("id = ?", id).First(&budget).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("budget_id %q referenced by %s %q does not exist", id, ownerType, ownerID)
		}
		return fmt.Errorf("failed to validate budget ownership for %s %q: %w", ownerType, ownerID, err)
	}

	if budget.TeamID != nil || budget.VirtualKeyID != nil || budget.ProviderConfigID != nil {
		return fmt.Errorf("budget_id %q is already owned by another governance entity and cannot be linked to %s %q", id, ownerType, ownerID)
	}

	modelQuery := tx.Model(&configstoreTables.TableModelConfig{}).Where("budget_id = ?", id)
	if ownerType == "model config" {
		modelQuery = modelQuery.Where("id <> ?", ownerID)
	}
	var modelOwner configstoreTables.TableModelConfig
	if err := modelQuery.Select("id").First(&modelOwner).Error; err == nil {
		return fmt.Errorf("budget_id %q is already linked to model config %q; cannot link to %s %q", id, modelOwner.ID, ownerType, ownerID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to validate budget ownership for %s %q: %w", ownerType, ownerID, err)
	}

	providerQuery := tx.Model(&configstoreTables.TableProvider{}).Where("budget_id = ?", id)
	if ownerType == "provider" {
		providerQuery = providerQuery.Where("name <> ?", ownerID)
	}
	var providerOwner configstoreTables.TableProvider
	if err := providerQuery.Select("name").First(&providerOwner).Error; err == nil {
		return fmt.Errorf("budget_id %q is already linked to provider %q; cannot link to %s %q", id, providerOwner.Name, ownerType, ownerID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to validate budget ownership for %s %q: %w", ownerType, ownerID, err)
	}

	return nil
}

func validateRateLimitLinkOwnership(tx *gorm.DB, rateLimitID *string, ownerType, ownerID string) error {
	if rateLimitID == nil {
		return nil
	}
	id := strings.TrimSpace(*rateLimitID)
	if id == "" {
		return nil
	}

	var rateLimit configstoreTables.TableRateLimit
	if err := tx.Select("id").Where("id = ?", id).First(&rateLimit).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("rate_limit_id %q referenced by %s %q does not exist", id, ownerType, ownerID)
		}
		return fmt.Errorf("failed to validate rate_limit ownership for %s %q: %w", ownerType, ownerID, err)
	}

	modelQuery := tx.Model(&configstoreTables.TableModelConfig{}).Where("rate_limit_id = ?", id)
	if ownerType == "model config" {
		modelQuery = modelQuery.Where("id <> ?", ownerID)
	}
	var modelOwner configstoreTables.TableModelConfig
	if err := modelQuery.Select("id").First(&modelOwner).Error; err == nil {
		return fmt.Errorf("rate_limit_id %q is already linked to model config %q; cannot link to %s %q", id, modelOwner.ID, ownerType, ownerID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to validate rate_limit ownership for %s %q: %w", ownerType, ownerID, err)
	}

	providerQuery := tx.Model(&configstoreTables.TableProvider{}).Where("rate_limit_id = ?", id)
	if ownerType == "provider" {
		providerQuery = providerQuery.Where("name <> ?", ownerID)
	}
	var providerOwner configstoreTables.TableProvider
	if err := providerQuery.Select("name").First(&providerOwner).Error; err == nil {
		return fmt.Errorf("rate_limit_id %q is already linked to provider %q; cannot link to %s %q", id, providerOwner.Name, ownerType, ownerID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to validate rate_limit ownership for %s %q: %w", ownerType, ownerID, err)
	}

	var teamOwner configstoreTables.TableTeam
	if err := tx.Model(&configstoreTables.TableTeam{}).
		Where("rate_limit_id = ?", id).
		Select("id").
		First(&teamOwner).Error; err == nil {
		return fmt.Errorf("rate_limit_id %q is already linked to team %q; cannot link to %s %q", id, teamOwner.ID, ownerType, ownerID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to validate rate_limit ownership for %s %q: %w", ownerType, ownerID, err)
	}

	var vkOwner configstoreTables.TableVirtualKeyProviderConfig
	if err := tx.Model(&configstoreTables.TableVirtualKeyProviderConfig{}).
		Where("rate_limit_id = ?", id).
		Select("id").
		First(&vkOwner).Error; err == nil {
		return fmt.Errorf("rate_limit_id %q is already linked to virtual-key provider config %d; cannot link to %s %q", id, vkOwner.ID, ownerType, ownerID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to validate rate_limit ownership for %s %q: %w", ownerType, ownerID, err)
	}

	return nil
}

// isBcryptHash checks if a string looks like a bcrypt hash
func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") ||
		strings.HasPrefix(s, "$2b$") ||
		strings.HasPrefix(s, "$2y$")
}

// preserveSecretVar returns a new SecretVar with the given value but preserving
// env var metadata (SecretVar reference and FromEnv flag) from the source.
// This allows the hashed password to be used as the value while retaining
// the original env var reference for display in the UI.
func preserveSecretVar(source *schemas.SecretVar, value string) *schemas.SecretVar {
	if source == nil {
		return schemas.NewSecretVar(value)
	}
	if source.IsFromSecret() {
		sv := *source
		sv.Val = value
		return &sv
	}
	return &schemas.SecretVar{Val: value}
}

// loadAuthConfig loads auth config from file.
// File config (configData) always takes precedence over DB config.
func loadAuthConfig(ctx context.Context, config *Config, configData *ConfigData) {
	hasFileConfig := configData != nil && (configData.AuthConfig != nil || (configData.Governance != nil && configData.Governance.AuthConfig != nil))
	if !hasFileConfig && (config.GovernanceConfig == nil || config.GovernanceConfig.AuthConfig == nil) {
		return
	}
	// Ensure GovernanceConfig is initialized
	if config.GovernanceConfig == nil {
		config.GovernanceConfig = &configstore.GovernanceConfig{}
	}
	if config.ConfigStore == nil {
		logger.Warn("config store is required to load auth config from file")
		if hasFileConfig {
			config.GovernanceConfig.AuthConfig = configData.AuthConfig
		}
		return
	}
	// Load existing auth config from DB
	dbAuthConfig, err := config.ConfigStore.GetAuthConfig(ctx)
	if err != nil {
		logger.Warn("failed to get auth config from store: %v", err)
		return
	}
	// If no file config, use DB config and return (no write needed)
	if !hasFileConfig {
		if dbAuthConfig != nil {
			config.GovernanceConfig.AuthConfig = dbAuthConfig
		}
		return
	}
	var authConfig *configstore.AuthConfig
	if configData.Governance != nil && configData.Governance.AuthConfig != nil {
		authConfig = configData.Governance.AuthConfig
	} else if configData.AuthConfig != nil {
		authConfig = configData.AuthConfig
	}
	if authConfig == nil {
		return
	}
	// Fail-closed: if env/vault reference is unresolved, don't persist empty credentials.
	if authConfig.AdminUserName != nil && authConfig.AdminUserName.GetValue() == "" && authConfig.AdminUserName.IsFromSecret() {
		logger.Warn("username set with external reference but value is empty: %s", authConfig.AdminUserName.GetRawRef())
	}
	if authConfig.AdminPassword != nil && authConfig.AdminPassword.GetValue() == "" && authConfig.AdminPassword.IsFromSecret() {
		logger.Warn("password set with external reference but value is empty: %s", authConfig.AdminPassword.GetRawRef())
	}
	if authConfig.AdminPassword == nil || authConfig.AdminUserName == nil {
		logger.Warn("auth config is missing admin_username or admin_password, skipping auth config processing")
		return
	}
	filePassword := authConfig.AdminPassword.GetValue()
	// If DB already matches file config, skip hashing and DB write
	if dbAuthConfig != nil {
		usernameMatch := dbAuthConfig.AdminUserName.GetValue() == authConfig.AdminUserName.GetValue()
		boolsMatch := dbAuthConfig.IsEnabled == authConfig.IsEnabled
		var passwordMatch bool
		if filePassword == "" {
			passwordMatch = dbAuthConfig.AdminPassword.GetValue() == ""
		} else if isBcryptHash(filePassword) {
			passwordMatch = dbAuthConfig.AdminPassword.GetValue() == filePassword
		} else {
			passwordMatch, _ = encrypt.CompareHash(dbAuthConfig.AdminPassword.GetValue(), filePassword)
		}
		if usernameMatch && passwordMatch && boolsMatch {
			// DB matches file -- use DB hash but preserve file env var references
			config.GovernanceConfig.AuthConfig = &configstore.AuthConfig{
				AdminUserName: authConfig.AdminUserName,
				AdminPassword: preserveSecretVar(authConfig.AdminPassword, dbAuthConfig.AdminPassword.GetValue()),
				IsEnabled:     authConfig.IsEnabled,
			}
			return
		}
		if !passwordMatch {
			// Here we nuke all sessions
			if err := config.ConfigStore.FlushSessions(ctx); err != nil {
				logger.Warn("failed to flush sessions: %v", err)
			}
		}
	}
	// Hash password if it's plaintext (not already a bcrypt hash)
	hashedPassword := filePassword
	if hashedPassword != "" && !isBcryptHash(hashedPassword) {
		var err error
		hashedPassword, err = encrypt.Hash(hashedPassword)
		if err != nil {
			logger.Warn("failed to hash auth password: %v", err)
			// Fall back to DB config if available rather than leaving AuthConfig unset
			if dbAuthConfig != nil {
				config.GovernanceConfig.AuthConfig = dbAuthConfig
			}
			return
		}
	}
	// Build auth config with hashed password but preserve env var references
	config.GovernanceConfig.AuthConfig = &configstore.AuthConfig{
		AdminUserName: authConfig.AdminUserName,
		AdminPassword: preserveSecretVar(authConfig.AdminPassword, hashedPassword),
		IsEnabled:     authConfig.IsEnabled,
	}
	// Persist to config store
	if err := config.ConfigStore.UpdateAuthConfig(ctx, config.GovernanceConfig.AuthConfig); err != nil {
		logger.Warn("failed to update auth config: %v", err)
	}
}

// loadPlugins loads and merges plugins from file
func loadPlugins(ctx context.Context, config *Config, configData *ConfigData) {
	// First load plugins from DB
	if config.ConfigStore != nil {
		logger.Debug("getting plugins from store")
		plugins, err := config.ConfigStore.GetPlugins(ctx)
		if err != nil {
			logger.Warn("failed to get plugins from store: %v", err)
		}
		if plugins != nil {
			config.PluginConfigs = make([]*schemas.PluginConfig, len(plugins))
			for i, plugin := range plugins {
				pluginConfig := &schemas.PluginConfig{
					Name:      plugin.Name,
					Enabled:   plugin.Enabled,
					Config:    plugin.Config,
					Path:      plugin.Path,
					Placement: plugin.Placement,
					Order:     plugin.Order,
				}
				if plugin.Name == semanticcache.PluginName {
					if err := config.ValidateSemanticCacheConfig(pluginConfig); err != nil {
						logger.Warn("failed to validate semantic cache config: %v", err)
					}
				}
				config.PluginConfigs[i] = pluginConfig
			}
		}
	}

	// Merge with config file plugins
	if len(configData.Plugins) > 0 {
		if configData.isConfigJSONSourceOfTruth() && configData.sectionPresent("plugins") {
			syncPluginsFromFile(ctx, config, configData)
		} else {
			mergePlugins(ctx, config, configData)
		}
	} else if configData.isConfigJSONSourceOfTruth() && configData.sectionPresent("plugins") {
		syncPluginsFromFile(ctx, config, configData)
	}
}

// placementEqual compares two optional PluginPlacement pointers.
func placementEqual(a, b *schemas.PluginPlacement) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// orderEqual compares two optional int pointers.
func orderEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// mergePlugins merges plugins from config file with existing config
func mergePlugins(ctx context.Context, config *Config, configData *ConfigData) {
	logger.Debug("processing plugins from config file")
	if len(config.PluginConfigs) == 0 {
		logger.Debug("no plugins found in store, using plugins from config file")
		config.PluginConfigs = configData.Plugins
	} else {
		// Merge new plugins and update if version is higher
		for _, plugin := range configData.Plugins {
			if plugin.Version == nil {
				plugin.Version = bifrost.Ptr(int16(1))
			}
			existingIdx := slices.IndexFunc(config.PluginConfigs, func(p *schemas.PluginConfig) bool {
				return p.Name == plugin.Name
			})
			if existingIdx == -1 {
				logger.Debug("adding new plugin %s to config.PluginConfigs", plugin.Name)
				config.PluginConfigs = append(config.PluginConfigs, plugin)
			} else {
				existingPlugin := config.PluginConfigs[existingIdx]
				existingVersion := int16(1)
				if existingPlugin.Version != nil {
					existingVersion = *existingPlugin.Version
				}
				placementChanged := !placementEqual(existingPlugin.Placement, plugin.Placement) || !orderEqual(existingPlugin.Order, plugin.Order)
				if *plugin.Version > existingVersion || placementChanged {
					logger.Debug("replacing plugin %s (version %d→%d, placementChanged=%v)", plugin.Name, existingVersion, *plugin.Version, placementChanged)
					config.PluginConfigs[existingIdx] = plugin
				}
			}
		}
	}

	// Update store
	if config.ConfigStore != nil {
		logger.Debug("updating plugins in store")
		for _, plugin := range config.PluginConfigs {
			pluginConfigCopy, err := DeepCopy(plugin.Config)
			if err != nil {
				logger.Warn("failed to deep copy plugin config, skipping database update: %v", err)
				continue
			}
			if plugin.Version == nil {
				plugin.Version = bifrost.Ptr(int16(1))
			}
			pluginConfig := &configstoreTables.TablePlugin{
				Name:      plugin.Name,
				Enabled:   plugin.Enabled,
				Config:    pluginConfigCopy,
				Path:      plugin.Path,
				Version:   *plugin.Version,
				Placement: plugin.Placement,
				Order:     plugin.Order,
			}
			if err := config.ConfigStore.UpsertPlugin(ctx, pluginConfig); err != nil {
				logger.Warn("failed to update plugin: %v", err)
			}
		}
	}
}

// syncPluginsFromFile replaces stored plugin configs with the plugins declared in config.json.
func syncPluginsFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	logger.Debug("source_of_truth=config.json: syncing plugins exactly from config file")
	if config.ConfigStore == nil {
		// No store to reconcile against, so in-memory cannot diverge from the DB.
		config.PluginConfigs = configData.Plugins
		return
	}
	keep := make(map[string]bool, len(configData.Plugins))
	for _, plugin := range configData.Plugins {
		if plugin == nil {
			continue
		}
		keep[plugin.Name] = true
	}
	err := config.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		existing, err := config.ConfigStore.GetPlugins(ctx)
		if err != nil {
			return fmt.Errorf("failed to get plugins from store: %w", err)
		}
		for _, plugin := range existing {
			if plugin != nil && !keep[plugin.Name] {
				if err := config.ConfigStore.DeletePlugin(ctx, plugin.Name, tx); err != nil {
					return fmt.Errorf("failed to delete plugin %s: %w", plugin.Name, err)
				}
			}
		}
		for _, plugin := range configData.Plugins {
			if plugin == nil {
				continue
			}
			pluginConfigCopy, err := DeepCopy(plugin.Config)
			if err != nil {
				return fmt.Errorf("failed to deep copy plugin config for %s: %w", plugin.Name, err)
			}
			if plugin.Version == nil {
				plugin.Version = bifrost.Ptr(int16(1))
			}
			tablePlugin := &configstoreTables.TablePlugin{
				Name:      plugin.Name,
				Enabled:   plugin.Enabled,
				Config:    pluginConfigCopy,
				Path:      plugin.Path,
				Version:   *plugin.Version,
				Placement: plugin.Placement,
				Order:     plugin.Order,
			}
			if err := config.ConfigStore.UpdatePlugin(ctx, tablePlugin, tx); err != nil {
				return fmt.Errorf("failed to update plugin %s: %w", plugin.Name, err)
			}
		}
		return nil
	})
	if err != nil {
		// Leave config.PluginConfigs untouched so in-memory state stays consistent
		// with the DB, which rolled back on failure.
		logger.Warn("failed to sync plugins from config file: %v", err)
		return
	}
	// Only adopt the file-declared plugins in memory after the durable commit.
	config.PluginConfigs = configData.Plugins
}

// buildMCPPricingDataFromStore builds MCP pricing data from the config store
func buildMCPPricingDataFromStore(ctx context.Context, configStore configstore.ConfigStore) mcpcatalog.MCPPricingData {
	mcpPricingData := mcpcatalog.MCPPricingData{}
	mcpConfig, err := configStore.GetMCPConfig(ctx)
	if err != nil {
		logger.Warn("failed to get MCP config from store: %v", err)
		return mcpPricingData
	}
	if mcpConfig != nil {
		for _, clientConfig := range mcpConfig.ClientConfigs {
			dbClientConfig, err := configStore.GetMCPClientByName(ctx, clientConfig.Name)
			if err != nil {
				logger.Warn("failed to get MCP client config from store: %v", err)
				continue
			}
			if dbClientConfig == nil {
				logger.Warn("MCP client config is nil for client: %s", clientConfig.Name)
				continue
			}
			for toolName, costPerExecution := range dbClientConfig.ToolPricing {
				// Tool names in the DB are stored without the client/server prefix.
				// Build the key using fmt.Sprintf("%s/%s", clientName, toolName) to match
				// buildMCPPricingDataFromConfig and EditMCPClient patterns.
				mcpPricingData[fmt.Sprintf("%s/%s", dbClientConfig.Name, toolName)] = mcpcatalog.PricingEntry{
					Server:           dbClientConfig.Name,
					ToolName:         toolName,
					CostPerExecution: costPerExecution,
				}
			}
		}
	}
	return mcpPricingData
}

func buildMCPPricingDataFromConfig(ctx context.Context, configData *ConfigData) mcpcatalog.MCPPricingData {
	mcpPricingData := mcpcatalog.MCPPricingData{}
	if configData == nil || configData.MCP == nil {
		return mcpPricingData
	}
	for _, clientConfig := range configData.MCP.ClientConfigs {
		for toolName, costPerExecution := range clientConfig.ToolPricing {
			mcpPricingData[fmt.Sprintf("%s/%s", clientConfig.Name, toolName)] = mcpcatalog.PricingEntry{
				Server:           clientConfig.Name,
				ToolName:         toolName,
				CostPerExecution: costPerExecution,
			}
		}
	}
	return mcpPricingData
}

// ResolveFrameworkPricingConfig resolves framework pricing configuration.
//
// Precedence order (highest → lowest): DB > config.json > built-in defaults.
//
// DB values are authoritative once written — this allows runtime changes via the
// management API to persist across restarts without requiring a config.json edit.
// When the DB is absent or contains a corrupted/zero value the file config is used,
// with the DB backfilled so the next startup finds a valid value.
//
// pricing_url supports the "env.VAR_NAME" prefix for full-string env substitution.
// The check is explicit (strings.HasPrefix "env.") so that non-prefixed URLs are
// never passed through the env lookup — partial/embedded references such as
// "https://host/env.PATH" are treated as plain strings without any expansion.
//
// NOTE on pricingSyncInterval naming:
// Despite its name, pricingSyncInterval is NOT a scheduling frequency.
// It defines the minimum allowed elapsed time between sync executions.
// The actual check occurs on a fixed ticker (syncWorkerTickerPeriod).
// Effective sync frequency = max(syncWorkerTickerPeriod, pricingSyncInterval).
func ResolveFrameworkPricingConfig(
	dbConfig *configstoreTables.TableFrameworkConfig,
	fileConfig *framework.FrameworkConfig,
) (*configstoreTables.TableFrameworkConfig, *modelcatalog.Config, bool) {
	defaultPricingURL := modelcatalog.DefaultPricingURL
	defaultModelParametersURL := modelcatalog.DefaultModelParametersURL
	defaultSyncSeconds := int64(modelcatalog.DefaultSyncInterval.Seconds())

	filePricingURL := (*string)(nil)
	fileModelParametersURL := (*string)(nil)
	fileSyncSeconds := (*int64)(nil)
	fileMCPLibraryURL := (*string)(nil)
	fileMCPLibrarySyncSeconds := (*int64)(nil)
	skipURLBackfill := false // prevent DB backfill of unresolved env references
	skipModelParamsURLBackfill := false
	skipMCPLibraryURLBackfill := false
	if fileConfig != nil && fileConfig.Pricing != nil {
		if fileConfig.Pricing.PricingURL != nil {
			raw := *fileConfig.Pricing.PricingURL
			if strings.HasPrefix(raw, "env.") {
				resolvedURL, err := envutils.ProcessEnvValue(raw)
				if err != nil {
					logger.Warn("pricing_url: env variable not found (%v); keeping original value %q", err, raw)
					filePricingURL = fileConfig.Pricing.PricingURL
					skipURLBackfill = true
				} else {
					filePricingURL = &resolvedURL
				}
			} else {
				filePricingURL = &raw
			}
		}
		if fileConfig.Pricing.ModelParametersURL != nil {
			raw := strings.TrimSpace(*fileConfig.Pricing.ModelParametersURL)
			if raw == "" {
				// Blank is treated as "not set"; fall back to default.
			} else if strings.HasPrefix(raw, "env.") {
				resolvedURL, err := envutils.ProcessEnvValue(raw)
				if err != nil {
					logger.Warn("model_parameters_url: env variable not found (%v); keeping original value %q", err, raw)
					fileModelParametersURL = fileConfig.Pricing.ModelParametersURL
					skipModelParamsURLBackfill = true
				} else {
					resolved := strings.TrimSpace(resolvedURL)
					if resolved != "" {
						fileModelParametersURL = &resolved
					}
				}
			} else {
				fileModelParametersURL = &raw
			}
		}
		if fileConfig.Pricing.PricingSyncInterval != nil {
			val := *fileConfig.Pricing.PricingSyncInterval
			switch {
			case val <= 0:
				logger.Warn("pricing_sync_interval in config.json is invalid (%d seconds), ignoring — using default (%d seconds)", val, defaultSyncSeconds)
			case val < modelcatalog.MinimumPricingSyncIntervalSec:
				clamped := modelcatalog.MinimumPricingSyncIntervalSec
				logger.Warn("pricing_sync_interval in config.json is below minimum (%d seconds), clamping to %d seconds", val, clamped)
				fileSyncSeconds = &clamped
			default:
				fileSyncSeconds = &val
			}
		}
		if fileConfig.Pricing.MCPLibraryURL != nil {
			raw := strings.TrimSpace(*fileConfig.Pricing.MCPLibraryURL)
			if raw == "" {
				// Blank is treated as "not set"; fall back to default.
			} else if strings.HasPrefix(raw, "env.") {
				resolvedURL, err := envutils.ProcessEnvValue(raw)
				if err != nil {
					logger.Warn("mcp_library_url: env variable not found (%v); keeping original value %q", err, raw)
					fileMCPLibraryURL = &raw
					skipMCPLibraryURLBackfill = true
				} else {
					resolved := strings.TrimSpace(resolvedURL)
					if resolved != "" {
						fileMCPLibraryURL = &resolved
					}
				}
			} else {
				fileMCPLibraryURL = &raw
			}
		}
		if fileConfig.Pricing.MCPLibrarySyncInterval != nil {
			val := *fileConfig.Pricing.MCPLibrarySyncInterval
			switch {
			case val <= 0:
				logger.Warn("mcp_library_sync_interval in config.json is invalid (%d seconds), ignoring — using default (%d seconds)", val, defaultSyncSeconds)
			case val < modelcatalog.MinimumPricingSyncIntervalSec:
				clamped := modelcatalog.MinimumPricingSyncIntervalSec
				logger.Warn("mcp_library_sync_interval in config.json is below minimum (%d seconds), clamping to %d seconds", val, clamped)
				fileMCPLibrarySyncSeconds = &clamped
			default:
				fileMCPLibrarySyncSeconds = &val
			}
		}
	}

	// --- Phase 2: apply file config over defaults ---

	resolvedPricingURL := &defaultPricingURL
	resolvedModelParametersURL := &defaultModelParametersURL
	resolvedSyncSeconds := &defaultSyncSeconds

	defaultMCPLibraryURL := modelcatalog.DefaultMCPLibraryURL
	defaultMCPLibrarySyncSeconds := int64(modelcatalog.DefaultSyncInterval.Seconds())
	resolvedMCPLibraryURL := &defaultMCPLibraryURL
	resolvedMCPLibrarySyncInterval := &defaultMCPLibrarySyncSeconds

	if filePricingURL != nil {
		resolvedPricingURL = filePricingURL
		logger.Debug("pricing_url resolved from file")
	}
	if fileModelParametersURL != nil {
		resolvedModelParametersURL = fileModelParametersURL
		logger.Debug("model_parameters_url resolved from file")
	}
	if fileSyncSeconds != nil {
		resolvedSyncSeconds = fileSyncSeconds
		logger.Debug("pricing_sync_interval resolved from file: %d seconds", *fileSyncSeconds)
	}

	// MCP library catalog sync source mirrors the datasheet URL handling for
	// defaults, env substitution, interval validation, and hash-gated config.json
	// changes. DB precedence is applied in Phase 3 below.
	if fileMCPLibraryURL != nil {
		resolvedMCPLibraryURL = fileMCPLibraryURL
	}
	if fileMCPLibrarySyncSeconds != nil {
		resolvedMCPLibrarySyncInterval = fileMCPLibrarySyncSeconds
	}

	// --- Phase 3: DB values applied; file wins on hash mismatch (file changed since last write) ---

	needsDBUpdate := false
	configID := uint(0)

	// Hash the file-resolved values; skip if nothing valid survived Phase 1.
	fileHash := ""
	fileHasHashableMCPConfig := (fileMCPLibraryURL != nil && !skipMCPLibraryURLBackfill) || fileMCPLibrarySyncSeconds != nil
	if fileConfig != nil && fileConfig.Pricing != nil && !skipURLBackfill && (filePricingURL != nil || (fileModelParametersURL != nil && !skipModelParamsURLBackfill) || fileSyncSeconds != nil || fileHasHashableMCPConfig) {
		var h string
		var err error
		if fileHasHashableMCPConfig {
			mcpHashURL := fileMCPLibraryURL
			if skipMCPLibraryURLBackfill {
				mcpHashURL = nil
			}
			h, err = configstore.GenerateFrameworkConfigHash(filePricingURL, fileModelParametersURL, fileSyncSeconds, configstore.FrameworkConfigHashOptions{
				MCPLibraryURL:          mcpHashURL,
				MCPLibrarySyncInterval: fileMCPLibrarySyncSeconds,
			})
		} else {
			h, err = configstore.GenerateFrameworkConfigHash(filePricingURL, fileModelParametersURL, fileSyncSeconds)
		}
		if err != nil {
			logger.Warn("failed to compute framework config hash: %v", err)
		} else {
			fileHash = h
		}
	}

	storedHash := ""
	if dbConfig != nil {
		storedHash = dbConfig.ConfigHash
	}
	fileChanged := fileHash != "" && fileHash != storedHash

	if dbConfig != nil {
		configID = dbConfig.ID

		if dbConfig.PricingURL != nil {
			if fileChanged && filePricingURL != nil {
				logger.Info("pricing_url from config.json overrides DB (file hash changed) — updating DB")
				needsDBUpdate = true
			} else {
				resolvedPricingURL = dbConfig.PricingURL
			}
		} else if !skipURLBackfill {
			needsDBUpdate = true
		}
		if dbConfig.ModelParametersURL != nil && *dbConfig.ModelParametersURL != "" {
			if fileChanged && fileModelParametersURL != nil && !skipModelParamsURLBackfill {
				logger.Info("model_parameters_url from config.json overrides DB (file hash changed) — updating DB")
				needsDBUpdate = true
			} else {
				resolvedModelParametersURL = dbConfig.ModelParametersURL
			}
		} else if !skipModelParamsURLBackfill {
			needsDBUpdate = true
		}

		if dbConfig.PricingSyncInterval != nil {
			val := *dbConfig.PricingSyncInterval
			if val <= 0 {
				logger.Warn("pricing_sync_interval in DB is corrupted (%d seconds), ignoring — backfilling with %d seconds", val, *resolvedSyncSeconds)
				needsDBUpdate = true
			} else if val < modelcatalog.MinimumPricingSyncIntervalSec {
				logger.Warn("pricing_sync_interval in DB is below minimum (%d seconds) — backfilling", val)
				if !fileChanged || fileSyncSeconds == nil {
					clamped := modelcatalog.MinimumPricingSyncIntervalSec
					resolvedSyncSeconds = &clamped
				}
				needsDBUpdate = true
			} else if fileChanged && fileSyncSeconds != nil {
				logger.Info("pricing_sync_interval from config.json overrides DB (file hash changed): file=%d db=%d seconds — updating DB", *fileSyncSeconds, val)
				needsDBUpdate = true
			} else {
				resolvedSyncSeconds = dbConfig.PricingSyncInterval
			}
		} else {
			needsDBUpdate = true
		}

		// MCP library config follows the same hash-gated config.json precedence as
		// datasheet config: DB wins while the file is unchanged; file wins and is
		// backfilled when the file changed since the last persisted hash.
		if dbConfig.MCPLibraryURL != nil {
			if trimmed := strings.TrimSpace(*dbConfig.MCPLibraryURL); trimmed != "" {
				if fileChanged && fileMCPLibraryURL != nil && !skipMCPLibraryURLBackfill {
					logger.Info("mcp_library_url from config.json overrides DB (file hash changed) — updating DB")
					needsDBUpdate = true
				} else {
					resolvedMCPLibraryURL = &trimmed
				}
			} else if !skipMCPLibraryURLBackfill {
				needsDBUpdate = true
			}
		} else if !skipMCPLibraryURLBackfill {
			needsDBUpdate = true
		}
		if dbConfig.MCPLibrarySyncInterval != nil {
			val := *dbConfig.MCPLibrarySyncInterval
			switch {
			case val <= 0:
				logger.Warn("mcp_library_sync_interval in DB is corrupted (%d seconds), ignoring — backfilling with %d seconds", val, *resolvedMCPLibrarySyncInterval)
				needsDBUpdate = true
			case val < modelcatalog.MinimumPricingSyncIntervalSec:
				logger.Warn("mcp_library_sync_interval in DB is below minimum (%d seconds) — backfilling", val)
				if !fileChanged || fileMCPLibrarySyncSeconds == nil {
					clamped := modelcatalog.MinimumPricingSyncIntervalSec
					resolvedMCPLibrarySyncInterval = &clamped
				}
				needsDBUpdate = true
			default:
				if fileChanged && fileMCPLibrarySyncSeconds != nil {
					logger.Info("mcp_library_sync_interval from config.json overrides DB (file hash changed): file=%d db=%d seconds — updating DB", *fileMCPLibrarySyncSeconds, val)
					needsDBUpdate = true
				} else {
					resolvedMCPLibrarySyncInterval = dbConfig.MCPLibrarySyncInterval
				}
			}
		} else {
			needsDBUpdate = true
		}
	}

	// --- Phase 4: nil guard ---
	if resolvedPricingURL == nil {
		logger.Warn("invariant violation: pricing_url resolved to nil — falling back to default %q", defaultPricingURL)
		resolvedPricingURL = &defaultPricingURL
	}
	if resolvedModelParametersURL == nil {
		logger.Warn("invariant violation: model_parameters_url resolved to nil — falling back to default %q", defaultModelParametersURL)
		resolvedModelParametersURL = &defaultModelParametersURL
	}
	if resolvedSyncSeconds == nil {
		logger.Warn("invariant violation: pricing_sync_interval resolved to nil — falling back to default %d seconds", defaultSyncSeconds)
		resolvedSyncSeconds = &defaultSyncSeconds
	}
	if resolvedMCPLibraryURL == nil {
		resolvedMCPLibraryURL = &defaultMCPLibraryURL
	}
	if resolvedMCPLibrarySyncInterval == nil {
		resolvedMCPLibrarySyncInterval = &defaultMCPLibrarySyncSeconds
	}

	// Only update the stored hash when the file actually changed; preserve the
	// existing hash for correction-only DB updates (null backfill, corruption fix).
	persistedHash := ""
	if dbConfig != nil {
		persistedHash = dbConfig.ConfigHash
	}
	if fileChanged {
		persistedHash = fileHash
	}

	return &configstoreTables.TableFrameworkConfig{
			ID:                     configID,
			PricingURL:             resolvedPricingURL,
			PricingSyncInterval:    resolvedSyncSeconds,
			ModelParametersURL:     resolvedModelParametersURL,
			MCPLibraryURL:          resolvedMCPLibraryURL,
			MCPLibrarySyncInterval: resolvedMCPLibrarySyncInterval,
			ConfigHash:             persistedHash,
		}, &modelcatalog.Config{
			PricingURL:             resolvedPricingURL,
			PricingSyncInterval:    resolvedSyncSeconds,
			ModelParametersURL:     resolvedModelParametersURL,
			MCPLibraryURL:          resolvedMCPLibraryURL,
			MCPLibrarySyncInterval: resolvedMCPLibrarySyncInterval,
		}, needsDBUpdate
}

// initFrameworkConfig initializes framework config and pricing manager from file
func initFrameworkConfig(ctx context.Context, config *Config, configData *ConfigData) {
	mcpPricingConfig := &mcpcatalog.Config{}
	var frameworkConfigFromDB *configstoreTables.TableFrameworkConfig
	if config.ConfigStore != nil {
		frameworkConfig, err := config.ConfigStore.GetFrameworkConfig(ctx)
		if err != nil {
			logger.Warn("failed to get framework config from store: %v", err)
		}
		frameworkConfigFromDB = frameworkConfig
		mcpPricingConfig.PricingData = buildMCPPricingDataFromStore(ctx, config.ConfigStore)
	}
	var fileFrameworkConfig *framework.FrameworkConfig
	if configData != nil {
		fileFrameworkConfig = configData.FrameworkConfig
	}
	normalizedFrameworkConfig, pricingConfig, needsFrameworkBackfill := ResolveFrameworkPricingConfig(frameworkConfigFromDB, fileFrameworkConfig)
	if config.ConfigStore != nil && (frameworkConfigFromDB == nil || needsFrameworkBackfill) {
		if err := config.ConfigStore.UpdateFrameworkConfig(ctx, normalizedFrameworkConfig); err != nil {
			logger.Warn("failed to normalize framework config in store: %v", err)
		}
	}

	// Initialize OAuth provider
	config.OAuthProvider = oauth2.NewOAuth2Provider(config.ConfigStore, logger)
	// Initialize per-user-headers credential provider. Storage parallel of
	// OAuthProvider for MCPAuthTypePerUserHeaders clients.
	config.MCPHeadersProvider = mcp_headers.NewProvider(config.ConfigStore, logger)

	// Start token refresh worker for automatic OAuth token refresh
	config.TokenRefreshWorker = oauth2.NewTokenRefreshWorker(config.OAuthProvider, logger)
	if config.TokenRefreshWorker != nil {
		config.TokenRefreshWorker.Start(ctx)
	}

	// Start per-user OAuth sweep worker: expires stale pending flows and reaps
	// long-orphaned token rows. Orphan retention defaults to 30 days.
	config.OAuthSweepWorker = oauth2.NewPerUserOAuthSweepWorker(config.OAuthProvider, 30*24*time.Hour, logger)
	if config.OAuthSweepWorker != nil {
		config.OAuthSweepWorker.Start(ctx)
	}

	// Start per-user headers credential sweep worker. Parallel of the OAuth
	// sweep but only reaps orphaned credential rows (no flow table to sweep).
	// Same 30-day retention so admin expectations stay uniform across the two
	// per-user auth surfaces.
	config.MCPHeadersSweepWorker = mcp_headers.NewCredentialSweepWorker(config.MCPHeadersProvider, 30*24*time.Hour, logger)
	if config.MCPHeadersSweepWorker != nil {
		config.MCPHeadersSweepWorker.Start(ctx)
	}

	config.FrameworkConfig = &framework.FrameworkConfig{
		Pricing: pricingConfig,
	}

	// Use default modelcatalog initialization when no enterprise overrides are provided
	pricingManager, err := modelcatalog.Init(ctx, pricingConfig, config.ConfigStore, logger)
	if err != nil {
		logger.Fatal("failed to initialize pricing manager: %v", err)
	}
	config.ModelCatalog = pricingManager

	// Initialize MCP catalog
	// Merge file-based pricing into mcpPricingConfig (DB data already loaded above).
	// File config is used as fallback; DB values take precedence via the merge order.
	if mcpPricingConfig.PricingData == nil {
		mcpPricingConfig.PricingData = mcpcatalog.MCPPricingData{}
	}
	for k, v := range buildMCPPricingDataFromConfig(ctx, configData) {
		if _, exists := mcpPricingConfig.PricingData[k]; !exists {
			mcpPricingConfig.PricingData[k] = v
		}
	}
	mcpCatalog, err := mcpcatalog.Init(ctx, mcpPricingConfig, logger)
	if err != nil {
		logger.Warn("failed to initialize MCP catalog: %v", err)
	}
	config.MCPCatalog = mcpCatalog

	// ModelCatalog is now initialized; replay pricing overrides for the no-store path.
	// loadGovernanceConfig ran before ModelCatalog existed, so the in-memory
	// load was skipped. Do it here now that ModelCatalog is available.
	if config.ModelCatalog != nil && config.GovernanceConfig != nil && len(config.GovernanceConfig.PricingOverrides) > 0 {
		if err := config.ModelCatalog.SetPricingOverrides(config.GovernanceConfig.PricingOverrides); err != nil {
			logger.Warn("failed to set pricing overrides from config file: %v", err)
		}
	}
}

// initEncryption initializes encryption from config data or environment variables.
// When configData.EncryptionKey is nil (no config file), falls through to env var check.
func initEncryption(configData *ConfigData) error {
	if configData.EncryptionKey == nil || configData.EncryptionKey.GetValue() == "" {
		// Checking if BIFROST_ENCRYPTION_KEY environment variable is set
		if os.Getenv("BIFROST_ENCRYPTION_KEY") != "" {
			configData.EncryptionKey = schemas.NewSecretVar("env.BIFROST_ENCRYPTION_KEY")
		}
	}
	// Checking if encryption key is set
	if configData.EncryptionKey != nil && configData.EncryptionKey.GetValue() != "" {
		encrypt.Init(configData.EncryptionKey.GetValue(), logger)
	}
	return nil
}

// initVault is a no-op stub at the OSS level.
// Vault initialization is performed by the enterprise layer via config_store.vault_store.
func initVault(_ *ConfigData) {}

// syncEncryption encrypts all plaintext rows in the config store if encryption is enabled.
// Called during bootup after encryption key is initialized and all config data has been loaded.
func syncEncryption(ctx context.Context, config *Config) {
	if !encrypt.IsEnabled() || config.ConfigStore == nil {
		return
	}
	if err := config.ConfigStore.EncryptPlaintextRows(ctx); err != nil {
		logger.Error("failed to sync encryption for plaintext rows: %v", err)
	}
}

// resolveMCPConfigClientIDs resolves MCPClientName to MCPClientID for each MCP config.
// This is needed when parsing virtual keys from config.json, which uses "mcp_client_name"
// instead of "mcp_client_id". The function looks up each MCP client by name and sets the
// corresponding MCPClientID. Configs with unresolvable names are logged and skipped.
// Returns the filtered slice containing only configs with valid MCPClientIDs.
func resolveMCPConfigClientIDs(
	ctx context.Context,
	store configstore.ConfigStore,
	mcpConfigs []configstoreTables.TableVirtualKeyMCPConfig,
	virtualKeyID string,
) []configstoreTables.TableVirtualKeyMCPConfig {
	if store == nil || len(mcpConfigs) == 0 {
		return mcpConfigs
	}

	resolvedConfigs := make([]configstoreTables.TableVirtualKeyMCPConfig, 0, len(mcpConfigs))

	for i := range mcpConfigs {
		mc := &mcpConfigs[i]

		// If MCPClientID is already set (e.g., from database or direct construction), keep it
		if mc.MCPClientID != 0 {
			resolvedConfigs = append(resolvedConfigs, *mc)
			continue
		}

		// If MCPClientName is set (from config.json parsing), resolve it to MCPClientID
		if mc.MCPClientName != "" {
			mcpClient, err := store.GetMCPClientByName(ctx, mc.MCPClientName)
			if err != nil {
				logger.Warn("virtual key %s: failed to resolve MCP client '%s': %v (skipping this MCP config)",
					virtualKeyID, mc.MCPClientName, err)
				continue
			}
			if mcpClient == nil {
				logger.Warn("virtual key %s: MCP client '%s' not found (skipping this MCP config)",
					virtualKeyID, mc.MCPClientName)
				continue
			}
			mc.MCPClientID = mcpClient.ID
			resolvedConfigs = append(resolvedConfigs, *mc)
			continue
		}

		// Neither MCPClientID nor MCPClientName is set - skip this config
		logger.Warn("virtual key %s: MCP config has neither mcp_client_id nor mcp_client_name set (skipping)",
			virtualKeyID)
	}

	return resolvedConfigs
}

// reconcileVirtualKeyAssociations reconciles ProviderConfigs and MCPConfigs associations
// for a virtual key when config.json changes (hash mismatch already detected at VK level).
//
// NOTE: This function is ONLY called when the virtual key's hash has changed,
// meaning something in config.json was modified for this VK. It is NOT called
// when hashes match (in that case, DB config is kept as-is).
//
// Reconciliation strategy (file is source of truth when hash changes):
// - Configs in both file and DB → update from file
// - Configs only in file → create new
// - Configs only in DB → DELETE (file is source of truth, extra configs are removed)
func reconcileVirtualKeyAssociations(
	ctx context.Context,
	store configstore.ConfigStore,
	tx *gorm.DB,
	vkID string,
	newProviderConfigs []configstoreTables.TableVirtualKeyProviderConfig,
	newMCPConfigs []configstoreTables.TableVirtualKeyMCPConfig,
) error {
	// Reconcile ProviderConfigs
	existingProviderConfigs, err := store.GetVirtualKeyProviderConfigs(ctx, vkID)
	if err != nil {
		return fmt.Errorf("failed to get existing provider configs: %w", err)
	}

	// Build lookup map for existing configs by Provider (unique per VK)
	existingByProvider := make(map[string]configstoreTables.TableVirtualKeyProviderConfig)
	for _, pc := range existingProviderConfigs {
		existingByProvider[pc.Provider] = pc
	}

	// Process provider configs from config.json
	newProviderSet := make(map[string]bool)
	for _, newPC := range newProviderConfigs {
		newProviderSet[newPC.Provider] = true
		newPC.VirtualKeyID = vkID
		if existing, found := existingByProvider[newPC.Provider]; found {
			// Update existing provider config from file
			existing.Weight = newPC.Weight
			existing.AllowedModels = newPC.AllowedModels
			existing.BlacklistedModels = newPC.BlacklistedModels
			existing.AllowAllKeys = newPC.AllowAllKeys
			existing.RateLimitID = newPC.RateLimitID
			existing.Keys = newPC.Keys
			if err := store.UpdateVirtualKeyProviderConfig(ctx, &existing, tx); err != nil {
				return fmt.Errorf("failed to update provider config for %s: %w", newPC.Provider, err)
			}
		} else {
			// Create new provider config from file
			if err := store.CreateVirtualKeyProviderConfig(ctx, &newPC, tx); err != nil {
				return fmt.Errorf("failed to create provider config for %s: %w", newPC.Provider, err)
			}
		}
	}

	// Delete provider configs that exist in DB but not in file
	for provider, existing := range existingByProvider {
		if !newProviderSet[provider] {
			if err := store.DeleteVirtualKeyProviderConfig(ctx, existing.ID, tx); err != nil {
				return fmt.Errorf("failed to delete provider config for %s: %w", provider, err)
			}
		}
	}

	// Reconcile MCPConfigs
	existingMCPConfigs, err := store.GetVirtualKeyMCPConfigs(ctx, vkID)
	if err != nil {
		return fmt.Errorf("failed to get existing MCP configs: %w", err)
	}

	// Build lookup map for existing MCP configs by MCPClientID
	existingByMCPClientID := make(map[uint]configstoreTables.TableVirtualKeyMCPConfig)
	for _, mc := range existingMCPConfigs {
		existingByMCPClientID[mc.MCPClientID] = mc
	}

	// Process MCP configs from config.json
	newMCPSet := make(map[uint]bool)
	for _, newMC := range newMCPConfigs {
		newMCPSet[newMC.MCPClientID] = true
		newMC.VirtualKeyID = vkID
		if existing, found := existingByMCPClientID[newMC.MCPClientID]; found {
			// Update existing MCP config from file
			existing.ToolsToExecute = newMC.ToolsToExecute
			if err := store.UpdateVirtualKeyMCPConfig(ctx, &existing, tx); err != nil {
				return fmt.Errorf("failed to update MCP config for client %d: %w", newMC.MCPClientID, err)
			}
		} else {
			// Create new MCP config from file
			if err := store.CreateVirtualKeyMCPConfig(ctx, &newMC, tx); err != nil {
				return fmt.Errorf("failed to create MCP config for client %d: %w", newMC.MCPClientID, err)
			}
		}
	}

	// Delete MCP configs that exist in DB but not in file
	for mcpClientID, existing := range existingByMCPClientID {
		if !newMCPSet[mcpClientID] {
			if err := store.DeleteVirtualKeyMCPConfig(ctx, existing.ID, tx); err != nil {
				return fmt.Errorf("failed to delete MCP config for client %d: %w", mcpClientID, err)
			}
		}
	}

	return nil
}

// GetRawConfigString returns the raw configuration string.
func (c *Config) GetRawConfigString() string {
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// GetProviderConfigRaw retrieves the raw, unredacted provider configuration from memory.
// This method is for internal use only, particularly by the account implementation.
//
// Performance characteristics:
//   - Memory access: ultra-fast direct memory access
//   - No database I/O or JSON parsing overhead
//   - Thread-safe with read locks for concurrent access
//
// Returns a copy of the configuration to prevent external modifications.
func (c *Config) GetProviderConfigRaw(provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	config, exists := c.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}
	// Return direct reference for maximum performance - this is used by Bifrost core
	// CRITICAL: Never modify the returned data as it's shared
	return &config, nil
}

// HandlerStore interface implementation

// ShouldAllowPerRequestStorageOverride returns whether per-request content storage overrides are permitted.
func (c *Config) ShouldAllowPerRequestStorageOverride() bool {
	return c.ClientConfig.AllowPerRequestContentStorageOverride
}

// ShouldAllowPerRequestRawOverride returns whether per-request raw request/response overrides are permitted.
func (c *Config) ShouldAllowPerRequestRawOverride() bool {
	return c.ClientConfig.AllowPerRequestRawOverride
}

// ShouldAllowDirectKeys returns whether callers may bypass the registered key pool via x-bf-direct-key header.
func (c *Config) ShouldAllowDirectKeys() bool {
	return c.ClientConfig.AllowDirectKeys
}

// GetMCPExternalClientURL returns the configured external base URL Bifrost uses as the
// redirect_uri when acting as an OAuth client to upstream MCP servers, or empty string
// if not configured. Resolves env var references automatically.
func (c *Config) GetMCPExternalClientURL() string {
	return c.ClientConfig.MCPExternalClientURL.GetValue()
}

// GetHeaderMatcher returns the precompiled header matcher for header filtering.
// Lock-free via atomic pointer; safe for concurrent reads from hot paths.
func (c *Config) GetHeaderMatcher() *HeaderMatcher {
	return c.headerMatcher.Load()
}

func (c *Config) GetModelCatalog() *modelcatalog.ModelCatalog {
	if c == nil {
		return nil
	}
	return c.ModelCatalog
}

// SetHeaderMatcher atomically stores a new precompiled header matcher.
// Called when header filter config changes.
func (c *Config) SetHeaderMatcher(m *HeaderMatcher) {
	c.headerMatcher.Store(m)
}

// GetMCPHeaderCombinedAllowlist returns the combined allowlist for MCP headers across all MCP clients.
// This method acquires a muMCP read lock and is safe for concurrent access from hot paths.
func (c *Config) GetMCPHeaderCombinedAllowlist() schemas.WhiteList {
	c.muMCP.RLock()
	defer c.muMCP.RUnlock()

	if c.MCPConfig == nil || len(c.MCPConfig.ClientConfigs) == 0 {
		return schemas.WhiteList{}
	}

	allowlist := schemas.WhiteList{}
	for _, mcpClient := range c.MCPConfig.ClientConfigs {
		if mcpClient == nil {
			continue
		}
		if mcpClient.AllowedExtraHeaders.IsUnrestricted() {
			return schemas.WhiteList{"*"}
		}
		allowlist = append(allowlist, mcpClient.AllowedExtraHeaders...)
	}
	return allowlist
}

// GetAllowOnAllVirtualKeysClients returns a map of clientID -> clientName for all MCP clients
// that have AllowOnAllVirtualKeys enabled. The returned map is a copy, safe for concurrent use.
func (c *Config) GetAllowOnAllVirtualKeysClients() map[string]string {
	c.muMCP.RLock()
	defer c.muMCP.RUnlock()

	if c.MCPConfig == nil {
		return nil
	}
	result := make(map[string]string)
	for _, client := range c.MCPConfig.ClientConfigs {
		if client != nil && client.AllowOnAllVirtualKeys {
			result[client.ID] = client.Name
		}
	}
	return result
}

// GetPluginOrder returns the names of all base plugins in their sorted placement order.
// This method is lock-free and safe for concurrent access from hot paths.
// Do not modify the returned slice; it is a shared snapshot and must be treated read-only.
func (c *Config) GetPluginOrder() []string {
	plugins := c.BasePlugins.Load()
	if plugins == nil {
		return nil
	}
	names := make([]string, len(*plugins))
	for i, p := range *plugins {
		names[i] = p.GetName()
	}
	return names
}

func (c *Config) GetLoadedLLMPlugins() []schemas.LLMPlugin {
	if plugins := c.LLMPlugins.Load(); plugins != nil {
		return slices.Clone(*plugins)
	}
	return nil
}

// GetLoadedPluginNames returns the sanitized names of every currently loaded plugin,
// matching the names embedded in their trace span names.
func (c *Config) GetLoadedPluginNames() []string {
	plugins := c.BasePlugins.Load()
	if plugins == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(*plugins))
	names := make([]string, 0, len(*plugins))
	for _, p := range *plugins {
		name := schemas.SanitizePluginSpanName(p.GetName())
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// pluginChunkInterceptor implements StreamChunkInterceptor by calling plugin hooks
type pluginChunkInterceptor struct {
	plugins []schemas.HTTPTransportPlugin
}

// InterceptChunk processes a chunk through all plugin HTTPTransportStreamChunkHook methods.
// Plugins are called in reverse order (same as PostHook) so modifications chain correctly.
func (i *pluginChunkInterceptor) InterceptChunk(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, stream *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	for j := len(i.plugins) - 1; j >= 0; j-- {
		plugin := i.plugins[j]
		pluginName := plugin.GetName()
		var (
			modified *schemas.BifrostStreamChunk
			err      error
		)
		func() {
			pluginCtx := ctx.WithPluginScope(&pluginName)
			defer pluginCtx.ReleasePluginScope()
			modified, err = plugin.HTTPTransportStreamChunkHook(pluginCtx, req, stream)
		}()
		if err != nil {
			return modified, fmt.Errorf("failed to intercept chunk with plugin %s: %w", pluginName, err)
		}
		if modified == nil {
			return nil, nil // Plugin wants to skip this chunk
		}
		stream = modified
	}
	return stream, nil
}

// GetStreamChunkInterceptor returns the chunk interceptor for streaming responses.
// Returns nil if no plugins are loaded.
func (c *Config) GetStreamChunkInterceptor() StreamChunkInterceptor {
	plugins := c.GetLoadedHTTPTransportPlugins()
	if len(plugins) == 0 {
		return nil
	}
	return &pluginChunkInterceptor{plugins: plugins}
}

// GetAsyncJobExecutor returns the async job executor.
// Returns nil if LogsStore or governance plugin is not configured.
func (c *Config) GetAsyncJobExecutor() *logstore.AsyncJobExecutor {
	return c.AsyncJobExecutor
}

// GetAsyncJobResultTTL returns the default TTL for async job results in seconds.
func (c *Config) GetAsyncJobResultTTL() int {
	if c.ClientConfig.AsyncJobResultTTL > 0 {
		return c.ClientConfig.AsyncJobResultTTL
	}
	return logstore.DefaultAsyncJobResultTTL
}

// GetKVStore returns the shared in-memory kvstore instance.
func (c *Config) GetKVStore() *kvstore.Store {
	return c.KVStore
}

// Close gracefully shuts down all background components associated with the Config.
// This includes ModelCatalog sync worker, TokenRefreshWorker, KVStore cleanup loop,
// ConfigStore, LogsStore, and VectorStore. It should be called when the Config is
// no longer needed to prevent goroutine leaks.
func (c *Config) Close(ctx context.Context) {
	if c.ModelCatalog != nil {
		c.ModelCatalog.Cleanup()
	}
	if c.TokenRefreshWorker != nil {
		c.TokenRefreshWorker.Stop()
	}
	if c.OAuthSweepWorker != nil {
		c.OAuthSweepWorker.Stop()
	}
	if c.MCPHeadersSweepWorker != nil {
		c.MCPHeadersSweepWorker.Stop()
	}
	if c.KVStore != nil {
		c.KVStore.Close()
	}
	if c.ConfigStore != nil {
		c.ConfigStore.Close(ctx)
	}
	if c.LogsStore != nil {
		c.LogsStore.Close(ctx)
	}
	if c.ObjectStore != nil {
		if err := c.ObjectStore.Close(); err != nil {
			logger.Warn("failed to close object store: %v", err)
		}
	}
	if c.VectorStore != nil {
		c.VectorStore.Close(ctx, "")
	}
}

func initSkillsObjectStore(ctx context.Context, config *Config, logStoreConfig *logstore.Config) error {
	if config == nil || config.ObjectStore != nil || logStoreConfig == nil || logStoreConfig.ObjectStorage == nil {
		return nil
	}
	objStore, err := objectstore.NewObjectStore(ctx, logStoreConfig.ObjectStorage, logger)
	if err != nil {
		return fmt.Errorf("failed to create skills object store: %w", err)
	}
	pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pingCancel()
	if err := objStore.Ping(pingCtx); err != nil {
		_ = objStore.Close()
		return fmt.Errorf("failed to ping skills object store: %w", err)
	}
	config.ObjectStore = objStore
	logger.Info("skills object store initialized")
	return nil
}

// initFeatureFlags constructs the feature flag store and applies overrides
// in precedence order: DB first (Hydrate), then config.json file (ApplyFile,
// which marks the entry as locked so it cannot be toggled via the UI).
// Errors from configstore are logged and ignored so a transient DB hiccup
// at boot does not block startup; the store falls back to defaults +
// file overrides.
func initFeatureFlags(ctx context.Context, config *Config, configData *ConfigData) error {
	// Type-assert to bool so a stored `false` (or non-bool / missing key)
	// resolves to OSS mode rather than the misleading "any non-nil = true"
	// behavior of the previous `!= nil` check. The schemas comment for this
	// context key declares it as bool, so the assertion is the documented
	// shape; the comma-ok zero-value handles the unset path cleanly.
	isEnterprise, _ := ctx.Value(schemas.BifrostContextKeyIsEnterprise).(bool)
	store, err := featureflags.New(featureflags.Config{IsEnterprise: isEnterprise})
	if err != nil {
		return fmt.Errorf("failed to initialize feature flags: %w", err)
	}
	config.FeatureFlags = store

	if config.ConfigStore != nil {
		rows, err := config.ConfigStore.ListFeatureFlags(ctx)
		if err != nil {
			logger.Warn("[featureflags] hydrate from configstore failed: %v", err)
		} else {
			hydration := make([]featureflags.HydrationRow, 0, len(rows))
			for _, row := range rows {
				hydration = append(hydration, featureflags.HydrationRow{
					ID:        row.ID,
					Enabled:   row.Enabled,
					UpdatedAt: row.UpdatedAt,
				})
			}
			store.Hydrate(hydration)
		}
	}

	if configData != nil && configData.FeatureFlags != nil {
		for id, val := range configData.FeatureFlags.Flags {
			store.ApplyFile(id, val.Enabled)
		}
	}
	return nil
}

// initKVStore initializes the kvstore for the config
func initKVStore(config *Config) error {
	var err error
	config.KVStore, err = kvstore.New(kvstore.Config{
		DefaultTTL:      30 * time.Minute,
		CleanupInterval: 1 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize kvstore: %w", err)
	}
	return nil
}

// GetLoadedMCPPlugins returns the current snapshot of loaded MCP plugins.
// This method is lock-free and safe for concurrent access from hot paths.
// It returns the plugin slice from the atomic pointer, which is safe to iterate
// even if plugins are being updated concurrently.
// Do not modify the returned slice; it is a shared snapshot and must be treated read-only.
func (c *Config) GetLoadedMCPPlugins() []schemas.MCPPlugin {
	if plugins := c.MCPPlugins.Load(); plugins != nil {
		return slices.Clone(*plugins)
	}
	return nil
}

// GetLoadedHTTPTransportPlugins returns all loaded plugins that implement HTTPTransportPlugin interface.
// This method returns a cached list that is updated on plugin add/reload/remove operations.
// It is lock-free and safe for concurrent access from hot paths.
// Do not modify the returned slice; it is a shared snapshot and must be treated read-only.
func (c *Config) GetLoadedHTTPTransportPlugins() []schemas.HTTPTransportPlugin {
	if plugins := c.HTTPTransportPlugins.Load(); plugins != nil {
		return slices.Clone(*plugins)
	}
	return nil
}

// rebuildInterfaceCaches rebuilds all plugin interface caches from BasePlugins
// This is called automatically after any RegisterPlugin/UnregisterPlugin operation
// PERFORMANCE: Single-pass implementation - iterates BasePlugins once and checks all interfaces
// This is 3x faster than the old approach of separate rebuilds (O(N) instead of O(3N))
func (c *Config) rebuildInterfaceCaches() {
	basePlugins := c.BasePlugins.Load()
	if basePlugins == nil {
		// Clear all caches atomically, except ConfigMarshallers which are preserved.
		emptyLLM := []schemas.LLMPlugin{}
		emptyMCP := []schemas.MCPPlugin{}
		emptyHTTP := []schemas.HTTPTransportPlugin{}
		c.LLMPlugins.Store(&emptyLLM)
		c.MCPPlugins.Store(&emptyMCP)
		c.HTTPTransportPlugins.Store(&emptyHTTP)
		return
	}

	// Single pass through all plugins - check all interfaces in one iteration
	var llm []schemas.LLMPlugin
	var mcp []schemas.MCPPlugin
	var httpTransport []schemas.HTTPTransportPlugin

	for _, p := range *basePlugins {
		if llmPlugin, ok := p.(schemas.LLMPlugin); ok {
			llm = append(llm, llmPlugin)
		}
		if mcpPlugin, ok := p.(schemas.MCPPlugin); ok {
			mcp = append(mcp, mcpPlugin)
		}
		if httpPlugin, ok := p.(schemas.HTTPTransportPlugin); ok {
			httpTransport = append(httpTransport, httpPlugin)
		}
		if cm, ok := p.(schemas.ConfigMarshallerPlugin); ok {
			// RegisterConfigMarshaller adds/updates atomically without clearing other entries
			c.RegisterConfigMarshaller(p.GetName(), cm)
		}
	}

	c.LLMPlugins.Store(&llm)
	c.MCPPlugins.Store(&mcp)
	c.HTTPTransportPlugins.Store(&httpTransport)
}

// RegisterConfigMarshaller registers a config marshaller for a plugin by name without
// adding the plugin to BasePlugins. Use this to register marshallers for disabled plugins
// at startup so their stored configs can still be redacted/expanded via the API.
func (c *Config) RegisterConfigMarshaller(name string, cm schemas.ConfigMarshallerPlugin) {
	for {
		old := c.ConfigMarshallers.Load()
		newMap := make(map[string]schemas.ConfigMarshallerPlugin)
		if old != nil {
			maps.Copy(newMap, *old)
		}
		newMap[name] = cm
		if c.ConfigMarshallers.CompareAndSwap(old, &newMap) {
			return
		}
	}
}

// RemoveConfigMarshaller explicitly removes a plugin's config marshaller.
// Call this only when a plugin is permanently deleted, not when it is disabled.
func (c *Config) RemoveConfigMarshaller(name string) {
	for {
		old := c.ConfigMarshallers.Load()
		if old == nil {
			return
		}
		newMap := make(map[string]schemas.ConfigMarshallerPlugin, len(*old))
		maps.Copy(newMap, *old)
		delete(newMap, name)
		if c.ConfigMarshallers.CompareAndSwap(old, &newMap) {
			return
		}
	}
}

// IsPluginLoaded checks if a plugin with the given name is currently loaded.
// This method is lock-free and safe for concurrent access from hot paths.
func (c *Config) IsPluginLoaded(name string) bool {
	basePlugins := c.BasePlugins.Load()
	if basePlugins == nil {
		return false
	}

	for _, p := range *basePlugins {
		if p.GetName() == name {
			return true
		}
	}

	return false
}

// UpdatePluginOverallStatus updates the overall status of a plugin
func (c *Config) UpdatePluginOverallStatus(name string, displayName string, status string, logs []string, types []schemas.PluginType) {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()

	if c.pluginStatus == nil {
		c.pluginStatus = make(map[string]schemas.PluginStatus)
	}

	logsCopy := make([]string, len(logs))
	copy(logsCopy, logs)

	typesCopy := make([]schemas.PluginType, len(types))
	copy(typesCopy, types)

	c.pluginStatus[name] = schemas.PluginStatus{
		Name:   displayName,
		Status: status,
		Logs:   logsCopy,
		Types:  typesCopy,
	}
}

// UpdatePluginDisplayName updates the display name of a plugin
func (c *Config) UpdatePluginDisplayName(name string, displayName string) error {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()

	// Make sure that the display name is not already in use
	seen := false
	for _, status := range c.pluginStatus {
		if status.Name == displayName {
			seen = true
			break
		}
	}
	if seen {
		return fmt.Errorf("display name %s already in use", displayName)
	}

	if _, ok := c.pluginStatus[name]; ok {
		c.pluginStatus[name] = schemas.PluginStatus{
			Name:   displayName,
			Status: c.pluginStatus[name].Status,
			Logs:   c.pluginStatus[name].Logs,
			Types:  c.pluginStatus[name].Types,
		}
		return nil
	}
	return fmt.Errorf("plugin %s not found", name)
}

// UpdatePluginStatus updates the status of a plugin
func (c *Config) UpdatePluginStatus(name string, status string) error {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()

	oldEntry, ok := c.pluginStatus[name]
	if !ok {
		return fmt.Errorf("plugin %s not found", name)
	}

	newEntry := oldEntry
	newEntry.Status = status

	c.pluginStatus[name] = newEntry
	return nil
}

// AppendPluginStateLogs appends logs to a plugin status entry
func (c *Config) AppendPluginStateLogs(name string, logs []string) error {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()
	oldEntry, ok := c.pluginStatus[name]
	if !ok {
		return fmt.Errorf("plugin %s not found", name)
	}
	newEntry := oldEntry
	newEntry.Logs = append(oldEntry.Logs, logs...)
	c.pluginStatus[name] = newEntry
	return nil
}

// GetPluginNameByDisplayName returns the name of a plugin by its display name
func (c *Config) GetPluginNameByDisplayName(displayName string) (string, bool) {
	c.pluginStatusMu.RLock()
	defer c.pluginStatusMu.RUnlock()
	for name, status := range c.pluginStatus {
		if status.Name == displayName {
			return name, true
		}
	}
	return "", false
}

// DeletePluginOverallStatus completely removes a plugin status entry
func (c *Config) DeletePluginOverallStatus(name string) {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()

	delete(c.pluginStatus, name)
}

// GetPluginStatus returns the status of all plugins
func (c *Config) GetPluginStatus() map[string]schemas.PluginStatus {
	c.pluginStatusMu.RLock()
	defer c.pluginStatusMu.RUnlock()

	result := make(map[string]schemas.PluginStatus, len(c.pluginStatus))
	maps.Copy(result, c.pluginStatus)

	return result
}

// GetPluginStatusByName returns the status of a specific plugin
func (c *Config) GetPluginStatusByName(name string) (schemas.PluginStatus, bool) {
	c.pluginStatusMu.RLock()
	defer c.pluginStatusMu.RUnlock()

	status, ok := c.pluginStatus[name]
	return status, ok
}

// ReloadPlugin adds or updates a plugin in the registry
// This is the single entry point for all plugin additions/updates
// If a plugin with the same name exists, it will be replaced (atomic find-and-replace)
// If no plugin exists with that name, it will be added
func (c *Config) ReloadPlugin(plugin schemas.BasePlugin) error {
	c.pluginsMu.Lock()
	defer c.pluginsMu.Unlock()

	name := plugin.GetName()

	for {
		oldPlugins := c.BasePlugins.Load()
		var newPlugins []schemas.BasePlugin

		if oldPlugins == nil {
			newPlugins = []schemas.BasePlugin{plugin}
		} else {
			newPlugins = make([]schemas.BasePlugin, 0, len(*oldPlugins)+1)

			replaced := false
			for _, p := range *oldPlugins {
				if p.GetName() == name {
					newPlugins = append(newPlugins, plugin) // Replace with new
					replaced = true
				} else {
					newPlugins = append(newPlugins, p) // Keep existing
				}
			}

			if !replaced {
				newPlugins = append(newPlugins, plugin) // Add as new
			}
		}

		if c.BasePlugins.CompareAndSwap(oldPlugins, &newPlugins) {
			c.rebuildInterfaceCaches()
			return nil
		}
		// CAS failed, retry with new snapshot
	}
}

// UnregisterPlugin removes a plugin from the registry
func (c *Config) UnregisterPlugin(name string) error {
	c.pluginsMu.Lock()
	defer c.pluginsMu.Unlock()

	for {
		oldPlugins := c.BasePlugins.Load()
		if oldPlugins == nil {
			return plugins.ErrPluginNotFound
		}

		newPlugins := make([]schemas.BasePlugin, 0, len(*oldPlugins))
		found := false
		for _, p := range *oldPlugins {
			if p.GetName() == name {
				found = true
				continue
			}
			newPlugins = append(newPlugins, p)
		}

		if !found {
			return plugins.ErrPluginNotFound
		}

		if c.BasePlugins.CompareAndSwap(oldPlugins, &newPlugins) {
			delete(c.pluginOrderMap, name)
			c.rebuildInterfaceCaches()
			return nil
		}
		// CAS failed, retry with new snapshot
	}
}

// SetPluginOrderInfo stores ordering metadata for a plugin.
// If placement is nil, defaults to "post_builtin". If order is nil, defaults to 0.
func (c *Config) SetPluginOrderInfo(name string, placement *schemas.PluginPlacement, order *int) {
	c.pluginsMu.Lock()
	defer c.pluginsMu.Unlock()

	if c.pluginOrderMap == nil {
		c.pluginOrderMap = make(map[string]pluginOrderInfo)
	}

	p := schemas.PluginPlacementPostBuiltin
	if placement != nil {
		p = *placement
	}
	o := 0
	if order != nil {
		o = *order
	}

	c.pluginOrderMap[name] = pluginOrderInfo{Placement: p, Order: o}
}

// SortAndRebuildPlugins sorts BasePlugins by placement group then order, and rebuilds caches.
// Placement groups execute in order: pre_builtin → builtin → post_builtin.
// Within each group, plugins are sorted by order (lower = earlier). Ties preserve registration order (stable sort).
func (c *Config) SortAndRebuildPlugins() {
	c.pluginsMu.Lock()
	defer c.pluginsMu.Unlock()

	oldPlugins := c.BasePlugins.Load()
	if oldPlugins == nil || len(*oldPlugins) == 0 {
		return
	}

	sorted := make([]schemas.BasePlugin, len(*oldPlugins))
	copy(sorted, *oldPlugins)

	groupRank := map[schemas.PluginPlacement]int{
		schemas.PluginPlacementPreBuiltin:  0,
		schemas.PluginPlacementBuiltin:     1,
		schemas.PluginPlacementPostBuiltin: 2,
	}
	defaultRank := 2 // Unknown placements default to post_builtin (least privileged)

	sort.SliceStable(sorted, func(i, j int) bool {
		iInfo := c.pluginOrderMap[sorted[i].GetName()]
		jInfo := c.pluginOrderMap[sorted[j].GetName()]
		iRank, iOk := groupRank[iInfo.Placement]
		if !iOk {
			iRank = defaultRank
		}
		jRank, jOk := groupRank[jInfo.Placement]
		if !jOk {
			jRank = defaultRank
		}
		if iRank != jRank {
			return iRank < jRank
		}
		return iInfo.Order < jInfo.Order
	})

	c.BasePlugins.Store(&sorted)
	c.rebuildInterfaceCaches()
}

// FindPluginAs finds a plugin by name in the given config and returns it as type T
// Returns error if plugin not found or doesn't implement T
// This is a type-safe finder that eliminates manual type assertions
// Usage: plugin, err := lib.FindPluginAs[*mypackage.MyPluginType](config, "plugin-name")
func FindPluginAs[T any](c *Config, name string) (T, error) {
	var zero T

	basePlugins := c.BasePlugins.Load()
	if basePlugins == nil {
		return zero, fmt.Errorf("plugin %s not found", name)
	}

	for _, p := range *basePlugins {
		if p.GetName() == name {
			if typed, ok := p.(T); ok {
				return typed, nil
			}
			return zero, fmt.Errorf("plugin %s does not implement required interface", name)
		}
	}

	return zero, fmt.Errorf("plugin %s not found", name)
}

// FindLLMPlugin is a convenience wrapper for finding LLM plugins
func (c *Config) FindLLMPlugin(name string) (schemas.LLMPlugin, error) {
	return FindPluginAs[schemas.LLMPlugin](c, name)
}

// FindMCPPlugin is a convenience wrapper for finding MCP plugins
func (c *Config) FindMCPPlugin(name string) (schemas.MCPPlugin, error) {
	return FindPluginAs[schemas.MCPPlugin](c, name)
}

// FindPluginByName returns a plugin as BasePlugin
// For most cases, use FindPluginAs[T] for type-safe access
func (c *Config) FindPluginByName(name string) (schemas.BasePlugin, error) {
	return FindPluginAs[schemas.BasePlugin](c, name)
}

// GetProviderConfigRedacted retrieves a provider configuration with sensitive values redacted.
// This method is intended for external API responses and logging.
//
// The returned configuration has sensitive values redacted:
// - API keys are redacted using RedactKey()
// - Values from environment variables show the original env var name (env.VAR_NAME)
//
// Returns a new copy with redacted values that is safe to expose externally.
func (c *Config) GetProviderConfigRedacted(provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	config, exists := c.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}

	return config.Redacted(), nil
}

// GetProviderKeysRaw retrieves the raw keys configured for a provider.
func (c *Config) GetProviderKeysRaw(provider schemas.ModelProvider) ([]schemas.Key, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	config, exists := c.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}

	keys := append([]schemas.Key(nil), config.Keys...)
	return keys, nil
}

// GetProviderKeysRedacted retrieves redacted keys configured for a provider.
func (c *Config) GetProviderKeysRedacted(provider schemas.ModelProvider) ([]schemas.Key, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	config, exists := c.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}

	return append([]schemas.Key(nil), config.Redacted().Keys...), nil
}

// GetProviderKeyRaw retrieves a single raw key configured for a provider.
func (c *Config) GetProviderKeyRaw(provider schemas.ModelProvider, keyID string) (*schemas.Key, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	config, exists := c.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}

	index := slices.IndexFunc(config.Keys, func(key schemas.Key) bool {
		return key.ID == keyID
	})
	if index == -1 {
		return nil, ErrNotFound
	}

	key := config.Keys[index]
	return &key, nil
}

// GetProviderKeyRedacted retrieves a single redacted key configured for a provider.
func (c *Config) GetProviderKeyRedacted(provider schemas.ModelProvider, keyID string) (*schemas.Key, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	config, exists := c.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}

	redacted := config.Redacted()
	index := slices.IndexFunc(redacted.Keys, func(key schemas.Key) bool {
		return key.ID == keyID
	})
	if index == -1 {
		return nil, ErrNotFound
	}

	key := redacted.Keys[index]
	return &key, nil
}

// GetAllProviders returns all configured provider names.
func (c *Config) GetAllProviders() ([]schemas.ModelProvider, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0, len(c.Providers))
	for provider := range c.Providers {
		providers = append(providers, provider)
	}

	return providers, nil
}

// AddProvider adds a new provider configuration to memory with full environment variable
// processing. This method is called when new providers are added via the HTTP API.
//
// The method:
//   - Validates that the provider doesn't already exist
//   - Processes environment variables in API keys, and key-level configs
//   - Stores the processed configuration in memory
//   - Updates metadata and timestamps
func (c *Config) AddProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	// Check if provider already exists
	if _, exists := c.Providers[provider]; exists {
		return fmt.Errorf("provider %s: %w", provider, ErrAlreadyExists)
	}
	// Validate CustomProviderConfig if present
	if err := ValidateCustomProvider(config, provider); err != nil {
		return err
	}
	for i, key := range config.Keys {
		if key.ID == "" {
			config.Keys[i].ID = uuid.NewString()
		}
	}
	// First add the provider to the store
	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		if err := c.ConfigStore.AddProvider(ctx, provider, config); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return ErrNotFound
			}
			// If the provider already exists in the DB (e.g., from a previous failed attempt)
			// but not in the in-memory map, sync it to memory and return ErrAlreadyExists
			// so the caller can proceed with an update instead of failing.
			if errors.Is(err, configstore.ErrAlreadyExists) {
				// Provider already exists in DB but not in memory - sync and return
				c.Providers[provider] = config
				logger.Info("provider %s already exists in DB, synced to memory", provider)
				return fmt.Errorf("provider/provider key name %s: %w", provider, ErrAlreadyExists)
			}
			return fmt.Errorf("failed to update provider config in store: %w", err)
		}
	}
	c.Providers[provider] = config
	logger.Info("added provider: %s", provider)
	return nil
}

// UpdateProviderConfig updates a provider configuration in memory with full environment
// variable processing. This method is called when provider configurations are modified
// via the HTTP API and ensures all data processing is done upfront.
//
// The method:
//   - Processes environment variables in API keys, and key-level configs
//   - Stores the processed configuration in memory
//   - Updates metadata and timestamps
//   - Thread-safe operation with write locks
//
// Note: Environment variable cleanup for deleted/updated keys is now handled automatically
// by the mergeKeys function before this method is called.
//
// Parameters:
//   - provider: The provider to update
//   - config: The new configuration
func (c *Config) UpdateProviderConfig(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	// Get existing configuration for validation
	existingConfig, exists := c.Providers[provider]
	if !exists {
		return ErrNotFound
	}
	// Validate CustomProviderConfig if present, ensuring immutable fields are not changed
	if err := ValidateCustomProviderUpdate(config, existingConfig, provider); err != nil {
		return err
	}
	// Preserve the existing ConfigHash - this is the original hash from config.json
	// and must be retained so that on server restart, the hash comparison works correctly
	// and user's key value changes are preserved (not overwritten by config.json)
	config.ConfigHash = existingConfig.ConfigHash
	// Update in-memory configuration first (so client can read updated config)
	c.Providers[provider] = config
	for i, key := range config.Keys {
		if key.ID == "" {
			config.Keys[i].ID = uuid.NewString()
		}
	}
	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		// Process environment variables in keys (including key-level configs)
		// Update provider in database within a transaction
		dbErr := c.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			if err := c.ConfigStore.UpdateProvider(ctx, provider, config, tx); err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return ErrNotFound
				}
				return fmt.Errorf("failed to update provider config in store: %w", err)
			}
			return nil
		})
		if dbErr != nil {
			// Rollback in-memory changes if database transaction failed
			c.Providers[provider] = existingConfig
			return dbErr
		}
	}
	// Release lock before calling client.UpdateProvider to avoid deadlock
	// client.UpdateProvider will call GetConfigForProvider which needs RLock
	c.Mu.Unlock()

	// Update client provider - this may acquire its own locks
	clientErr := c.client.UpdateProvider(provider)

	// Re-acquire lock for cleanup (defer will unlock at function return)
	c.Mu.Lock()

	if clientErr != nil {
		// Rollback in-memory changes if client update failed and the current config is still the one this call applied to
		if reflect.DeepEqual(c.Providers[provider], config) {
			c.Providers[provider] = existingConfig
		}
		// If database was updated, we can't rollback the transaction here
		// but the in-memory state will be consistent
		return fmt.Errorf("failed to update provider: %w", clientErr)
	}

	logger.Info("Updated configuration for provider: %s", provider)
	return nil
}

// AddProviderKey adds a new key to an existing provider configuration.
func (c *Config) AddProviderKey(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	existingConfig, exists := c.Providers[provider]
	if !exists {
		return ErrNotFound
	}

	if key.ID == "" {
		key.ID = uuid.NewString()
	}

	updatedConfig := existingConfig
	updatedConfig.Keys = append(append([]schemas.Key(nil), existingConfig.Keys...), key)

	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		if err := c.ConfigStore.CreateProviderKey(ctx, provider, key); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return ErrNotFound
			}
			if errors.Is(err, configstore.ErrAlreadyExists) {
				return ErrAlreadyExists
			}
			return fmt.Errorf("failed to create provider key in store: %w", err)
		}
		// The vault store callback rewrites the secret into a vault reference
		// during the DB write, but only on the store-side row copy. Re-read so the
		// in-memory key (and API responses) carry FromVault/VaultRef instead of the
		// original plaintext.
		storedKey, err := c.ConfigStore.GetProviderKey(ctx, provider, key.ID)
		if err != nil {
			// The DB write succeeded but we could not re-read the vault-rewritten
			// key. Failing here avoids committing the original plaintext into
			// c.Providers (and serving it via the keys API) on vault deployments.
			logger.Error("failed to re-read stored key %s for provider %s after create: %v", key.ID, provider, err)
			return fmt.Errorf("failed to re-read provider key after create: %w", err)
		}
		if idx := slices.IndexFunc(updatedConfig.Keys, func(k schemas.Key) bool { return k.ID == key.ID }); idx != -1 {
			updatedConfig.Keys[idx] = *storedKey
		}
	}

	c.Providers[provider] = updatedConfig

	c.Mu.Unlock()
	clientErr := c.client.UpdateProvider(provider)
	c.Mu.Lock()

	if clientErr != nil {
		if reflect.DeepEqual(c.Providers[provider], updatedConfig) {
			c.Providers[provider] = existingConfig
		}
		return fmt.Errorf("failed to update provider: %w", clientErr)
	}

	logger.Info("Added key %s to provider: %s", key.ID, provider)
	return nil
}

// UpdateProviderKey updates a single key on an existing provider configuration.
func (c *Config) UpdateProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, key schemas.Key) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	existingConfig, exists := c.Providers[provider]
	if !exists {
		return ErrNotFound
	}

	index := slices.IndexFunc(existingConfig.Keys, func(existingKey schemas.Key) bool {
		return existingKey.ID == keyID
	})
	if index == -1 {
		return ErrNotFound
	}

	updatedConfig := existingConfig
	updatedConfig.Keys = append([]schemas.Key(nil), existingConfig.Keys...)
	key.ID = keyID
	updatedConfig.Keys[index] = key

	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		if err := c.ConfigStore.UpdateProviderKey(ctx, provider, keyID, key); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return ErrNotFound
			}
			if errors.Is(err, configstore.ErrAlreadyExists) {
				return ErrAlreadyExists
			}
			return fmt.Errorf("failed to update provider key in store: %w", err)
		}
		// The vault store callback rewrites the secret into a vault reference
		// during the DB write, but only on the store-side row copy. Re-read so the
		// in-memory key (and API responses) carry FromVault/VaultRef instead of the
		// original plaintext.
		storedKey, err := c.ConfigStore.GetProviderKey(ctx, provider, keyID)
		if err != nil {
			// The DB write succeeded but we could not re-read the vault-rewritten
			// key. Failing here avoids committing the original plaintext into
			// c.Providers (and serving it via the keys API) on vault deployments.
			logger.Error("failed to re-read stored key %s for provider %s after update: %v", keyID, provider, err)
			return fmt.Errorf("failed to re-read provider key after update: %w", err)
		}
		updatedConfig.Keys[index] = *storedKey
	}

	c.Providers[provider] = updatedConfig

	c.Mu.Unlock()
	clientErr := c.client.UpdateProvider(provider)
	c.Mu.Lock()

	if clientErr != nil {
		if reflect.DeepEqual(c.Providers[provider], updatedConfig) {
			c.Providers[provider] = existingConfig
		}
		return fmt.Errorf("failed to update provider: %w", clientErr)
	}

	logger.Info("Updated key %s for provider: %s", keyID, provider)
	return nil
}

// RemoveProviderKey removes a single key from an existing provider configuration.
func (c *Config) RemoveProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	existingConfig, exists := c.Providers[provider]
	if !exists {
		return ErrNotFound
	}

	index := slices.IndexFunc(existingConfig.Keys, func(existingKey schemas.Key) bool {
		return existingKey.ID == keyID
	})
	if index == -1 {
		return ErrNotFound
	}

	updatedConfig := existingConfig
	updatedConfig.Keys = append([]schemas.Key(nil), existingConfig.Keys[:index]...)
	updatedConfig.Keys = append(updatedConfig.Keys, existingConfig.Keys[index+1:]...)

	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		if err := c.ConfigStore.DeleteProviderKey(ctx, provider, keyID); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("failed to delete provider key from store: %w", err)
		}
	}

	c.Providers[provider] = updatedConfig

	c.Mu.Unlock()
	clientErr := c.client.UpdateProvider(provider)
	c.Mu.Lock()

	if clientErr != nil {
		if reflect.DeepEqual(c.Providers[provider], updatedConfig) {
			c.Providers[provider] = existingConfig
		}
		return fmt.Errorf("failed to update provider: %w", clientErr)
	}

	logger.Info("Removed key %s from provider: %s", keyID, provider)
	return nil
}

// RemoveProvider removes a provider configuration from memory.
func (c *Config) RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	// Delete from DB first to avoid memory/DB inconsistency if DB delete fails
	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		if err := c.ConfigStore.DeleteProvider(ctx, provider); err != nil {
			return fmt.Errorf("failed to delete provider config from store: %w", err)
		}
	}
	if _, exists := c.Providers[provider]; !exists {
		return nil
	}
	delete(c.Providers, provider)
	logger.Info("Removed provider: %s", provider)
	return nil
}

// GetAllKeys returns the redacted keys
func (c *Config) GetAllKeys() ([]configstoreTables.TableKey, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	keys := make([]configstoreTables.TableKey, 0)
	for providerKey, provider := range c.Providers {
		for _, key := range provider.Keys {
			models := key.Models
			if models == nil {
				models = []string{}
			}
			blacklisted := key.BlacklistedModels
			if blacklisted == nil {
				blacklisted = []string{}
			}
			configStoreKey := configstoreTables.TableKey{
				KeyID:             key.ID,
				Name:              key.Name,
				Value:             *key.Value.Redacted(),
				Models:            models,
				BlacklistedModels: blacklisted,
				Weight:            bifrost.Ptr(key.Weight),
				Provider:          string(providerKey),
				ConfigHash:        key.ConfigHash,
			}
			if key.AzureKeyConfig != nil {
				cfg := *key.AzureKeyConfig // safe copy
				cfg.Endpoint = *cfg.Endpoint.Redacted()
				cfg.ClientID = cfg.ClientID.Redacted()
				cfg.ClientSecret = cfg.ClientSecret.Redacted()
				cfg.TenantID = cfg.TenantID.Redacted()
				configStoreKey.AzureKeyConfig = &cfg
			}
			if key.BedrockKeyConfig != nil {
				cfg := *key.BedrockKeyConfig // safe copy
				cfg.ARN = key.BedrockKeyConfig.ARN.Redacted()
				cfg.AccessKey = *cfg.AccessKey.Redacted()
				cfg.ExternalID = cfg.ExternalID.Redacted()
				cfg.Region = cfg.Region.Redacted()
				cfg.RoleARN = cfg.RoleARN.Redacted()
				cfg.RoleSessionName = cfg.RoleSessionName.Redacted()
				cfg.SecretKey = *cfg.SecretKey.Redacted()
				cfg.SessionToken = cfg.SessionToken.Redacted()
				configStoreKey.BedrockKeyConfig = &cfg
			}
			if key.BedrockMantleKeyConfig != nil {
				cfg := *key.BedrockMantleKeyConfig // safe copy
				cfg.AccessKey = *cfg.AccessKey.Redacted()
				cfg.SecretKey = *cfg.SecretKey.Redacted()
				cfg.SessionToken = cfg.SessionToken.Redacted()
				cfg.Region = cfg.Region.Redacted()
				cfg.RoleARN = cfg.RoleARN.Redacted()
				cfg.ExternalID = cfg.ExternalID.Redacted()
				cfg.RoleSessionName = cfg.RoleSessionName.Redacted()
				configStoreKey.BedrockMantleKeyConfig = &cfg
			}
			if key.VertexKeyConfig != nil {
				cfg := *key.VertexKeyConfig // safe copy
				cfg.ProjectID = *cfg.ProjectID.Redacted()
				cfg.ProjectNumber = *cfg.ProjectNumber.Redacted()
				cfg.Region = *cfg.Region.Redacted()
				cfg.AuthCredentials = *cfg.AuthCredentials.Redacted()
				configStoreKey.VertexKeyConfig = &cfg
			}
			if key.ReplicateKeyConfig != nil {
				configStoreKey.ReplicateKeyConfig = key.ReplicateKeyConfig
			}
			if key.VLLMKeyConfig != nil {
				cfg := *key.VLLMKeyConfig // safe copy
				cfg.URL = *cfg.URL.Redacted()
				configStoreKey.VLLMKeyConfig = &cfg
			}
			if key.OllamaKeyConfig != nil {
				cfg := *key.OllamaKeyConfig // safe copy
				cfg.URL = *cfg.URL.Redacted()
				configStoreKey.OllamaKeyConfig = &cfg
			}
			if key.SGLKeyConfig != nil {
				cfg := *key.SGLKeyConfig // safe copy
				cfg.URL = *cfg.URL.Redacted()
				configStoreKey.SGLKeyConfig = &cfg
			}
			keys = append(keys, configStoreKey)
		}
	}

	return keys, nil
}

// SetBifrostClient sets the Bifrost client in the store.
// This is used to allow the store to access the Bifrost client.
// This is useful for the MCP handler to access the Bifrost client.
func (c *Config) SetBifrostClient(client *bifrost.Bifrost) {
	c.muMCP.Lock()
	defer c.muMCP.Unlock()

	c.client = client
}

// GetMCPClient gets an MCP client configuration from the configuration.
// This method is called when an MCP client is reconnected via the HTTP API.
//
// Parameters:
//   - id: ID of the client to get
//
// Returns:
//   - *schemas.MCPClientConfig: The MCP client configuration (not redacted)
//   - error: Any retrieval error
func (c *Config) GetMCPClient(id string) (*schemas.MCPClientConfig, error) {
	c.muMCP.RLock()
	defer c.muMCP.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("bifrost client not set")
	}

	if c.MCPConfig == nil {
		return nil, fmt.Errorf("no MCP config found")
	}

	for _, clientConfig := range c.MCPConfig.ClientConfigs {
		if clientConfig.ID == id {
			return clientConfig, nil
		}
	}

	return nil, fmt.Errorf("MCP client '%s' not found", id)
}

// AddMCPClient adds a new MCP client to the configuration.
// This method is called when a new MCP client is added via the HTTP API.
//
// The method:
//   - Validates that the MCP client doesn't already exist
//   - Processes environment variables in the MCP client configuration
//   - Stores the processed configuration in memory
func (c *Config) AddMCPClient(ctx context.Context, clientConfig *schemas.MCPClientConfig) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}
	c.muMCP.Lock()
	defer c.muMCP.Unlock()
	if c.MCPConfig == nil {
		c.MCPConfig = &schemas.MCPConfig{}
	}
	// Track new environment variables
	c.MCPConfig.ClientConfigs = append(c.MCPConfig.ClientConfigs, clientConfig)
	// Config with processed env vars
	if err := c.client.AddMCPClient(ctx, clientConfig); err != nil {
		c.MCPConfig.ClientConfigs = c.MCPConfig.ClientConfigs[:len(c.MCPConfig.ClientConfigs)-1]
		return fmt.Errorf("failed to connect MCP client: %w", err)
	}
	// Update MCP catalog pricing data for the new client
	if c.MCPCatalog != nil && c.ConfigStore != nil {
		// Get the created client config from store to get tool_pricing
		dbClientConfig, err := c.ConfigStore.GetMCPClientByName(ctx, clientConfig.Name)
		if err != nil {
			logger.Warn("failed to get MCP client config for catalog update: %v", err)
		} else if dbClientConfig != nil {
			for toolName, costPerExecution := range dbClientConfig.ToolPricing {
				c.MCPCatalog.UpdatePricingData(dbClientConfig.Name, toolName, costPerExecution)
			}
			logger.Debug("updated MCP catalog pricing for client: %s (%d tools)", dbClientConfig.Name, len(dbClientConfig.ToolPricing))
		}
	}
	return nil
}

// UpdateMCPClient edits an MCP client configuration.
// This allows for dynamic MCP client management at runtime with proper env var handling.
//
// Parameters:
//   - id: ID of the client to edit
//   - updatedConfig: Updated MCP client configuration
func (c *Config) UpdateMCPClient(ctx context.Context, id string, updatedConfig *schemas.MCPClientConfig) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}
	c.muMCP.Lock()
	defer c.muMCP.Unlock()

	if c.MCPConfig == nil {
		return fmt.Errorf("no MCP config found")
	}
	// Find the existing client config
	var oldConfig *schemas.MCPClientConfig
	var found bool
	var configIndex int
	for i, clientConfig := range c.MCPConfig.ClientConfigs {
		if clientConfig.ID == id {
			oldConfig = clientConfig
			configIndex = i
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("MCP client '%s' not found", id)
	}
	oldDisabled := oldConfig.Disabled
	// Check if client is registered in Bifrost (can be not registered if client initialization failed)
	clientRegistered := false
	if clients, err := c.client.GetMCPClients(); err == nil && len(clients) > 0 {
		for _, client := range clients {
			if client.Config.ID == id {
				clientRegistered = true
				if err := c.client.UpdateMCPClient(id, updatedConfig); err != nil {
					// Rollback in-memory changes
					c.MCPConfig.ClientConfigs[configIndex] = oldConfig
					return fmt.Errorf("failed to edit MCP client: %w", err)
				}
				break
			}
		}
	}
	// Update MCP catalog pricing data for the edited client
	if c.MCPCatalog != nil {
		// If the client name has changed, delete all old pricing entries under the old name
		if updatedConfig.Name != oldConfig.Name {
			for toolName := range oldConfig.ToolPricing {
				c.MCPCatalog.DeletePricingData(oldConfig.Name, toolName)
			}
			logger.Debug("deleted old MCP catalog pricing for renamed client: %s -> %s (%d tools)", oldConfig.Name, updatedConfig.Name, len(oldConfig.ToolPricing))
		} else {
			// If name hasn't changed, remove pricing entries that were deleted
			for toolName := range oldConfig.ToolPricing {
				if _, exists := updatedConfig.ToolPricing[toolName]; !exists {
					c.MCPCatalog.DeletePricingData(updatedConfig.Name, toolName)
				}
			}
		}
		// Then, add or update pricing entries from the new config (with new name if changed)
		for toolName, costPerExecution := range updatedConfig.ToolPricing {
			c.MCPCatalog.UpdatePricingData(updatedConfig.Name, toolName, costPerExecution)
		}
		logger.Debug("updated MCP catalog pricing for client: %s (%d tools)", updatedConfig.Name, len(updatedConfig.ToolPricing))
	}
	// Update the in-memory configuration with only the fields that were changed
	// Preserve connection info (connection_type, connection_string, stdio_config) from oldConfig
	// as these are read-only and not sent in the update request
	c.MCPConfig.ClientConfigs[configIndex].Name = updatedConfig.Name
	c.MCPConfig.ClientConfigs[configIndex].IsCodeModeClient = updatedConfig.IsCodeModeClient
	c.MCPConfig.ClientConfigs[configIndex].Headers = updatedConfig.Headers
	c.MCPConfig.ClientConfigs[configIndex].ToolsToExecute = updatedConfig.ToolsToExecute
	c.MCPConfig.ClientConfigs[configIndex].ToolsToAutoExecute = updatedConfig.ToolsToAutoExecute
	c.MCPConfig.ClientConfigs[configIndex].AllowedExtraHeaders = updatedConfig.AllowedExtraHeaders
	c.MCPConfig.ClientConfigs[configIndex].ToolPricing = updatedConfig.ToolPricing
	c.MCPConfig.ClientConfigs[configIndex].IsPingAvailable = updatedConfig.IsPingAvailable
	c.MCPConfig.ClientConfigs[configIndex].ToolSyncInterval = updatedConfig.ToolSyncInterval
	c.MCPConfig.ClientConfigs[configIndex].ToolExecutionTimeout = updatedConfig.ToolExecutionTimeout
	c.MCPConfig.ClientConfigs[configIndex].AllowOnAllVirtualKeys = updatedConfig.AllowOnAllVirtualKeys
	c.MCPConfig.ClientConfigs[configIndex].Disabled = updatedConfig.Disabled
	c.MCPConfig.ClientConfigs[configIndex].PerUserHeaderKeys = updatedConfig.PerUserHeaderKeys

	// Handle disable/enable lifecycle when the Disabled flag toggles and the client
	// is registered at runtime. We call the core bifrost methods directly (not the
	// Config wrappers) to avoid a redundant DB write — the caller is responsible for
	// persisting the disabled flag to the DB before calling UpdateMCPClient.
	if oldDisabled != updatedConfig.Disabled && clientRegistered {
		if updatedConfig.Disabled {
			if err := c.client.DisableMCPClient(id); err != nil {
				// Rollback the in-memory Disabled flag so the runtime view stays
				// consistent with what the caller can observe.
				c.MCPConfig.ClientConfigs[configIndex].Disabled = oldDisabled
				return fmt.Errorf("failed to disable MCP client: %w", err)
			}
		} else {
			if err := c.client.EnableMCPClient(id); err != nil {
				c.MCPConfig.ClientConfigs[configIndex].Disabled = oldDisabled
				return fmt.Errorf("failed to enable MCP client: %w", err)
			}
		}
	}
	return nil
}

// UpdateMCPClientConnection updates the auth credentials (headers) for an existing MCP client.
// It delegates the actual reconnection (with the new credentials) to the Bifrost client.
func (c *Config) UpdateMCPClientConnection(ctx context.Context, id string, newConfig *schemas.MCPClientConfig) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}

	c.muMCP.RLock()
	if c.MCPConfig == nil {
		c.muMCP.RUnlock()
		return fmt.Errorf("in-memory MCPConfig absent; cannot update MCP client connection")
	}
	found := false
	for _, cc := range c.MCPConfig.ClientConfigs {
		if cc == nil {
			continue
		}
		if cc.ID == id {
			found = true
			break
		}
	}
	c.muMCP.RUnlock()
	if !found {
		return fmt.Errorf("MCP client %s not found in in-memory MCP config", id)
	}

	// Attempt the credential swap on the runtime side first.
	// If this fails, nothing in our in-memory config has changed.
	if err := c.client.UpdateMCPClientConnection(id, newConfig); err != nil {
		return fmt.Errorf("failed to update MCP client credentials: %w", err)
	}

	// Reconnect succeeded — mirror the new credentials into the in-memory config
	// so that subsequent reads of MCPConfig reflect the live state.
	c.muMCP.Lock()
	defer c.muMCP.Unlock()
	if c.MCPConfig != nil {
		found := false
		for _, cc := range c.MCPConfig.ClientConfigs {
			if cc == nil {
				continue
			}
			if cc.ID == id {
				found = true
				if newConfig.Headers != nil {
					cc.Headers = maps.Clone(newConfig.Headers)
				}
				if newConfig.OauthConfigID != nil {
					cc.OauthConfigID = newConfig.OauthConfigID
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("MCP client %s not found in in-memory config after successful reconnect", id)
		}
	}
	return nil
}

// RemoveMCPClient removes an MCP client from the configuration.
// This method is called when an MCP client is removed via the HTTP API.
//
// The method:
//   - Validates that the MCP client exists
//   - Removes the MCP client from the configuration
//   - Removes the MCP client from the Bifrost client
func (c *Config) RemoveMCPClient(ctx context.Context, id string) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}
	c.muMCP.Lock()
	defer c.muMCP.Unlock()
	if c.MCPConfig == nil {
		return fmt.Errorf("no MCP config found")
	}
	// Check if client is registered in Bifrost (can be not registered if client initialization failed)
	if clients, err := c.client.GetMCPClients(); err == nil && len(clients) > 0 {
		for _, client := range clients {
			if client.Config.ID == id {
				if err := c.client.RemoveMCPClient(id); err != nil {
					return fmt.Errorf("failed to remove MCP client: %w", err)
				}
				break
			}
		}
	}
	// Find and remove client from in-memory config
	for i, clientConfig := range c.MCPConfig.ClientConfigs {
		if clientConfig.ID == id {
			c.MCPConfig.ClientConfigs = append(c.MCPConfig.ClientConfigs[:i], c.MCPConfig.ClientConfigs[i+1:]...)
			break
		}
	}
	return nil
}

// DisableMCPClient persists disabled=true to the DB and shuts down the client's
// connection, health monitor, and tool syncer at runtime.
func (c *Config) DisableMCPClient(ctx context.Context, id string) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}

	if c.ConfigStore == nil {
		return fmt.Errorf("config store not set")
	}

	dbClient, err := c.ConfigStore.GetMCPClientByID(ctx, id)
	if err != nil {
		return fmt.Errorf("MCP client '%s' not found: %w", id, err)
	}
	if err := c.client.DisableMCPClient(id); err != nil {
		return fmt.Errorf("failed to disable MCP client: %w", err)
	}

	dbClient.Disabled = true
	if err := c.ConfigStore.UpdateMCPClientConfig(ctx, id, dbClient); err != nil {
		_ = c.client.EnableMCPClient(id) // rollback
		return fmt.Errorf("failed to persist disabled state: %w", err)
	}

	c.muMCP.Lock()
	defer c.muMCP.Unlock()
	if c.MCPConfig != nil {
		for _, cc := range c.MCPConfig.ClientConfigs {
			if cc.ID == id {
				cc.Disabled = true
				break
			}
		}
	}
	return nil
}

// EnableMCPClient persists disabled=false to the DB and reconnects the client
// at runtime, restarting its health monitor and tool syncer.
func (c *Config) EnableMCPClient(ctx context.Context, id string) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}
	if c.ConfigStore == nil {
		return fmt.Errorf("config store not set")
	}

	dbClient, err := c.ConfigStore.GetMCPClientByID(ctx, id)
	if err != nil {
		return fmt.Errorf("MCP client '%s' not found: %w", id, err)
	}
	dbClient.Disabled = false
	if err := c.ConfigStore.UpdateMCPClientConfig(ctx, id, dbClient); err != nil {
		return fmt.Errorf("failed to persist enabled state: %w", err)
	}

	c.muMCP.Lock()
	if c.MCPConfig != nil {
		for _, cc := range c.MCPConfig.ClientConfigs {
			if cc.ID == id {
				cc.Disabled = false
				break
			}
		}
	}
	c.muMCP.Unlock()

	return c.client.EnableMCPClient(id)
}

// RedactMCPClientConfig creates a redacted copy of a MCPClientConfig configuration.
// Connection strings and headers are redacted for safe external exposure.
func (c *Config) RedactMCPClientConfig(config *schemas.MCPClientConfig) *schemas.MCPClientConfig {
	// Create an actual copy of the struct (not just a pointer copy)
	// This prevents modifying the original config when redacting
	configCopy := *config

	// Redact connection string if present
	if config.ConnectionString != nil {
		configCopy.ConnectionString = config.ConnectionString.Redacted()
	}

	// Redact Header values if present
	if config.Headers != nil {
		configCopy.Headers = make(map[string]schemas.SecretVar, len(config.Headers))
		for header, value := range config.Headers {
			configCopy.Headers[header] = *value.Redacted()
		}
	}

	// Redact OAuth client credentials
	if config.OauthClientID != nil {
		configCopy.OauthClientID = config.OauthClientID.Redacted()
	}
	if config.OauthClientSecret != nil {
		configCopy.OauthClientSecret = config.OauthClientSecret.Redacted()
	}

	// Redact credentials inside the inline `oauth_config` bootstrap block.
	// Copy the struct first — configCopy shares the pointer with the live
	// config, and Redacted() returns a fresh SecretVar so the live stash is
	// never mutated.
	if config.PendingOAuthConfig != nil {
		pendingCopy := *config.PendingOAuthConfig
		pendingCopy.ClientID = pendingCopy.ClientID.Redacted()
		pendingCopy.ClientSecret = pendingCopy.ClientSecret.Redacted()
		configCopy.PendingOAuthConfig = &pendingCopy
	}

	// Redact TLS CA cert PEM if present
	if config.TLSConfig != nil {
		tlsCopy := *config.TLSConfig
		if config.TLSConfig.CACertPEM != nil {
			tlsCopy.CACertPEM = config.TLSConfig.CACertPEM.Redacted()
		}
		configCopy.TLSConfig = &tlsCopy
	}

	return &configCopy
}

// GetOAuth2SigningKey returns the OAuth2 signing key, loading it from the
// config store on first use and caching it for the process lifetime. The key is
// immutable once created (see the oauth2SigningKey field), so a process-lifetime
// cache is safe and lets the JWKS, token-issuance, and JWT-verify paths share a
// single load — skipping a DB read + private-key decrypt per request. Returns an
// error when no config store is wired.
func (c *Config) GetOAuth2SigningKey(ctx context.Context) (*configstoreTables.OAuth2SigningKey, error) {
	if k := c.oauth2SigningKey.Load(); k != nil {
		return k, nil
	}
	if c.ConfigStore == nil {
		return nil, fmt.Errorf("config store unavailable")
	}
	k, err := c.ConfigStore.GetOAuth2SigningKey(ctx)
	if err != nil {
		return nil, err
	}
	c.oauth2SigningKey.Store(k)
	return k, nil
}

// Webhook endpoint config is served from memory on the submit path and by the
// delivery worker, so neither performs a database read. Handlers keep it in
// lockstep with every database write.

// WebhookEndpointByID returns the endpoint for id, or false when it does not
// exist. Callers must treat the returned endpoint as read-only.
func (c *Config) WebhookEndpointByID(id string) (*configstoreTables.TableWebhookEndpoint, bool) {
	c.muWebhooks.RLock()
	defer c.muWebhooks.RUnlock()
	endpoint, ok := c.webhookEndpoints[id]
	return endpoint, ok
}

// WebhookEndpointByName returns the endpoint with the given unique name, or
// false when it does not exist. Callers must treat the returned endpoint as
// read-only.
func (c *Config) WebhookEndpointByName(name string) (*configstoreTables.TableWebhookEndpoint, bool) {
	c.muWebhooks.RLock()
	defer c.muWebhooks.RUnlock()
	id, ok := c.webhookEndpointsByName[name]
	if !ok {
		return nil, false
	}
	endpoint, ok := c.webhookEndpoints[id]
	return endpoint, ok
}

// SetWebhookEndpoint upserts an endpoint in the in-memory store, replacing
// any previous entry with the same ID (including a stale name-index entry
// after a rename). Called by handlers right after a successful database
// write, and by the config load.
func (c *Config) SetWebhookEndpoint(endpoint *configstoreTables.TableWebhookEndpoint) {
	if endpoint == nil || endpoint.ID == "" {
		return
	}
	copied := *endpoint
	c.muWebhooks.Lock()
	defer c.muWebhooks.Unlock()
	if c.webhookEndpoints == nil {
		c.webhookEndpoints = make(map[string]*configstoreTables.TableWebhookEndpoint)
		c.webhookEndpointsByName = make(map[string]string)
	}
	if previous, ok := c.webhookEndpoints[copied.ID]; ok && previous.Name != copied.Name {
		delete(c.webhookEndpointsByName, previous.Name)
	}
	c.webhookEndpoints[copied.ID] = &copied
	c.webhookEndpointsByName[copied.Name] = copied.ID
}

// RemoveWebhookEndpoint deletes an endpoint from the in-memory store. Called
// by handlers right after a successful database delete.
func (c *Config) RemoveWebhookEndpoint(id string) {
	c.muWebhooks.Lock()
	defer c.muWebhooks.Unlock()
	endpoint, ok := c.webhookEndpoints[id]
	if !ok {
		return
	}
	delete(c.webhookEndpointsByName, endpoint.Name)
	delete(c.webhookEndpoints, id)
}

// replaceWebhookEndpoints swaps the whole in-memory store for the given rows.
func (c *Config) replaceWebhookEndpoints(endpoints []configstoreTables.TableWebhookEndpoint) {
	byID := make(map[string]*configstoreTables.TableWebhookEndpoint, len(endpoints))
	byName := make(map[string]string, len(endpoints))
	for i := range endpoints {
		endpoint := endpoints[i]
		byID[endpoint.ID] = &endpoint
		byName[endpoint.Name] = endpoint.ID
	}
	c.muWebhooks.Lock()
	defer c.muWebhooks.Unlock()
	c.webhookEndpoints = byID
	c.webhookEndpointsByName = byName
}

// autoDetectProviders automatically detects common environment variables and sets up providers
// when no configuration file exists. This enables zero-config startup when users have set
// standard environment variables like OPENAI_API_KEY, ANTHROPIC_API_KEY, etc.
//
// Supported environment variables:
//   - OpenAI: OPENAI_API_KEY, OPENAI_KEY
//   - Anthropic: ANTHROPIC_API_KEY, ANTHROPIC_KEY
//   - Mistral: MISTRAL_API_KEY, MISTRAL_KEY
//
// For each detected provider, it creates a default configuration with:
//   - The detected API key with weight 1.0
//   - Empty models list (provider will use default models)
//   - Default concurrency and buffer size settings
func (c *Config) autoDetectProviders(ctx context.Context) {
	// Define common environment variable patterns for each provider
	providerEnvVars := map[schemas.ModelProvider][]string{
		schemas.OpenAI:    {"OPENAI_API_KEY", "OPENAI_KEY"},
		schemas.Anthropic: {"ANTHROPIC_API_KEY", "ANTHROPIC_KEY"},
		schemas.Mistral:   {"MISTRAL_API_KEY", "MISTRAL_KEY"},
	}

	detectedCount := 0

	for provider, envVars := range providerEnvVars {
		for _, envVar := range envVars {
			if os.Getenv(envVar) != "" {
				// Generate a unique ID for the auto-detected key
				keyID := uuid.NewString()
				// Create default provider configuration
				providerConfig := configstore.ProviderConfig{
					Keys: []schemas.Key{
						{
							ID:     keyID,
							Name:   fmt.Sprintf("%s_auto_detected", envVar),
							Value:  *schemas.NewSecretVar("env." + envVar),
							Models: schemas.WhiteList{"*"},
							Weight: 1.0,
						},
					},
					ConcurrencyAndBufferSize: &schemas.DefaultConcurrencyAndBufferSize,
				}
				// Add to providers map
				c.Providers[provider] = providerConfig
				logger.Info("auto-detected %s provider from environment variable %s", provider, envVar)
				detectedCount++
				break // Only use the first found env var for each provider
			}
		}
	}
	if detectedCount > 0 {
		logger.Info("auto-configured %d provider(s) from environment variables", detectedCount)
		if c.ConfigStore != nil {
			if err := c.ConfigStore.UpdateProvidersConfig(ctx, c.Providers); err != nil {
				logger.Error("failed to update providers in store: %v", err)
			}
		}
	}
}

// GetVectorStoreConfigRedacted retrieves the vector store configuration with password redacted for safe external exposure
func (c *Config) GetVectorStoreConfigRedacted(ctx context.Context) (*vectorstore.Config, error) {
	var err error
	var vectorStoreConfig *vectorstore.Config
	if c.ConfigStore != nil {
		vectorStoreConfig, err = c.ConfigStore.GetVectorStoreConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get vector store config: %w", err)
		}
	}
	if vectorStoreConfig == nil {
		return nil, nil
	}
	if vectorStoreConfig.Type == vectorstore.VectorStoreTypeWeaviate {
		weaviateConfig, ok := vectorStoreConfig.Config.(*vectorstore.WeaviateConfig)
		if !ok {
			return nil, fmt.Errorf("failed to cast vector store config to weaviate config")
		}
		// Create a copy to avoid modifying the original
		redactedWeaviateConfig := *weaviateConfig
		// Redact password if it exists
		if redactedWeaviateConfig.APIKey != nil {
			redactedWeaviateConfig.APIKey = redactedWeaviateConfig.APIKey.Redacted()
		}
		redactedVectorStoreConfig := *vectorStoreConfig
		redactedVectorStoreConfig.Config = &redactedWeaviateConfig
		return &redactedVectorStoreConfig, nil
	}
	return nil, nil
}

// ValidateCustomProvider validates the custom provider configuration
func ValidateCustomProvider(config configstore.ProviderConfig, provider schemas.ModelProvider) error {
	if config.CustomProviderConfig == nil {
		return nil
	}

	if bifrost.IsStandardProvider(provider) {
		return fmt.Errorf("custom provider validation failed: cannot be created on standard providers: %s", provider)
	}

	cpc := config.CustomProviderConfig

	// Validate base provider type
	if cpc.BaseProviderType == "" {
		return fmt.Errorf("custom provider validation failed: base_provider_type is required")
	}

	// Check if base provider is a supported base provider
	if !bifrost.IsSupportedBaseProvider(cpc.BaseProviderType) {
		return fmt.Errorf("custom provider validation failed: unsupported base_provider_type: %s", cpc.BaseProviderType)
	}

	// Reject Bedrock providers with IsKeyLess=true
	if cpc.BaseProviderType == schemas.Bedrock && cpc.IsKeyLess {
		return fmt.Errorf("custom provider validation failed: Bedrock providers cannot be keyless (is_key_less=true)")
	}

	return nil
}

// ValidateCustomProviderUpdate validates that immutable fields in CustomProviderConfig are not changed during updates
func ValidateCustomProviderUpdate(newConfig, existingConfig configstore.ProviderConfig, provider schemas.ModelProvider) error {
	// If neither config has CustomProviderConfig, no validation needed
	if newConfig.CustomProviderConfig == nil && existingConfig.CustomProviderConfig == nil {
		return nil
	}

	// If new config doesn't have CustomProviderConfig but existing does, return an error
	if newConfig.CustomProviderConfig == nil {
		return fmt.Errorf("custom_provider_config cannot be removed after creation for provider %s", provider)
	}

	// If existing config doesn't have CustomProviderConfig but new one does, that's fine (adding it)
	if existingConfig.CustomProviderConfig == nil {
		return ValidateCustomProvider(newConfig, provider)
	}

	// Both configs have CustomProviderConfig, validate immutable fields
	newCPC := newConfig.CustomProviderConfig
	existingCPC := existingConfig.CustomProviderConfig

	// CustomProviderKey is internally set and immutable, no validation needed

	// Check if BaseProviderType is being changed
	if newCPC.BaseProviderType != existingCPC.BaseProviderType {
		return fmt.Errorf("provider %s: base_provider_type cannot be changed from %s to %s after creation",
			provider, existingCPC.BaseProviderType, newCPC.BaseProviderType)
	}

	// Validate the new config (this will catch Bedrock+IsKeyLess configurations)
	if err := ValidateCustomProvider(newConfig, provider); err != nil {
		return err
	}

	return nil
}

func (c *Config) ValidateSemanticCacheConfig(config *schemas.PluginConfig) error {
	if config.Name != semanticcache.PluginName {
		return nil
	}

	// Check if config.Config exists
	if config.Config == nil {
		return fmt.Errorf("semantic_cache plugin config is nil")
	}

	// Type assert config.Config to map[string]interface{}
	configMap, ok := config.Config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("semantic_cache plugin config must be a map, got %T", config.Config)
	}

	dimension, hasDimension, err := semanticCacheConfigDimension(configMap)
	if err != nil {
		return err
	}

	// Check if provider key exists and is a string
	providerVal, exists := configMap["provider"]
	if !exists {
		if hasDimension && dimension == 1 {
			delete(configMap, "keys")
			delete(configMap, "embedding_model")
			return nil
		}
		return fmt.Errorf("semantic_cache plugin requires 'provider' for semantic mode (dimension > 1). For direct-only mode, set dimension: 1 and omit provider")
	}

	provider, ok := providerVal.(string)
	if !ok {
		return fmt.Errorf("semantic_cache plugin 'provider' field must be a string, got %T", providerVal)
	}
	provider = strings.TrimSpace(provider)
	configMap["provider"] = provider

	if provider == "" {
		if hasDimension && dimension == 1 {
			delete(configMap, "provider")
			delete(configMap, "keys")
			delete(configMap, "embedding_model")
			return nil
		}
		return fmt.Errorf("semantic_cache plugin requires a non-empty 'provider' for semantic mode (dimension > 1). For direct-only mode, set dimension: 1 and omit provider")
	}
	if !hasDimension {
		return fmt.Errorf("semantic_cache plugin requires 'dimension' for provider-backed semantic mode. For direct-only mode, set dimension: 1 and omit provider")
	}
	if dimension <= 1 {
		return fmt.Errorf("semantic_cache plugin requires 'dimension' > 1 when 'provider' is set. Use dimension: 1 only for direct-only mode without a provider")
	}

	embeddingModelVal, exists := configMap["embedding_model"]
	if !exists {
		return fmt.Errorf("semantic_cache plugin requires 'embedding_model' when 'provider' is set")
	}
	embeddingModel, ok := embeddingModelVal.(string)
	if !ok {
		return fmt.Errorf("semantic_cache plugin 'embedding_model' field must be a string, got %T", embeddingModelVal)
	}
	embeddingModel = strings.TrimSpace(embeddingModel)
	if embeddingModel == "" {
		return fmt.Errorf("semantic_cache plugin requires a non-empty 'embedding_model' when 'provider' is set")
	}
	configMap["embedding_model"] = embeddingModel

	// Validate that the provider is configured in the global client (keys are inherited automatically).
	if _, err := c.GetProviderConfigRaw(schemas.ModelProvider(provider)); err != nil {
		return fmt.Errorf("failed to get provider config for %s: %w", provider, err)
	}

	return nil
}

func semanticCacheConfigDimension(configMap map[string]interface{}) (int, bool, error) {
	dimensionVal, exists := configMap["dimension"]
	if !exists {
		return 0, false, nil
	}

	switch v := dimensionVal.(type) {
	case int:
		if v < 1 {
			return 0, false, fmt.Errorf("semantic_cache plugin 'dimension' must be >= 1, got %d", v)
		}
		return v, true, nil
	case int32:
		if v < 1 {
			return 0, false, fmt.Errorf("semantic_cache plugin 'dimension' must be >= 1, got %d", v)
		}
		return int(v), true, nil
	case int64:
		if v < 1 {
			return 0, false, fmt.Errorf("semantic_cache plugin 'dimension' must be >= 1, got %d", v)
		}
		return int(v), true, nil
	case float64:
		if v != math.Trunc(v) {
			return 0, false, fmt.Errorf("semantic_cache plugin 'dimension' field must be an integer, got %v", v)
		}
		if v < 1 {
			return 0, false, fmt.Errorf("semantic_cache plugin 'dimension' must be >= 1, got %v", v)
		}
		return int(v), true, nil
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, false, fmt.Errorf("semantic_cache plugin 'dimension' field must be an integer, got %q", v)
		}
		if parsed < 1 {
			return 0, false, fmt.Errorf("semantic_cache plugin 'dimension' must be >= 1, got %d", parsed)
		}
		return int(parsed), true, nil
	default:
		return 0, false, fmt.Errorf("semantic_cache plugin 'dimension' field must be numeric, got %T", dimensionVal)
	}
}

func DeepCopy[T any](in T) (T, error) {
	var out T
	b, err := sonic.Marshal(in)
	if err != nil {
		return out, err
	}
	err = sonic.Unmarshal(b, &out)
	return out, err
}