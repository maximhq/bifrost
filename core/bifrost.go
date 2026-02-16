// Package bifrost provides the core implementation of the Bifrost system.
// Bifrost is a unified interface for interacting with various AI model providers,
// managing concurrent requests, and handling provider-specific configurations.
package bifrost

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/mcp/codemode/starlark"
	"github.com/maximhq/bifrost/core/pool"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/azure"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/cerebras"
	"github.com/maximhq/bifrost/core/providers/cohere"
	"github.com/maximhq/bifrost/core/providers/elevenlabs"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/providers/huggingface"
	"github.com/maximhq/bifrost/core/providers/mistral"
	"github.com/maximhq/bifrost/core/providers/nebius"
	"github.com/maximhq/bifrost/core/providers/ollama"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/openrouter"
	"github.com/maximhq/bifrost/core/providers/parasail"
	"github.com/maximhq/bifrost/core/providers/perplexity"
	"github.com/maximhq/bifrost/core/providers/replicate"
	"github.com/maximhq/bifrost/core/providers/sgl"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/providers/vertex"
	"github.com/maximhq/bifrost/core/providers/xai"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ChannelMessage represents a message passed through the request channel.
// It contains the request, response and error channels, and the request type.
type ChannelMessage struct {
	schemas.BifrostRequest
	Context        *schemas.BifrostContext
	Response       chan *schemas.BifrostResponse
	ResponseStream chan chan *schemas.BifrostStreamChunk
	Err            chan *schemas.BifrostError

	// Pool pointer bookkeeping: original *chan pointers from Get()
	// so that Put() returns the same address that was tracked.
	responsePoolPtr       *chan *schemas.BifrostResponse
	errPoolPtr            *chan *schemas.BifrostError
	responseStreamPoolPtr *chan chan *schemas.BifrostStreamChunk
}

// Bifrost manages providers and maintains specified open channels for concurrent processing.
// It handles request routing, provider management, and response processing.
type Bifrost struct {
	ctx                 *schemas.BifrostContext
	cancel              context.CancelFunc
	account             schemas.Account                                   // account interface
	llmPlugins          atomic.Pointer[[]schemas.LLMPlugin]               // list of llm plugins
	mcpPlugins          atomic.Pointer[[]schemas.MCPPlugin]               // list of mcp plugins
	providers           atomic.Pointer[[]schemas.Provider]                // list of providers
	requestQueues       sync.Map                                          // provider request queues (thread-safe), stores *ProviderQueue
	waitGroups          sync.Map                                          // wait groups for each provider (thread-safe)
	providerMutexes     sync.Map                                          // mutexes for each provider to prevent concurrent updates (thread-safe)
	channelMessagePool  *pool.Pool[ChannelMessage]                        // Pool for ChannelMessage objects, initial pool size is set in Init
	responseChannelPool *pool.Pool[chan *schemas.BifrostResponse]         // Pool for response channels, initial pool size is set in Init
	errorChannelPool    *pool.Pool[chan *schemas.BifrostError]            // Pool for error channels, initial pool size is set in Init
	responseStreamPool  *pool.Pool[chan chan *schemas.BifrostStreamChunk] // Pool for response stream channels, initial pool size is set in Init
	pluginPipelinePool  *pool.Pool[PluginPipeline]                        // Pool for PluginPipeline objects
	mcpRequestPool      *pool.Pool[schemas.BifrostMCPRequest]             // Pool for BifrostMCPRequest objects
	oauth2Provider      schemas.OAuth2Provider                            // OAuth provider instance
	logger              schemas.Logger                                    // logger instance, default logger is used if not provided
	tracer              atomic.Value                                      // tracer for distributed tracing (stores schemas.Tracer, NoOpTracer if not configured)
	MCPManager          mcp.MCPManagerInterface                           // MCP integration manager (nil if MCP not configured)
	mcpInitOnce         sync.Once                                         // Ensures MCP manager is initialized only once
	dropExcessRequests  atomic.Bool                                       // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
	keySelector         schemas.KeySelector                               // Custom key selector function
}

// ProviderQueue wraps a provider's request channel with lifecycle management
// to prevent "send on closed channel" panics during provider removal/update.
// Producers must check the closing flag or select on the done channel before sending.
type ProviderQueue struct {
	queue      chan *ChannelMessage // the actual request queue channel
	done       chan struct{}        // closed to signal shutdown to producers
	closing    uint32               // atomic: 0 = open, 1 = closing
	signalOnce sync.Once
	closeOnce  sync.Once
}

// signalClosing signals the closing of the provider queue.
// This is lock-free: uses atomic store and sync.Once to safely signal shutdown.
func (pq *ProviderQueue) signalClosing() {
	pq.signalOnce.Do(func() {
		atomic.StoreUint32(&pq.closing, 1)
		close(pq.done)
	})
}

// closeQueue closes the provider queue.
// Protected by sync.Once to prevent double-close.
func (pq *ProviderQueue) closeQueue() {
	pq.closeOnce.Do(func() {
		close(pq.queue)
	})
}

// isClosing returns true if the provider queue is closing.
// Uses atomic load for lock-free checking.
func (pq *ProviderQueue) isClosing() bool {
	return atomic.LoadUint32(&pq.closing) == 1
}

// PluginPipeline encapsulates the execution of plugin PreHooks and PostHooks, tracks how many plugins ran, and manages short-circuiting and error aggregation.
type PluginPipeline struct {
	llmPlugins []schemas.LLMPlugin
	mcpPlugins []schemas.MCPPlugin
	logger     schemas.Logger
	tracer     schemas.Tracer

	// Number of PreHooks that were executed (used to determine which PostHooks to run in reverse order)
	executedPreHooks int
	// Errors from PreHooks and PostHooks
	preHookErrors  []error
	postHookErrors []error

	// Streaming post-hook timing accumulation (for aggregated spans)
	postHookTimings     map[string]*pluginTimingAccumulator // keyed by plugin name
	postHookPluginOrder []string                            // order in which post-hooks ran (for nested span creation)
	chunkCount          int
}

// pluginTimingAccumulator accumulates timing information for a plugin across streaming chunks
type pluginTimingAccumulator struct {
	totalDuration time.Duration
	invocations   int
	errors        int
}

// tracerWrapper wraps a Tracer to ensure atomic.Value stores consistent types.
// This is necessary because atomic.Value.Store() panics if called with values
// of different concrete types, even if they implement the same interface.
type tracerWrapper struct {
	tracer schemas.Tracer
}

// INITIALIZATION

// Init initializes a new Bifrost instance with the given configuration.
// It sets up the account, plugins, object pools, and initializes providers.
// Returns an error if initialization fails.
// Initial Memory Allocations happens here as per the initial pool size.
func Init(ctx context.Context, config schemas.BifrostConfig) (*Bifrost, error) {
	if config.Account == nil {
		return nil, fmt.Errorf("account is required to initialize Bifrost")
	}

	if config.Logger == nil {
		config.Logger = NewDefaultLogger(schemas.LogLevelInfo)
	}
	providerUtils.SetLogger(config.Logger)

	// Initialize tracer (use NoOpTracer if not provided)
	tracer := config.Tracer
	if tracer == nil {
		tracer = schemas.DefaultTracer()
	}

	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(ctx)
	bifrost := &Bifrost{
		ctx:            bifrostCtx,
		cancel:         cancel,
		account:        config.Account,
		llmPlugins:     atomic.Pointer[[]schemas.LLMPlugin]{},
		mcpPlugins:     atomic.Pointer[[]schemas.MCPPlugin]{},
		requestQueues:  sync.Map{},
		waitGroups:     sync.Map{},
		keySelector:    config.KeySelector,
		oauth2Provider: config.OAuth2Provider,
		logger:         config.Logger,
	}
	bifrost.tracer.Store(&tracerWrapper{tracer: tracer})
	if config.LLMPlugins == nil {
		config.LLMPlugins = make([]schemas.LLMPlugin, 0)
	}
	if config.MCPPlugins == nil {
		config.MCPPlugins = make([]schemas.MCPPlugin, 0)
	}
	bifrost.llmPlugins.Store(&config.LLMPlugins)
	bifrost.mcpPlugins.Store(&config.MCPPlugins)

	// Initialize providers slice
	bifrost.providers.Store(&[]schemas.Provider{})

	bifrost.dropExcessRequests.Store(config.DropExcessRequests)

	if bifrost.keySelector == nil {
		bifrost.keySelector = WeightedRandomKeySelector
	}

	// Initialize object pools
	bifrost.channelMessagePool = pool.New("ChannelMessage", func() *ChannelMessage {
		return &ChannelMessage{}
	})
	bifrost.responseChannelPool = pool.New("BifrostResponseChannel", func() *chan *schemas.BifrostResponse {
		cn := make(chan *schemas.BifrostResponse, 1)
		return &cn
	})
	bifrost.errorChannelPool = pool.New("BifrostErrorChannel", func() *chan *schemas.BifrostError {
		cn := make(chan *schemas.BifrostError, 1)
		return &cn
	})
	bifrost.responseStreamPool = pool.New("BifrostResponseStreamChannel", func() *chan chan *schemas.BifrostStreamChunk {
		cn := make(chan chan *schemas.BifrostStreamChunk, 1)
		return &cn
	})
	bifrost.pluginPipelinePool = pool.New("PluginPipeline", func() *PluginPipeline {
		return &PluginPipeline{
			preHookErrors:  make([]error, 0),
			postHookErrors: make([]error, 0),
		}
	})
	bifrost.mcpRequestPool = pool.New("BifrostMCPRequest", func() *schemas.BifrostMCPRequest {
		return &schemas.BifrostMCPRequest{}
	})
	// Prewarm pools with multiple objects
	bifrost.channelMessagePool.Prewarm(config.InitialPoolSize)
	bifrost.responseChannelPool.Prewarm(config.InitialPoolSize)
	bifrost.errorChannelPool.Prewarm(config.InitialPoolSize)
	bifrost.responseStreamPool.Prewarm(config.InitialPoolSize)
	bifrost.pluginPipelinePool.Prewarm(config.InitialPoolSize)
	bifrost.mcpRequestPool.Prewarm(config.InitialPoolSize)

	providerKeys, err := bifrost.account.GetConfiguredProviders()
	if err != nil {
		return nil, err
	}

	// Initialize MCP manager if configured
	if config.MCPConfig != nil {
		bifrost.mcpInitOnce.Do(func() {
			// Set up plugin pipeline provider functions for executeCode tool hooks
			mcpConfig := *config.MCPConfig
			mcpConfig.PluginPipelineProvider = func() interface{} {
				return bifrost.getPluginPipeline()
			}
			mcpConfig.ReleasePluginPipeline = func(pipeline interface{}) {
				if pp, ok := pipeline.(*PluginPipeline); ok {
					bifrost.releasePluginPipeline(pp)
				}
			}
			// Create Starlark CodeMode for code execution
			var codeModeConfig *mcp.CodeModeConfig
			if mcpConfig.ToolManagerConfig != nil {
				codeModeConfig = &mcp.CodeModeConfig{
					BindingLevel:         mcpConfig.ToolManagerConfig.CodeModeBindingLevel,
					ToolExecutionTimeout: mcpConfig.ToolManagerConfig.ToolExecutionTimeout,
				}
			}
			codeMode := starlark.NewStarlarkCodeMode(codeModeConfig, bifrost.logger)
			bifrost.MCPManager = mcp.NewMCPManager(bifrostCtx, mcpConfig, bifrost.oauth2Provider, bifrost.logger, codeMode)
			bifrost.logger.Info("MCP integration initialized successfully")
		})
	}

	// Create buffered channels for each provider and start workers
	for _, providerKey := range providerKeys {
		if strings.TrimSpace(string(providerKey)) == "" {
			bifrost.logger.Warn("provider key is empty, skipping init")
			continue
		}

		config, err := bifrost.account.GetConfigForProvider(providerKey)
		if err != nil {
			bifrost.logger.Warn("failed to get config for provider, skipping init: %v", err)
			continue
		}
		if config == nil {
			bifrost.logger.Warn("config is nil for provider %s, skipping init", providerKey)
			continue
		}

		// Lock the provider mutex during initialization
		providerMutex := bifrost.getProviderMutex(providerKey)
		providerMutex.Lock()
		err = bifrost.prepareProvider(providerKey, config)
		providerMutex.Unlock()

		if err != nil {
			bifrost.logger.Warn("failed to prepare provider %s: %v", providerKey, err)
		}
	}
	return bifrost, nil
}

// SetTracer sets the tracer for the Bifrost instance.
func (bifrost *Bifrost) SetTracer(tracer schemas.Tracer) {
	if tracer == nil {
		// Fall back to no-op tracer if not provided
		tracer = schemas.DefaultTracer()
	}
	bifrost.tracer.Store(&tracerWrapper{tracer: tracer})
}

// getTracer returns the tracer from atomic storage with type assertion.
func (bifrost *Bifrost) getTracer() schemas.Tracer {
	return bifrost.tracer.Load().(*tracerWrapper).tracer
}

// ReloadConfig reloads the config from DB
// Currently we update account, drop excess requests, and plugin lists
// We will keep on adding other aspects as required
func (bifrost *Bifrost) ReloadConfig(config schemas.BifrostConfig) error {
	bifrost.dropExcessRequests.Store(config.DropExcessRequests)
	return nil
}

