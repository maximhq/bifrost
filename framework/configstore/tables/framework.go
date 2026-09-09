package tables

// TableFrameworkConfig represents the framework configurations
// We will keep on adding different columns here as we add new features to the framework
type TableFrameworkConfig struct {
	ID                  uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	PricingURL          *string `gorm:"type:text" json:"pricing_url"`
	PricingSyncInterval *int64  `gorm:"" json:"pricing_sync_interval"`
	ModelParametersURL  *string `gorm:"type:text" json:"model_parameters_url"`
	// MCPLibraryURL is the endpoint the MCP server library catalog is synced
	// from. Empty/nil falls back to modelcatalog.DefaultMCPLibraryURL. Mirrors
	// PricingURL: the default ships out of the box and the user can override it.
	MCPLibraryURL          *string `gorm:"type:text" json:"mcp_library_url"`
	MCPLibrarySyncInterval *int64  `gorm:"" json:"mcp_library_sync_interval"`
	// LiveModelsSyncInterval is how often each provider's list-models response
	// is re-fetched in the background, in seconds. 0 disables the refresher.
	LiveModelsSyncInterval *int64 `gorm:"" json:"live_models_sync_interval"`
	ConfigHash             string `gorm:"type:text" json:"config_hash"`
}

// TableName sets the table name for each model
func (TableFrameworkConfig) TableName() string { return "framework_configs" }
