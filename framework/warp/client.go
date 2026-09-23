package warp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

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
	// closed is set by Shutdown, under mu, and checked on the build path.
	//
	// A streaming turn outlives its HTTP handler, so RunTurn can reach
	// instanceFor after the server has shut down. With current already swapped
	// to nil, the signature check missed and a whole new Bifrost instance was
	// built that no owner would ever release.
	closed bool
	// lifecycle scopes the cached instance to the server, not to whichever
	// request happened to build it. bifrost.Init derives the instance's own
	// context from the one it is handed, so passing a request context would let
	// one user closing their tab shut the shared client down for everyone.
	lifecycle context.Context
}

type clientInstance struct {
	client *bifrost.Bifrost
	// signature identifies the config the instance was built from, so a settings
	// save that did not touch the model does not tear down a working client.
	signature string
	// grace is how long this instance must outlive its own replacement, computed
	// from the config it was built with. It is stored rather than recomputed at
	// retirement because by then only the replacement's config is in hand, and a
	// turn still running here holds a budget derived from this one.
	grace time.Duration
}

// NewClient creates the holder. The Bifrost instance is built on first use,
// against a server-scoped context rather than a request's.
func NewClient(logger schemas.Logger) *Client {
	return &Client{logger: logger, lifecycle: context.Background()}
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
// The request context is deliberately ignored - see lifecycle on Client.
func (c *Client) instanceFor(_ context.Context, config *schemas.WarpConfig) (*bifrost.Bifrost, error) {
	signature := configSignature(config)
	if existing := c.current.Load(); existing != nil && existing.signature == signature {
		return existing.client, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("warp: model client is shutting down")
	}
	// Re-check under the lock: two concurrent first requests would otherwise
	// each build an instance and one would leak.
	if existing := c.current.Load(); existing != nil && existing.signature == signature {
		return existing.client, nil
	}

	// Deliberately the lifecycle context, not the request's: Init keeps a derived
	// context on the instance, so building from ctx would tie every later
	// request to the first one's lifetime.
	lifecycle := c.lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	client, err := bifrost.Init(lifecycle, schemas.BifrostConfig{
		Account: &warpAccount{config: config},
		Logger:  c.logger,
		// Warp is one dashboard user asking one question at a time. A large pool
		// would reserve memory for concurrency that cannot exist.
		InitialPoolSize: 8,
	})
	if err != nil {
		return nil, err
	}

	previous := c.current.Swap(&clientInstance{
		client: client, signature: signature, grace: retirementGrace(config),
	})
	if previous != nil {
		// Shut the old instance down off the request path, and not immediately.
		// Shutdown cancels the instance context and drains queued requests with
		// errors, so tearing it down the moment a settings save lands would abort
		// whatever chat was mid-answer against it. Waiting out one request budget
		// lets those finish; the instance is unreachable to new callers either way
		// because the pointer has already been swapped.
		// previous.grace, not the replacement's: a turn already running on the old
		// instance was admitted under the old budget, and retiring it on a shorter
		// successor's grace lets a settings save reach back and cancel work that
		// started before it.
		scheduleRetirement(previous.grace, previous.client.Shutdown)
	}
	return client, nil
}

// scheduleRetirement defers a replaced instance's shutdown. A variable so tests
// can observe the delay chosen without waiting it out.
var scheduleRetirement = time.AfterFunc

// retirementGrace is how long a replaced instance is left alive.
//
// A whole turn, not one call: the agent makes up to EffectiveMaxIterations
// sequential model calls, each bounded by the per-call timeout, so waiting only
// one call's worth let Shutdown cancel a later iteration of a turn that started
// before the settings were saved. This is the same product Turn.Budget uses.
func retirementGrace(config *schemas.WarpConfig) time.Duration {
	return time.Duration(config.EffectiveMaxIterations()*config.EffectiveRequestTimeoutSeconds()) * time.Second
}

// Shutdown releases the instance at server stop.
//
// The flag and the swap happen together under mu, so a build that is already
// waiting on the lock sees closed rather than racing the swap and leaving its
// fresh instance in current with nobody left to release it. The instance's own
// Shutdown drains queued requests, so it runs outside the lock - by then no
// build can proceed anyway.
func (c *Client) Shutdown() {
	c.mu.Lock()
	c.closed = true
	instance := c.current.Swap(nil)
	c.mu.Unlock()
	if instance != nil {
		instance.client.Shutdown()
	}
}
