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

// transportProvider is the provider Warp speaks on the wire, which is not
// the provider that ends up serving the request.
//
// The two are separate on purpose. Warp's base URL points at this Bifrost's
// OpenAI-compatible mount, and a provider implementation builds its own path
// from that base: the Anthropic one asks for /v1/messages, which under /openai
// is not a route at all and comes back as "Method Not Allowed". Speaking OpenAI
// to the compatibility layer and naming the real provider in the model string is
// what lets Warp run on Anthropic, Bedrock or Vertex without needing a wire
// format per provider.
//
// The consequence to know: a base URL pointed somewhere other than this Bifrost
// has to be OpenAI-compatible. Every provider Bifrost fronts is reachable
// through the default, so this only binds someone who has deliberately pointed
// Warp elsewhere.
func transportProvider() schemas.ModelProvider {
	return schemas.OpenAI
}

// modelForRequest returns the model name to send upstream.
//
// With the default base URL Warp talks to this Bifrost, which routes on the
// model name alone - so a bare "gpt-5.5" gets whichever provider Bifrost picks
// for it, and Warp's configured provider is silently ignored. Sending
// "provider/model" pins it, which is the difference between Warp's traffic
// landing on the provider that was chosen for it and landing wherever the
// deployment's routing happens to send that model name.
//
// A model that already carries a prefix is left alone, so an operator who typed
// the qualified form gets exactly what they typed.
func modelForRequest(config *schemas.WarpConfig) string {
	if config.Provider == "" {
		return config.Model
	}
	// A slash does not make a name qualified. Native slugs carry their own -
	// "meta/llama-3-8b", "meta-llama/Llama-3.1-8B" - and treating those as a
	// prefix dropped the configured provider from the wire name, so the request
	// could take the default route instead of the one the operator chose.
	// ParseModelString splits only on a known Bifrost provider, which is exactly
	// the distinction being made here.
	if prefix, _ := schemas.ParseModelString(config.Model, ""); prefix != "" {
		return config.Model
	}
	return string(config.Provider) + "/" + config.Model
}

// catalogModel strips the provider prefix for a pricing lookup.
//
// modelForRequest deliberately sends a qualified name on the wire, but the
// model catalog keys on the bare one - so looking up "anthropic/claude-sonnet-5"
// matches nothing and the turn silently prices at zero, which is worse than no
// figure at all because it reads as free.
func catalogModel(model string) string {
	// Only a real provider segment comes off. Cutting at the first slash turned
	// "meta/llama-3-8b" into "llama-3-8b", which matches no catalog entry - the
	// same silent zero price this function exists to prevent, arrived at from
	// the other direction.
	_, bare := schemas.ParseModelString(model, "")
	return bare
}

// costProviderFor names the provider a turn should be priced against.
//
// Pricing follows routing: modelForRequest routes a provider-qualified model
// by its own prefix, so "vertex/gemini-2.5-pro" under Provider "anthropic"
// runs on Vertex - and pricing it against Anthropic's rate card reports a
// wrong or zero cost. The configured provider prices only an unqualified
// model, with the same known-provider distinction modelForRequest draws: a
// slash that is not a Bifrost provider is part of the model's own name.
func costProviderFor(config *schemas.WarpConfig) schemas.ModelProvider {
	if prefix, _ := schemas.ParseModelString(config.Model, ""); prefix != "" {
		return prefix
	}
	return schemas.ModelProvider(config.Provider)
}

// account is the minimal account implementation over the stored Warp config.
type warpAccount struct {
	config *schemas.WarpConfig
}

// getConfiguredProviders reports the wire protocol Warp speaks, not the provider
// that serves the request - see transportProvider.
func (a *warpAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{transportProvider()}, nil
}

