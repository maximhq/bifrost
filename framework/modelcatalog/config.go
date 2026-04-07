package modelcatalog

import "time"

const (
	DefaultSyncInterval           = 24 * time.Hour
	DefaultPricingURL             = "https://getbifrost.ai/datasheet"
	DefaultModelParametersURL     = "https://getbifrost.ai/datasheet/model-parameters"
	DefaultPricingTimeout         = 45 * time.Second
	DefaultModelParametersTimeout = 45 * time.Second
)

// Config is the model pricing configuration.
type Config struct {
	PricingURL          *string        `json:"pricing_url,omitempty"`
	PricingSyncInterval *time.Duration `json:"pricing_sync_interval,omitempty"`
}
