package lib

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// GlobalProxyFileConfig is the global outbound proxy as config.json declares it: the same settings
// the dashboard's proxy page edits, including which components use it (SCIM directory sync,
// inference, API). The URL and credentials take env./vault references like other file secrets.
type GlobalProxyFileConfig struct {
	Enabled            bool                    `json:"enabled"`
	Type               network.GlobalProxyType `json:"type,omitempty"`
	URL                *schemas.SecretVar      `json:"url,omitempty"`
	Username           *schemas.SecretVar      `json:"username,omitempty"`
	Password           *schemas.SecretVar      `json:"password,omitempty"`
	NoProxy            string                  `json:"no_proxy,omitempty"`
	Timeout            int                     `json:"timeout,omitempty"`
	SkipTLSVerify      bool                    `json:"skip_tls_verify,omitempty"`
	EnableForSCIM      bool                    `json:"enable_for_scim,omitempty"`
	EnableForInference bool                    `json:"enable_for_inference,omitempty"`
	EnableForAPI       bool                    `json:"enable_for_api,omitempty"`
}

// resolved is the declaration with its references resolved, in the shape the store keeps. An
// omitted type reads as http, the only type the proxy supports today.
func (c *GlobalProxyFileConfig) resolved() configstoreTables.GlobalProxyConfig {
	proxyType := c.Type
	if proxyType == "" {
		proxyType = network.GlobalProxyTypeHTTP
	}
	return configstoreTables.GlobalProxyConfig{
		Enabled:            c.Enabled,
		Type:               proxyType,
		URL:                secretValue(c.URL),
		Username:           secretValue(c.Username),
		Password:           secretValue(c.Password),
		NoProxy:            c.NoProxy,
		Timeout:            c.Timeout,
		SkipTLSVerify:      c.SkipTLSVerify,
		EnableForSCIM:      c.EnableForSCIM,
		EnableForInference: c.EnableForInference,
		EnableForAPI:       c.EnableForAPI,
	}
}

// secretValue resolves an optional reference, "" when it is absent.
func secretValue(v *schemas.SecretVar) string {
	if v == nil {
		return ""
	}
	return v.GetValue()
}

// validateGlobalProxyConfig applies the rules the proxy settings endpoint applies, so config.json
// cannot store a proxy the dashboard would refuse.
func validateGlobalProxyConfig(c configstoreTables.GlobalProxyConfig) error {
	if !c.Enabled {
		return nil
	}
	switch c.Type {
	case network.GlobalProxyTypeHTTP:
	case network.GlobalProxyTypeSOCKS5, network.GlobalProxyTypeTCP:
		return fmt.Errorf("proxy type %s is not yet supported", c.Type)
	default:
		return fmt.Errorf("invalid proxy type: %s", c.Type)
	}
	if c.URL == "" {
		return errors.New("proxy url is required when the proxy is enabled")
	}
	if c.Timeout < 0 {
		return errors.New("proxy timeout must be non-negative")
	}
	return nil
}

// generateGlobalProxyConfigHash hashes a resolved declaration, so a rotated credential behind an
// env. reference reads as a change.
func generateGlobalProxyConfigHash(c configstoreTables.GlobalProxyConfig) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// loadProxyConfig applies config.json's proxy_config to the stored global proxy, the way the other
// file sections are reconciled:
//
//   - split (the default): the declaration is applied when it is new or changed since the hash
//     recorded at the last sync, and a dashboard edit to an unchanged declaration is kept
//   - config.json source of truth: the declaration is applied on every boot
//
// The hash lives in its own row (ConfigProxyHashKey) rather than inside the proxy row, because the
// dashboard saves that row whole: a hash inside it would be wiped by the first edit, and the next
// boot would re-apply the file over the edit. An absent proxy_config leaves the stored proxy alone.
func loadProxyConfig(ctx context.Context, config *Config, configData *ConfigData) {
	if configData == nil || configData.ProxyConfig == nil {
		return
	}
	declared := configData.ProxyConfig.resolved()
	if err := validateGlobalProxyConfig(declared); err != nil {
		logger.Warn("invalid proxy_config in config.json, not applied: %v", err)
		return
	}
	if config.ConfigStore == nil {
		config.ProxyConfig = &declared
		return
	}
	fileHash, err := generateGlobalProxyConfigHash(declared)
	if err != nil {
		logger.Warn("failed to hash proxy_config from config.json: %v", err)
		return
	}
	stored, err := config.ConfigStore.GetProxyConfig(ctx)
	if err != nil {
		logger.Warn("failed to read the stored proxy config: %v", err)
		return
	}
	if stored != nil && !configData.isConfigJSONSourceOfTruth() {
		storedHash, err := config.ConfigStore.GetConfig(ctx, configstoreTables.ConfigProxyHashKey)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			logger.Warn("failed to read the proxy config hash: %v", err)
			return
		}
		if storedHash != nil && storedHash.Value == fileHash {
			logger.Debug("proxy config hash matches, keeping DB config")
			config.ProxyConfig = stored
			return
		}
	}
	if err := config.ConfigStore.UpdateProxyConfig(ctx, &declared); err != nil {
		logger.Warn("failed to save proxy_config from config.json: %v", err)
		return
	}
	if err := config.ConfigStore.UpdateConfig(ctx, &configstoreTables.TableGovernanceConfig{
		Key:   configstoreTables.ConfigProxyHashKey,
		Value: fileHash,
	}); err != nil {
		logger.Warn("failed to record the proxy config hash: %v", err)
	}
	logger.Info("proxy config synced from config.json (enabled=%t)", declared.Enabled)
	config.ProxyConfig = &declared
}
