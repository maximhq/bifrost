// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
)

// BaseAccount implements the Account interface for Bifrost.
// It manages provider configurations using a in-memory store for persistent storage.
// All data processing (environment variables, key configs) is done upfront in the store.
type BaseAccount struct {
	config *Config // store for in-memory configuration
}

// NewBaseAccount creates a new BaseAccount with the given store
func NewBaseAccount(config *Config) *BaseAccount {
	return &BaseAccount{
		config: config,
	}
}

// GetConfiguredProviders returns a list of all configured providers.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	if baseAccount.config == nil {
		return nil, fmt.Errorf("config not initialized")
	}
	providers := []schemas.ModelProvider{}
	baseAccount.config.LiveStore.Collections.Providers.Range(func(p *registry.Provider) bool {
		providers = append(providers, p.Name)
		return true
	})
	return providers, nil
}

// GetKeysForProvider returns the API keys configured for a specific provider.
// Keys are already processed (environment variables resolved) by the store.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	if baseAccount.config == nil {
		return nil, fmt.Errorf("config not initialized")
	}
	if _, ok := baseAccount.config.liveProvider(providerKey); !ok {
		return nil, ErrNotFound
	}
	// Shared with the live store: the slice is filtered into a new one, never changed in place.
	var keys []schemas.Key
	if list, ok := baseAccount.config.LiveStore.Views.ProviderKeyLists.Get(string(providerKey)); ok {
		keys = *list
	}
	if v := ctx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys); v != nil {
		if includeOnlyKeys, ok := v.([]string); ok {
			if len(includeOnlyKeys) == 0 {
				// header present but empty means "no keys allowed"
				keys = nil
			} else {
				set := make(map[string]struct{}, len(includeOnlyKeys))
				for _, id := range includeOnlyKeys {
					set[id] = struct{}{}
				}
				filtered := make([]schemas.Key, 0, len(keys))
				for _, key := range keys {
					if _, ok := set[key.ID]; ok {
						filtered = append(filtered, key)
					}
				}
				keys = filtered
			}
		}
	}
	return keys, nil
}

// GetConfiguredMCPClients returns the IDs of the MCP clients in the live store.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfiguredMCPClients() ([]string, error) {
	if baseAccount.config == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	ids := []string{}
	baseAccount.config.LiveStore.Collections.MCPClients.Range(func(client *registry.MCPClient) bool {
		ids = append(ids, client.Config.ID)
		return true
	})
	return ids, nil
}

// GetConfigForMCPClient returns an MCP client's configuration as the live store holds it, so the
// caller must not change it. Implements the Account interface.
func (baseAccount *BaseAccount) GetConfigForMCPClient(clientID string) (*schemas.MCPClientConfig, error) {
	if baseAccount.config == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	client, ok := baseAccount.config.liveMCPClient(clientID)
	if !ok {
		return nil, ErrNotFound
	}
	return &client.Config, nil
}

// GetConfigForMCPClientByName returns the configuration of the MCP client with this name as the
// live store holds it, so the caller must not change it. Implements the Account interface.
func (baseAccount *BaseAccount) GetConfigForMCPClientByName(name string) (*schemas.MCPClientConfig, error) {
	if baseAccount.config == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	client, ok := baseAccount.config.LiveStore.Collections.MCPClients.GetBy(registry.MCPClientByName, name)
	if !ok {
		return nil, ErrNotFound
	}
	return &client.Config, nil
}

// GetConfigForProvider returns the complete configuration for a specific provider.
// Configuration is already fully processed (environment variables, key configs) by the store.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if baseAccount.config == nil {
		return nil, fmt.Errorf("config not initialized")
	}
	p, ok := baseAccount.config.liveProvider(providerKey)
	if !ok {
		return nil, ErrNotFound
	}
	config := &p.Settings
	providerConfig := &schemas.ProviderConfig{}
	if config.ProxyConfig != nil {
		providerConfig.ProxyConfig = config.ProxyConfig
	}
	if config.NetworkConfig != nil {
		providerConfig.NetworkConfig = *config.NetworkConfig
	} else {
		providerConfig.NetworkConfig = schemas.DefaultNetworkConfig
	}
	inherited, ok, err := inheritGlobalProxy(providerConfig.ProxyConfig, baseAccount.config.GetGlobalProxyConfig())
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w", providerKey, err)
	}
	if ok {
		// skip_tls_verify and the proxy CA travel on inherited.proxy, which applies the
		// skip to the https:// proxy's own certificate only. Setting
		// NetworkConfig.InsecureSkipVerify instead would skip verification of targets.
		providerConfig.ProxyConfig = inherited.proxy
	}
	if config.ConcurrencyAndBufferSize != nil {
		providerConfig.ConcurrencyAndBufferSize = *config.ConcurrencyAndBufferSize
	} else {
		providerConfig.ConcurrencyAndBufferSize = schemas.DefaultConcurrencyAndBufferSize
	}
	providerConfig.SendBackRawRequest = config.SendBackRawRequest
	providerConfig.SendBackRawResponse = config.SendBackRawResponse
	providerConfig.StoreRawRequestResponse = config.StoreRawRequestResponse
	providerConfig.IgnoreProviderCost = config.IgnoreProviderCost
	if config.CustomProviderConfig != nil {
		providerConfig.CustomProviderConfig = config.CustomProviderConfig
	}
	if config.OpenAIConfig != nil {
		providerConfig.OpenAIConfig = config.OpenAIConfig
	}
	if config.PromptCache != nil {
		providerConfig.PromptCache = config.PromptCache
	}
	if config.InjectedTools != nil {
		providerConfig.InjectedTools = config.InjectedTools
	}
	if config.Passthrough != nil {
		providerConfig.Passthrough = config.Passthrough
	}
	return providerConfig, nil
}