// placeholderBearer is the value Warp's private key carries. Core refuses to
// select an OpenAI-transport key with an empty value, so the key must hold
// something, but nothing Warp holds is a credential: it reaches its model
// through this Bifrost, which supplies the real key from its own pool. The
// receiving side only reads a bearer that carries the virtual-key prefix, so
// this value is ignored there. Deployments that enforce auth on inference
// would need a virtual key here instead - not supported yet.
const placeholderBearer = "warp"

// getKeysForProvider returns Warp's one key. The whitelist is "*" because the
// account serves exactly one model and the config already names it.
func (a *warpAccount) GetKeysForProvider(_ context.Context, _ schemas.ModelProvider) ([]schemas.Key, error) {
	key := schemas.Key{
		ID:     "warp",
		Name:   "warp",
		Value:  *schemas.NewSecretVar(placeholderBearer),
		Models: schemas.WhiteList{"*"},
		Weight: 1,
	}
	// A pinned key is named to the receiving Bifrost by header, not by bearer -
	// see requestHeaders. The id is kept on the local key only so a settings
	// change is visible in the instance signature.
	if a.config.APIKeyID != "" {
		key.ID = a.config.APIKeyID
	}
	return []schemas.Key{key}, nil
}

// getConfigForProvider supplies Warp's network settings. BaseURL lives here
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

// ConversationHeader labels Warp's own upstream calls with the thread they
// belong to.
//
// The x-bf-lh- prefix is the logging plugin's own convention: everything after
// it becomes a metadata key on the log row, filterable through
// SearchFilters.MetadataFilters. With the default base URL Warp talks to this
// Bifrost, so its research calls are logged like any other traffic - and a
// question that took five model calls would otherwise land as five unrelated
// rows with nothing tying them together.
const ConversationHeader = "x-bf-lh-warp-conversation-id"

// SessionHeader binds a thread's calls to one provider key.
//
// x-bf-session-id is Bifrost's session-stickiness header: requests carrying the
// same value reuse the same key from the pool. A conversation is exactly the
// unit that wants that - prompt caches, rate-limit buckets and any per-key state
// are all keyed on the credential, so a thread that hops keys between turns
// throws that away and pays full price for context it already sent.
const SessionHeader = "x-bf-session-id"

// UserAgent labels Warp's traffic so the logs can tell it apart.
//
// Bifrost derives a log row's app from the User-Agent, so this is what turns
// Warp's own calls into a named client in the Logs view instead of an anonymous
// share of "API". It matters more here than for a normal integration: Warp reads
// the same table it writes to, so being able to see - and filter out - its own
// traffic is what keeps its answers about the deployment rather than about
// itself. Matched by schemas.Warp.
const UserAgent = "bifrost-warp/1"

// chat resolves (building if needed) the instance for this config and runs one
// completion against it.
func (c *Client) Chat(ctx context.Context, config *schemas.WarpConfig, conversationID string) ChatFunc {
	return func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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
		bifrostCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, requestHeaders(config, conversationID))
		return instance.ResponsesRequest(bifrostCtx, req)
	}
}

// PinnedKeyHeader is Bifrost's request header for pinning one provider key by
// id (read in the HTTP transport's context builder). Governance ignores a
// bearer that is not a virtual key, so the bearer cannot carry the pin; this
// header is the only way a specific key reaches the selector.
const PinnedKeyHeader = "x-bf-api-key-id"

// requestHeaders builds the extra headers for one of Warp's upstream calls.
// The User-Agent is always set so Warp's traffic is labelled even for calls
// outside a conversation; the rest are present only when they mean something.
func requestHeaders(config *schemas.WarpConfig, conversationID string) map[string][]string {
	headers := map[string][]string{"User-Agent": {UserAgent}}
	if conversationID != "" {
		// Both headers carry the same value for different ends: one groups the
		// thread's rows in the log table, the other keeps it on one key.
		headers[ConversationHeader] = []string{conversationID}
		headers[SessionHeader] = []string{conversationID}
	}
	if config != nil && config.APIKeyID != "" {
		headers[PinnedKeyHeader] = []string{config.APIKeyID}
	}
	return headers
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
