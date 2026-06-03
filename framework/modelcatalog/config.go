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

	ConfigLastPricingSyncKey = "LastModelPricingSync"
	ConfigLastParamsSyncKey  = "LastModelParametersSync"
)

// Config is the model pricing configuration.
type Config struct {
	PricingURL          *string `json:"pricing_url,omitempty"`
	PricingSyncInterval *int64  `json:"pricing_sync_interval,omitempty"` // seconds
	ModelParametersURL  *string `json:"model_parameters_url,omitempty"`
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
)

// syncWorkerTickerPeriod is the fixed interval at which the background sync worker
// wakes up to check whether a sync is due. This is independent of pricingSyncInterval —
// the ticker defines the check granularity, not the sync frequency.
// Kept well below MinimumPricingSyncIntervalSec so the threshold check is not
// defeated by ticker drift when pricingSyncInterval is set near the minimum.
const syncWorkerTickerPeriod = 5 * time.Minute
