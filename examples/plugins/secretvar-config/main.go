package main

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
)

// apiKeyHeader is the header this plugin stamps onto outgoing requests, using
// the resolved secret - proof that Init already has the real value (not the
// "env."/"vault." reference) by the time hooks run.
const apiKeyHeader = "x-secretvar-plugin-key"

// Config is this plugin's typed configuration. APIKey is a *schemas.SecretVar
// instead of a plain string, so config.json / the plugins API can set it to:
//
//	"api_key": "sk-literal-value"        -> plain text
//	"api_key": "env.MY_PLUGIN_API_KEY"   -> resolved from the environment
//	"api_key": "vault.path/to/secret"    -> resolved from vault (enterprise)
//
// Resolution needs no extra code: schemas.SecretVar implements
// json.Unmarshaler, so it happens the moment raw config is unmarshaled into
// this struct.
type Config struct {
	APIKey *schemas.SecretVar `json:"api_key"`
}

var resolvedConfig atomic.Pointer[Config]

// parseConfig unmarshals raw plugin config (a map[string]any, as delivered by
// the plugin loader) into Config. This is the step that resolves env./vault.
// references, since *schemas.SecretVar implements json.Unmarshaler.
func parseConfig(raw any) (*Config, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// MarshalForStorage serializes Config with APIKey flattened to a plain string
// ("env.X"/"vault.X" reference, or the literal value) for DB/config-file
// persistence - same convention plugins/otel and plugins/telemetry use.
func (c *Config) MarshalForStorage() ([]byte, error) {
	return json.Marshal(struct {
		APIKey string `json:"api_key,omitempty"`
	}{APIKey: schemas.SecretVarAsString(c.APIKey)})
}

// Redacted returns a copy of Config with APIKey's value fully masked (no
// substring of a literal secret is ever exposed), keeping its reference
// visible (e.g. "env.MY_PLUGIN_API_KEY") for API responses. Same convention
// plugins/telemetry uses for its Kafka password field.
func (c *Config) Redacted() *Config {
	return &Config{APIKey: c.APIKey.FullyRedacted()}
}

// Init parses and resolves the plugin config.
func Init(config any) error {
	c, err := parseConfig(config)
	if err != nil {
		return err
	}
	if c.APIKey.GetValue() == "" {
		return fmt.Errorf("plugin config api_key must resolve to a non-empty value")
	}
	resolvedConfig.Store(c)
	return nil
}

// GetName returns the plugin's system identifier.
func GetName() string {
	return "secretvar-config"
}

// HTTPTransportPreHook stamps the resolved secret onto the request as a header
func HTTPTransportPreHook(_ *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	c := resolvedConfig.Load()
	if req == nil || c == nil {
		return nil, nil
	}
	if req.Headers == nil {
		req.Headers = make(map[string]string, 1)
	}
	req.Headers[apiKeyHeader] = c.APIKey.GetValue()

	fmt.Printf("[secretvar-config] HTTPTransportPreHook fired: injected %s=%s into request headers\n", apiKeyHeader, c.APIKey.GetValue())
	return nil, nil
}

// MarshalConfigForStorage implements schemas.ConfigMarshallerPlugin. Bifrost
// calls this before writing config to the DB.
func MarshalConfigForStorage(raw map[string]any) (map[string]any, error) {
	c, err := parseConfig(raw)
	if err != nil {
		return raw, err
	}
	normalized, err := c.MarshalForStorage()
	if err != nil {
		return raw, err
	}
	var out map[string]any
	if err := json.Unmarshal(normalized, &out); err != nil {
		return raw, err
	}
	return out, nil
}

// RedactConfig implements schemas.ConfigMarshallerPlugin. Bifrost calls this
// when building an API response.
func RedactConfig(raw map[string]any) (map[string]any, error) {
	c, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	redacted, err := json.Marshal(c.Redacted())
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(redacted, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Cleanup releases plugin resources. This plugin does not allocate any.
func Cleanup() error {
	return nil
}