// hasOwnProxy reports whether a provider's proxy_config names a proxy of its own.
// The UI saves type "none" when the proxy form is left untouched, so "none" and an
// empty type mean "not configured", the same as nil.
func hasOwnProxy(proxyConfig *schemas.ProxyConfig) bool {
	return proxyConfig != nil && proxyConfig.Type != "" && proxyConfig.Type != schemas.NoProxy
}

// inheritedProxy is the global proxy translated for one provider.
type inheritedProxy struct {
	proxy *schemas.ProxyConfig
}

// InheritedGlobalProxy is the proxy a relay with no proxy_config of its own inherits
// from the global proxy, nil when the global proxy is off or not enabled for inference,
// and an error when it is on but its URL cannot be read.
func InheritedGlobalProxy(global *configstoreTables.GlobalProxyConfig) (*schemas.ProxyConfig, error) {
	inherited, ok, err := inheritGlobalProxy(nil, global)
	if err != nil || !ok {
		return nil, err
	}
	return inherited.proxy, nil
}

// inheritGlobalProxy returns the global proxy for a provider that has none of its
// own, when the global proxy is enabled for inference. A provider's own proxy always
// wins. ok is false when nothing should be inherited.
//
// no_proxy carries over, so hosts such as a VPC endpoint stay direct. skip_tls_verify
// and ca_cert_pem carry over on the proxy config: the skip covers only the https://
// proxy's own certificate, and targets stay verified (the CA is trusted for them).
// The global timeout does not carry over: each provider keeps its own network_config
// timeouts.
func inheritGlobalProxy(own *schemas.ProxyConfig, global *configstoreTables.GlobalProxyConfig) (inheritedProxy, bool, error) {
	if hasOwnProxy(own) || global == nil || !global.Enabled || !global.EnableForInference || strings.TrimSpace(global.URL) == "" {
		return inheritedProxy{}, false, nil
	}
	var proxyType schemas.ProxyType
	switch global.Type {
	case network.GlobalProxyTypeHTTP, "":
		proxyType = schemas.HTTPProxy
	case network.GlobalProxyTypeSOCKS5:
		proxyType = schemas.Socks5Proxy
	default:
		// "tcp" has no provider-level equivalent (the UI does not offer it).
		return inheritedProxy{}, false, nil
	}
	// A bare host:port takes the type's scheme, as the settings page reads it. Providers
	// parse the URL to add credentials and fall back to no proxy when it does not parse, so
	// they get the normalized URL. A URL that cannot be normalized is an error, not "no
	// proxy": a stored URL can predate the validator, and dropping it would send inference
	// direct around a proxy that is on. A scheme that does not match the type is kept, not
	// refused: config.json stored such proxies before the scheme was checked, and the
	// dialer follows the URL's scheme.
	proxyURL, err := parseGlobalProxyURL(global.Type, strings.TrimSpace(global.URL))
	if err != nil {
		return inheritedProxy{}, false, fmt.Errorf("global proxy enabled for inference: %w", err)
	}
	return inheritedProxy{
		proxy: &schemas.ProxyConfig{
			Type:     proxyType,
			URL:      plainSecret(proxyURL.String()),
			Username: plainSecret(global.Username),
			Password: plainSecret(global.Password),
			NoProxy:  global.NoProxy,
			// skip_tls_verify covers only the https:// proxy's own certificate; the
			// proxy CA is trusted for that hop and for TLS through the proxy, so
			// targets are always verified. NetworkConfig is deliberately left alone.
			SkipTLSVerify: global.SkipTLSVerify,
			CACertPEM:     plainSecret(global.CACertPEM),
		},
	}, true, nil
}

// plainSecret wraps a literal value. The global proxy stores plain strings, so a
// password that happens to start with "env." must not be read as an env reference,
// which NewSecretVar would do.
func plainSecret(value string) *schemas.SecretVar {
	if value == "" {
		return nil
	}
	return &schemas.SecretVar{Val: value, SecretType: schemas.SecretTypePlainText}
}
