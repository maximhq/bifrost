package tables

import "time"

// TableRoutingPolicy defines an adaptive routing policy for provider/model selection.
type TableRoutingPolicy struct {
	ID               string    `gorm:"primaryKey;type:text"`
	Name             string    `gorm:"uniqueIndex;not null"`
	Enabled          bool      `gorm:"default:true"`
	Strategy         string    `gorm:"type:text"` // "latency_optimized"|"cost_optimized"|"quality_optimized"|"availability_optimized"|"balanced"|"canary"
	VirtualKeyID     string    `gorm:"index"`     // empty = global
	WeightLatency    float64   `gorm:"default:0.25"`
	WeightCost       float64   `gorm:"default:0.25"`
	WeightQuality    float64   `gorm:"default:0.25"`
	WeightAvail      float64   `gorm:"default:0.25"`
	MaxLatencyMs     *float64
	MaxErrorRatePct  *float64
	MinQualityScore  *float64
	CanaryConfigJSON string    `gorm:"type:text"` // JSON: [{provider, model, pct}]
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (TableRoutingPolicy) TableName() string { return "routing_policies" }

// TableProviderMetrics stores aggregated provider/model performance metrics per time window.
type TableProviderMetrics struct {
	ID            string    `gorm:"primaryKey;type:text"`
	Provider      string    `gorm:"index;not null"`
	Model         string    `gorm:"index;not null"`
	WindowStart   time.Time `gorm:"index"`
	WindowMinutes int       // 5|60|1440 (5m, 1h, 24h)
	P50Ms         float64
	P95Ms         float64
	P99Ms         float64
	ErrorRatePct  float64
	TotalRequests int64
	TotalCost     float64
	AvgTokens     float64
	UpdatedAt     time.Time
}

func (TableProviderMetrics) TableName() string { return "provider_metrics" }

// TableModelQualityScore records quality scores per provider/model from feedback or benchmarks.
type TableModelQualityScore struct {
	Provider  string    `gorm:"primaryKey;type:text"`
	Model     string    `gorm:"primaryKey;type:text"`
	Score     float64   // 0.0–1.0
	Source    string    `gorm:"type:text"` // "manual"|"feedback"|"benchmark"
	UpdatedAt time.Time
}

func (TableModelQualityScore) TableName() string { return "model_quality_scores" }
