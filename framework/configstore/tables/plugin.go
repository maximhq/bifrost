package tables

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TablePlugin represents a plugin configuration in the database

type TablePlugin struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name       string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"name"`
	Enabled    bool      `json:"enabled"`
	Path       *string   `json:"path,omitempty"`
	ConfigJSON string    `gorm:"type:text" json:"-"` // JSON serialized plugin.Config
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
	Version    int16     `gorm:"not null;default:1" json:"version"`
	UpdatedAt  time.Time `gorm:"index;not null" json:"updated_at"`
	IsCustom   bool      `gorm:"not null;default:false" json:"isCustom"`

	Placement *schemas.PluginPlacement `gorm:"column:placement;type:varchar(20);null" json:"placement,omitempty"`
	Order     *int    `gorm:"column:exec_order;type:int;null" json:"order,omitempty"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	EncryptionStatus string `gorm:"type:varchar(20);default:'plain_text'" json:"-"`

	// Virtual fields for runtime use (not stored in DB)
	Config any `gorm:"-" json:"config,omitempty"`
}

// TableName sets the table name for each model
func (TablePlugin) TableName() string { return "config_plugins" }

// ErrUnresolvedEnvVars is returned when a plugin config references environment variables
// that are not set in the process environment at save time.
type ErrUnresolvedEnvVars struct {
	Vars []string
}

func (e *ErrUnresolvedEnvVars) Error() string {
	return fmt.Sprintf("environment variables not set: %s", strings.Join(e.Vars, ", "))
}

// normalizePluginConfigForStorage recursively walks a plugin config map and converts
// any EnvVar-shaped object ({value, env_var, from_env}) into a plain string —
// either the literal value or the "env.VAR_NAME" token. This keeps stored JSON
// consistent with the plain-string format used by config.json and ProxyConfig.MarshalForStorage.
func normalizePluginConfigForStorage(v any) any {
	switch val := v.(type) {
	case map[string]any:
		// Detect an EnvVar object: presence of any EnvVar key is sufficient — sparse
		// objects (e.g. only from_env+env_var, without value) are valid.
		_, hasValue := val["value"]
		_, hasEnvVar := val["env_var"]
		_, hasFromEnv := val["from_env"]
		if hasValue || hasEnvVar || hasFromEnv {
			if fromEnv, ok := val["from_env"].(bool); ok && fromEnv {
				if envVar, ok := val["env_var"].(string); ok && envVar != "" {
					return envVar // "env.VAR_NAME"
				}
			}
			if value, ok := val["value"].(string); ok {
				return value
			}
		}
		// Regular nested map — recurse
		result := make(map[string]any, len(val))
		for k, child := range val {
			result[k] = normalizePluginConfigForStorage(child)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = normalizePluginConfigForStorage(item)
		}
		return result
	default:
		return v
	}
}

// checkEnvVarsResolved walks a normalized plugin config (after normalizePluginConfigForStorage)
// and returns any "env.VAR_NAME" references whose env var is not set in the process environment.
func checkEnvVarsResolved(v any) []string {
	var unresolved []string
	switch val := v.(type) {
	case string:
		if strings.HasPrefix(val, "env.") {
			envKey := strings.TrimPrefix(val, "env.")
			if _, ok := os.LookupEnv(envKey); !ok {
				unresolved = append(unresolved, val)
			}
		}
	case map[string]any:
		for _, child := range val {
			unresolved = append(unresolved, checkEnvVarsResolved(child)...)
		}
	case []any:
		for _, item := range val {
			unresolved = append(unresolved, checkEnvVarsResolved(item)...)
		}
	}
	return unresolved
}

// BeforeSave is a GORM hook that serializes the plugin Config into a JSON column and
// encrypts it before writing to the database. Empty configs ("{}") are not encrypted.
func (p *TablePlugin) BeforeSave(tx *gorm.DB) error {
	if p.Config != nil {
		// Normalize any EnvVar-shaped objects to plain strings before marshaling
		// so the DB always stores "env.VAR_NAME" or literal values, not JSON objects.
		if configMap, ok := p.Config.(map[string]any); ok {
			p.Config = normalizePluginConfigForStorage(configMap)
		}
		if unresolved := checkEnvVarsResolved(p.Config); len(unresolved) > 0 {
			return &ErrUnresolvedEnvVars{Vars: unresolved}
		}
		data, err := json.Marshal(p.Config)
		if err != nil {
			return err
		}
		p.ConfigJSON = string(data)
	} else {
		p.ConfigJSON = "{}"
	}

	// Encrypt config after serialization
	if encrypt.IsEnabled() && p.ConfigJSON != "" && p.ConfigJSON != "{}" {
		encrypted, err := encrypt.Encrypt(p.ConfigJSON)
		if err != nil {
			return fmt.Errorf("failed to encrypt plugin config: %w", err)
		}
		p.ConfigJSON = encrypted
		p.EncryptionStatus = EncryptionStatusEncrypted
	}

	return nil
}

// denormalizePluginConfigFromStorage is the inverse of normalizePluginConfigForStorage.
// It converts plain "env.VAR_NAME" strings back into {value, env_var, from_env} objects
// so the API response carries the same shape as provider key EnvVar fields.
func denormalizePluginConfigFromStorage(v any) any {
	switch val := v.(type) {
	case string:
		if strings.HasPrefix(val, "env.") {
			redacted := schemas.NewEnvVar(val).Redacted()
			return map[string]any{"value": redacted.Val, "env_var": redacted.EnvVar, "from_env": redacted.FromEnv}
		}
		return val
	case map[string]any:
		result := make(map[string]any, len(val))
		for k, child := range val {
			result[k] = denormalizePluginConfigFromStorage(child)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = denormalizePluginConfigFromStorage(item)
		}
		return result
	default:
		return v
	}
}

// AfterFind is a GORM hook that decrypts the plugin config JSON (if encrypted) and
// deserializes it back into the runtime Config field after reading from the database.
func (p *TablePlugin) AfterFind(tx *gorm.DB) error {
	if p.EncryptionStatus == "encrypted" && p.ConfigJSON != "" {
		decrypted, err := encrypt.Decrypt(p.ConfigJSON)
		if err != nil {
			return fmt.Errorf("failed to decrypt plugin config: %w", err)
		}
		p.ConfigJSON = decrypted
	}
	if p.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(p.ConfigJSON), &p.Config); err != nil {
			return err
		}
		if configMap, ok := p.Config.(map[string]any); ok {
			p.Config = denormalizePluginConfigFromStorage(configMap)
		}
	} else {
		p.Config = nil
	}

	return nil
}
