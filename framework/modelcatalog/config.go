package modelcatalog

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
)

const (
	DefaultSyncInterval           = datasheet.DefaultSyncInterval
	MinimumPricingSyncIntervalSec = int64(3600)

	// DefaultLiveModelsSyncInterval is how often the gateway re-fetches each
	// provider's list-models response in the background.
	DefaultLiveModelsSyncInterval = 60 * time.Minute
	// MinimumLiveModelsSyncIntervalSec is the floor for a non-zero live models
	// sync interval. Deliberately far below MinimumPricingSyncIntervalSec:
	// pricing is a single shared datasheet fetch on a 24h cadence, whereas an
	// operator fronting a fast-moving aggregator may legitimately want model
	// discovery to run every few minutes.
	MinimumLiveModelsSyncIntervalSec = int64(60)
	// LiveModelsSyncDisabled is the sentinel for "never refresh in the
	// background". Distinct from a negative value, which is treated as
	// corrupted config and falls back to the default.
	LiveModelsSyncDisabled = int64(0)

	// MCPLibrarySyncDisabled is the sentinel for "never sync the MCP server
	// catalog in the background". Mirrors LiveModelsSyncDisabled: 0 is a
	// deliberate opt-out, a negative value is corrupted config that falls back
	// to the default. Set this on an air-gapped deployment that does not ship a
	// local catalog file, so the gateway stops dialing the default endpoint on
	// every tick. An explicit force-sync from the UI still runs.
	MCPLibrarySyncDisabled = int64(0)

	ConfigLastPricingSyncKey    = "LastModelPricingSync"
	ConfigLastParamsSyncKey     = "LastModelParametersSync"
	ConfigLastMCPLibrarySyncKey = "LastMCPLibrarySync"
)

// Config is the model pricing configuration.
type Config struct {
	PricingURL          *string `json:"pricing_url,omitempty"`
	PricingSyncInterval *int64  `json:"pricing_sync_interval,omitempty"` // seconds
	ModelParametersURL  *string `json:"model_parameters_url,omitempty"`

	// MCPLibraryURL overrides the endpoint the MCP server library catalog is
	// synced from. Empty/nil uses DefaultMCPLibraryURL. Mirrors PricingURL: the
	// default ships out of the box and the user can point it at a custom source.
	MCPLibraryURL          *string `json:"mcp_library_url,omitempty"`
	MCPLibrarySyncInterval *int64  `json:"mcp_library_sync_interval,omitempty"` // seconds

	// LiveModelsSyncInterval is how often each provider's list-models response
	// is re-fetched in the background, in seconds. Nil uses
	// DefaultLiveModelsSyncInterval; LiveModelsSyncDisabled (0) turns the
	// background refresher off entirely.
	//
	// Unlike the sync intervals above, nothing in this package acts on it: the
	// live model cache is per-process and the refresh has to go through the
	// Bifrost routing pipeline, so the HTTP server owns that ticker. This field
	// is the transport's configuration channel, carried here to keep every
	// catalog cadence in one place.
	LiveModelsSyncInterval *int64 `json:"live_models_sync_interval,omitempty"` // seconds
}

// Type re-exports so external callers can continue importing the legacy
// names (PricingEntry, PricingOptions, etc.) without changing imports.
// Internally these live in the datasheet / keyconfig subpackages.
type (
	PricingEntry        = datasheet.Entry
	PricingOptions      = datasheet.Options
	PricingOverride     = datasheet.Override
	PricingLookupScopes = datasheet.LookupScopes
	ScopeKind           = datasheet.ScopeKind
	MatchType           = datasheet.MatchType

	CatalogPricingOverrides = datasheet.CatalogPricingOverrides

	KeyConfigEntry = keyconfig.KeyEntry
	AliasOwner     = keyconfig.AliasOwner
)

// Scope kind constants re-exported for callers that compare by value.
const (
	ScopeKindGlobal                = datasheet.ScopeKindGlobal
	ScopeKindProvider              = datasheet.ScopeKindProvider
	ScopeKindProviderKey           = datasheet.ScopeKindProviderKey
	ScopeKindVirtualKey            = datasheet.ScopeKindVirtualKey
	ScopeKindVirtualKeyProvider    = datasheet.ScopeKindVirtualKeyProvider
	ScopeKindVirtualKeyProviderKey = datasheet.ScopeKindVirtualKeyProviderKey
	ScopeKindUser                  = datasheet.ScopeKindUser
	ScopeKindUserProvider          = datasheet.ScopeKindUserProvider
	ScopeKindUserProviderKey       = datasheet.ScopeKindUserProviderKey

	MatchTypeExact    = datasheet.MatchTypeExact
	MatchTypeWildcard = datasheet.MatchTypeWildcard
)

// PricingLookupScopesFromContext is re-exported so callers don't have to
// change their imports.
func PricingLookupScopesFromContext(ctx *schemas.BifrostContext, provider string) *PricingLookupScopes {
	return datasheet.LookupScopesFromContext(ctx, provider)
}

// Sync timing defaults re-exported from datasheet for consumers of the
// historical constants.
const (
	DefaultPricingURL             = datasheet.DefaultURL
	DefaultModelParametersURL     = datasheet.DefaultModelParametersURL
	DefaultPricingTimeout         = datasheet.DefaultPricingTimeout
	DefaultModelParametersTimeout = datasheet.DefaultModelParametersTimeout

	DefaultMCPLibraryURL     = "https://getbifrost.ai/mcp-library"
	DefaultMCPLibraryTimeout = 45 * time.Second
)

// syncWorkerTickerPeriod is the fixed interval at which the background sync worker
// wakes up to check whether a sync is due. This is independent of pricingSyncInterval —
// the ticker defines the check granularity, not the sync frequency.
// Kept well below MinimumPricingSyncIntervalSec so the threshold check is not
// defeated by ticker drift when pricingSyncInterval is set near the minimum.
const syncWorkerTickerPeriod = 5 * time.Minute
