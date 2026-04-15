package modelcatalog

import (
	"time"
)

const (
	DefaultPricingSyncInterval    = 24 * time.Hour
	MinimumPricingSyncIntervalSec = int64(3600)

	// syncWorkerTickerPeriod is the fixed interval at which the background sync worker
	// wakes up to check whether a sync is due. This is independent of pricingSyncInterval —
	// the ticker defines the check granularity, not the sync frequency.
	// Setting pricingSyncInterval below this value has no effect on actual sync frequency.
	syncWorkerTickerPeriod = 1 * time.Hour

	ConfigLastPricingSyncKey      = "LastModelPricingSync"
	ConfigLastParamsSyncKey       = "LastModelParametersSync"
	DefaultPricingURL             = "https://getbifrost.ai/datasheet"
	DefaultModelParametersURL     = "https://getbifrost.ai/datasheet/model-parameters"
	DefaultPricingTimeout         = 45 * time.Second
	DefaultModelParametersTimeout = 45 * time.Second
)

// Config is the model pricing configuration.
type Config struct {
	PricingURL          *string `json:"pricing_url,omitempty"`
	PricingSyncInterval *int64  `json:"pricing_sync_interval,omitempty"` // seconds
}
