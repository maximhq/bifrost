package modelcatalog

import "time"

const (
	DefaultPricingSyncInterval = 24 * time.Hour
	ConfigLastPricingSyncKey   = "LastModelPricingSync"
	DefaultPricingURL          = "https://getbifrost.ai/datasheet"
	DefaultPricingTimeout      = 45 * time.Second
)

// Config is the model pricing configuration.
type Config struct {
	PricingURL          *string        `json:"pricing_url,omitempty"`
	PricingSyncInterval *time.Duration `json:"pricing_sync_interval,omitempty"`
}