// RemovePlugin removes a plugin from the server.
func (bifrost *Bifrost) RemovePlugin(name string, pluginTypes []schemas.PluginType) error {
	for _, pluginType := range pluginTypes {
		switch pluginType {
		case schemas.PluginTypeLLM:
			err := bifrost.removeLLMPlugin(name)
			if err != nil {
				return err
			}
		case schemas.PluginTypeMCP:
			err := bifrost.removeMCPPlugin(name)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// removeLLMPlugin removes an LLM plugin from the server.
func (bifrost *Bifrost) removeLLMPlugin(name string) error {
	for {
		oldPlugins := bifrost.llmPlugins.Load()
		if oldPlugins == nil {
			return nil
		}
		var pluginToCleanup schemas.LLMPlugin
		found := false
		// Create new slice without the plugin to remove
		newPlugins := make([]schemas.LLMPlugin, 0, len(*oldPlugins))
		for _, p := range *oldPlugins {
			if p.GetName() == name {
				pluginToCleanup = p
				bifrost.logger.Debug("removing LLM plugin %s", name)
				found = true
			} else {
				newPlugins = append(newPlugins, p)
			}
		}
		if !found {
			return nil
		}
		// Atomic compare-and-swap
		if bifrost.llmPlugins.CompareAndSwap(oldPlugins, &newPlugins) {
			// Cleanup the old plugin
			err := pluginToCleanup.Cleanup()
			if err != nil {
				bifrost.logger.Warn("failed to cleanup old LLM plugin %s: %v", pluginToCleanup.GetName(), err)
			}
			return nil
		}
		// Retrying as swapping did not work
	}
}

// removeMCPPlugin removes an MCP plugin from the server.
func (bifrost *Bifrost) removeMCPPlugin(name string) error {
	for {
		oldPlugins := bifrost.mcpPlugins.Load()
		if oldPlugins == nil {
			return nil
		}
		var pluginToCleanup schemas.MCPPlugin
		found := false
		// Create new slice without the plugin to remove
		newPlugins := make([]schemas.MCPPlugin, 0, len(*oldPlugins))
		for _, p := range *oldPlugins {
			if p.GetName() == name {
				pluginToCleanup = p
				bifrost.logger.Debug("removing MCP plugin %s", name)
				found = true
			} else {
				newPlugins = append(newPlugins, p)
			}
		}
		if !found {
			return nil
		}
		// Atomic compare-and-swap
		if bifrost.mcpPlugins.CompareAndSwap(oldPlugins, &newPlugins) {
			// Cleanup the old plugin
			err := pluginToCleanup.Cleanup()
			if err != nil {
				bifrost.logger.Warn("failed to cleanup old MCP plugin %s: %v", pluginToCleanup.GetName(), err)
			}
			return nil
		}
		// Retrying as swapping did not work
	}
}

// ReloadPlugin reloads a plugin with new instance
// During the reload - it's stop the world phase where we take a global lock on the plugin mutex
func (bifrost *Bifrost) ReloadPlugin(plugin schemas.BasePlugin, pluginTypes []schemas.PluginType) error {
	for _, pluginType := range pluginTypes {
		switch pluginType {
		case schemas.PluginTypeLLM:
			llmPlugin, ok := plugin.(schemas.LLMPlugin)
			if !ok {
				return fmt.Errorf("plugin %s is not an LLMPlugin", plugin.GetName())
			}
			err := bifrost.reloadLLMPlugin(llmPlugin)
			if err != nil {
				return err
			}
		case schemas.PluginTypeMCP:
			mcpPlugin, ok := plugin.(schemas.MCPPlugin)
			if !ok {
				return fmt.Errorf("plugin %s is not an MCPPlugin", plugin.GetName())
			}
			err := bifrost.reloadMCPPlugin(mcpPlugin)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// reloadLLMPlugin reloads an LLM plugin with new instance
func (bifrost *Bifrost) reloadLLMPlugin(plugin schemas.LLMPlugin) error {
	for {
		var pluginToCleanup schemas.LLMPlugin
		found := false
		oldPlugins := bifrost.llmPlugins.Load()

		// Create new slice with replaced plugin or initialize empty slice
		var newPlugins []schemas.LLMPlugin
		if oldPlugins == nil {
			// Initialize new empty slice for the first plugin
			newPlugins = make([]schemas.LLMPlugin, 0)
		} else {
			newPlugins = make([]schemas.LLMPlugin, len(*oldPlugins))
			copy(newPlugins, *oldPlugins)
		}

		for i, p := range newPlugins {
			if p.GetName() == plugin.GetName() {
				// Cleaning up old plugin before replacing it
				pluginToCleanup = p
				bifrost.logger.Debug("replacing LLM plugin %s with new instance", plugin.GetName())
				newPlugins[i] = plugin
				found = true
				break
			}
		}
		if !found {
			// This means that user is adding a new plugin
			bifrost.logger.Debug("adding new LLM plugin %s", plugin.GetName())
			newPlugins = append(newPlugins, plugin)
		}
		// Atomic compare-and-swap
		if bifrost.llmPlugins.CompareAndSwap(oldPlugins, &newPlugins) {
			// Cleanup the old plugin
			if found && pluginToCleanup != nil {
				err := pluginToCleanup.Cleanup()
				if err != nil {
					bifrost.logger.Warn("failed to cleanup old LLM plugin %s: %v", pluginToCleanup.GetName(), err)
				}
			}
			return nil
		}
		// Retrying as swapping did not work
	}
}

// reloadMCPPlugin reloads an MCP plugin with new instance
func (bifrost *Bifrost) reloadMCPPlugin(plugin schemas.MCPPlugin) error {
	for {
		var pluginToCleanup schemas.MCPPlugin
		found := false
		oldPlugins := bifrost.mcpPlugins.Load()
		if oldPlugins == nil {
			return nil
		}
		// Create new slice with replaced plugin
		newPlugins := make([]schemas.MCPPlugin, len(*oldPlugins))
		copy(newPlugins, *oldPlugins)
		for i, p := range newPlugins {
			if p.GetName() == plugin.GetName() {
				// Cleaning up old plugin before replacing it
				pluginToCleanup = p
				bifrost.logger.Debug("replacing MCP plugin %s with new instance", plugin.GetName())
				newPlugins[i] = plugin
				found = true
				break
			}
		}
		if !found {
			// This means that user is adding a new plugin
			bifrost.logger.Debug("adding new MCP plugin %s", plugin.GetName())
			newPlugins = append(newPlugins, plugin)
		}
		// Atomic compare-and-swap
		if bifrost.mcpPlugins.CompareAndSwap(oldPlugins, &newPlugins) {
			// Cleanup the old plugin
			if found && pluginToCleanup != nil {
				err := pluginToCleanup.Cleanup()
				if err != nil {
					bifrost.logger.Warn("failed to cleanup old MCP plugin %s: %v", pluginToCleanup.GetName(), err)
				}
			}
			return nil
		}
		// Retrying as swapping did not work
	}
}

// GetConfiguredProviders returns the configured providers.
//
// Returns:
//   - []schemas.ModelProvider: List of configured providers
//   - error: Any error that occurred during the retrieval process
//
// Example:
//
//	providers, err := bifrost.GetConfiguredProviders()
//	if err != nil {
//		return nil, err
//	}
//	fmt.Println(providers)
func (bifrost *Bifrost) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	providers := bifrost.providers.Load()
	if providers == nil {
		return nil, fmt.Errorf("no providers configured")
	}
	modelProviders := make([]schemas.ModelProvider, len(*providers))
	for i, provider := range *providers {
		modelProviders[i] = provider.GetProviderKey()
	}
	return modelProviders, nil
}

// RemoveProvider removes a provider from the server.
// This method gracefully stops all workers for the provider,
// closes the request queue, and removes the provider from the providers slice.
//
// Parameters:
//   - providerKey: The provider to remove
//
// Returns:
//   - error: Any error that occurred during the removal process
func (bifrost *Bifrost) RemoveProvider(providerKey schemas.ModelProvider) error {
	bifrost.logger.Info("Removing provider %s", providerKey)
	providerMutex := bifrost.getProviderMutex(providerKey)
	providerMutex.Lock()
	defer providerMutex.Unlock()

	// Step 1: Load the ProviderQueue and verify provider exists
	pqValue, exists := bifrost.requestQueues.Load(providerKey)
	if !exists {
		return fmt.Errorf("provider %s not found in request queues", providerKey)
	}
	pq := pqValue.(*ProviderQueue)

	// Step 2: Signal closing to producers (prevents new sends)
	// This must happen before closing the queue to avoid "send on closed channel" panics
	pq.signalClosing()
	bifrost.logger.Debug("signaled closing for provider %s", providerKey)

	// Step 3: Now safe to close the queue (no new producers can send)
	pq.closeQueue()
	bifrost.logger.Debug("closed request queue for provider %s", providerKey)

	// Step 4: Wait for all workers to finish processing in-flight requests
	waitGroup, exists := bifrost.waitGroups.Load(providerKey)
	if exists {
		waitGroup.(*sync.WaitGroup).Wait()
		bifrost.logger.Debug("all workers for provider %s have stopped", providerKey)
	}

	// Step 5: Remove the provider from the request queues
	bifrost.requestQueues.Delete(providerKey)

	// Step 6: Remove the provider from the wait groups
	bifrost.waitGroups.Delete(providerKey)

	// Step 7: Remove the provider from the providers slice
	replacementAttempts := 0
	maxReplacementAttempts := 100 // Prevent infinite loops in high-contention scenarios
	for {
		replacementAttempts++
		if replacementAttempts > maxReplacementAttempts {
			return fmt.Errorf("failed to replace provider %s in providers slice after %d attempts", providerKey, maxReplacementAttempts)
		}
		oldPtr := bifrost.providers.Load()
		var oldSlice []schemas.Provider
		if oldPtr != nil {
			oldSlice = *oldPtr
		}
		// Create new slice without the old provider of this key
		// Use exact capacity to avoid allocations
		if len(oldSlice) == 0 {
			return fmt.Errorf("provider %s not found in providers slice", providerKey)
		}
		newSlice := make([]schemas.Provider, 0, len(oldSlice)-1)
		for _, existingProvider := range oldSlice {
			if existingProvider.GetProviderKey() != providerKey {
				newSlice = append(newSlice, existingProvider)
			}
		}
		if bifrost.providers.CompareAndSwap(oldPtr, &newSlice) {
			bifrost.logger.Debug("successfully removed provider instance for %s in providers slice", providerKey)
			break
		}
		// Retrying as swapping did not work (likely due to concurrent modification)
	}

	bifrost.logger.Info("successfully removed provider %s", providerKey)
	schemas.UnregisterKnownProvider(providerKey)
	return nil
}

// UpdateProvider dynamically updates a provider with new configuration.
// This method gracefully recreates the provider instance with updated settings,
// stops existing workers, creates a new queue with updated settings,
// and starts new workers with the updated provider and concurrency configuration.
//
// Parameters:
//   - providerKey: The provider to update
//
// Returns:
//   - error: Any error that occurred during the update process
//
// Note: This operation will temporarily pause request processing for the specified provider
// while the transition occurs. In-flight requests will complete before workers are stopped.
// Buffered requests in the old queue will be transferred to the new queue to prevent loss.
func (bifrost *Bifrost) UpdateProvider(providerKey schemas.ModelProvider) error {
	bifrost.logger.Info(fmt.Sprintf("Updating provider configuration for provider %s", providerKey))
	// Get the updated configuration from the account
	providerConfig, err := bifrost.account.GetConfigForProvider(providerKey)
	if err != nil {
		return fmt.Errorf("failed to get updated config for provider %s: %v", providerKey, err)
	}
	if providerConfig == nil {
		return fmt.Errorf("config is nil for provider %s", providerKey)
	}
	// Lock the provider to prevent concurrent access during update
	providerMutex := bifrost.getProviderMutex(providerKey)
	providerMutex.Lock()
	defer providerMutex.Unlock()

	// Check if provider currently exists
	oldPqValue, exists := bifrost.requestQueues.Load(providerKey)
	if !exists {
		bifrost.logger.Debug("provider %s not currently active, initializing with new configuration", providerKey)
		// If provider doesn't exist, just prepare it with new configuration
		return bifrost.prepareProvider(providerKey, providerConfig)
	}

	oldPq := oldPqValue.(*ProviderQueue)

	bifrost.logger.Debug("gracefully stopping existing workers for provider %s", providerKey)

	// Step 1: Create new ProviderQueue with updated buffer size
	newPq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, providerConfig.ConcurrencyAndBufferSize.BufferSize),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
		closeOnce:  sync.Once{},
	}

	// Step 2: Atomically replace the queue FIRST (new producers immediately get the new queue)
	// This minimizes the window where requests fail during the update
	bifrost.requestQueues.Store(providerKey, newPq)
	bifrost.logger.Debug("stored new queue for provider %s, new producers will use it", providerKey)

	// Step 3: Signal old queue is closing to producers that already have a reference
	// Only in-flight producers with the old reference will see this
	oldPq.signalClosing()
	bifrost.logger.Debug("signaled closing for old queue of provider %s", providerKey)

	// Step 4: Transfer any buffered requests from old queue to new queue
	// This prevents request loss during the transition
	transferredCount := 0
	var transferWaitGroup sync.WaitGroup
	for {
		select {
		case msg := <-oldPq.queue:
			select {
			case newPq.queue <- msg:
				transferredCount++
			default:
				// New queue is full, handle this request in a goroutine
				// This is unlikely with proper buffer sizing but provides safety
				transferWaitGroup.Add(1)
				go func(m *ChannelMessage) {
					defer transferWaitGroup.Done()
					select {
					case newPq.queue <- m:
						// Message successfully transferred
					case <-time.After(5 * time.Second):
						bifrost.logger.Warn("Failed to transfer buffered request to new queue within timeout")
						// Send error response to avoid hanging the client
						provider, model, _ := m.BifrostRequest.GetRequestFields()
						// This temp function returns a BifrostError with the request type, provider, and model requested
						requestFailedDueToConcurrency := func() *schemas.BifrostError {
							err := schemas.AcquireBifrostError()
							err.IsBifrostError = false
							err.Error.Message = "request failed during provider concurrency update"
							if err.ExtraFields == nil {
								err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
							}
							err.ExtraFields.RequestType = m.RequestType
							err.ExtraFields.Provider = provider
							err.ExtraFields.ModelRequested = model
							return err
						}
						concurrencyErr := requestFailedDueToConcurrency()
						select {
						case m.Err <- concurrencyErr:
						case <-time.After(1 * time.Second):
							// If we can't send the error either, just log and release the object.
							bifrost.logger.Warn("Failed to send error response during transfer timeout")
							schemas.ReleaseBifrostError(concurrencyErr)
						}
					}
				}(msg)
				goto transferComplete
			}
		default:
			// No more buffered messages
			goto transferComplete
		}
	}

transferComplete:
	// Wait for all transfer goroutines to complete
	transferWaitGroup.Wait()
	if transferredCount > 0 {
		bifrost.logger.Info("transferred %d buffered requests to new queue for provider %s", transferredCount, providerKey)
	}

	// Step 5: Close the old queue to signal workers to stop
	oldPq.closeQueue()
	bifrost.logger.Debug("closed old request queue for provider %s", providerKey)

	// Step 6: Wait for all existing workers to finish processing in-flight requests
	waitGroup, exists := bifrost.waitGroups.Load(providerKey)
	if exists {
		waitGroup.(*sync.WaitGroup).Wait()
		bifrost.logger.Debug("all workers for provider %s have stopped", providerKey)
	}

	// Step 7: Create new wait group for the updated workers
	bifrost.waitGroups.Store(providerKey, &sync.WaitGroup{})

	// Step 8: Create provider instance
	provider, err := bifrost.createBaseProvider(providerKey, providerConfig)
	if err != nil {
		return fmt.Errorf("failed to create provider instance for %s: %v", providerKey, err)
	}

	// Step 8.5: Atomically replace the provider in the providers slice
	// This must happen before starting new workers to prevent stale reads
	bifrost.logger.Debug("atomically replacing provider instance in providers slice for %s", providerKey)

	replacementAttempts := 0
	maxReplacementAttempts := 100 // Prevent infinite loops in high-contention scenarios

	for {
		replacementAttempts++
		if replacementAttempts > maxReplacementAttempts {
			return fmt.Errorf("failed to replace provider %s in providers slice after %d attempts", providerKey, maxReplacementAttempts)
		}

		oldPtr := bifrost.providers.Load()
		var oldSlice []schemas.Provider
		if oldPtr != nil {
			oldSlice = *oldPtr
		}

		// Create new slice without the old provider of this key
		// Use exact capacity to avoid allocations
		newSlice := make([]schemas.Provider, 0, len(oldSlice))
		oldProviderFound := false

		for _, existingProvider := range oldSlice {
			if existingProvider.GetProviderKey() != providerKey {
				newSlice = append(newSlice, existingProvider)
			} else {
				oldProviderFound = true
			}
		}

		// Add the new provider
		newSlice = append(newSlice, provider)

		if bifrost.providers.CompareAndSwap(oldPtr, &newSlice) {
			if oldProviderFound {
				bifrost.logger.Debug("successfully replaced existing provider instance for %s in providers slice", providerKey)
			} else {
				bifrost.logger.Debug("successfully added new provider instance for %s to providers slice", providerKey)
			}
			break
		}
		// Retrying as swapping did not work (likely due to concurrent modification)
	}

	// Step 9: Start new workers with updated concurrency
	bifrost.logger.Debug("starting %d new workers for provider %s with buffer size %d",
		providerConfig.ConcurrencyAndBufferSize.Concurrency,
		providerKey,
		providerConfig.ConcurrencyAndBufferSize.BufferSize)

	waitGroupValue, _ := bifrost.waitGroups.Load(providerKey)
	currentWaitGroup := waitGroupValue.(*sync.WaitGroup)

	for range providerConfig.ConcurrencyAndBufferSize.Concurrency {
		currentWaitGroup.Add(1)
		go bifrost.requestWorker(provider, providerConfig, newPq)
	}

	bifrost.logger.Info("successfully updated provider configuration for provider %s", providerKey)
	return nil
}

// GetDropExcessRequests returns the current value of DropExcessRequests
func (bifrost *Bifrost) GetDropExcessRequests() bool {
	return bifrost.dropExcessRequests.Load()
}

// UpdateDropExcessRequests updates the DropExcessRequests setting at runtime.
// This allows for hot-reloading of this configuration value.
func (bifrost *Bifrost) UpdateDropExcessRequests(value bool) {
	bifrost.dropExcessRequests.Store(value)
	bifrost.logger.Info("drop_excess_requests updated to: %v", value)
}

// getProviderMutex gets or creates a mutex for the given provider
func (bifrost *Bifrost) getProviderMutex(providerKey schemas.ModelProvider) *sync.RWMutex {
	mutexValue, _ := bifrost.providerMutexes.LoadOrStore(providerKey, &sync.RWMutex{})
	return mutexValue.(*sync.RWMutex)
}

// PROVIDER MANAGEMENT

// createBaseProvider creates a provider based on the base provider type
func (bifrost *Bifrost) createBaseProvider(providerKey schemas.ModelProvider, config *schemas.ProviderConfig) (schemas.Provider, error) {
	// Determine which provider type to create
	targetProviderKey := providerKey

	if config.CustomProviderConfig != nil {
		// Validate custom provider config
		if config.CustomProviderConfig.BaseProviderType == "" {
			return nil, fmt.Errorf("custom provider config missing base provider type")
		}

		// Validate that base provider type is supported
		if !IsSupportedBaseProvider(config.CustomProviderConfig.BaseProviderType) {
			return nil, fmt.Errorf("unsupported base provider type: %s", config.CustomProviderConfig.BaseProviderType)
		}

		// Automatically set the custom provider key to the provider name
		config.CustomProviderConfig.CustomProviderKey = string(providerKey)

		targetProviderKey = config.CustomProviderConfig.BaseProviderType
	}

	switch targetProviderKey {
	case schemas.OpenAI:
		return openai.NewOpenAIProvider(config, bifrost.logger), nil
	case schemas.Anthropic:
		return anthropic.NewAnthropicProvider(config, bifrost.logger), nil
	case schemas.Bedrock:
		return bedrock.NewBedrockProvider(config, bifrost.logger)
	case schemas.Cohere:
		return cohere.NewCohereProvider(config, bifrost.logger)
	case schemas.Azure:
		return azure.NewAzureProvider(config, bifrost.logger)
	case schemas.Vertex:
		return vertex.NewVertexProvider(config, bifrost.logger)
	case schemas.Mistral:
		return mistral.NewMistralProvider(config, bifrost.logger), nil
	case schemas.Ollama:
		return ollama.NewOllamaProvider(config, bifrost.logger)
	case schemas.Groq:
		return groq.NewGroqProvider(config, bifrost.logger)
	case schemas.SGL:
		return sgl.NewSGLProvider(config, bifrost.logger)
	case schemas.Parasail:
		return parasail.NewParasailProvider(config, bifrost.logger)
	case schemas.Perplexity:
		return perplexity.NewPerplexityProvider(config, bifrost.logger)
	case schemas.Cerebras:
		return cerebras.NewCerebrasProvider(config, bifrost.logger)
	case schemas.Gemini:
		return gemini.NewGeminiProvider(config, bifrost.logger), nil
	case schemas.OpenRouter:
		return openrouter.NewOpenRouterProvider(config, bifrost.logger), nil
	case schemas.Elevenlabs:
		return elevenlabs.NewElevenlabsProvider(config, bifrost.logger), nil
	case schemas.Nebius:
		return nebius.NewNebiusProvider(config, bifrost.logger)
	case schemas.HuggingFace:
		return huggingface.NewHuggingFaceProvider(config, bifrost.logger), nil
	case schemas.XAI:
		return xai.NewXAIProvider(config, bifrost.logger)
	case schemas.Replicate:
		return replicate.NewReplicateProvider(config, bifrost.logger)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", targetProviderKey)
	}
}

// prepareProvider sets up a provider with its configuration, keys, and worker channels.
// It initializes the request queue and starts worker goroutines for processing requests.
// Note: This function assumes the caller has already acquired the appropriate mutex for the provider.
func (bifrost *Bifrost) prepareProvider(providerKey schemas.ModelProvider, config *schemas.ProviderConfig) error {
	// Create the provider first before setting up the queue,
	// so that unsupported providers don't leave empty queues with no workers.
	provider, err := bifrost.createBaseProvider(providerKey, config)
	if err != nil {
		return fmt.Errorf("failed to create provider for the given key: %v", err)
	}

	// Create ProviderQueue with lifecycle management
	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, config.ConcurrencyAndBufferSize.BufferSize),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
		closeOnce:  sync.Once{},
	}

	bifrost.requestQueues.Store(providerKey, pq)

	// Start specified number of workers
	bifrost.waitGroups.Store(providerKey, &sync.WaitGroup{})

	waitGroupValue, _ := bifrost.waitGroups.Load(providerKey)
	currentWaitGroup := waitGroupValue.(*sync.WaitGroup)

	// Atomically append provider to the providers slice
	for {
		oldPtr := bifrost.providers.Load()
		var oldSlice []schemas.Provider
		if oldPtr != nil {
			oldSlice = *oldPtr
		}
		newSlice := make([]schemas.Provider, len(oldSlice)+1)
		copy(newSlice, oldSlice)
		newSlice[len(oldSlice)] = provider
		if bifrost.providers.CompareAndSwap(oldPtr, &newSlice) {
			break
		}
	}

	schemas.RegisterKnownProvider(providerKey)

	for range config.ConcurrencyAndBufferSize.Concurrency {
		currentWaitGroup.Add(1)
		go bifrost.requestWorker(provider, config, pq)
	}

	return nil
}

// getProviderQueue returns the ProviderQueue for a given provider key.
// If the queue doesn't exist, it creates one at runtime and initializes the provider,
// given the provider config is provided in the account interface implementation.
// This function uses read locks to prevent race conditions during provider updates.
// Callers must check the closing flag or select on the done channel before sending.
func (bifrost *Bifrost) getProviderQueue(providerKey schemas.ModelProvider) (*ProviderQueue, error) {
	// Use read lock to allow concurrent reads but prevent concurrent updates
	providerMutex := bifrost.getProviderMutex(providerKey)
	providerMutex.RLock()

	if pqValue, exists := bifrost.requestQueues.Load(providerKey); exists {
		pq := pqValue.(*ProviderQueue)
		providerMutex.RUnlock()
		return pq, nil
	}

	// Provider doesn't exist, need to create it
	// Upgrade to write lock for creation
	providerMutex.RUnlock()
	providerMutex.Lock()
	defer providerMutex.Unlock()

	// Double-check after acquiring write lock (another goroutine might have created it)
	if pqValue, exists := bifrost.requestQueues.Load(providerKey); exists {
		pq := pqValue.(*ProviderQueue)
		return pq, nil
	}
	bifrost.logger.Debug(fmt.Sprintf("Creating new request queue for provider %s at runtime", providerKey))
	config, err := bifrost.account.GetConfigForProvider(providerKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get config for provider: %v", err)
	}
	if config == nil {
		return nil, fmt.Errorf("config is nil for provider %s", providerKey)
	}
	if err := bifrost.prepareProvider(providerKey, config); err != nil {
		return nil, err
	}
	pqValue, ok := bifrost.requestQueues.Load(providerKey)
	if !ok {
		return nil, fmt.Errorf("request queue not found for provider %s", providerKey)
	}
	pq := pqValue.(*ProviderQueue)
	return pq, nil
}

// getProviderByKey retrieves a provider instance from the providers array by its provider key.
// Returns the provider if found, or nil if no provider with the given key exists.
func (bifrost *Bifrost) getProviderByKey(providerKey schemas.ModelProvider) schemas.Provider {
	providers := bifrost.providers.Load()
	if providers == nil {
		return nil
	}
	// Checking if provider is in the memory
	for _, provider := range *providers {
		if provider.GetProviderKey() == providerKey {
			return provider
		}
	}
	// Could happen when provider is not initialized yet, check if provider config exists in account and if so, initialize it
	config, err := bifrost.account.GetConfigForProvider(providerKey)
	if err != nil || config == nil {
		if slices.Contains(dynamicallyConfigurableProviders, providerKey) {
			bifrost.logger.Info(fmt.Sprintf("initializing provider %s with default config", providerKey))
			// If no config found, use default config
			config = &schemas.ProviderConfig{
				NetworkConfig:            schemas.DefaultNetworkConfig,
				ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
			}
		} else {
			return nil
		}
	}
	// Lock the provider mutex to avoid races
	providerMutex := bifrost.getProviderMutex(providerKey)
	providerMutex.Lock()
	defer providerMutex.Unlock()
	// Double-check after acquiring the lock
	providers = bifrost.providers.Load()
	if providers != nil {
		for _, p := range *providers {
			if p.GetProviderKey() == providerKey {
				return p
			}
		}
	}
	// Preparing provider
	if err := bifrost.prepareProvider(providerKey, config); err != nil {
		return nil
	}
	// Return newly prepared provider without recursion
	providers = bifrost.providers.Load()
	if providers != nil {
		for _, p := range *providers {
			if p.GetProviderKey() == providerKey {
				return p
			}
		}
	}
	return nil
}

// CORE INTERNAL LOGIC

// shouldTryFallbacks handles the primary error and returns true if we should proceed with fallbacks, false if we should return immediately
func (bifrost *Bifrost) shouldTryFallbacks(req *schemas.BifrostRequest, primaryErr *schemas.BifrostError) bool {
	// If no primary error, we succeeded
	if primaryErr == nil {
		bifrost.logger.Debug("no primary error, we should not try fallbacks")
		return false
	}

	// Handle request cancellation
	if primaryErr.Error != nil && primaryErr.Error.Type != nil && *primaryErr.Error.Type == schemas.RequestCancelled {
		bifrost.logger.Debug("request cancelled, we should not try fallbacks")
		return false
	}

	// Check if this is a short-circuit error that doesn't allow fallbacks
	// Note: AllowFallbacks = nil is treated as true (allow fallbacks by default)
	if primaryErr.AllowFallbacks != nil && !*primaryErr.AllowFallbacks {
		bifrost.logger.Debug("allowFallbacks is false, we should not try fallbacks")
		return false
	}

	// If no fallbacks configured, return primary error
	_, _, fallbacks := req.GetRequestFields()
	if len(fallbacks) == 0 {
		bifrost.logger.Debug("no fallbacks configured, we should not try fallbacks")
		return false
	}

	// Should proceed with fallbacks
	return true
}

// prepareFallbackRequest creates a fallback request and validates the provider config
// Returns the fallback request or nil if this fallback should be skipped
func (bifrost *Bifrost) prepareFallbackRequest(req *schemas.BifrostRequest, fallback schemas.Fallback) *schemas.BifrostRequest {
	// Check if we have config for this fallback provider
	_, err := bifrost.account.GetConfigForProvider(fallback.Provider)
	if err != nil {
		bifrost.logger.Warn("config not found for provider %s, skipping fallback: %v", fallback.Provider, err)
		return nil
	}

	// Create a new request with the fallback provider and model
	fallbackReq := *req

	if req.TextCompletionRequest != nil {
		tmp := *req.TextCompletionRequest
		tmp.Provider = fallback.Provider
		tmp.Model = fallback.Model
		fallbackReq.TextCompletionRequest = &tmp
	}

	if req.ChatRequest != nil {
		tmp := *req.ChatRequest
		tmp.Provider = fallback.Provider
		tmp.Model = fallback.Model
		fallbackReq.ChatRequest = &tmp
	}

	if req.ResponsesRequest != nil {
		tmp := *req.ResponsesRequest
		tmp.Provider = fallback.Provider
		tmp.Model = fallback.Model
		fallbackReq.ResponsesRequest = &tmp
	}

	if req.CountTokensRequest != nil {
		tmp := *req.CountTokensRequest
		tmp.Provider = fallback.Provider
		tmp.Model = fallback.Model
		fallbackReq.CountTokensRequest = &tmp
	}

	if req.EmbeddingRequest != nil {
		tmp := *req.EmbeddingRequest
		tmp.Provider = fallback.Provider
		tmp.Model = fallback.Model
		fallbackReq.EmbeddingRequest = &tmp
	}

	if req.SpeechRequest != nil {
		tmp := *req.SpeechRequest
		tmp.Provider = fallback.Provider
		tmp.Model = fallback.Model
		fallbackReq.SpeechRequest = &tmp
	}

	if req.TranscriptionRequest != nil {
		tmp := *req.TranscriptionRequest
		tmp.Provider = fallback.Provider
		tmp.Model = fallback.Model
		fallbackReq.TranscriptionRequest = &tmp
	}
	if req.ImageGenerationRequest != nil {
		tmp := *req.ImageGenerationRequest
		tmp.Provider = fallback.Provider
		tmp.Model = fallback.Model
		fallbackReq.ImageGenerationRequest = &tmp
	}

	return &fallbackReq
}

// shouldContinueWithFallbacks processes errors from fallback attempts
// Returns true if we should continue with more fallbacks, false if we should stop
func (bifrost *Bifrost) shouldContinueWithFallbacks(fallback schemas.Fallback, fallbackErr *schemas.BifrostError) bool {
	if fallbackErr.Error.Type != nil && *fallbackErr.Error.Type == schemas.RequestCancelled {
		return false
	}

	// Check if it was a short-circuit error that doesn't allow fallbacks
	if fallbackErr.AllowFallbacks != nil && !*fallbackErr.AllowFallbacks {
		return false
	}

	bifrost.logger.Debug(fmt.Sprintf("Fallback provider %s failed: %s", fallback.Provider, fallbackErr.Error.Message))
	return true
}

// handleRequest handles the request to the provider based on the request type
// It handles plugin hooks, request validation, response processing, and fallback providers.
// If the primary provider fails, it will try each fallback provider in order until one succeeds.
// It is the wrapper for all non-streaming public API methods.
func (bifrost *Bifrost) handleRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	provider, model, fallbacks := req.GetRequestFields()
	if err := validateRequest(req); err != nil {
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = req.RequestType
		err.ExtraFields.Provider = provider
		err.ExtraFields.ModelRequested = model
		return nil, err
	}

	// Handle nil context early to prevent blocking
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrost.logger.Debug(fmt.Sprintf("primary provider %s with model %s and %d fallbacks", provider, model, len(fallbacks)))

	// Try the primary provider first
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)
	// Ensure request ID is set in context before PreHooks
	if _, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok {
		requestID := uuid.New().String()
		ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	}
	primaryResult, primaryErr := bifrost.tryRequest(ctx, req)
	if primaryErr != nil {
		if primaryErr.Error != nil {
			bifrost.logger.Debug(fmt.Sprintf("primary provider %s with model %s returned error: %s", provider, model, primaryErr.Error.Message))
		} else {
			bifrost.logger.Debug(fmt.Sprintf("primary provider %s with model %s returned error: %v", provider, model, primaryErr))
		}
		if len(fallbacks) > 0 {
			bifrost.logger.Debug(fmt.Sprintf("check if we should try %d fallbacks", len(fallbacks)))
		}
	}

	// Check if we should proceed with fallbacks
	shouldTryFallbacks := bifrost.shouldTryFallbacks(req, primaryErr)
	if !shouldTryFallbacks {
		if primaryErr != nil {
			if primaryErr.ExtraFields == nil {
				primaryErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			primaryErr.ExtraFields.RequestType = req.RequestType
			primaryErr.ExtraFields.Provider = provider
			primaryErr.ExtraFields.ModelRequested = model
		}
		return primaryResult, primaryErr
	}

	// Try fallbacks in order
	for i, fallback := range fallbacks {
		ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, i+1)
		bifrost.logger.Debug(fmt.Sprintf("trying fallback provider %s with model %s", fallback.Provider, fallback.Model))
		ctx.SetValue(schemas.BifrostContextKeyFallbackRequestID, uuid.New().String())

		// Start span for fallback attempt
		tracer := bifrost.getTracer()
		spanCtx, handle := tracer.StartSpan(ctx, fmt.Sprintf("fallback.%s.%s", fallback.Provider, fallback.Model), schemas.SpanKindFallback)
		tracer.SetAttribute(handle, schemas.AttrProviderName, string(fallback.Provider))
		tracer.SetAttribute(handle, schemas.AttrRequestModel, fallback.Model)
		tracer.SetAttribute(handle, "fallback.index", i+1)
		ctx.SetValue(schemas.BifrostContextKeySpanID, spanCtx.Value(schemas.BifrostContextKeySpanID))

		fallbackReq := bifrost.prepareFallbackRequest(req, fallback)
		if fallbackReq == nil {
			bifrost.logger.Debug(fmt.Sprintf("fallback provider %s with model %s is nil", fallback.Provider, fallback.Model))
			tracer.SetAttribute(handle, "error", "fallback request preparation failed")
			tracer.EndSpan(handle, schemas.SpanStatusError, "fallback request preparation failed")
			continue
		}

		// Try the fallback provider
		result, fallbackErr := bifrost.tryRequest(ctx, fallbackReq)
		if fallbackErr == nil {
			// Fallback succeeded; release the primary error that will never be returned.
			schemas.ReleaseBifrostError(primaryErr)
			bifrost.logger.Debug(fmt.Sprintf("successfully used fallback provider %s with model %s", fallback.Provider, fallback.Model))
			tracer.EndSpan(handle, schemas.SpanStatusOk, "")
			return result, nil
		}

		// End span with error status
		if fallbackErr.Error != nil {
			tracer.SetAttribute(handle, "error", fallbackErr.Error.Message)
		}
		tracer.EndSpan(handle, schemas.SpanStatusError, "fallback failed")

		// Check if we should continue with more fallbacks
		if !bifrost.shouldContinueWithFallbacks(fallback, fallbackErr) {
			// This fallback's error stops the chain; release the primary error.
			schemas.ReleaseBifrostError(primaryErr)
			if fallbackErr.ExtraFields == nil {
				fallbackErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			fallbackErr.ExtraFields.RequestType = req.RequestType
			fallbackErr.ExtraFields.Provider = fallback.Provider
			fallbackErr.ExtraFields.ModelRequested = fallback.Model
			return nil, fallbackErr
		}
		// Continuing to next fallback; release this fallback's error.
		schemas.ReleaseBifrostError(fallbackErr)
	}

	if primaryErr != nil {
		if primaryErr.ExtraFields == nil {
			primaryErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		primaryErr.ExtraFields.RequestType = req.RequestType
		primaryErr.ExtraFields.Provider = provider
		primaryErr.ExtraFields.ModelRequested = model
	}

	// All providers failed, return the original error
	return nil, primaryErr
}

// handleStreamRequest handles the stream request to the provider based on the request type
// It handles plugin hooks, request validation, response processing, and fallback providers.
// If the primary provider fails, it will try each fallback provider in order until one succeeds.
// It is the wrapper for all streaming public API methods.
func (bifrost *Bifrost) handleStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	provider, model, fallbacks := req.GetRequestFields()
	if err := validateRequest(req); err != nil {
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = req.RequestType
		err.ExtraFields.Provider = provider
		err.ExtraFields.ModelRequested = model
		err.StatusCode = schemas.Ptr(fasthttp.StatusBadRequest)
		return nil, err
	}

	// Handle nil context early to prevent blocking
	if ctx == nil {
		ctx = bifrost.ctx
	}

	// Try the primary provider first
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)
	// Ensure request ID is set in context before PreHooks
	if _, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok {
		requestID := uuid.New().String()
		ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	}
	primaryResult, primaryErr := bifrost.tryStreamRequest(ctx, req)

	// Check if we should proceed with fallbacks
	shouldTryFallbacks := bifrost.shouldTryFallbacks(req, primaryErr)
	if !shouldTryFallbacks {
		if primaryErr != nil {
			if primaryErr.ExtraFields == nil {
				primaryErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			primaryErr.ExtraFields.RequestType = req.RequestType
			primaryErr.ExtraFields.Provider = provider
			primaryErr.ExtraFields.ModelRequested = model
		}
		return primaryResult, primaryErr
	}

	// Try fallbacks in order
	for i, fallback := range fallbacks {
		ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, i+1)
		ctx.SetValue(schemas.BifrostContextKeyFallbackRequestID, uuid.New().String())

		// Start span for fallback attempt
		tracer := bifrost.getTracer()
		spanCtx, handle := tracer.StartSpan(ctx, fmt.Sprintf("fallback.%s.%s", fallback.Provider, fallback.Model), schemas.SpanKindFallback)
		tracer.SetAttribute(handle, schemas.AttrProviderName, string(fallback.Provider))
		tracer.SetAttribute(handle, schemas.AttrRequestModel, fallback.Model)
		tracer.SetAttribute(handle, "fallback.index", i+1)
		ctx.SetValue(schemas.BifrostContextKeySpanID, spanCtx.Value(schemas.BifrostContextKeySpanID))

		fallbackReq := bifrost.prepareFallbackRequest(req, fallback)
		if fallbackReq == nil {
			tracer.SetAttribute(handle, "error", "fallback request preparation failed")
			tracer.EndSpan(handle, schemas.SpanStatusError, "fallback request preparation failed")
			continue
		}

		// Try the fallback provider
		result, fallbackErr := bifrost.tryStreamRequest(ctx, fallbackReq)
		if fallbackErr == nil {
			// Fallback succeeded; release the primary error that will never be returned.
			schemas.ReleaseBifrostError(primaryErr)
			bifrost.logger.Debug(fmt.Sprintf("successfully used fallback provider %s with model %s", fallback.Provider, fallback.Model))
			tracer.EndSpan(handle, schemas.SpanStatusOk, "")
			return result, nil
		}

		// End span with error status
		if fallbackErr.Error != nil {
			tracer.SetAttribute(handle, "error", fallbackErr.Error.Message)
		}
		tracer.EndSpan(handle, schemas.SpanStatusError, "fallback failed")

		// Check if we should continue with more fallbacks
		if !bifrost.shouldContinueWithFallbacks(fallback, fallbackErr) {
			// This fallback's error stops the chain; release the primary error.
			schemas.ReleaseBifrostError(primaryErr)
			if fallbackErr.ExtraFields == nil {
				fallbackErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			fallbackErr.ExtraFields.RequestType = req.RequestType
			fallbackErr.ExtraFields.Provider = fallback.Provider
			fallbackErr.ExtraFields.ModelRequested = fallback.Model
			return nil, fallbackErr
		}
		// Continuing to next fallback; release this fallback's error.
		schemas.ReleaseBifrostError(fallbackErr)
	}

	if primaryErr != nil {
		if primaryErr.ExtraFields == nil {
			primaryErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		primaryErr.ExtraFields.RequestType = req.RequestType
		primaryErr.ExtraFields.Provider = provider
		primaryErr.ExtraFields.ModelRequested = model
	}

	// All providers failed, return the original error
	return nil, primaryErr
}

// tryRequest is a generic function that handles common request processing logic
// It consolidates queue setup, plugin pipeline execution, enqueue logic, and response handling
func (bifrost *Bifrost) tryRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	provider, model, _ := req.GetRequestFields()
	pq, err := bifrost.getProviderQueue(provider)
	if err != nil {
		bifrostErr := newBifrostError(err)
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	}

	// Add MCP tools to request if MCP is configured and requested
	if bifrost.MCPManager != nil {
		req = bifrost.MCPManager.AddToolsToRequest(ctx, req)
	}

	tracer := bifrost.getTracer()
	if tracer == nil {
		return nil, newBifrostErrorFromMsg("tracer not found in context")
	}

	// Store tracer in context BEFORE calling requestHandler, so streaming goroutines
	// have access to it for completing deferred spans when the stream ends.
	// The streaming goroutine captures the context when it starts, so these values
	// must be set before requestHandler() is called.
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)

	pipeline := bifrost.getPluginPipeline()
	defer bifrost.releasePluginPipeline(pipeline)

	preReq, shortCircuit, preCount := pipeline.RunLLMPreHooks(ctx, req)
	if shortCircuit != nil {
		// Handle short-circuit with response (success case)
		if shortCircuit.Response != nil {
			resp, bifrostErr := pipeline.RunPostLLMHooks(ctx, shortCircuit.Response, nil, preCount)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			return resp, nil
		}
		// Handle short-circuit with error
		if shortCircuit.Error != nil {
			resp, bifrostErr := pipeline.RunPostLLMHooks(ctx, nil, shortCircuit.Error, preCount)
			if bifrostErr != nil {
				// Hook replaced the original short-circuit error with a different one.
				if bifrostErr != shortCircuit.Error {
					schemas.ReleaseBifrostError(shortCircuit.Error)
				}
				return nil, bifrostErr
			}
			// Hook recovered the short-circuit error into a response.
			schemas.ReleaseBifrostError(shortCircuit.Error)
			return resp, nil
		}
	}
	if preReq == nil {
		bifrostErr := newBifrostErrorFromMsg("bifrost request after plugin hooks cannot be nil")
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	}

	msg := bifrost.getChannelMessage(*preReq)
	msg.Context = ctx

	// Check if provider is closing before attempting to send (lock-free atomic check)
	// This prevents "send on closed channel" panics during provider removal/update
	if pq.isClosing() {
		bifrost.releaseChannelMessage(msg)
		bifrostErr := newBifrostErrorFromMsg("provider is shutting down")
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	}

	// Use select with done channel to detect shutdown during send
	select {
	case pq.queue <- msg:
		// Message was sent successfully
	case <-pq.done:
		bifrost.releaseChannelMessage(msg)
		bifrostErr := newBifrostErrorFromMsg("provider is shutting down")
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	case <-ctx.Done():
		bifrost.releaseChannelMessage(msg)
		bifrostErr := newBifrostErrorFromMsg("request cancelled while waiting for queue space")
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	default:
		if bifrost.dropExcessRequests.Load() {
			bifrost.releaseChannelMessage(msg)
			bifrost.logger.Warn("request dropped: queue is full, please increase the queue size or set dropExcessRequests to false")
			bifrostErr := newBifrostErrorFromMsg("request dropped: queue is full")
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.RequestType = req.RequestType
			bifrostErr.ExtraFields.Provider = provider
			bifrostErr.ExtraFields.ModelRequested = model
			return nil, bifrostErr
		}
		// Re-check closing flag before blocking send (lock-free atomic check)
		if pq.isClosing() {
			bifrost.releaseChannelMessage(msg)
			bifrostErr := newBifrostErrorFromMsg("provider is shutting down")
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.RequestType = req.RequestType
			bifrostErr.ExtraFields.Provider = provider
			bifrostErr.ExtraFields.ModelRequested = model
			return nil, bifrostErr
		}
		select {
		case pq.queue <- msg:
			// Message was sent successfully
		case <-pq.done:
			bifrost.releaseChannelMessage(msg)
			bifrostErr := newBifrostErrorFromMsg("provider is shutting down")
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.RequestType = req.RequestType
			bifrostErr.ExtraFields.Provider = provider
			bifrostErr.ExtraFields.ModelRequested = model
			return nil, bifrostErr
		case <-ctx.Done():
			bifrost.releaseChannelMessage(msg)
			bifrostErr := newBifrostErrorFromMsg("request cancelled while waiting for queue space")
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.RequestType = req.RequestType
			bifrostErr.ExtraFields.Provider = provider
			bifrostErr.ExtraFields.ModelRequested = model
			return nil, bifrostErr
		}
	}

	var result *schemas.BifrostResponse
	var resp *schemas.BifrostResponse
	pluginCount := len(*bifrost.llmPlugins.Load())
	select {
	case result = <-msg.Response:
		resp, bifrostErr := pipeline.RunPostLLMHooks(msg.Context, result, nil, pluginCount)
		if bifrostErr != nil {
			bifrost.releaseChannelMessage(msg)
			return nil, bifrostErr
		}
		bifrost.releaseChannelMessage(msg)
		// Checking if need to drop raw messages
		// This we use for requests like containers, container files, skills etc.
		if drop, ok := ctx.Value(schemas.BifrostContextKeyRawRequestResponseForLogging).(bool); ok && drop && resp != nil {
			extraField := resp.GetExtraFields()
			extraField.RawRequest = nil
			extraField.RawResponse = nil
		}
		return resp, nil
	case bifrostErrVal := <-msg.Err:
		bifrostErrPtr := bifrostErrVal
		resp, bifrostErrPtr = pipeline.RunPostLLMHooks(msg.Context, nil, bifrostErrPtr, pluginCount)
		bifrost.releaseChannelMessage(msg)
		// Drop raw request/response on error path too
		if drop, ok := ctx.Value(schemas.BifrostContextKeyRawRequestResponseForLogging).(bool); ok && drop {
			if bifrostErrPtr != nil {
				bifrostErrPtr.ExtraFields.RawRequest = nil
				bifrostErrPtr.ExtraFields.RawResponse = nil
			}
			if resp != nil {
				extraField := resp.GetExtraFields()
				extraField.RawRequest = nil
				extraField.RawResponse = nil
			}
		}
		if bifrostErrPtr != nil {
			// Hook replaced the original error with a different object; release the orphaned original.
			if bifrostErrPtr != bifrostErrVal {
				schemas.ReleaseBifrostError(bifrostErrVal)
			}
			return nil, bifrostErrPtr
		}
		// Hook recovered the error into a successful response; release the original error.
		schemas.ReleaseBifrostError(bifrostErrVal)
		return resp, nil
	}
}

