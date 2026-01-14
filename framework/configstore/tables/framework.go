package tables

// TableFrameworkConfig represents the framework configurations
// We will keep on adding different columns here as we add new features to the framework
type TableFrameworkConfig struct {
	ID                  uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	PricingURL          *string `gorm:"type:text" json:"pricing_url"`
	PricingSyncInterval *int64  `gorm:"" json:"pricing_sync_interval"`
}

// TableName sets the table name for each model
func (TableFrameworkConfig) TableName() string { return "framework_configs" }
