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
	// OpenAPIURL is the OpenAPI spec /v1/mcp builds its tools from: an http(s) URL
	// or a path under the app dir. Empty/nil falls back to the build's default spec.
	OpenAPIURL *string `gorm:"column:openapi_url;type:text" json:"openapi_url"`
	ConfigHash string  `gorm:"type:text" json:"config_hash"`
	// PricingURLPublicOnly, ModelParametersURLPublicOnly and
	// MCPLibraryURLPublicOnly record how the matching URL was set. True: it was
	// saved through the API, which accepts only a public http(s) address, so
	// every sync fetches it under that same public-address rule. False: it came
	// from config.json (or is the built-in default), where a private-network
	// host and file:// are allowed. This is stored state, not configuration, so
	// it is left out of JSON: a client can neither read nor send it.
	PricingURLPublicOnly         bool `gorm:"column:pricing_url_public_only;not null;default:false" json:"-"`
	ModelParametersURLPublicOnly bool `gorm:"column:model_parameters_url_public_only;not null;default:false" json:"-"`
	MCPLibraryURLPublicOnly      bool `gorm:"column:mcp_library_url_public_only;not null;default:false" json:"-"`
}

// TableName sets the table name for each model
func (TableFrameworkConfig) TableName() string { return "framework_configs" }