// tryStreamRequest is a generic function that handles common request processing logic
// It consolidates queue setup, plugin pipeline execution, enqueue logic, and response handling
func (bifrost *Bifrost) tryStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	provider, model, _ := req.GetRequestFields()
	pq, err := bifrost.getProviderQueue(provider)
	if err != nil {
		bifrostErr := newBifrostError(err)
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	}

	// Add MCP tools to request if MCP is configured and requested
	if req.RequestType != schemas.SpeechStreamRequest && req.RequestType != schemas.TranscriptionStreamRequest && bifrost.MCPManager != nil {
		req = bifrost.MCPManager.AddToolsToRequest(ctx, req)
	}

	tracer := bifrost.getTracer()
	if tracer == nil {
		return nil, newBifrostErrorFromMsg("tracer not found in context")
	}

	// Store tracer in context BEFORE calling RunLLMPreHooks, so plugins and streaming goroutines
	// have access to it for completing deferred spans when the stream ends.
	// The streaming goroutine captures the context when it starts, so these values
	// must be set before requestHandler() is called.
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)

	pipeline := bifrost.getPluginPipeline()
	defer func() {
		if pipeline != nil {
			bifrost.releasePluginPipeline(pipeline)
		}
	}()

	preReq, shortCircuit, preCount := pipeline.RunLLMPreHooks(ctx, req)
	if shortCircuit != nil {
		// Handle short-circuit with response (success case)
		if shortCircuit.Response != nil {
			resp, bifrostErr := pipeline.RunPostLLMHooks(ctx, shortCircuit.Response, nil, preCount)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			return newBifrostMessageChan(resp), nil
		}
		// Handle short-circuit with stream
		if shortCircuit.Stream != nil {
			outputStream := make(chan *schemas.BifrostStreamChunk)

			// Transfer pipeline ownership to the goroutine so it is released after
			// the stream is fully consumed, not when this function returns.
			streamPipeline := pipeline
			pipeline = nil

			pipelinePostHookRunner := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return streamPipeline.RunPostLLMHooks(ctx, result, err, preCount)
			}

			go func() {
				defer bifrost.releasePluginPipeline(streamPipeline)
				defer close(outputStream)

				for streamMsg := range shortCircuit.Stream {
					if streamMsg == nil {
						continue
					}

					bifrostResponse := schemas.AcquireBifrostResponse()
					if streamMsg.BifrostTextCompletionResponse != nil {
						bifrostResponse.TextCompletionResponse = streamMsg.BifrostTextCompletionResponse
					}
					if streamMsg.BifrostChatResponse != nil {
						bifrostResponse.ChatResponse = streamMsg.BifrostChatResponse
					}
					if streamMsg.BifrostResponsesStreamResponse != nil {
						bifrostResponse.ResponsesStreamResponse = streamMsg.BifrostResponsesStreamResponse
					}
					if streamMsg.BifrostSpeechStreamResponse != nil {
						bifrostResponse.SpeechStreamResponse = streamMsg.BifrostSpeechStreamResponse
					}
					if streamMsg.BifrostTranscriptionStreamResponse != nil {
						bifrostResponse.TranscriptionStreamResponse = streamMsg.BifrostTranscriptionStreamResponse
					}
					if streamMsg.BifrostImageGenerationStreamResponse != nil {
						bifrostResponse.ImageGenerationStreamResponse = streamMsg.BifrostImageGenerationStreamResponse
					}

					// Run post hooks on the stream message
					processedResponse, processedError := pipelinePostHookRunner(ctx, bifrostResponse, streamMsg.BifrostError)

					// Release the bifrostResponse wrapper back to pool after post hooks have copied needed data
					schemas.ReleaseBifrostResponse(bifrostResponse)

					streamResponse := schemas.AcquireBifrostStreamChunk()
					if processedResponse != nil {
						streamResponse.BifrostTextCompletionResponse = processedResponse.TextCompletionResponse
						streamResponse.BifrostChatResponse = processedResponse.ChatResponse
						streamResponse.BifrostResponsesStreamResponse = processedResponse.ResponsesStreamResponse
						streamResponse.BifrostSpeechStreamResponse = processedResponse.SpeechStreamResponse
						streamResponse.BifrostTranscriptionStreamResponse = processedResponse.TranscriptionStreamResponse
						streamResponse.BifrostImageGenerationStreamResponse = processedResponse.ImageGenerationStreamResponse
					}
					if processedError != nil {
						streamResponse.BifrostError = processedError
					}

					// Send the processed message to the output stream
					outputStream <- streamResponse
				}
			}()

			return outputStream, nil
		}
		// Handle short-circuit with error
		if shortCircuit.Error != nil {
			resp, bifrostErr := pipeline.RunPostLLMHooks(ctx, nil, shortCircuit.Error, preCount)
			if bifrostErr != nil {
				// Hook replaced the original short-circuit error with a different one.
				if bifrostErr != shortCircuit.Error {
					schemas.ReleaseBifrostError(shortCircuit.Error)
				}
				return nil, bifrostErr
			}
			// Hook recovered the short-circuit error into a response.
			schemas.ReleaseBifrostError(shortCircuit.Error)
			return newBifrostMessageChan(resp), nil
		}
	}
	if preReq == nil {
		bifrostErr := newBifrostErrorFromMsg("bifrost request after plugin hooks cannot be nil")
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	}

	msg := bifrost.getChannelMessage(*preReq)
	msg.Context = ctx

	// Check if provider is closing before attempting to send (lock-free atomic check)
	// This prevents "send on closed channel" panics during provider removal/update
	if pq.isClosing() {
		bifrost.releaseChannelMessage(msg)
		bifrostErr := newBifrostErrorFromMsg("provider is shutting down")
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	}

	// Use select with done channel to detect shutdown during send
	select {
	case pq.queue <- msg:
		// Message was sent successfully
	case <-pq.done:
		bifrost.releaseChannelMessage(msg)
		bifrostErr := newBifrostErrorFromMsg("provider is shutting down")
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	case <-ctx.Done():
		bifrost.releaseChannelMessage(msg)
		bifrostErr := newBifrostErrorFromMsg("request cancelled while waiting for queue space")
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.RequestType = req.RequestType
		bifrostErr.ExtraFields.Provider = provider
		bifrostErr.ExtraFields.ModelRequested = model
		return nil, bifrostErr
	default:
		if bifrost.dropExcessRequests.Load() {
			bifrost.releaseChannelMessage(msg)
			bifrost.logger.Warn("request dropped: queue is full, please increase the queue size or set dropExcessRequests to false")
			bifrostErr := newBifrostErrorFromMsg("request dropped: queue is full")
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.RequestType = req.RequestType
			bifrostErr.ExtraFields.Provider = provider
			bifrostErr.ExtraFields.ModelRequested = model
			return nil, bifrostErr
		}
		// Re-check closing flag before blocking send (lock-free atomic check)
		if pq.isClosing() {
			bifrost.releaseChannelMessage(msg)
			bifrostErr := newBifrostErrorFromMsg("provider is shutting down")
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.RequestType = req.RequestType
			bifrostErr.ExtraFields.Provider = provider
			bifrostErr.ExtraFields.ModelRequested = model
			return nil, bifrostErr
		}
		select {
		case pq.queue <- msg:
			// Message was sent successfully
		case <-pq.done:
			bifrost.releaseChannelMessage(msg)
			bifrostErr := newBifrostErrorFromMsg("provider is shutting down")
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.RequestType = req.RequestType
			bifrostErr.ExtraFields.Provider = provider
			bifrostErr.ExtraFields.ModelRequested = model
			return nil, bifrostErr
		case <-ctx.Done():
			bifrost.releaseChannelMessage(msg)
			bifrostErr := newBifrostErrorFromMsg("request cancelled while waiting for queue space")
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.RequestType = req.RequestType
			bifrostErr.ExtraFields.Provider = provider
			bifrostErr.ExtraFields.ModelRequested = model
			return nil, bifrostErr
		}
	}

	select {
	case stream := <-msg.ResponseStream:
		bifrost.releaseChannelMessage(msg)
		return stream, nil
	case bifrostErrVal := <-msg.Err:
		if bifrostErrVal.Error != nil {
			bifrost.logger.Debug("error while executing stream request: %s", bifrostErrVal.Error.Message)
		} else {
			bifrost.logger.Debug("error while executing stream request: %+v", bifrostErrVal)
		}
		// Marking final chunk
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		// On error we will complete post-hooks
		recoveredResp, recoveredErr := pipeline.RunPostLLMHooks(ctx, nil, bifrostErrVal, len(*bifrost.llmPlugins.Load()))
		bifrost.releaseChannelMessage(msg)
		if recoveredErr != nil {
			// Hook replaced the original error with a different object; release the orphaned original.
			if recoveredErr != bifrostErrVal {
				schemas.ReleaseBifrostError(bifrostErrVal)
			}
			return nil, recoveredErr
		}
		if recoveredResp != nil {
			// Hook recovered the error into a successful response; release the original error.
			schemas.ReleaseBifrostError(bifrostErrVal)
			return newBifrostMessageChan(recoveredResp), nil
		}
		return nil, bifrostErrVal
	}
}

