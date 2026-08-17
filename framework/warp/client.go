package warp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Warp runs on its own Bifrost instance rather than the gateway's shared client.
// That looks like duplication until you consider what sharing would mean:
//
//  1. Self-pollution. The gateway client runs the logging plugin, so Warp's own
//     calls would be written into the very table Warp reads. Ask "how many
//     requests today?" twice and the second answer differs because the first one
//     changed it. That is a corrupted product, not an accounting quirk.
//  2. BaseURL is account-level, not per-request. The per-request credential
//     override exists, but there is no per-request base URL, so a self-hosted
//     Warp model would be unreachable through the shared client.
//  3. Governance. Budgets and rate limits sized for tenant traffic could throttle
//     the dashboard assistant for reasons unrelated to it.
//
// The cost is one small worker pool for one provider, and the fact that Warp's
// own spend does not appear in the gateway's logs. The usage figure on the done
// event is the compensating control.

// warpAccount is the minimal Account implementation over the stored Warp config.
type warpAccount struct {
	config *schemas.WarpConfig
}

// GetConfiguredProviders reports the single provider Warp is configured to use.
func (a *warpAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{a.config.Provider}, nil
}

// GetKeysForProvider returns Warp's one key. The whitelist is "*" because the
// account serves exactly one model and the config already names it.
func (a *warpAccount) GetKeysForProvider(_ context.Context, _ schemas.ModelProvider) ([]schemas.Key, error) {
	key := schemas.Key{
		ID:     "warp",
		Name:   "warp",
		Models: schemas.WhiteList{"*"},
		Weight: 1,
	}
	// The stored value is a reference to one of the deployment's own provider
	// keys, not a secret. Warp reaches its model through this Bifrost, which
	// resolves the id against its key pool, so the id travels as the key value
	// and Bifrost substitutes the real credential.
	//
	// An empty reference is legitimate: a model behind a trusted-network BaseURL,
	// or a provider using ambient IAM credentials, needs none.
	if a.config.APIKeyID != "" {
		key.ID = a.config.APIKeyID
		key.Value = *schemas.NewSecretVar(a.config.APIKeyID)
	}
	return []schemas.Key{key}, nil
}

// GetConfigForProvider supplies Warp's network settings. BaseURL lives here
// rather than per-request, which is one of the reasons Warp cannot share the
// gateway's client.
func (a *warpAccount) GetConfigForProvider(_ schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        a.config.BaseURL,
			DefaultRequestTimeoutInSeconds: a.config.EffectiveRequestTimeoutSeconds(),
		},
	}
	config.CheckAndSetDefaults()
	return config, nil
}

// Client owns the lazily-built instance and swaps it when settings change.
type Client struct {
	mu      sync.Mutex
	current atomic.Pointer[clientInstance]
	logger  schemas.Logger
}

type clientInstance struct {
	client *bifrost.Bifrost
	// signature identifies the config the instance was built from, so a settings
	// save that did not touch the model does not tear down a working client.
	signature string
}

// NewClient creates the holder. The Bifrost instance is built on first use.
func NewClient(logger schemas.Logger) *Client {
	return &Client{logger: logger}
}

// configSignature identifies the settings an instance was built from, so a
// save that did not touch the model does not tear down a working client. The key
// reference can be included verbatim - it is an id, not a credential.
func configSignature(config *schemas.WarpConfig) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d", config.Provider, config.Model, config.BaseURL, config.APIKeyID, config.EffectiveRequestTimeoutSeconds())
}

// Chat resolves (building if needed) the instance for this config and returns
// the function that runs one completion against it.
func (c *Client) Chat(ctx context.Context, config *schemas.WarpConfig) ChatFunc {
	return func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		instance, err := c.instanceFor(ctx, config)
		if err != nil {
			return nil, &schemas.BifrostError{
				Error: &schemas.ErrorField{Message: fmt.Sprintf("could not start Warp's model client: %s", err.Error())},
			}
		}
		// The scope-carrying context becomes the BifrostContext, so anything the
		// snapshot preserved travels with the inference call too.
		bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(ctx)
		defer cancel()
		return instance.ChatCompletionRequest(bifrostCtx, req)
	}
}

// instanceFor returns the instance matching config, building and swapping it in
// if the settings changed. The double-checked lock matters on first use, where
// concurrent requests would otherwise each build an instance and leak all but one.
func (c *Client) instanceFor(ctx context.Context, config *schemas.WarpConfig) (*bifrost.Bifrost, error) {
	signature := configSignature(config)
	if existing := c.current.Load(); existing != nil && existing.signature == signature {
		return existing.client, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check under the lock: two concurrent first requests would otherwise
	// each build an instance and one would leak.
	if existing := c.current.Load(); existing != nil && existing.signature == signature {
		return existing.client, nil
	}

	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account: &warpAccount{config: config},
		Logger:  c.logger,
		// Warp is one dashboard user asking one question at a time. A large pool
		// would reserve memory for concurrency that cannot exist.
		InitialPoolSize: 8,
	})
	if err != nil {
		return nil, err
	}

	previous := c.current.Swap(&clientInstance{client: client, signature: signature})
	if previous != nil {
		// Shut the old instance down off the request path. In-flight requests hold
		// their own reference and finish against it.
		go previous.client.Shutdown()
	}
	return client, nil
}

// Shutdown releases the instance at server stop.
func (c *Client) Shutdown() {
	if instance := c.current.Swap(nil); instance != nil {
		instance.client.Shutdown()
	}
}
