package configstore

var (
	ConfigStoreTypeSqlite string = "sqlite"
)

// Config represents the configuration for the config store.
type Config struct {
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"`
	Config  any    `json:"config"`
}