// executeRequestWithRetries is a generic function that handles common request processing logic
// It consolidates retry logic, backoff calculation, and error handling
// It is not a bifrost method because interface methods in go cannot be generic
func executeRequestWithRetries[T any](
	ctx *schemas.BifrostContext,
	config *schemas.ProviderConfig,
	requestHandler func() (T, *schemas.BifrostError),
	requestType schemas.RequestType,
	providerKey schemas.ModelProvider,
	model string,
	req *schemas.BifrostRequest,
	logger schemas.Logger,
) (T, *schemas.BifrostError) {
	var result T
	var bifrostError *schemas.BifrostError
	var attempts int

	for attempts = 0; attempts <= config.NetworkConfig.MaxRetries; attempts++ {
		ctx.SetValue(schemas.BifrostContextKeyNumberOfRetries, attempts)
		if attempts > 0 {
			// Log retry attempt
			var retryMsg string
			if bifrostError != nil && bifrostError.Error != nil {
				retryMsg = bifrostError.Error.Message
			} else if bifrostError != nil && bifrostError.StatusCode != nil {
				retryMsg = fmt.Sprintf("status=%d", *bifrostError.StatusCode)
				if bifrostError.Type != nil {
					retryMsg += ", type=" + *bifrostError.Type
				}
			}
			logger.Debug("retrying request (attempt %d/%d) for model %s: %s", attempts, config.NetworkConfig.MaxRetries, model, retryMsg)

			// Release the error from the previous failed attempt before overwriting it.
			// The final attempt's error is returned to the caller which is responsible for releasing it.
			if bifrostError != nil {
				schemas.ReleaseBifrostError(bifrostError)
				bifrostError = nil
			}

			// Calculate and apply backoff
			backoff := calculateBackoff(attempts-1, config)
			logger.Debug("sleeping for %s before retry", backoff)

			time.Sleep(backoff)
		}

		logger.Debug("attempting %s request for provider %s", requestType, providerKey)

		// Start span for LLM call (or retry attempt)
		tracer, ok := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
		if !ok || tracer == nil {
			logger.Error("tracer not found in context of executeRequestWithRetries")
			return result, newBifrostErrorFromMsg("tracer not found in context")
		}
		var spanName string
		var spanKind schemas.SpanKind
		if attempts > 0 {
			spanName = fmt.Sprintf("retry.attempt.%d", attempts)
			spanKind = schemas.SpanKindRetry
		} else {
			spanName = "llm.call"
			spanKind = schemas.SpanKindLLMCall
		}
		spanCtx, handle := tracer.StartSpan(ctx, spanName, spanKind)
		tracer.SetAttribute(handle, schemas.AttrProviderName, string(providerKey))
		tracer.SetAttribute(handle, schemas.AttrRequestModel, model)
		tracer.SetAttribute(handle, "request.type", string(requestType))
		if attempts > 0 {
			tracer.SetAttribute(handle, "retry.count", attempts)
		}

		// Add context-related attributes (selected key, virtual key, team, customer, etc.)
		if selectedKeyID, ok := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string); ok && selectedKeyID != "" {
			tracer.SetAttribute(handle, schemas.AttrSelectedKeyID, selectedKeyID)
		}
		if selectedKeyName, ok := ctx.Value(schemas.BifrostContextKeySelectedKeyName).(string); ok && selectedKeyName != "" {
			tracer.SetAttribute(handle, schemas.AttrSelectedKeyName, selectedKeyName)
		}
		if virtualKeyID, ok := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string); ok && virtualKeyID != "" {
			tracer.SetAttribute(handle, schemas.AttrVirtualKeyID, virtualKeyID)
		}
		if virtualKeyName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyName).(string); ok && virtualKeyName != "" {
			tracer.SetAttribute(handle, schemas.AttrVirtualKeyName, virtualKeyName)
		}
		if teamID, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamID).(string); ok && teamID != "" {
			tracer.SetAttribute(handle, schemas.AttrTeamID, teamID)
		}
		if teamName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamName).(string); ok && teamName != "" {
			tracer.SetAttribute(handle, schemas.AttrTeamName, teamName)
		}
		if customerID, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID).(string); ok && customerID != "" {
			tracer.SetAttribute(handle, schemas.AttrCustomerID, customerID)
		}
		if customerName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerName).(string); ok && customerName != "" {
			tracer.SetAttribute(handle, schemas.AttrCustomerName, customerName)
		}
		if fallbackIndex, ok := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int); ok {
			tracer.SetAttribute(handle, schemas.AttrFallbackIndex, fallbackIndex)
		}
		tracer.SetAttribute(handle, schemas.AttrNumberOfRetries, attempts)

		// Populate LLM request attributes (messages, parameters, etc.)
		if req != nil {
			tracer.PopulateLLMRequestAttributes(handle, req)
		}

		// Update context with span ID
		ctx.SetValue(schemas.BifrostContextKeySpanID, spanCtx.Value(schemas.BifrostContextKeySpanID))

		// Record stream start time for TTFT calculation (only for streaming requests)
		// This is also used by RunPostLLMHooks to detect streaming mode
		if IsStreamRequestType(requestType) {
			streamStartTime := time.Now()
			ctx.SetValue(schemas.BifrostContextKeyStreamStartTime, streamStartTime)
		}

		// Attempt the request
		result, bifrostError = requestHandler()

		// Check if result is a streaming channel - if so, defer span completion
		if _, isStreamChan := any(result).(chan *schemas.BifrostStreamChunk); isStreamChan {
			// For streaming requests, store the span handle in TraceStore keyed by trace ID
			// This allows the provider's streaming goroutine to retrieve it later
			if traceID, ok := ctx.Value(schemas.BifrostContextKeyTraceID).(string); ok && traceID != "" {
				tracer.StoreDeferredSpan(traceID, handle)
			}
			// Don't end the span here - it will be ended when streaming completes
		} else {
			// Populate LLM response attributes for non-streaming responses
			if resp, ok := any(result).(*schemas.BifrostResponse); ok {
				tracer.PopulateLLMResponseAttributes(handle, resp, bifrostError)
			}

			// End span with appropriate status
			if bifrostError != nil {
				if bifrostError.Error != nil {
					tracer.SetAttribute(handle, "error", bifrostError.Error.Message)
				}
				if bifrostError.StatusCode != nil {
					tracer.SetAttribute(handle, "status_code", *bifrostError.StatusCode)
				}
				tracer.EndSpan(handle, schemas.SpanStatusError, "request failed")
			} else {
				tracer.EndSpan(handle, schemas.SpanStatusOk, "")
			}
		}

		logger.Debug("request %s for provider %s completed", requestType, providerKey)

		// Check if successful or if we should retry
		if bifrostError == nil ||
			bifrostError.IsBifrostError ||
			(bifrostError.Error != nil && bifrostError.Error.Type != nil && *bifrostError.Error.Type == schemas.RequestCancelled) {
			break
		}

		// Check if we should retry based on status code or error message
		shouldRetry := false

		if bifrostError.Error != nil && (bifrostError.Error.Message == schemas.ErrProviderDoRequest || bifrostError.Error.Message == schemas.ErrProviderNetworkError) {
			shouldRetry = true
			logger.Debug("detected request HTTP/network error, will retry: %s", bifrostError.Error.Message)
		}

		// Retry if status code or error object indicates rate limiting
		if (bifrostError.StatusCode != nil && retryableStatusCodes[*bifrostError.StatusCode]) ||
			(bifrostError.Error != nil &&
				(IsRateLimitErrorMessage(bifrostError.Error.Message) ||
					(bifrostError.Error.Type != nil && IsRateLimitErrorMessage(*bifrostError.Error.Type)))) {
			shouldRetry = true
			logger.Debug("detected rate limit error in message, will retry: %s", bifrostError.Error.Message)
		}

		if !shouldRetry {
			break
		}
	}

	// Add retry information to error
	if attempts > 0 {
		logger.Debug("request failed after %d %s", attempts, map[bool]string{true: "attempts", false: "attempt"}[attempts > 1])
	}

	return result, bifrostError
}

