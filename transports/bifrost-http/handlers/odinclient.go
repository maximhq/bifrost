package handlers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Odin runs on its own Bifrost instance rather than the gateway's shared client.
// That looks like duplication until you consider what sharing would mean:
//
//  1. Self-pollution. The gateway client runs the logging plugin, so Odin's own
//     calls would be written into the very table Odin reads. Ask "how many
//     requests today?" twice and the second answer differs because the first one
//     changed it. That is a corrupted product, not an accounting quirk.
//  2. BaseURL is account-level, not per-request. The per-request credential
//     override exists, but there is no per-request base URL, so a self-hosted
//     Odin model would be unreachable through the shared client.
//  3. Governance. Budgets and rate limits sized for tenant traffic could throttle
//     the dashboard assistant for reasons unrelated to it.
//
// The cost is one small worker pool for one provider, and the fact that Odin's
// own spend does not appear in the gateway's logs. The usage figure on the done
// event is the compensating control.

// odinAccount is the minimal Account implementation over the stored Odin config.
type odinAccount struct {
	config *schemas.OdinConfig
}

// GetConfiguredProviders reports the single provider Odin is configured to use.
func (a *odinAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{a.config.Provider}, nil
}

// GetKeysForProvider returns Odin's one key. The whitelist is "*" because the
// account serves exactly one model and the config already names it.
func (a *odinAccount) GetKeysForProvider(_ context.Context, _ schemas.ModelProvider) ([]schemas.Key, error) {
	key := schemas.Key{
		ID:     "odin",
		Name:   "odin",
		Models: schemas.WhiteList{"*"},
		Weight: 1,
	}
	// The stored value is a reference to one of the deployment's own provider
	// keys, not a secret. Odin reaches its model through this Bifrost, which
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

// GetConfigForProvider supplies Odin's network settings. BaseURL lives here
// rather than per-request, which is one of the reasons Odin cannot share the
// gateway's client.
func (a *odinAccount) GetConfigForProvider(_ schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        a.config.BaseURL,
			DefaultRequestTimeoutInSeconds: a.config.EffectiveRequestTimeoutSeconds(),
		},
	}
	config.CheckAndSetDefaults()
	return config, nil
}

// odinClient owns the lazily-built instance and swaps it when settings change.
type odinClient struct {
	mu      sync.Mutex
	current atomic.Pointer[odinClientInstance]
	logger  schemas.Logger
}

type odinClientInstance struct {
	client *bifrost.Bifrost
	// signature identifies the config the instance was built from, so a settings
	// save that did not touch the model does not tear down a working client.
	signature string
}

// newOdinClient creates the holder. The Bifrost instance is built on first use.
func newOdinClient(logger schemas.Logger) *odinClient {
	return &odinClient{logger: logger}
}

// odinConfigSignature identifies the settings an instance was built from, so a
// save that did not touch the model does not tear down a working client. The key
// reference can be included verbatim - it is an id, not a credential.
func odinConfigSignature(config *schemas.OdinConfig) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d", config.Provider, config.Model, config.BaseURL, config.APIKeyID, config.EffectiveRequestTimeoutSeconds())
}

// chat resolves (building if needed) the instance for this config and runs one
// completion against it.
func (c *odinClient) chat(ctx context.Context, config *schemas.OdinConfig) odinChatFunc {
	return func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		instance, err := c.instanceFor(ctx, config)
		if err != nil {
			return nil, &schemas.BifrostError{
				Error: &schemas.ErrorField{Message: fmt.Sprintf("could not start Odin's model client: %s", err.Error())},
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
func (c *odinClient) instanceFor(ctx context.Context, config *schemas.OdinConfig) (*bifrost.Bifrost, error) {
	signature := odinConfigSignature(config)
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
		Account: &odinAccount{config: config},
		Logger:  c.logger,
		// Odin is one dashboard user asking one question at a time. A large pool
		// would reserve memory for concurrency that cannot exist.
		InitialPoolSize: 8,
	})
	if err != nil {
		return nil, err
	}

	previous := c.current.Swap(&odinClientInstance{client: client, signature: signature})
	if previous != nil {
		// Shut the old instance down off the request path. In-flight requests hold
		// their own reference and finish against it.
		go previous.client.Shutdown()
	}
	return client, nil
}

// Shutdown releases the instance at server stop.
func (c *odinClient) Shutdown() {
	if instance := c.current.Swap(nil); instance != nil {
		instance.client.Shutdown()
	}
}
