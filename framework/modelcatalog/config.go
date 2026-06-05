package modelcatalog

import (
	"time"
)

const (
	DefaultSyncInterval           = 24 * time.Hour
	MinimumPricingSyncIntervalSec = int64(3600)

	// syncWorkerTickerPeriod is the fixed interval at which the background sync worker
	// wakes up to check whether a sync is due. This is independent of pricingSyncInterval —
	// the ticker defines the check granularity, not the sync frequency.
	// Kept well below MinimumPricingSyncIntervalSec so the threshold check is not
	// defeated by ticker drift when pricingSyncInterval is set near the minimum.
	syncWorkerTickerPeriod = 5 * time.Minute

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
	ModelParametersURL  *string `json:"model_parameters_url,omitempty"`
}