// requestWorker handles incoming requests from the queue for a specific provider.
// It manages retries, error handling, and response processing.
func (bifrost *Bifrost) requestWorker(provider schemas.Provider, config *schemas.ProviderConfig, pq *ProviderQueue) {
	defer func() {
		if waitGroupValue, ok := bifrost.waitGroups.Load(provider.GetProviderKey()); ok {
			waitGroup := waitGroupValue.(*sync.WaitGroup)
			waitGroup.Done()
		}
	}()

	for req := range pq.queue {
		_, model, _ := req.BifrostRequest.GetRequestFields()

		var result *schemas.BifrostResponse
		var stream chan *schemas.BifrostStreamChunk
		var bifrostError *schemas.BifrostError
		var err error

		// Determine the base provider type for key requirement checks
		baseProvider := provider.GetProviderKey()
		if cfg := config.CustomProviderConfig; cfg != nil && cfg.BaseProviderType != "" {
			baseProvider = cfg.BaseProviderType
		}
		req.Context.SetValue(schemas.BifrostContextKeyIsCustomProvider, !IsStandardProvider(baseProvider))

		key := schemas.Key{}
		var keys []schemas.Key
		if providerRequiresKey(baseProvider, config.CustomProviderConfig) {
			// ListModels needs all enabled/supported keys so providers can aggregate
			// and report per-key statuses (KeyStatuses).
			if req.RequestType == schemas.ListModelsRequest {
				keys, err = bifrost.getAllSupportedKeys(req.Context, provider.GetProviderKey(), baseProvider)
				if err != nil {
					bifrost.logger.Debug("error getting supported keys for list models: %v", err)
					bfErr := schemas.AcquireBifrostError()
					bfErr.IsBifrostError = false
					bfErr.Error.Message = err.Error()
					bfErr.Error.Error = err
					bfErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
					bfErr.ExtraFields.Provider = provider.GetProviderKey()
					bfErr.ExtraFields.ModelRequested = model
					bfErr.ExtraFields.RequestType = req.RequestType
					req.Err <- bfErr
					continue
				}
			} else {
				// Determine if this is a multi-key batch/file/container operation
				// BatchCreate, FileUpload, ContainerCreate, ContainerFileCreate use single key; other batch/file/container ops use multiple keys
				isMultiKeyBatchOp := isBatchRequestType(req.RequestType) && req.RequestType != schemas.BatchCreateRequest
				isMultiKeyFileOp := isFileRequestType(req.RequestType) && req.RequestType != schemas.FileUploadRequest
				isMultiKeyContainerOp := isContainerRequestType(req.RequestType) && req.RequestType != schemas.ContainerCreateRequest && req.RequestType != schemas.ContainerFileCreateRequest

				if isMultiKeyBatchOp || isMultiKeyFileOp || isMultiKeyContainerOp {
					var modelPtr *string
					if model != "" {
						modelPtr = &model
					}
					keys, err = bifrost.getKeysForBatchAndFileOps(req.Context, provider.GetProviderKey(), baseProvider, modelPtr, isMultiKeyBatchOp)
					if err != nil {
						bifrost.logger.Debug("error getting keys for batch/file operation: %v", err)
						bfErr := schemas.AcquireBifrostError()
						bfErr.IsBifrostError = false
						bfErr.Error.Message = err.Error()
						bfErr.Error.Error = err
						bfErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
						bfErr.ExtraFields.Provider = provider.GetProviderKey()
						bfErr.ExtraFields.ModelRequested = model
						bfErr.ExtraFields.RequestType = req.RequestType
						req.Err <- bfErr
						continue
					}
				} else {
					// Use the custom provider name for actual key selection, but pass base provider type for key validation
					// Start span for key selection
					keyTracer := bifrost.getTracer()
					keySpanCtx, keyHandle := keyTracer.StartSpan(req.Context, "key.selection", schemas.SpanKindInternal)
					keyTracer.SetAttribute(keyHandle, schemas.AttrProviderName, string(provider.GetProviderKey()))
					keyTracer.SetAttribute(keyHandle, schemas.AttrRequestModel, model)

					key, err = bifrost.selectKeyFromProviderForModel(req.Context, req.RequestType, provider.GetProviderKey(), model, baseProvider)
					if err != nil {
						keyTracer.SetAttribute(keyHandle, "error", err.Error())
						keyTracer.EndSpan(keyHandle, schemas.SpanStatusError, err.Error())
						bifrost.logger.Debug("error selecting key for model %s: %v", model, err)
						bfErr := schemas.AcquireBifrostError()
						bfErr.IsBifrostError = false
						bfErr.Error.Message = err.Error()
						bfErr.Error.Error = err
						bfErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
						bfErr.ExtraFields.Provider = provider.GetProviderKey()
						bfErr.ExtraFields.ModelRequested = model
						bfErr.ExtraFields.RequestType = req.RequestType
						req.Err <- bfErr
						continue
					}
					keyTracer.SetAttribute(keyHandle, "key.id", key.ID)
					keyTracer.SetAttribute(keyHandle, "key.name", key.Name)
					keyTracer.EndSpan(keyHandle, schemas.SpanStatusOk, "")
					// Update context with span ID for subsequent operations
					req.Context.SetValue(schemas.BifrostContextKeySpanID, keySpanCtx.Value(schemas.BifrostContextKeySpanID))
					req.Context.SetValue(schemas.BifrostContextKeySelectedKeyID, key.ID)
					req.Context.SetValue(schemas.BifrostContextKeySelectedKeyName, key.Name)
				}
			}
		}
		// Create plugin pipeline for streaming requests outside retry loop to prevent leaks
		var postHookRunner schemas.PostHookRunner
		var pipeline *PluginPipeline
		var pipelineReleased atomic.Bool // Track if pipeline has been released to prevent double-release
		if IsStreamRequestType(req.RequestType) {
			pipeline = bifrost.getPluginPipeline()
			postHookRunner = func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				resp, bifrostErr := pipeline.RunPostLLMHooks(ctx, result, err, len(*bifrost.llmPlugins.Load()))
				if bifrostErr != nil {
					return nil, bifrostErr
				}
				return resp, nil
			}
			// Store a finalizer callback to create aggregated post-hook spans at stream end.
			// This closure captures the pipeline reference and releases it after finalization.
			// The pipelineReleased flag prevents double-release when:
			// 1. Streaming completes normally (finalizer releases pipeline)
			// 2. Error occurs mid-stream (finalizer releases, then requestWorker tries to release)
			postHookSpanFinalizer := func(ctx context.Context) {
				if pipelineReleased.Swap(true) {
					return // Already released, skip
				}
				pipeline.FinalizeStreamingPostHookSpans(ctx)
				bifrost.releasePluginPipeline(pipeline)
			}
			req.Context.SetValue(schemas.BifrostContextKeyPostHookSpanFinalizer, postHookSpanFinalizer)
		}

		// Execute request with retries
		if IsStreamRequestType(req.RequestType) {
			stream, bifrostError = executeRequestWithRetries(req.Context, config, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				return bifrost.handleProviderStreamRequest(provider, req, key, postHookRunner)
			}, req.RequestType, provider.GetProviderKey(), model, &req.BifrostRequest, bifrost.logger)
		} else {
			result, bifrostError = executeRequestWithRetries(req.Context, config, func() (*schemas.BifrostResponse, *schemas.BifrostError) {
				return bifrost.handleProviderRequest(provider, req, key, keys)
			}, req.RequestType, provider.GetProviderKey(), model, &req.BifrostRequest, bifrost.logger)
		}

		// Release pipeline immediately for non-streaming requests only
		// For streaming, the pipeline is released in the postHookSpanFinalizer after streaming completes
		// Exception: if streaming request has an error, release immediately since finalizer won't be called
		if pipeline != nil && (!IsStreamRequestType(req.RequestType) || bifrostError != nil) {
			// Use atomic flag to prevent double-release. The finalizer may have already released
			// the pipeline if streaming started but failed mid-stream (error sent after some chunks).
			if !pipelineReleased.Swap(true) {
				bifrost.releasePluginPipeline(pipeline)
			}
			// Clear the stale postHookSpanFinalizer from context. It captures a reference
			// to the pipeline we just released; if completeDeferredSpan's safety-net defer
			// calls it later, the pool pointer may have been recycled, causing a double-release panic.
			req.Context.SetValue(schemas.BifrostContextKeyPostHookSpanFinalizer, nil)
		}

		if bifrostError != nil {
			if bifrostError.ExtraFields == nil {
				bifrostError.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostError.ExtraFields.RequestType = req.RequestType
			bifrostError.ExtraFields.Provider = provider.GetProviderKey()
			bifrostError.ExtraFields.ModelRequested = model
			// Send error: try non-blocking first (always succeeds for buffered size-1 channel),
			// fall back to context-aware send only if channel is unexpectedly full.
			select {
			case req.Err <- bifrostError:
				// Error sent successfully
			default:
				select {
				case req.Err <- bifrostError:
					// Error sent successfully
				case <-req.Context.Done():
					schemas.ReleaseBifrostError(bifrostError)
					bifrost.logger.Debug("Client context cancelled while sending error response")
				case <-time.After(5 * time.Second):
					schemas.ReleaseBifrostError(bifrostError)
					bifrost.logger.Warn("Timeout while sending error response, client may have disconnected")
				}
			}
		} else {
			if IsStreamRequestType(req.RequestType) {
				// Send stream: try non-blocking first (always succeeds for buffered size-1 channel),
				// fall back to context-aware send only if channel is unexpectedly full.
				streamSent := false
				select {
				case req.ResponseStream <- stream:
					streamSent = true
				default:
					select {
					case req.ResponseStream <- stream:
						streamSent = true
					case <-req.Context.Done():
						bifrost.logger.Debug("Client context cancelled while sending stream response")
					case <-time.After(5 * time.Second):
						bifrost.logger.Warn("Timeout while sending stream response, client may have disconnected")
					}
				}
				// If stream wasn't delivered, no consumer will call completeDeferredSpan,
				// so the PluginPipeline's postHookSpanFinalizer will never fire. Release it here.
				if !streamSent && pipeline != nil {
					if !pipelineReleased.Swap(true) {
						bifrost.releasePluginPipeline(pipeline)
					}
					req.Context.SetValue(schemas.BifrostContextKeyPostHookSpanFinalizer, nil)
				}
			} else {
				// Send response: try non-blocking first (always succeeds for buffered size-1 channel),
				// fall back to context-aware send only if channel is unexpectedly full.
				select {
				case req.Response <- result:
					// Response sent successfully
				default:
					select {
					case req.Response <- result:
						// Response sent successfully
					case <-req.Context.Done():
						schemas.ReleaseBifrostResponse(result)
						bifrost.logger.Debug("Client context cancelled while sending response")
					case <-time.After(5 * time.Second):
						schemas.ReleaseBifrostResponse(result)
						bifrost.logger.Warn("Timeout while sending response, client may have disconnected")
					}
				}
			}
		}
	}

	// bifrost.logger.Debug("worker for provider %s exiting...", provider.GetProviderKey())
}

