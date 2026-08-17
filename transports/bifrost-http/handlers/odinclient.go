package handlers

import (
	"context"
	"fmt"
	"strings"
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

// odinTransportProvider is the provider Odin speaks on the wire, which is not
// the provider that ends up serving the request.
//
// The two are separate on purpose. Odin's base URL points at this Bifrost's
// OpenAI-compatible mount, and a provider implementation builds its own path
// from that base: the Anthropic one asks for /v1/messages, which under /openai
// is not a route at all and comes back as "Method Not Allowed". Speaking OpenAI
// to the compatibility layer and naming the real provider in the model string is
// what lets Odin run on Anthropic, Bedrock or Vertex without needing a wire
// format per provider.
//
// The consequence to know: a base URL pointed somewhere other than this Bifrost
// has to be OpenAI-compatible. Every provider Bifrost fronts is reachable
// through the default, so this only binds someone who has deliberately pointed
// Odin elsewhere.
func odinTransportProvider() schemas.ModelProvider {
	return schemas.OpenAI
}

// odinModelForRequest returns the model name to send upstream.
//
// With the default base URL Odin talks to this Bifrost, which routes on the
// model name alone - so a bare "gpt-5.5" gets whichever provider Bifrost picks
// for it, and Odin's configured provider is silently ignored. Sending
// "provider/model" pins it, which is the difference between Odin's traffic
// landing on the provider that was chosen for it and landing wherever the
// deployment's routing happens to send that model name.
//
// A model that already carries a prefix is left alone, so an operator who typed
// the qualified form gets exactly what they typed.
func odinModelForRequest(config *schemas.OdinConfig) string {
	if config.Provider == "" || strings.Contains(config.Model, "/") {
		return config.Model
	}
	return string(config.Provider) + "/" + config.Model
}

// odinAccount is the minimal Account implementation over the stored Odin config.
type odinAccount struct {
	config *schemas.OdinConfig
}

// GetConfiguredProviders reports the wire protocol Odin speaks, not the provider
// that serves the request - see odinTransportProvider.
func (a *odinAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{odinTransportProvider()}, nil
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

// odinConversationHeader labels Odin's own upstream calls with the thread they
// belong to.
//
// The x-bf-lh- prefix is the logging plugin's own convention: everything after
// it becomes a metadata key on the log row, filterable through
// SearchFilters.MetadataFilters. With the default base URL Odin talks to this
// Bifrost, so its research calls are logged like any other traffic - and a
// question that took five model calls would otherwise land as five unrelated
// rows with nothing tying them together.
const odinConversationHeader = "x-bf-lh-odin-conversation-id"

// odinSessionHeader binds a thread's calls to one provider key.
//
// x-bf-session-id is Bifrost's session-stickiness header: requests carrying the
// same value reuse the same key from the pool. A conversation is exactly the
// unit that wants that - prompt caches, rate-limit buckets and any per-key state
// are all keyed on the credential, so a thread that hops keys between turns
// throws that away and pays full price for context it already sent.
const odinSessionHeader = "x-bf-session-id"

// odinUserAgent labels Odin's traffic so the logs can tell it apart.
//
// Bifrost derives a log row's app from the User-Agent, so this is what turns
// Odin's own calls into a named client in the Logs view instead of an anonymous
// share of "API". It matters more here than for a normal integration: Odin reads
// the same table it writes to, so being able to see - and filter out - its own
// traffic is what keeps its answers about the deployment rather than about
// itself. Matched by schemas.Odin.
const odinUserAgent = "bifrost-odin/1"

// chat resolves (building if needed) the instance for this config and runs one
// completion against it.
func (c *odinClient) chat(ctx context.Context, config *schemas.OdinConfig, conversationID string) odinChatFunc {
	return func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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
		if conversationID != "" {
			// Both headers carry the same value for different ends: one groups the
			// thread's rows in the log table, the other keeps it on one key.
			bifrostCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
				odinConversationHeader: {conversationID},
				odinSessionHeader:      {conversationID},
				"User-Agent":           {odinUserAgent},
			})
		}
		return instance.ResponsesRequest(bifrostCtx, req)
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