// handleProviderRequest handles the request to the provider based on the request type
// key is used for single-key operations, keys is used for batch/file operations that need multiple keys
func (bifrost *Bifrost) handleProviderRequest(provider schemas.Provider, req *ChannelMessage, key schemas.Key, keys []schemas.Key) (*schemas.BifrostResponse, *schemas.BifrostError) {
	response := &schemas.BifrostResponse{}
	switch req.RequestType {
	case schemas.ListModelsRequest:
		listModelsResponse, bifrostError := provider.ListModels(req.Context, keys, req.BifrostRequest.ListModelsRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ListModelsResponse = listModelsResponse
	case schemas.TextCompletionRequest:
		textCompletionResponse, bifrostError := provider.TextCompletion(req.Context, key, req.BifrostRequest.TextCompletionRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.TextCompletionResponse = textCompletionResponse
	case schemas.ChatCompletionRequest:
		chatCompletionResponse, bifrostError := provider.ChatCompletion(req.Context, key, req.BifrostRequest.ChatRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ChatResponse = chatCompletionResponse
	case schemas.ResponsesRequest:
		responsesResponse, bifrostError := provider.Responses(req.Context, key, req.BifrostRequest.ResponsesRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ResponsesResponse = responsesResponse
	case schemas.CountTokensRequest:
		countTokensResponse, bifrostError := provider.CountTokens(req.Context, key, req.BifrostRequest.CountTokensRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.CountTokensResponse = countTokensResponse
	case schemas.EmbeddingRequest:
		embeddingResponse, bifrostError := provider.Embedding(req.Context, key, req.BifrostRequest.EmbeddingRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.EmbeddingResponse = embeddingResponse
	case schemas.SpeechRequest:
		speechResponse, bifrostError := provider.Speech(req.Context, key, req.BifrostRequest.SpeechRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.SpeechResponse = speechResponse
	case schemas.TranscriptionRequest:
		transcriptionResponse, bifrostError := provider.Transcription(req.Context, key, req.BifrostRequest.TranscriptionRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.TranscriptionResponse = transcriptionResponse
	case schemas.ImageGenerationRequest:
		imageResponse, bifrostError := provider.ImageGeneration(req.Context, key, req.BifrostRequest.ImageGenerationRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ImageGenerationResponse = imageResponse
	case schemas.ImageEditRequest:
		imageEditResponse, bifrostError := provider.ImageEdit(req.Context, key, req.BifrostRequest.ImageEditRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ImageGenerationResponse = imageEditResponse
	case schemas.ImageVariationRequest:
		imageVariationResponse, bifrostError := provider.ImageVariation(req.Context, key, req.BifrostRequest.ImageVariationRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ImageGenerationResponse = imageVariationResponse
	case schemas.FileUploadRequest:
		fileUploadResponse, bifrostError := provider.FileUpload(req.Context, key, req.BifrostRequest.FileUploadRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.FileUploadResponse = fileUploadResponse
	case schemas.FileListRequest:
		fileListResponse, bifrostError := provider.FileList(req.Context, keys, req.BifrostRequest.FileListRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.FileListResponse = fileListResponse
	case schemas.FileRetrieveRequest:
		fileRetrieveResponse, bifrostError := provider.FileRetrieve(req.Context, keys, req.BifrostRequest.FileRetrieveRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.FileRetrieveResponse = fileRetrieveResponse
	case schemas.FileDeleteRequest:
		fileDeleteResponse, bifrostError := provider.FileDelete(req.Context, keys, req.BifrostRequest.FileDeleteRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.FileDeleteResponse = fileDeleteResponse
	case schemas.FileContentRequest:
		fileContentResponse, bifrostError := provider.FileContent(req.Context, keys, req.BifrostRequest.FileContentRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.FileContentResponse = fileContentResponse
	case schemas.BatchCreateRequest:
		batchCreateResponse, bifrostError := provider.BatchCreate(req.Context, key, req.BifrostRequest.BatchCreateRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.BatchCreateResponse = batchCreateResponse
	case schemas.BatchListRequest:
		batchListResponse, bifrostError := provider.BatchList(req.Context, keys, req.BifrostRequest.BatchListRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.BatchListResponse = batchListResponse
	case schemas.BatchRetrieveRequest:
		batchRetrieveResponse, bifrostError := provider.BatchRetrieve(req.Context, keys, req.BifrostRequest.BatchRetrieveRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.BatchRetrieveResponse = batchRetrieveResponse
	case schemas.BatchCancelRequest:
		batchCancelResponse, bifrostError := provider.BatchCancel(req.Context, keys, req.BifrostRequest.BatchCancelRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.BatchCancelResponse = batchCancelResponse
	case schemas.BatchResultsRequest:
		batchResultsResponse, bifrostError := provider.BatchResults(req.Context, keys, req.BifrostRequest.BatchResultsRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.BatchResultsResponse = batchResultsResponse
	case schemas.ContainerCreateRequest:
		containerCreateResponse, bifrostError := provider.ContainerCreate(req.Context, key, req.BifrostRequest.ContainerCreateRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerCreateResponse = containerCreateResponse
	case schemas.ContainerListRequest:
		containerListResponse, bifrostError := provider.ContainerList(req.Context, keys, req.BifrostRequest.ContainerListRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerListResponse = containerListResponse
	case schemas.ContainerRetrieveRequest:
		containerRetrieveResponse, bifrostError := provider.ContainerRetrieve(req.Context, keys, req.BifrostRequest.ContainerRetrieveRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerRetrieveResponse = containerRetrieveResponse
	case schemas.ContainerDeleteRequest:
		containerDeleteResponse, bifrostError := provider.ContainerDelete(req.Context, keys, req.BifrostRequest.ContainerDeleteRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerDeleteResponse = containerDeleteResponse
	case schemas.ContainerFileCreateRequest:
		containerFileCreateResponse, bifrostError := provider.ContainerFileCreate(req.Context, key, req.BifrostRequest.ContainerFileCreateRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerFileCreateResponse = containerFileCreateResponse
	case schemas.ContainerFileListRequest:
		containerFileListResponse, bifrostError := provider.ContainerFileList(req.Context, keys, req.BifrostRequest.ContainerFileListRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerFileListResponse = containerFileListResponse
	case schemas.ContainerFileRetrieveRequest:
		containerFileRetrieveResponse, bifrostError := provider.ContainerFileRetrieve(req.Context, keys, req.BifrostRequest.ContainerFileRetrieveRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerFileRetrieveResponse = containerFileRetrieveResponse
	case schemas.ContainerFileContentRequest:
		containerFileContentResponse, bifrostError := provider.ContainerFileContent(req.Context, keys, req.BifrostRequest.ContainerFileContentRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerFileContentResponse = containerFileContentResponse
	case schemas.ContainerFileDeleteRequest:
		containerFileDeleteResponse, bifrostError := provider.ContainerFileDelete(req.Context, keys, req.BifrostRequest.ContainerFileDeleteRequest)
		if bifrostError != nil {
			return nil, bifrostError
		}
		response.ContainerFileDeleteResponse = containerFileDeleteResponse
	default:
		_, model, _ := req.BifrostRequest.GetRequestFields()
		bifrostError := schemas.AcquireBifrostError()
		bifrostError.IsBifrostError = false
		bifrostError.Error.Message = fmt.Sprintf("unsupported request type: %s", req.RequestType)
		bifrostError.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		bifrostError.ExtraFields.RequestType = req.RequestType
		bifrostError.ExtraFields.Provider = provider.GetProviderKey()
		bifrostError.ExtraFields.ModelRequested = model
		return nil, bifrostError
	}
	return response, nil
}

// handleProviderStreamRequest handles the stream request to the provider based on the request type
func (bifrost *Bifrost) handleProviderStreamRequest(provider schemas.Provider, req *ChannelMessage, key schemas.Key, postHookRunner schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	switch req.RequestType {
	case schemas.TextCompletionStreamRequest:
		return provider.TextCompletionStream(req.Context, postHookRunner, key, req.BifrostRequest.TextCompletionRequest)
	case schemas.ChatCompletionStreamRequest:
		return provider.ChatCompletionStream(req.Context, postHookRunner, key, req.BifrostRequest.ChatRequest)
	case schemas.ResponsesStreamRequest:
		return provider.ResponsesStream(req.Context, postHookRunner, key, req.BifrostRequest.ResponsesRequest)
	case schemas.SpeechStreamRequest:
		return provider.SpeechStream(req.Context, postHookRunner, key, req.BifrostRequest.SpeechRequest)
	case schemas.TranscriptionStreamRequest:
		return provider.TranscriptionStream(req.Context, postHookRunner, key, req.BifrostRequest.TranscriptionRequest)
	case schemas.ImageGenerationStreamRequest:
		return provider.ImageGenerationStream(req.Context, postHookRunner, key, req.BifrostRequest.ImageGenerationRequest)
	case schemas.ImageEditStreamRequest:
		return provider.ImageEditStream(req.Context, postHookRunner, key, req.BifrostRequest.ImageEditRequest)
	default:
		_, model, _ := req.BifrostRequest.GetRequestFields()
		bifrostError := schemas.AcquireBifrostError()
		bifrostError.IsBifrostError = false
		bifrostError.Error.Message = fmt.Sprintf("unsupported request type: %s", req.RequestType)
		bifrostError.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		bifrostError.ExtraFields.RequestType = req.RequestType
		bifrostError.ExtraFields.Provider = provider.GetProviderKey()
		bifrostError.ExtraFields.ModelRequested = model
		return nil, bifrostError
	}
}

// handleMCPToolExecution is the common handler for MCP tool execution with plugin pipeline support.
// It handles pre-hooks, execution, post-hooks, and error handling for both Chat and Responses formats.
//
// Parameters:
//   - ctx: Execution context
//   - mcpRequest: The MCP request to execute (already populated with tool call)
//   - requestType: The request type for error reporting (ChatCompletionRequest or ResponsesRequest)
//
// Returns:
//   - *schemas.BifrostMCPResponse: The MCP response after all hooks
//   - *schemas.BifrostError: Any execution error
func (bifrost *Bifrost) handleMCPToolExecution(ctx *schemas.BifrostContext, mcpRequest *schemas.BifrostMCPRequest, requestType schemas.RequestType) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	if bifrost.MCPManager == nil {
		bifrostError := schemas.AcquireBifrostError()
		bifrostError.IsBifrostError = false
		bifrostError.Error.Message = "MCP is not configured in this Bifrost instance"
		bifrostError.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		bifrostError.ExtraFields.RequestType = requestType
		return nil, bifrostError
	}

	// Ensure request ID exists for hooks/tracing consistency
	if _, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok {
		ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.New().String())
	}

	// Get plugin pipeline for MCP hooks
	pipeline := bifrost.getPluginPipeline()
	defer bifrost.releasePluginPipeline(pipeline)

	// Run pre-hooks
	preReq, shortCircuit, preCount := pipeline.RunMCPPreHooks(ctx, mcpRequest)

	// Handle short-circuit cases
	if shortCircuit != nil {
		// Handle short-circuit with response (success case)
		if shortCircuit.Response != nil {
			finalMcpResp, bifrostErr := pipeline.RunMCPPostHooks(ctx, shortCircuit.Response, nil, preCount)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			return finalMcpResp, nil
		}
		// Handle short-circuit with error
		if shortCircuit.Error != nil {
			// Capture post-hook results to respect transformations or recovery
			finalResp, finalErr := pipeline.RunMCPPostHooks(ctx, nil, shortCircuit.Error, preCount)
			// Return post-hook error if present (post-hook may have transformed the error)
			if finalErr != nil {
				return nil, finalErr
			}
			// Return post-hook response if present (post-hook may have recovered from error)
			if finalResp != nil {
				return finalResp, nil
			}
			// Fall back to original short-circuit error if post-hooks returned nil/nil
			return nil, shortCircuit.Error
		}
	}

	if preReq == nil {
		bifrostError := schemas.AcquireBifrostError()
		bifrostError.IsBifrostError = false
		bifrostError.Error.Message = "MCP request after plugin hooks cannot be nil"
		bifrostError.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		bifrostError.ExtraFields.RequestType = requestType
		return nil, bifrostError
	}

	// Execute tool with modified request
	result, err := bifrost.MCPManager.ExecuteToolCall(ctx, preReq)

	// Prepare MCP response and error for post-hooks
	var mcpResp *schemas.BifrostMCPResponse
	var bifrostErr *schemas.BifrostError

	if err != nil {
		bifrostError := schemas.AcquireBifrostError()
		bifrostError.IsBifrostError = false
		bifrostError.Error.Message = err.Error()
		bifrostError.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		bifrostError.ExtraFields.RequestType = requestType
		return nil, bifrostError
	} else if result == nil {
		bifrostError := schemas.AcquireBifrostError()
		bifrostError.IsBifrostError = false
		bifrostError.Error.Message = "tool execution returned nil result"
		bifrostError.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		bifrostError.ExtraFields.RequestType = requestType
		return nil, bifrostError
	} else {
		// Use the MCP response directly
		mcpResp = result
	}

	// Run post-hooks
	finalResp, finalErr := pipeline.RunMCPPostHooks(ctx, mcpResp, bifrostErr, preCount)

	if finalErr != nil {
		return nil, finalErr
	}

	return finalResp, nil
}

// executeMCPToolWithHooks is a wrapper around handleMCPToolExecution that matches the signature
// expected by the agent's executeToolFunc parameter. It runs MCP plugin hooks before and after
// tool execution to enable logging, telemetry, and other plugin functionality.
func (bifrost *Bifrost) executeMCPToolWithHooks(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
	// Defensive check: context must be non-nil to prevent panics in plugin hooks
	if ctx == nil {
		return nil, fmt.Errorf("context cannot be nil")
	}

	if request == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	// Determine request type from the MCP request - explicitly handle all known types
	var requestType schemas.RequestType
	switch request.RequestType {
	case schemas.MCPRequestTypeChatToolCall:
		requestType = schemas.ChatCompletionRequest
	case schemas.MCPRequestTypeResponsesToolCall:
		requestType = schemas.ResponsesRequest
	default:
		// Return error for unknown/unsupported request types instead of silently defaulting
		return nil, fmt.Errorf("unsupported MCP request type: %s", request.RequestType)
	}

	resp, bifrostErr := bifrost.handleMCPToolExecution(ctx, request, requestType)
	if bifrostErr != nil {
		return nil, fmt.Errorf("%s", GetErrorMessage(bifrostErr))
	}
	return resp, nil
}

// PLUGIN MANAGEMENT

// RunLLMPreHooks executes PreHooks in order, tracks how many ran, and returns the final request, any short-circuit decision, and the count.
func (p *PluginPipeline) RunLLMPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, int) {
	// If the skip plugin pipeline flag is set, skip the plugin pipeline
	if skipPluginPipeline, ok := ctx.Value(schemas.BifrostContextKeySkipPluginPipeline).(bool); ok && skipPluginPipeline {
		return req, nil, 0
	}
	var shortCircuit *schemas.LLMPluginShortCircuit
	var err error
	ctx.BlockRestrictedWrites()
	defer ctx.UnblockRestrictedWrites()
	for i, plugin := range p.llmPlugins {
		pluginName := plugin.GetName()
		p.logger.Debug("running pre-hook for plugin %s", pluginName)
		// Start span for this plugin's PreLLMHook
		spanCtx, handle := p.tracer.StartSpan(ctx, fmt.Sprintf("plugin.%s.prehook", sanitizeSpanName(pluginName)), schemas.SpanKindPlugin)
		// Update pluginCtx with span context for nested operations
		if spanCtx != nil {
			if spanID, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok {
				ctx.SetValue(schemas.BifrostContextKeySpanID, spanID)
			}
		}

		req, shortCircuit, err = plugin.PreLLMHook(ctx, req)

		// End span with appropriate status
		if err != nil {
			p.tracer.SetAttribute(handle, "error", err.Error())
			p.tracer.EndSpan(handle, schemas.SpanStatusError, err.Error())
			p.preHookErrors = append(p.preHookErrors, err)
			p.logger.Warn("error in PreLLMHook for plugin %s: %s", pluginName, err.Error())
		} else if shortCircuit != nil {
			p.tracer.SetAttribute(handle, "short_circuit", true)
			p.tracer.EndSpan(handle, schemas.SpanStatusOk, "short-circuit")
		} else {
			p.tracer.EndSpan(handle, schemas.SpanStatusOk, "")
		}

		p.executedPreHooks = i + 1
		if shortCircuit != nil {
			return req, shortCircuit, p.executedPreHooks // short-circuit: only plugins up to and including i ran
		}
	}
	return req, nil, p.executedPreHooks
}

// RunPostLLMHooks executes PostHooks in reverse order for the plugins whose PreLLMHook ran.
// Accepts the response and error, and allows plugins to transform either (e.g., recover from error, or invalidate a response).
// Returns the final response and error after all hooks. If both are set, error takes precedence unless error is nil.
// runFrom is the count of plugins whose PreHooks ran; PostHooks will run in reverse from index (runFrom - 1) down to 0
// For streaming requests, it accumulates timing per plugin instead of creating individual spans per chunk.
func (p *PluginPipeline) RunPostLLMHooks(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, runFrom int) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// If the skip plugin pipeline flag is set, skip the plugin pipeline
	if skipPluginPipeline, ok := ctx.Value(schemas.BifrostContextKeySkipPluginPipeline).(bool); ok && skipPluginPipeline {
		return resp, bifrostErr
	}
	// Defensive: ensure count is within valid bounds
	if runFrom < 0 {
		runFrom = 0
	}
	if runFrom > len(p.llmPlugins) {
		runFrom = len(p.llmPlugins)
	}
	// Detect streaming mode - if StreamStartTime is set, we're in a streaming context
	isStreaming := ctx.Value(schemas.BifrostContextKeyStreamStartTime) != nil
	ctx.BlockRestrictedWrites()
	defer ctx.UnblockRestrictedWrites()
	var err error
	for i := runFrom - 1; i >= 0; i-- {
		plugin := p.llmPlugins[i]
		pluginName := plugin.GetName()
		p.logger.Debug("running post-hook for plugin %s", pluginName)
		if isStreaming {
			// For streaming: accumulate timing, don't create individual spans per chunk
			start := time.Now()
			resp, bifrostErr, err = plugin.PostLLMHook(ctx, resp, bifrostErr)
			duration := time.Since(start)

			p.accumulatePluginTiming(pluginName, duration, err != nil)
			if err != nil {
				p.postHookErrors = append(p.postHookErrors, err)
				p.logger.Warn("error in PostLLMHook for plugin %s: %v", pluginName, err)
			}
		} else {
			// For non-streaming: create span per plugin (existing behavior)
			spanCtx, handle := p.tracer.StartSpan(ctx, fmt.Sprintf("plugin.%s.posthook", sanitizeSpanName(pluginName)), schemas.SpanKindPlugin)
			// Update pluginCtx with span context for nested operations
			if spanCtx != nil {
				if spanID, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok {
					ctx.SetValue(schemas.BifrostContextKeySpanID, spanID)
				}
			}
			resp, bifrostErr, err = plugin.PostLLMHook(ctx, resp, bifrostErr)
			// End span with appropriate status
			if err != nil {
				p.tracer.SetAttribute(handle, "error", err.Error())
				p.tracer.EndSpan(handle, schemas.SpanStatusError, err.Error())
				p.postHookErrors = append(p.postHookErrors, err)
				p.logger.Warn("error in PostLLMHook for plugin %s: %v", pluginName, err)
			} else {
				p.tracer.EndSpan(handle, schemas.SpanStatusOk, "")
			}
		}
		// If a plugin recovers from an error (sets bifrostErr to nil and sets resp), allow that
		// If a plugin invalidates a response (sets resp to nil and sets bifrostErr), allow that
	}
	// Increment chunk count for streaming
	if isStreaming {
		p.chunkCount++
	}
	// Final logic: if both are set, error takes precedence, unless error is nil
	if bifrostErr != nil {
		if resp != nil && bifrostErr.StatusCode == nil && bifrostErr.Error != nil && bifrostErr.Error.Type == nil &&
			bifrostErr.Error.Message == "" && bifrostErr.Error.Error == nil {
			// Defensive: treat as recovery if error is empty
			return resp, nil
		}
		return resp, bifrostErr
	}
	return resp, nil
}

// RunMCPPreHooks executes MCP PreHooks in order for all registered MCP plugins.
// Returns the modified request, any short-circuit decision, and the count of hooks that ran.
// If a plugin short-circuits, only PostHooks for plugins up to and including that plugin will run.
func (p *PluginPipeline) RunMCPPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	// If the skip plugin pipeline flag is set, skip the plugin pipeline
	if skipPluginPipeline, ok := ctx.Value(schemas.BifrostContextKeySkipPluginPipeline).(bool); ok && skipPluginPipeline {
		return req, nil, 0
	}
	var shortCircuit *schemas.MCPPluginShortCircuit
	var err error
	ctx.BlockRestrictedWrites()
	defer ctx.UnblockRestrictedWrites()
	for i, plugin := range p.mcpPlugins {
		pluginName := plugin.GetName()
		p.logger.Debug("running MCP pre-hook for plugin %s", pluginName)
		// Start span for this plugin's PreMCPHook
		spanCtx, handle := p.tracer.StartSpan(ctx, fmt.Sprintf("plugin.%s.mcp_prehook", sanitizeSpanName(pluginName)), schemas.SpanKindPlugin)
		// Update pluginCtx with span context for nested operations
		if spanCtx != nil {
			if spanID, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok {
				ctx.SetValue(schemas.BifrostContextKeySpanID, spanID)
			}
		}

		req, shortCircuit, err = plugin.PreMCPHook(ctx, req)

		// End span with appropriate status
		if err != nil {
			p.tracer.SetAttribute(handle, "error", err.Error())
			p.tracer.EndSpan(handle, schemas.SpanStatusError, err.Error())
			p.preHookErrors = append(p.preHookErrors, err)
			p.logger.Warn("error in PreMCPHook for plugin %s: %s", pluginName, err.Error())
		} else if shortCircuit != nil {
			p.tracer.SetAttribute(handle, "short_circuit", true)
			p.tracer.EndSpan(handle, schemas.SpanStatusOk, "short-circuit")
		} else {
			p.tracer.EndSpan(handle, schemas.SpanStatusOk, "")
		}

		p.executedPreHooks = i + 1
		if shortCircuit != nil {
			return req, shortCircuit, p.executedPreHooks // short-circuit: only plugins up to and including i ran
		}
	}
	return req, nil, p.executedPreHooks
}

// RunMCPPostHooks executes MCP PostHooks in reverse order for the plugins whose PreMCPHook ran.
// Accepts the MCP response and error, and allows plugins to transform either (e.g., recover from error, or invalidate a response).
// Returns the final MCP response and error after all hooks. If both are set, error takes precedence unless error is nil.
// runFrom is the count of plugins whose PreHooks ran; PostHooks will run in reverse from index (runFrom - 1) down to 0
func (p *PluginPipeline) RunMCPPostHooks(ctx *schemas.BifrostContext, mcpResp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError, runFrom int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	// If the skip plugin pipeline flag is set, skip the plugin pipeline
	if skipPluginPipeline, ok := ctx.Value(schemas.BifrostContextKeySkipPluginPipeline).(bool); ok && skipPluginPipeline {
		return mcpResp, bifrostErr
	}
	// Defensive: ensure count is within valid bounds
	if runFrom < 0 {
		runFrom = 0
	}
	if runFrom > len(p.mcpPlugins) {
		runFrom = len(p.mcpPlugins)
	}
	ctx.BlockRestrictedWrites()
	defer ctx.UnblockRestrictedWrites()
	var err error
	for i := runFrom - 1; i >= 0; i-- {
		plugin := p.mcpPlugins[i]
		pluginName := plugin.GetName()
		p.logger.Debug("running MCP post-hook for plugin %s", pluginName)
		// Create span per plugin
		spanCtx, handle := p.tracer.StartSpan(ctx, fmt.Sprintf("plugin.%s.mcp_posthook", sanitizeSpanName(pluginName)), schemas.SpanKindPlugin)
		// Update pluginCtx with span context for nested operations
		if spanCtx != nil {
			if spanID, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok {
				ctx.SetValue(schemas.BifrostContextKeySpanID, spanID)
			}
		}

		mcpResp, bifrostErr, err = plugin.PostMCPHook(ctx, mcpResp, bifrostErr)

		// End span with appropriate status
		if err != nil {
			p.tracer.SetAttribute(handle, "error", err.Error())
			p.tracer.EndSpan(handle, schemas.SpanStatusError, err.Error())
			p.postHookErrors = append(p.postHookErrors, err)
			p.logger.Warn("error in PostMCPHook for plugin %s: %v", pluginName, err)
		} else {
			p.tracer.EndSpan(handle, schemas.SpanStatusOk, "")
		}
		// If a plugin recovers from an error (sets bifrostErr to nil and sets mcpResp), allow that
		// If a plugin invalidates a response (sets mcpResp to nil and sets bifrostErr), allow that
	}
	// Final logic: if both are set, error takes precedence, unless error is nil
	if bifrostErr != nil {
		if mcpResp != nil && bifrostErr.StatusCode == nil && bifrostErr.Error != nil && bifrostErr.Error.Type == nil &&
			bifrostErr.Error.Message == "" && bifrostErr.Error.Error == nil {
			// Defensive: treat as recovery if error is empty
			return mcpResp, nil
		}
		return mcpResp, bifrostErr
	}
	return mcpResp, nil
}

// resetPluginPipeline resets a PluginPipeline instance for reuse
func (p *PluginPipeline) resetPluginPipeline() {
	p.executedPreHooks = 0
	p.preHookErrors = p.preHookErrors[:0]
	p.postHookErrors = p.postHookErrors[:0]
	// Reset streaming timing accumulation
	p.chunkCount = 0
	if p.postHookTimings != nil {
		clear(p.postHookTimings)
	}
	p.postHookPluginOrder = p.postHookPluginOrder[:0]
}

// accumulatePluginTiming accumulates timing for a plugin during streaming
func (p *PluginPipeline) accumulatePluginTiming(pluginName string, duration time.Duration, hasError bool) {
	if p.postHookTimings == nil {
		p.postHookTimings = make(map[string]*pluginTimingAccumulator)
	}
	timing, ok := p.postHookTimings[pluginName]
	if !ok {
		timing = &pluginTimingAccumulator{}
		p.postHookTimings[pluginName] = timing
		// Track order on first occurrence (first chunk)
		p.postHookPluginOrder = append(p.postHookPluginOrder, pluginName)
	}
	timing.totalDuration += duration
	timing.invocations++
	if hasError {
		timing.errors++
	}
}

// FinalizeStreamingPostHookSpans creates aggregated spans for each plugin after streaming completes.
// This should be called once at the end of streaming to create one span per plugin with average timing.
// Spans are nested to mirror the pre-hook hierarchy (each post-hook is a child of the previous one).
func (p *PluginPipeline) FinalizeStreamingPostHookSpans(ctx context.Context) {
	if p.postHookTimings == nil || len(p.postHookPluginOrder) == 0 {
		return
	}

	// Collect handles and timing info to end spans in reverse order
	type spanInfo struct {
		handle    schemas.SpanHandle
		hasErrors bool
	}
	spans := make([]spanInfo, 0, len(p.postHookPluginOrder))
	currentCtx := ctx

	// Start spans in execution order (nested: each is a child of the previous)
	for _, pluginName := range p.postHookPluginOrder {
		timing, ok := p.postHookTimings[pluginName]
		if !ok || timing.invocations == 0 {
			continue
		}

		// Create span as child of the previous span (nested hierarchy)
		newCtx, handle := p.tracer.StartSpan(currentCtx, fmt.Sprintf("plugin.%s.posthook", sanitizeSpanName(pluginName)), schemas.SpanKindPlugin)
		if handle == nil {
			continue
		}

		// Calculate average duration in milliseconds
		avgMs := float64(timing.totalDuration.Milliseconds()) / float64(timing.invocations)

		// Set aggregated attributes
		p.tracer.SetAttribute(handle, schemas.AttrPluginInvocations, timing.invocations)
		p.tracer.SetAttribute(handle, schemas.AttrPluginAvgDurationMs, avgMs)
		p.tracer.SetAttribute(handle, schemas.AttrPluginTotalDurationMs, timing.totalDuration.Milliseconds())

		if timing.errors > 0 {
			p.tracer.SetAttribute(handle, schemas.AttrPluginErrorCount, timing.errors)
		}

		spans = append(spans, spanInfo{handle: handle, hasErrors: timing.errors > 0})
		currentCtx = newCtx
	}

	// End spans in reverse order (innermost first, like unwinding a call stack)
	for i := len(spans) - 1; i >= 0; i-- {
		if spans[i].hasErrors {
			p.tracer.EndSpan(spans[i].handle, schemas.SpanStatusError, "some invocations failed")
		} else {
			p.tracer.EndSpan(spans[i].handle, schemas.SpanStatusOk, "")
		}
	}
}

// GetChunkCount returns the number of chunks processed during streaming
func (p *PluginPipeline) GetChunkCount() int {
	return p.chunkCount
}

// getPluginPipeline gets a PluginPipeline from the pool and configures it
func (bifrost *Bifrost) getPluginPipeline() *PluginPipeline {
	pipeline := bifrost.pluginPipelinePool.Get()
	pipeline.llmPlugins = *bifrost.llmPlugins.Load()
	pipeline.mcpPlugins = *bifrost.mcpPlugins.Load()
	pipeline.logger = bifrost.logger
	pipeline.tracer = bifrost.getTracer()
	return pipeline
}

// releasePluginPipeline returns a PluginPipeline to the pool
func (bifrost *Bifrost) releasePluginPipeline(pipeline *PluginPipeline) {
	pipeline.resetPluginPipeline()
	bifrost.pluginPipelinePool.Put(pipeline)
}

// POOL & RESOURCE MANAGEMENT

// getChannelMessage gets a ChannelMessage from the pool and configures it with the request.
// It also gets response and error channels from their respective pools.
func (bifrost *Bifrost) getChannelMessage(req schemas.BifrostRequest) *ChannelMessage {
	// Get channels from pool, preserving original pointers for Put()
	responsePtr := bifrost.responseChannelPool.Get()
	responseChan := *responsePtr
	errorPtr := bifrost.errorChannelPool.Get()
	errorChan := *errorPtr

	// Clear any previous values to avoid leaking between requests
	select {
	case <-responseChan:
	default:
	}
	select {
	case <-errorChan:
	default:
	}

	// Get message from pool and configure it
	msg := bifrost.channelMessagePool.Get()
	msg.BifrostRequest = req
	msg.Response = responseChan
	msg.Err = errorChan
	msg.responsePoolPtr = responsePtr
	msg.errPoolPtr = errorPtr

	// Conditionally allocate ResponseStream for streaming requests only
	if IsStreamRequestType(req.RequestType) {
		streamPtr := bifrost.responseStreamPool.Get()
		responseStreamChan := *streamPtr
		// Clear any previous values to avoid leaking between requests
		select {
		case <-responseStreamChan:
		default:
		}
		msg.ResponseStream = responseStreamChan
		msg.responseStreamPoolPtr = streamPtr
	}

	return msg
}

// releaseChannelMessage returns a ChannelMessage and its channels to their respective pools.
func (bifrost *Bifrost) releaseChannelMessage(msg *ChannelMessage) {
	// Return channels to pools using the original pointers from Get(),
	// so debug tracking (pooldebug build tag) sees matching addresses.
	bifrost.responseChannelPool.Put(msg.responsePoolPtr)
	bifrost.errorChannelPool.Put(msg.errPoolPtr)

	// Return ResponseStream to pool if it was used
	if msg.responseStreamPoolPtr != nil {
		// Drain any remaining channels to prevent memory leaks
		select {
		case <-msg.ResponseStream:
		default:
		}
		bifrost.responseStreamPool.Put(msg.responseStreamPoolPtr)
	}

	// Clear all references before returning to pool
	msg.Response = nil
	msg.Err = nil
	msg.ResponseStream = nil
	msg.responsePoolPtr = nil
	msg.errPoolPtr = nil
	msg.responseStreamPoolPtr = nil

	// Release of Bifrost Request is handled in handle methods as they are required for fallbacks
	bifrost.channelMessagePool.Put(msg)
}

// resetMCPRequest resets a BifrostMCPRequest instance for reuse
func resetMCPRequest(req *schemas.BifrostMCPRequest) {
	req.RequestType = ""
	req.ChatAssistantMessageToolCall = nil
	req.ResponsesToolMessage = nil
}

// getMCPRequest gets a BifrostMCPRequest from the pool
func (bifrost *Bifrost) getMCPRequest() *schemas.BifrostMCPRequest {
	return bifrost.mcpRequestPool.Get()
}

// releaseMCPRequest returns a BifrostMCPRequest to the pool
func (bifrost *Bifrost) releaseMCPRequest(req *schemas.BifrostMCPRequest) {
	resetMCPRequest(req)
	bifrost.mcpRequestPool.Put(req)
}

// getAllSupportedKeys retrieves all valid keys for a ListModels request.
// allowing the provider to aggregate results from multiple keys.
func (bifrost *Bifrost) getAllSupportedKeys(ctx *schemas.BifrostContext, providerKey schemas.ModelProvider, baseProviderType schemas.ModelProvider) ([]schemas.Key, error) {
	// Check if key has been set in the context explicitly
	if ctx != nil {
		key, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
		if ok {
			// If a direct key is specified, return it as a single-element slice
			return []schemas.Key{key}, nil
		}
	}

	keys, err := bifrost.account.GetKeysForProvider(ctx, providerKey)
	if err != nil {
		return nil, err
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("no keys found for provider: %v", providerKey)
	}

	// Filter keys for ListModels - only check if key has a value
	var supportedKeys []schemas.Key
	for _, k := range keys {
		// Skip disabled keys (default enabled when nil)
		if k.Enabled != nil && !*k.Enabled {
			continue
		}
		if strings.TrimSpace(k.Value.GetValue()) != "" || canProviderKeyValueBeEmpty(baseProviderType) || hasAzureEntraIDCredentials(baseProviderType, k) {
			supportedKeys = append(supportedKeys, k)
		}
	}

	bifrost.logger.Debug("[Bifrost] Provider %s: %d enabled keys found", providerKey, len(supportedKeys))

	if len(supportedKeys) == 0 {
		return nil, fmt.Errorf("no valid keys found for provider: %v", providerKey)
	}

	return supportedKeys, nil
}

// getKeysForBatchAndFileOps retrieves keys for batch and file operations with model filtering.
// For batch operations, only keys with UseForBatchAPI enabled are included.
// Model filtering: if model is specified and key has model restrictions, only include if model is in list.
func (bifrost *Bifrost) getKeysForBatchAndFileOps(ctx *schemas.BifrostContext, providerKey schemas.ModelProvider, baseProviderType schemas.ModelProvider, model *string, isBatchOp bool) ([]schemas.Key, error) {
	// Check if key has been set in the context explicitly
	if ctx != nil {
		key, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
		if ok {
			// If a direct key is specified, return it as a single-element slice
			return []schemas.Key{key}, nil
		}
	}

	keys, err := bifrost.account.GetKeysForProvider(ctx, providerKey)
	if err != nil {
		return nil, err
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("no keys found for provider: %v", providerKey)
	}

	var filteredKeys []schemas.Key
	for _, k := range keys {
		// Skip disabled keys
		if k.Enabled != nil && !*k.Enabled {
			continue
		}

		// For batch operations, only include keys with UseForBatchAPI enabled
		if isBatchOp && (k.UseForBatchAPI == nil || !*k.UseForBatchAPI) {
			continue
		}

		// Model filtering logic:
		// - If model is nil or empty → include all keys (no model filter)
		// - If model is specified:
		//   - If key.Models is empty → include key (supports all models)
		//   - If key.Models is non-empty → only include if model is in list
		if model != nil && *model != "" && len(k.Models) > 0 {
			if !slices.Contains(k.Models, *model) {
				continue
			}
		}

		// Check key value (or if provider allows empty keys or has Azure Entra ID credentials)
		if strings.TrimSpace(k.Value.GetValue()) != "" || canProviderKeyValueBeEmpty(baseProviderType) || hasAzureEntraIDCredentials(baseProviderType, k) {
			filteredKeys = append(filteredKeys, k)
		}
	}

	if len(filteredKeys) == 0 {
		modelStr := ""
		if model != nil {
			modelStr = *model
		}
		if isBatchOp {
			return nil, fmt.Errorf("no batch-enabled keys found for provider: %v and model: %s", providerKey, modelStr)
		}
		return nil, fmt.Errorf("no keys found for provider: %v and model: %s", providerKey, modelStr)
	}

	// Sort keys by ID for deterministic pagination order across requests
	sort.Slice(filteredKeys, func(i, j int) bool {
		return filteredKeys[i].ID < filteredKeys[j].ID
	})

	return filteredKeys, nil
}

// selectKeyFromProviderForModel selects an appropriate API key for a given provider and model.
// It uses weighted random selection if multiple keys are available.
func (bifrost *Bifrost) selectKeyFromProviderForModel(ctx *schemas.BifrostContext, requestType schemas.RequestType, providerKey schemas.ModelProvider, model string, baseProviderType schemas.ModelProvider) (schemas.Key, error) {
	// Check if key has been set in the context explicitly
	if ctx != nil {
		key, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
		if ok {
			return key, nil
		}
	}
	// Check if key skipping is allowed
	if skipKeySelection, ok := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); ok && skipKeySelection && isKeySkippingAllowed(providerKey) {
		return schemas.Key{}, nil
	}
	// Get keys for provider
	keys, err := bifrost.account.GetKeysForProvider(ctx, providerKey)
	if err != nil {
		return schemas.Key{}, err
	}
	// Check if no keys found
	if len(keys) == 0 {
		return schemas.Key{}, fmt.Errorf("no keys found for provider: %v and model: %s", providerKey, model)
	}

	// For batch API operations, filter keys to only include those with UseForBatchAPI enabled
	if isBatchRequestType(requestType) || isFileRequestType(requestType) {
		var batchEnabledKeys []schemas.Key
		for _, k := range keys {
			if k.UseForBatchAPI != nil && *k.UseForBatchAPI {
				batchEnabledKeys = append(batchEnabledKeys, k)
			}
		}
		if len(batchEnabledKeys) == 0 {
			return schemas.Key{}, fmt.Errorf("no config found for batch APIs. Please enable 'Use for Batch APIs' on at least one key for provider: %v", providerKey)
		}
		keys = batchEnabledKeys
	}

	// filter out keys which don't support the model, if the key has no models, it is supported for all models
	var supportedKeys []schemas.Key

	// Skip model check conditions
	// We can improve these conditions in the future
	skipModelCheck := (model == "" && (isFileRequestType(requestType) || isBatchRequestType(requestType) || isContainerRequestType(requestType))) || requestType == schemas.ListModelsRequest
	if skipModelCheck {
		// When skipping model check: just verify keys are enabled and have values
		for _, k := range keys {
			// Skip disabled keys
			if k.Enabled != nil && !*k.Enabled {
				continue
			}
			if strings.TrimSpace(k.Value.GetValue()) != "" || canProviderKeyValueBeEmpty(baseProviderType) || hasAzureEntraIDCredentials(baseProviderType, k) {
				supportedKeys = append(supportedKeys, k)
			}
		}
	} else {
		// When NOT skipping model check: do full model/deployment filtering
		for _, key := range keys {
			// Skip disabled keys
			if key.Enabled != nil && !*key.Enabled {
				continue
			}
			hasValue := strings.TrimSpace(key.Value.GetValue()) != "" || canProviderKeyValueBeEmpty(baseProviderType) || hasAzureEntraIDCredentials(baseProviderType, key)
			modelSupported := (len(key.Models) == 0 && hasValue) || (slices.Contains(key.Models, model) && hasValue)
			// Additional deployment checks for Azure, Bedrock and Vertex
			deploymentSupported := true
			if baseProviderType == schemas.Azure && key.AzureKeyConfig != nil {
				// For Azure, check if deployment exists for this model
				if len(key.AzureKeyConfig.Deployments) > 0 {
					_, deploymentSupported = key.AzureKeyConfig.Deployments[model]
				}
			} else if baseProviderType == schemas.Bedrock && key.BedrockKeyConfig != nil {
				// For Bedrock, check if deployment exists for this model
				if len(key.BedrockKeyConfig.Deployments) > 0 {
					_, deploymentSupported = key.BedrockKeyConfig.Deployments[model]
				}
			} else if baseProviderType == schemas.Vertex && key.VertexKeyConfig != nil {
				// For Vertex, check if deployment exists for this model
				if len(key.VertexKeyConfig.Deployments) > 0 {
					_, deploymentSupported = key.VertexKeyConfig.Deployments[model]
				}
			} else if baseProviderType == schemas.Replicate && key.ReplicateKeyConfig != nil {
				// For Replicate, check if deployment exists for this model
				if len(key.ReplicateKeyConfig.Deployments) > 0 {
					_, deploymentSupported = key.ReplicateKeyConfig.Deployments[model]
				}
			}

			if modelSupported && deploymentSupported {
				supportedKeys = append(supportedKeys, key)
			}
		}
	}
	if len(supportedKeys) == 0 {
		if baseProviderType == schemas.Azure || baseProviderType == schemas.Bedrock || baseProviderType == schemas.Vertex || baseProviderType == schemas.Replicate {
			return schemas.Key{}, fmt.Errorf("no keys found that support model/deployment: %s", model)
		}
		return schemas.Key{}, fmt.Errorf("no keys found that support model: %s", model)
	}

	var requestedKeyName string
	if ctx != nil {
		if keyName, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyName).(string); ok {
			requestedKeyName = strings.TrimSpace(keyName)
		}
	}

	if requestedKeyName != "" {
		for _, key := range supportedKeys {
			if key.Name == requestedKeyName {
				return key, nil
			}
		}
		return schemas.Key{}, fmt.Errorf("no key found with name %q for provider: %v", requestedKeyName, providerKey)
	}

	if len(supportedKeys) == 1 {
		return supportedKeys[0], nil
	}

	selectedKey, err := bifrost.keySelector(ctx, supportedKeys, providerKey, model)
	if err != nil {
		return schemas.Key{}, err
	}

	return selectedKey, nil

}

func WeightedRandomKeySelector(ctx *schemas.BifrostContext, keys []schemas.Key, providerKey schemas.ModelProvider, model string) (schemas.Key, error) {
	// Use a weighted random selection based on key weights
	totalWeight := 0
	for _, key := range keys {
		totalWeight += int(key.Weight * 100) // Convert float to int for better performance
	}

	// Use global thread-safe random (Go 1.20+) - no allocation, no syscall
	randomValue := rand.Intn(totalWeight)

	// Select key based on weight
	currentWeight := 0
	for _, key := range keys {
		currentWeight += int(key.Weight * 100)
		if randomValue < currentWeight {
			return key, nil
		}
	}

	// Fallback to first key if something goes wrong
	return keys[0], nil
}

// Shutdown gracefully stops all workers when triggered.
// It closes all request channels and waits for workers to exit.
func (bifrost *Bifrost) Shutdown() {
	bifrost.logger.Info("closing all request channels...")
	// Cancel the context if not already done
	if bifrost.ctx.Err() == nil && bifrost.cancel != nil {
		bifrost.cancel()
	}
	// ALWAYS close all provider queues to signal workers to stop,
	// even if context was already cancelled. This prevents goroutine leaks.
	// Use the ProviderQueue lifecycle: signal closing, then close the queue
	bifrost.requestQueues.Range(func(key, value interface{}) bool {
		pq := value.(*ProviderQueue)
		// Signal closing to producers (uses sync.Once internally)
		pq.signalClosing()
		// Close the queue to signal workers (uses sync.Once internally)
		pq.closeQueue()
		return true
	})

	// Wait for all workers to exit
	bifrost.waitGroups.Range(func(key, value interface{}) bool {
		waitGroup := value.(*sync.WaitGroup)
		waitGroup.Wait()
		return true
	})

	// Cleanup MCP manager
	if bifrost.MCPManager != nil {
		err := bifrost.MCPManager.Cleanup()
		if err != nil {
			bifrost.logger.Warn("Error cleaning up MCP manager: %s", err.Error())
		}
	}

	// Stop the tracerWrapper to clean up background goroutines
	if tracerWrapper := bifrost.tracer.Load().(*tracerWrapper); tracerWrapper != nil && tracerWrapper.tracer != nil {
		tracerWrapper.tracer.Stop()
	}

	// Cleanup plugins
	if llmPlugins := bifrost.llmPlugins.Load(); llmPlugins != nil {
		for _, plugin := range *llmPlugins {
			err := plugin.Cleanup()
			if err != nil {
				bifrost.logger.Warn(fmt.Sprintf("Error cleaning up LLM plugin: %s", err.Error()))
			}
		}
	}
	if mcpPlugins := bifrost.mcpPlugins.Load(); mcpPlugins != nil {
		for _, plugin := range *mcpPlugins {
			err := plugin.Cleanup()
			if err != nil {
				bifrost.logger.Warn(fmt.Sprintf("Error cleaning up MCP plugin: %s", err.Error()))
			}
		}
	}
	bifrost.logger.Info("all request channels closed")
}
