// Package bifrost provides the core implementation of the Bifrost system.
// Bifrost is a unified interface for interacting with various AI model providers,
// managing concurrent requests, and handling provider-specific configurations.
package bifrost

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ChannelMessage represents a message passed through the request channel.
// It contains the request, response and error channels, and the request type.
type ChannelMessage struct {
	schemas.BifrostRequest
	Context        context.Context
	Response       chan *schemas.BifrostResponse
	ResponseStream chan chan *schemas.BifrostStream
	Err            chan schemas.BifrostError
}

// Bifrost manages providers and maintains specified open channels for concurrent processing.
// It handles request routing, provider management, and response processing.
type Bifrost struct {
	ctx                 context.Context
	account             schemas.Account                  // account interface
	plugins             atomic.Pointer[[]schemas.Plugin] // list of plugins
	requestQueues       sync.Map                         // provider request queues (thread-safe)
	waitGroups          sync.Map                         // wait groups for each provider (thread-safe)
	providerMutexes     sync.Map                         // mutexes for each provider to prevent concurrent updates (thread-safe)
	channelMessagePool  sync.Pool                        // Pool for ChannelMessage objects, initial pool size is set in Init
	responseChannelPool sync.Pool                        // Pool for response channels, initial pool size is set in Init
	errorChannelPool    sync.Pool                        // Pool for error channels, initial pool size is set in Init
	responseStreamPool  sync.Pool                        // Pool for response stream channels, initial pool size is set in Init
	pluginPipelinePool  sync.Pool                        // Pool for PluginPipeline objects
	bifrostRequestPool  sync.Pool                        // Pool for BifrostRequest objects
	logger              schemas.Logger                   // logger instance, default logger is used if not provided
	mcpManager          *MCPManager                      // MCP integration manager (nil if MCP not configured)
	dropExcessRequests  atomic.Bool                      // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
	keySelector         schemas.KeySelector              // Custom key selector function
}

// PluginPipeline encapsulates the execution of plugin PreHooks and PostHooks, tracks how many plugins ran, and manages short-circuiting and error aggregation.
type PluginPipeline struct {
	plugins []schemas.Plugin
	logger  schemas.Logger

	// Number of PreHooks that were executed (used to determine which PostHooks to run in reverse order)
	executedPreHooks int
	// Errors from PreHooks and PostHooks
	preHookErrors  []error
	postHookErrors []error
}

// Define a set of retryable status codes
var retryableStatusCodes = map[int]bool{
	500: true, // Internal Server Error
	502: true, // Bad Gateway
	503: true, // Service Unavailable
	504: true, // Gateway Timeout
	429: true, // Too Many Requests
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

	bifrost := &Bifrost{
		ctx:           ctx,
		account:       config.Account,
		plugins:       atomic.Pointer[[]schemas.Plugin]{},
		requestQueues: sync.Map{},
		waitGroups:    sync.Map{},
		keySelector:   config.KeySelector,
	}
	bifrost.plugins.Store(&config.Plugins)
	bifrost.dropExcessRequests.Store(config.DropExcessRequests)

	if bifrost.keySelector == nil {
		bifrost.keySelector = WeightedRandomKeySelector
	}

	// Initialize object pools
	bifrost.channelMessagePool = sync.Pool{
		New: func() interface{} {
			return &ChannelMessage{}
		},
	}
	bifrost.responseChannelPool = sync.Pool{
		New: func() interface{} {
			return make(chan *schemas.BifrostResponse, 1)
		},
	}
	bifrost.errorChannelPool = sync.Pool{
		New: func() interface{} {
			return make(chan schemas.BifrostError, 1)
		},
	}
	bifrost.responseStreamPool = sync.Pool{
		New: func() interface{} {
			return make(chan chan *schemas.BifrostStream, 1)
		},
	}
	bifrost.pluginPipelinePool = sync.Pool{
		New: func() interface{} {
			return &PluginPipeline{
				preHookErrors:  make([]error, 0),
				postHookErrors: make([]error, 0),
			}
		},
	}
	bifrost.bifrostRequestPool = sync.Pool{
		New: func() interface{} {
			return &schemas.BifrostRequest{}
		},
	}

	// Prewarm pools with multiple objects
	for range config.InitialPoolSize {
		// Create and put new objects directly into pools
		bifrost.channelMessagePool.Put(&ChannelMessage{})
		bifrost.responseChannelPool.Put(make(chan *schemas.BifrostResponse, 1))
		bifrost.errorChannelPool.Put(make(chan schemas.BifrostError, 1))
		bifrost.responseStreamPool.Put(make(chan chan *schemas.BifrostStream, 1))
		bifrost.pluginPipelinePool.Put(&PluginPipeline{
			preHookErrors:  make([]error, 0),
			postHookErrors: make([]error, 0),
		})
		bifrost.bifrostRequestPool.Put(&schemas.BifrostRequest{})
	}

	providerKeys, err := bifrost.account.GetConfiguredProviders()
	if err != nil {
		return nil, err
	}

	if config.Logger == nil {
		config.Logger = NewDefaultLogger(schemas.LogLevelInfo)
	}
	bifrost.logger = config.Logger

	// Initialize MCP manager if configured
	if config.MCPConfig != nil {
		mcpManager, err := newMCPManager(ctx, *config.MCPConfig, bifrost.logger)
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("failed to initialize MCP manager: %v", err))
		} else {
			bifrost.mcpManager = mcpManager
			bifrost.logger.Info("MCP integration initialized successfully")
		}
	}

	// Create buffered channels for each provider and start workers
	for _, providerKey := range providerKeys {
		if strings.TrimSpace(string(providerKey)) == "" {
			bifrost.logger.Warn("provider key is empty, skipping init")
			continue
		}

		config, err := bifrost.account.GetConfigForProvider(providerKey)
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("failed to get config for provider, skipping init: %v", err))
			continue
		}

		// Lock the provider mutex during initialization
		providerMutex := bifrost.getProviderMutex(providerKey)
		providerMutex.Lock()
		err = bifrost.prepareProvider(providerKey, config)
		providerMutex.Unlock()

		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("failed to prepare provider %s: %v", providerKey, err))
		}
	}

	return bifrost, nil
}

// ReloadConfig reloads the config from DB
// Currently we only update account and drop excess requests
// We will keep on adding other aspects as required
func (bifrost *Bifrost) ReloadConfig(config schemas.BifrostConfig) error {
	bifrost.dropExcessRequests.Store(config.DropExcessRequests)
	return nil
}

// PUBLIC API METHODS

// TextCompletionRequest sends a text completion request to the specified provider.
func (bifrost *Bifrost) TextCompletionRequest(ctx context.Context, req *schemas.BifrostTextCompletionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "text completion request is nil",
			},
		}
	}
	if req.Input == nil || (req.Input.PromptStr == nil && req.Input.PromptArray == nil) {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "prompt not provided for text completion request",
			},
		}
	}
	// Preparing request
	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.TextCompletionRequest
	bifrostReq.TextCompletionRequest = req
	// Hand over to bifrost core
	return bifrost.handleRequest(ctx, bifrostReq)
}

// TextCompletionStreamRequest sends a streaming text completion request to the specified provider.
func (bifrost *Bifrost) TextCompletionStreamRequest(ctx context.Context, req *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "text completion stream request is nil",
			},
		}
	}
	if req.Input == nil || (req.Input.PromptStr == nil && req.Input.PromptArray == nil) {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "text not provided for text completion stream request",
			},
		}
	}
	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.TextCompletionStreamRequest
	bifrostReq.TextCompletionRequest = req
	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// ChatCompletionRequest sends a chat completion request to the specified provider.
func (bifrost *Bifrost) ChatCompletionRequest(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "chat completion request is nil",
			},
		}
	}
	if req.Input == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "chats not provided for chat completion request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.ChatCompletionRequest
	bifrostReq.ChatRequest = req

	return bifrost.handleRequest(ctx, bifrostReq)
}

// ChatCompletionStreamRequest sends a chat completion stream request to the specified provider.
func (bifrost *Bifrost) ChatCompletionStreamRequest(ctx context.Context, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "chat completion stream request is nil",
			},
		}
	}
	if req.Input == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "chats not provided for chat completion request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.ChatCompletionStreamRequest
	bifrostReq.ChatRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// ResponsesRequest sends a responses request to the specified provider.
func (bifrost *Bifrost) ResponsesRequest(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "responses request is nil",
			},
		}
	}
	if req.Input == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "responses not provided for responses request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.ResponsesRequest
	bifrostReq.ResponsesRequest = req

	return bifrost.handleRequest(ctx, bifrostReq)
}

// ResponsesStreamRequest sends a responses stream request to the specified provider.
func (bifrost *Bifrost) ResponsesStreamRequest(ctx context.Context, req *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "responses stream request is nil",
			},
		}
	}
	if req.Input == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "responses not provided for responses stream request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.ResponsesStreamRequest
	bifrostReq.ResponsesRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// EmbeddingRequest sends an embedding request to the specified provider.
func (bifrost *Bifrost) EmbeddingRequest(ctx context.Context, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "embedding request is nil",
			},
		}
	}
	if req.Input == nil || (req.Input.Text == nil && req.Input.Texts == nil && req.Input.Embedding == nil && req.Input.Embeddings == nil) {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "embedding input not provided for embedding request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.EmbeddingRequest
	bifrostReq.EmbeddingRequest = req

	return bifrost.handleRequest(ctx, bifrostReq)
}

// SpeechRequest sends a speech request to the specified provider.
func (bifrost *Bifrost) SpeechRequest(ctx context.Context, req *schemas.BifrostSpeechRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "speech request is nil",
			},
		}
	}
	if req.Input == nil || req.Input.Input == "" {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "speech input not provided for speech request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.SpeechRequest
	bifrostReq.SpeechRequest = req

	return bifrost.handleRequest(ctx, bifrostReq)
}

// SpeechStreamRequest sends a speech stream request to the specified provider.
func (bifrost *Bifrost) SpeechStreamRequest(ctx context.Context, req *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "speech stream request is nil",
			},
		}
	}
	if req.Input == nil || req.Input.Input == "" {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "speech input not provided for speech stream request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.SpeechStreamRequest
	bifrostReq.SpeechRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// TranscriptionRequest sends a transcription request to the specified provider.
func (bifrost *Bifrost) TranscriptionRequest(ctx context.Context, req *schemas.BifrostTranscriptionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "transcription request is nil",
			},
		}
	}
	if req.Input == nil || req.Input.File == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "transcription input not provided for transcription request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.TranscriptionRequest
	bifrostReq.TranscriptionRequest = req

	return bifrost.handleRequest(ctx, bifrostReq)
}

// TranscriptionStreamRequest sends a transcription stream request to the specified provider.
func (bifrost *Bifrost) TranscriptionStreamRequest(ctx context.Context, req *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "transcription stream request is nil",
			},
		}
	}
	if req.Input == nil || req.Input.File == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "transcription input not provided for transcription stream request",
			},
		}
	}

	bifrostReq := bifrost.getBifrostRequest()
	bifrostReq.Provider = req.Provider
	bifrostReq.Model = req.Model
	bifrostReq.Fallbacks = req.Fallbacks
	bifrostReq.RequestType = schemas.TranscriptionStreamRequest
	bifrostReq.TranscriptionRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// RemovePlugin removes a plugin from the server.
func (bifrost *Bifrost) RemovePlugin(name string) error {

	for {
		oldPlugins := bifrost.plugins.Load()
		if oldPlugins == nil {
			return nil
		}
		var pluginToCleanup schemas.Plugin
		found := false
		// Create new slice with replaced plugin
		newPlugins := make([]schemas.Plugin, len(*oldPlugins))
		copy(newPlugins, *oldPlugins)
		for i, p := range newPlugins {
			if p.GetName() == name {
				pluginToCleanup = p
				bifrost.logger.Debug("removing plugin %s", name)
				newPlugins = append(newPlugins[:i], newPlugins[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return nil
		}
		if pluginToCleanup != nil {
			// Atomic compare-and-swap
			if bifrost.plugins.CompareAndSwap(oldPlugins, &newPlugins) {
				// Cleanup the old plugin
				err := pluginToCleanup.Cleanup()
				if err != nil {
					bifrost.logger.Warn("failed to cleanup old plugin %s: %v", pluginToCleanup.GetName(), err)
				}
				return nil
			}
		}
		// Retrying as swapping did not work
	}
}

// ReloadPlugin reloads a plugin with new instance
// During the reload - it's stop the world phase where we take a global lock on the plugin mutex
func (bifrost *Bifrost) ReloadPlugin(plugin schemas.Plugin) error {
	for {
		var pluginToCleanup schemas.Plugin
		found := false
		oldPlugins := bifrost.plugins.Load()
		if oldPlugins == nil {
			return nil
		}
		// Create new slice with replaced plugin
		newPlugins := make([]schemas.Plugin, len(*oldPlugins))
		copy(newPlugins, *oldPlugins)
		for i, p := range newPlugins {
			if p.GetName() == plugin.GetName() {
				// Cleaning up old plugin before replacing it
				pluginToCleanup = p
				bifrost.logger.Debug("replacing plugin %s with new instance", plugin.GetName())
				newPlugins[i] = plugin
				found = true
				break
			}
		}
		if !found {
			// This means that user is adding a new plugin
			bifrost.logger.Debug("adding new plugin %s", plugin.GetName())
			newPlugins = append(newPlugins, plugin)
		}
		// Atomic compare-and-swap
		if bifrost.plugins.CompareAndSwap(oldPlugins, &newPlugins) {
			// Cleanup the old plugin
			if found && pluginToCleanup != nil {
				err := pluginToCleanup.Cleanup()
				if err != nil {
					bifrost.logger.Warn("failed to cleanup old plugin %s: %v", pluginToCleanup.GetName(), err)
				}
			}
			return nil
		}
		// Retrying as swapping did not work
	}
}

// UpdateProviderConcurrency dynamically updates the queue size and concurrency for an existing provider.
// This method gracefully stops existing workers, creates a new queue with updated settings,
// and starts new workers with the updated concurrency configuration.
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
func (bifrost *Bifrost) UpdateProviderConcurrency(providerKey schemas.ModelProvider) error {
	bifrost.logger.Info(fmt.Sprintf("Updating concurrency configuration for provider %s", providerKey))

	// Get the updated configuration from the account
	providerConfig, err := bifrost.account.GetConfigForProvider(providerKey)
	if err != nil {
		return fmt.Errorf("failed to get updated config for provider %s: %v", providerKey, err)
	}

	// Lock the provider to prevent concurrent access during update
	providerMutex := bifrost.getProviderMutex(providerKey)
	providerMutex.Lock()
	defer providerMutex.Unlock()

	// Check if provider currently exists
	oldQueueValue, exists := bifrost.requestQueues.Load(providerKey)
	if !exists {
		bifrost.logger.Debug("provider %s not currently active, initializing with new configuration", providerKey)
		// If provider doesn't exist, just prepare it with new configuration
		return bifrost.prepareProvider(providerKey, providerConfig)
	}

	oldQueue := oldQueueValue.(chan *ChannelMessage)

	bifrost.logger.Debug("gracefully stopping existing workers for provider %s", providerKey)

	// Step 1: Create new queue with updated buffer size
	newQueue := make(chan *ChannelMessage, providerConfig.ConcurrencyAndBufferSize.BufferSize)

	// Step 2: Transfer any buffered requests from old queue to new queue
	// This prevents request loss during the transition
	transferredCount := 0
	var transferWaitGroup sync.WaitGroup
	for {
		select {
		case msg := <-oldQueue:
			select {
			case newQueue <- msg:
				transferredCount++
			default:
				// New queue is full, handle this request in a goroutine
				// This is unlikely with proper buffer sizing but provides safety
				transferWaitGroup.Add(1)
				go func(m *ChannelMessage) {
					defer transferWaitGroup.Done()
					select {
					case newQueue <- m:
						// Message successfully transferred
					case <-time.After(5 * time.Second):
						bifrost.logger.Warn("Failed to transfer buffered request to new queue within timeout")
						// Send error response to avoid hanging the client
						select {
						case m.Err <- schemas.BifrostError{
							IsBifrostError: false,
							Error: &schemas.ErrorField{
								Message: "request failed during provider concurrency update",
							},
						}:
						case <-time.After(1 * time.Second):
							// If we can't send the error either, just log and continue
							bifrost.logger.Warn("Failed to send error response during transfer timeout")
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

	// Step 3: Close the old queue to signal workers to stop
	close(oldQueue)

	// Step 4: Atomically replace the queue
	bifrost.requestQueues.Store(providerKey, newQueue)

	// Step 5: Wait for all existing workers to finish processing in-flight requests
	waitGroup, exists := bifrost.waitGroups.Load(providerKey)
	if exists {
		waitGroup.(*sync.WaitGroup).Wait()
		bifrost.logger.Debug("all workers for provider %s have stopped", providerKey)
	}

	// Step 6: Create new wait group for the updated workers
	bifrost.waitGroups.Store(providerKey, &sync.WaitGroup{})

	// Step 7: Create provider instance
	provider, err := bifrost.createBaseProvider(providerKey, providerConfig)
	if err != nil {
		return fmt.Errorf("failed to create provider instance for %s: %v", providerKey, err)
	}

	// Step 8: Start new workers with updated concurrency
	bifrost.logger.Debug("starting %d new workers for provider %s with buffer size %d",
		providerConfig.ConcurrencyAndBufferSize.Concurrency,
		providerKey,
		providerConfig.ConcurrencyAndBufferSize.BufferSize)

	waitGroupValue, _ := bifrost.waitGroups.Load(providerKey)
	currentWaitGroup := waitGroupValue.(*sync.WaitGroup)

	for range providerConfig.ConcurrencyAndBufferSize.Concurrency {
		currentWaitGroup.Add(1)
		go bifrost.requestWorker(provider, providerConfig, newQueue)
	}

	bifrost.logger.Info("successfully updated concurrency configuration for provider %s", providerKey)
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

// MCP PUBLIC API

// RegisterMCPTool registers a typed tool handler with the MCP integration.
// This allows developers to easily add custom tools that will be available
// to all LLM requests processed by this Bifrost instance.
//
// Parameters:
//   - name: Unique tool name
//   - description: Human-readable tool description
//   - handler: Function that handles tool execution
//   - toolSchema: Bifrost tool schema for function calling
//
// Returns:
//   - error: Any registration error
//
// Example:
//
//	type EchoArgs struct {
//	    Message string `json:"message"`
//	}
//
//	err := bifrost.RegisterMCPTool("echo", "Echo a message",
//	    func(args EchoArgs) (string, error) {
//	        return args.Message, nil
//	    }, toolSchema)
func (bifrost *Bifrost) RegisterMCPTool(name, description string, handler func(args any) (string, error), toolSchema schemas.ChatTool) error {
	if bifrost.mcpManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	return bifrost.mcpManager.registerTool(name, description, handler, toolSchema)
}

// ExecuteMCPTool executes an MCP tool call and returns the result as a tool message.
// This is the main public API for manual MCP tool execution.
//
// Parameters:
//   - ctx: Execution context
//   - toolCall: The tool call to execute (from assistant message)
//
// Returns:
//   - schemas.ChatMessage: Tool message with execution result
//   - schemas.BifrostError: Any execution error
func (bifrost *Bifrost) ExecuteMCPTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	if bifrost.mcpManager == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "MCP is not configured in this Bifrost instance",
			},
		}
	}

	result, err := bifrost.mcpManager.executeTool(ctx, toolCall)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: err.Error(),
				Error:   err,
			},
		}
	}

	return result, nil
}

// IMPORTANT: Running the MCP client management operations (GetMCPClients, AddMCPClient, RemoveMCPClient, EditMCPClientTools)
// may temporarily increase latency for incoming requests while the operations are being processed.
// These operations involve network I/O and connection management that require mutex locks
// which can block briefly during execution.

// GetMCPClients returns all MCP clients managed by the Bifrost instance.
//
// Returns:
//   - []schemas.MCPClient: List of all MCP clients
//   - error: Any retrieval error
func (bifrost *Bifrost) GetMCPClients() ([]schemas.MCPClient, error) {
	if bifrost.mcpManager == nil {
		return nil, fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	clients, err := bifrost.mcpManager.GetClients()
	if err != nil {
		return nil, err
	}

	clientsInConfig := make([]schemas.MCPClient, 0, len(clients))
	for _, client := range clients {
		tools := make([]string, 0, len(client.ToolMap))
		for toolName := range client.ToolMap {
			tools = append(tools, toolName)
		}

		state := schemas.MCPConnectionStateConnected
		if client.Conn == nil {
			state = schemas.MCPConnectionStateDisconnected
		}

		clientsInConfig = append(clientsInConfig, schemas.MCPClient{
			Name:   client.Name,
			Config: client.ExecutionConfig,
			Tools:  tools,
			State:  state,
		})
	}

	return clientsInConfig, nil
}

// AddMCPClient adds a new MCP client to the Bifrost instance.
// This allows for dynamic MCP client management at runtime.
//
// Parameters:
//   - config: MCP client configuration
//
// Returns:
//   - error: Any registration error
//
// Example:
//
//	err := bifrost.AddMCPClient(schemas.MCPClientConfig{
//	    Name: "my-mcp-client",
//	    ConnectionType: schemas.MCPConnectionTypeHTTP,
//	    ConnectionString: &url,
//	})
func (bifrost *Bifrost) AddMCPClient(config schemas.MCPClientConfig) error {
	if bifrost.mcpManager == nil {
		manager := &MCPManager{
			ctx:       bifrost.ctx,
			clientMap: make(map[string]*MCPClient),
			logger:    bifrost.logger,
		}

		bifrost.mcpManager = manager
	}

	return bifrost.mcpManager.AddClient(config)
}

// RemoveMCPClient removes an MCP client from the Bifrost instance.
// This allows for dynamic MCP client management at runtime.
//
// Parameters:
//   - name: Name of the client to remove
//
// Returns:
//   - error: Any removal error
//
// Example:
//
//	err := bifrost.RemoveMCPClient("my-mcp-client")
//	if err != nil {
//	    log.Fatalf("Failed to remove MCP client: %v", err)
//	}
func (bifrost *Bifrost) RemoveMCPClient(name string) error {
	if bifrost.mcpManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	return bifrost.mcpManager.RemoveClient(name)
}

// EditMCPClientTools edits the tools of an MCP client.
// This allows for dynamic MCP client tool management at runtime.
//
// Parameters:
//   - name: Name of the client to edit
//   - toolsToAdd: Tools to add to the client
//   - toolsToRemove: Tools to remove from the client
//
// Returns:
//   - error: Any edit error
//
// Example:
//
//	err := bifrost.EditMCPClientTools("my-mcp-client", []string{"tool1", "tool2"}, []string{"tool3"})
//	if err != nil {
//	    log.Fatalf("Failed to edit MCP client tools: %v", err)
//	}
func (bifrost *Bifrost) EditMCPClientTools(name string, toolsToAdd []string, toolsToRemove []string) error {
	if bifrost.mcpManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	return bifrost.mcpManager.EditClientTools(name, toolsToAdd, toolsToRemove)
}

// ReconnectMCPClient attempts to reconnect an MCP client if it is disconnected.
//
// Parameters:
//   - name: Name of the client to reconnect
//
// Returns:
//   - error: Any reconnection error
func (bifrost *Bifrost) ReconnectMCPClient(name string) error {
	if bifrost.mcpManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	return bifrost.mcpManager.ReconnectClient(name)
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
		return providers.NewOpenAIProvider(config, bifrost.logger), nil
	case schemas.Anthropic:
		return providers.NewAnthropicProvider(config, bifrost.logger), nil
	case schemas.Bedrock:
		return providers.NewBedrockProvider(config, bifrost.logger)
	case schemas.Cohere:
		return providers.NewCohereProvider(config, bifrost.logger), nil
	case schemas.Azure:
		return providers.NewAzureProvider(config, bifrost.logger)
	case schemas.Vertex:
		return providers.NewVertexProvider(config, bifrost.logger)
	case schemas.Mistral:
		return providers.NewMistralProvider(config, bifrost.logger), nil
	case schemas.Ollama:
		return providers.NewOllamaProvider(config, bifrost.logger)
	case schemas.Groq:
		return providers.NewGroqProvider(config, bifrost.logger)
	case schemas.SGL:
		return providers.NewSGLProvider(config, bifrost.logger)
	case schemas.Parasail:
		return providers.NewParasailProvider(config, bifrost.logger)
	case schemas.Cerebras:
		return providers.NewCerebrasProvider(config, bifrost.logger)
	case schemas.Gemini:
		return providers.NewGeminiProvider(config, bifrost.logger), nil
	case schemas.OpenRouter:
		return providers.NewOpenRouterProvider(config, bifrost.logger), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", targetProviderKey)
	}
}

// prepareProvider sets up a provider with its configuration, keys, and worker channels.
// It initializes the request queue and starts worker goroutines for processing requests.
// Note: This function assumes the caller has already acquired the appropriate mutex for the provider.
func (bifrost *Bifrost) prepareProvider(providerKey schemas.ModelProvider, config *schemas.ProviderConfig) error {
	providerConfig, err := bifrost.account.GetConfigForProvider(providerKey)
	if err != nil {
		return fmt.Errorf("failed to get config for provider: %v", err)
	}

	queue := make(chan *ChannelMessage, providerConfig.ConcurrencyAndBufferSize.BufferSize) // Buffered channel per provider

	bifrost.requestQueues.Store(providerKey, queue)

	// Start specified number of workers
	bifrost.waitGroups.Store(providerKey, &sync.WaitGroup{})

	provider, err := bifrost.createBaseProvider(providerKey, config)
	if err != nil {
		return fmt.Errorf("failed to create provider for the given key: %v", err)
	}

	waitGroupValue, _ := bifrost.waitGroups.Load(providerKey)
	currentWaitGroup := waitGroupValue.(*sync.WaitGroup)

	for range providerConfig.ConcurrencyAndBufferSize.Concurrency {
		currentWaitGroup.Add(1)
		go bifrost.requestWorker(provider, providerConfig, queue)
	}

	return nil
}

// getProviderQueue returns the request queue for a given provider key.
// If the queue doesn't exist, it creates one at runtime and initializes the provider,
// given the provider config is provided in the account interface implementation.
// This function uses read locks to prevent race conditions during provider updates.
func (bifrost *Bifrost) getProviderQueue(providerKey schemas.ModelProvider) (chan *ChannelMessage, error) {
	// Use read lock to allow concurrent reads but prevent concurrent updates
	providerMutex := bifrost.getProviderMutex(providerKey)
	providerMutex.RLock()

	if queueValue, exists := bifrost.requestQueues.Load(providerKey); exists {
		queue := queueValue.(chan *ChannelMessage)
		providerMutex.RUnlock()
		return queue, nil
	}

	// Provider doesn't exist, need to create it
	// Upgrade to write lock for creation
	providerMutex.RUnlock()
	providerMutex.Lock()
	defer providerMutex.Unlock()

	// Double-check after acquiring write lock (another goroutine might have created it)
	if queueValue, exists := bifrost.requestQueues.Load(providerKey); exists {
		queue := queueValue.(chan *ChannelMessage)
		return queue, nil
	}

	bifrost.logger.Debug(fmt.Sprintf("Creating new request queue for provider %s at runtime", providerKey))

	config, err := bifrost.account.GetConfigForProvider(providerKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get config for provider: %v", err)
	}

	if err := bifrost.prepareProvider(providerKey, config); err != nil {
		return nil, err
	}

	queueValue, _ := bifrost.requestQueues.Load(providerKey)
	queue := queueValue.(chan *ChannelMessage)

	return queue, nil
}

// CORE INTERNAL LOGIC

// shouldTryFallbacks handles the primary error and returns true if we should proceed with fallbacks, false if we should return immediately
func (bifrost *Bifrost) shouldTryFallbacks(req *schemas.BifrostRequest, primaryErr *schemas.BifrostError) bool {
	// If no primary error, we succeeded
	if primaryErr == nil {
		bifrost.logger.Debug("No primary error, we should not try fallbacks")
		return false
	}

	// Handle request cancellation
	if primaryErr.Error != nil && primaryErr.Error.Type != nil && *primaryErr.Error.Type == schemas.RequestCancelled {
		bifrost.logger.Debug("Request cancelled, we should not try fallbacks")
		return false
	}

	// Check if this is a short-circuit error that doesn't allow fallbacks
	// Note: AllowFallbacks = nil is treated as true (allow fallbacks by default)
	if primaryErr.AllowFallbacks != nil && !*primaryErr.AllowFallbacks {
		bifrost.logger.Debug("AllowFallbacks is false, we should not try fallbacks")
		return false
	}

	// If no fallbacks configured, return primary error
	if len(req.Fallbacks) == 0 {
		bifrost.logger.Debug("No fallbacks configured, we should not try fallbacks")
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
		bifrost.logger.Warn(fmt.Sprintf("Config not found for provider %s, skipping fallback: %v", fallback.Provider, err))
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

	fallbackReq.Provider = fallback.Provider
	fallbackReq.Model = fallback.Model

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

	bifrost.logger.Warn(fmt.Sprintf("Fallback provider %s failed: %s", fallback.Provider, fallbackErr.Error.Message))
	return true
}

// handleRequest handles the request to the provider based on the request type
// It handles plugin hooks, request validation, response processing, and fallback providers.
// If the primary provider fails, it will try each fallback provider in order until one succeeds.
// It is the wrapper for all non-streaming public API methods.
func (bifrost *Bifrost) handleRequest(ctx context.Context, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	defer bifrost.releaseBifrostRequest(req)

	if err := validateRequest(req); err != nil {
		err.ExtraFields = schemas.BifrostErrorExtraFields{
			Provider:       req.Provider,
			ModelRequested: req.Model,
			RequestType:    req.RequestType,
		}
		return nil, err
	}

	// Handle nil context early to prevent blocking
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrost.logger.Debug(fmt.Sprintf("Primary provider %s with model %s and %d fallbacks", req.Provider, req.Model, len(req.Fallbacks)))

	// Try the primary provider first
	primaryResult, primaryErr := bifrost.tryRequest(req, ctx)

	if primaryErr != nil {
		bifrost.logger.Debug(fmt.Sprintf("Primary provider %s with model %s returned error: %v", req.Provider, req.Model, primaryErr))
		if len(req.Fallbacks) > 0 {
			bifrost.logger.Debug(fmt.Sprintf("Check if we should try %d fallbacks", len(req.Fallbacks)))
		}
	}

	// Check if we should proceed with fallbacks
	shouldTryFallbacks := bifrost.shouldTryFallbacks(req, primaryErr)
	if !shouldTryFallbacks {
		return primaryResult, primaryErr
	}

	// Try fallbacks in order
	for _, fallback := range req.Fallbacks {
		bifrost.logger.Debug(fmt.Sprintf("Trying fallback provider %s with model %s", fallback.Provider, fallback.Model))
		ctx = context.WithValue(ctx, schemas.BifrostContextKeyFallbackRequestID, uuid.New().String())

		fallbackReq := bifrost.prepareFallbackRequest(req, fallback)
		if fallbackReq == nil {
			bifrost.logger.Debug(fmt.Sprintf("Fallback provider %s with model %s is nil", fallback.Provider, fallback.Model))
			continue
		}

		// Try the fallback provider
		result, fallbackErr := bifrost.tryRequest(fallbackReq, ctx)
		if fallbackErr == nil {
			bifrost.logger.Debug(fmt.Sprintf("Successfully used fallback provider %s with model %s", fallback.Provider, fallback.Model))
			return result, nil
		}

		// Check if we should continue with more fallbacks
		if !bifrost.shouldContinueWithFallbacks(fallback, fallbackErr) {
			return nil, fallbackErr
		}
	}

	// All providers failed, return the original error
	return nil, primaryErr
}

// handleStreamRequest handles the stream request to the provider based on the request type
// It handles plugin hooks, request validation, response processing, and fallback providers.
// If the primary provider fails, it will try each fallback provider in order until one succeeds.
// It is the wrapper for all streaming public API methods.
func (bifrost *Bifrost) handleStreamRequest(ctx context.Context, req *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	defer bifrost.releaseBifrostRequest(req)

	if err := validateRequest(req); err != nil {
		err.ExtraFields = schemas.BifrostErrorExtraFields{
			Provider:       req.Provider,
			ModelRequested: req.Model,
			RequestType:    req.RequestType,
		}
		return nil, err
	}

	// Handle nil context early to prevent blocking
	if ctx == nil {
		ctx = bifrost.ctx
	}

	// Try the primary provider first
	primaryResult, primaryErr := bifrost.tryStreamRequest(req, ctx)

	// Check if we should proceed with fallbacks
	shouldTryFallbacks := bifrost.shouldTryFallbacks(req, primaryErr)
	if !shouldTryFallbacks {
		return primaryResult, primaryErr
	}

	// Try fallbacks in order
	for _, fallback := range req.Fallbacks {
		ctx = context.WithValue(ctx, schemas.BifrostContextKeyFallbackRequestID, uuid.New().String())

		fallbackReq := bifrost.prepareFallbackRequest(req, fallback)
		if fallbackReq == nil {
			continue
		}

		// Try the fallback provider
		result, fallbackErr := bifrost.tryStreamRequest(fallbackReq, ctx)
		if fallbackErr == nil {
			bifrost.logger.Debug(fmt.Sprintf("Successfully used fallback provider %s with model %s", fallback.Provider, fallback.Model))
			return result, nil
		}

		// Check if we should continue with more fallbacks
		if !bifrost.shouldContinueWithFallbacks(fallback, fallbackErr) {
			return nil, fallbackErr
		}
	}
	// All providers failed, return the original error
	return nil, primaryErr
}

// tryRequest is a generic function that handles common request processing logic
// It consolidates queue setup, plugin pipeline execution, enqueue logic, and response handling
func (bifrost *Bifrost) tryRequest(req *schemas.BifrostRequest, ctx context.Context) (*schemas.BifrostResponse, *schemas.BifrostError) {
	queue, err := bifrost.getProviderQueue(req.Provider)
	if err != nil {
		return nil, newBifrostError(err)
	}

	// Add MCP tools to request if MCP is configured and requested
	if req.RequestType != schemas.EmbeddingRequest &&
		req.RequestType != schemas.SpeechRequest &&
		req.RequestType != schemas.TranscriptionRequest &&
		bifrost.mcpManager != nil {
		req = bifrost.mcpManager.addMCPToolsToBifrostRequest(ctx, req)
	}

	pipeline := bifrost.getPluginPipeline()
	defer bifrost.releasePluginPipeline(pipeline)

	preReq, shortCircuit, preCount := pipeline.RunPreHooks(&ctx, req)
	if shortCircuit != nil {
		// Handle short-circuit with response (success case)
		if shortCircuit.Response != nil {
			resp, bifrostErr := pipeline.RunPostHooks(&ctx, shortCircuit.Response, nil, preCount)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			return resp, nil
		}
		// Handle short-circuit with error
		if shortCircuit.Error != nil {
			resp, bifrostErr := pipeline.RunPostHooks(&ctx, nil, shortCircuit.Error, preCount)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			return resp, nil
		}
	}
	if preReq == nil {
		return nil, newBifrostErrorFromMsg("bifrost request after plugin hooks cannot be nil")
	}

	msg := bifrost.getChannelMessage(*preReq)
	msg.Context = ctx
	select {
	case queue <- msg:
		// Message was sent successfully
	case <-ctx.Done():
		bifrost.releaseChannelMessage(msg)
		return nil, newBifrostErrorFromMsg("request cancelled while waiting for queue space")
	default:
		if bifrost.dropExcessRequests.Load() {
			bifrost.releaseChannelMessage(msg)
			bifrost.logger.Warn("Request dropped: queue is full, please increase the queue size or set dropExcessRequests to false")
			return nil, newBifrostErrorFromMsg("request dropped: queue is full")
		}
		select {
		case queue <- msg:
			// Message was sent successfully
		case <-ctx.Done():
			bifrost.releaseChannelMessage(msg)
			return nil, newBifrostErrorFromMsg("request cancelled while waiting for queue space")
		}
	}

	var result *schemas.BifrostResponse
	var resp *schemas.BifrostResponse
	pluginCount := len(*bifrost.plugins.Load())
	select {
	case result = <-msg.Response:
		resp, bifrostErr := pipeline.RunPostHooks(&msg.Context, result, nil, pluginCount)
		if bifrostErr != nil {
			bifrost.releaseChannelMessage(msg)
			return nil, bifrostErr
		}
		bifrost.releaseChannelMessage(msg)
		return resp, nil
	case bifrostErrVal := <-msg.Err:
		bifrostErrPtr := &bifrostErrVal
		resp, bifrostErrPtr = pipeline.RunPostHooks(&msg.Context, nil, bifrostErrPtr, pluginCount)
		bifrost.releaseChannelMessage(msg)
		if bifrostErrPtr != nil {
			return nil, bifrostErrPtr
		}
		return resp, nil
	}
}

// tryStreamRequest is a generic function that handles common request processing logic
// It consolidates queue setup, plugin pipeline execution, enqueue logic, and response handling
func (bifrost *Bifrost) tryStreamRequest(req *schemas.BifrostRequest, ctx context.Context) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	queue, err := bifrost.getProviderQueue(req.Provider)
	if err != nil {
		return nil, newBifrostError(err)
	}

	// Add MCP tools to request if MCP is configured and requested
	if req.RequestType != schemas.SpeechStreamRequest && req.RequestType != schemas.TranscriptionStreamRequest && bifrost.mcpManager != nil {
		req = bifrost.mcpManager.addMCPToolsToBifrostRequest(ctx, req)
	}

	pipeline := bifrost.getPluginPipeline()
	defer bifrost.releasePluginPipeline(pipeline)

	preReq, shortCircuit, preCount := pipeline.RunPreHooks(&ctx, req)
	if shortCircuit != nil {
		// Handle short-circuit with response (success case)
		if shortCircuit.Response != nil {
			resp, bifrostErr := pipeline.RunPostHooks(&ctx, shortCircuit.Response, nil, preCount)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			return newBifrostMessageChan(resp), nil
		}
		// Handle short-circuit with stream
		if shortCircuit.Stream != nil {
			outputStream := make(chan *schemas.BifrostStream)

			// Create a post hook runner cause pipeline object is put back in the pool on defer
			pipelinePostHookRunner := func(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return pipeline.RunPostHooks(ctx, result, err, preCount)
			}

			go func() {
				defer close(outputStream)

				for streamMsg := range shortCircuit.Stream {
					if streamMsg == nil {
						continue
					}

					// Run post hooks on the stream message
					processedResp, processedErr := pipelinePostHookRunner(&ctx, streamMsg.BifrostResponse, streamMsg.BifrostError)

					// Send the processed message to the output stream
					outputStream <- &schemas.BifrostStream{
						BifrostResponse: processedResp,
						BifrostError:    processedErr,
					}
				}
			}()

			return outputStream, nil
		}
		// Handle short-circuit with error
		if shortCircuit.Error != nil {
			resp, bifrostErr := pipeline.RunPostHooks(&ctx, nil, shortCircuit.Error, preCount)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			return newBifrostMessageChan(resp), nil
		}
	}
	if preReq == nil {
		return nil, newBifrostErrorFromMsg("bifrost request after plugin hooks cannot be nil")
	}

	msg := bifrost.getChannelMessage(*preReq)
	msg.Context = ctx

	select {
	case queue <- msg:
		// Message was sent successfully
	case <-ctx.Done():
		bifrost.releaseChannelMessage(msg)
		return nil, newBifrostErrorFromMsg("request cancelled while waiting for queue space")
	default:
		if bifrost.dropExcessRequests.Load() {
			bifrost.releaseChannelMessage(msg)
			bifrost.logger.Warn("Request dropped: queue is full, please increase the queue size or set dropExcessRequests to false")
			return nil, newBifrostErrorFromMsg("request dropped: queue is full")
		}
		select {
		case queue <- msg:
			// Message was sent successfully
		case <-ctx.Done():
			bifrost.releaseChannelMessage(msg)
			return nil, newBifrostErrorFromMsg("request cancelled while waiting for queue space")
		}
	}

	select {
	case stream := <-msg.ResponseStream:
		bifrost.releaseChannelMessage(msg)
		return stream, nil
	case bifrostErrVal := <-msg.Err:
		bifrost.logger.Warn("error while executing stream request: %v", bifrostErrVal.Error.Message)
		// Marking final chunk
		ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
		// On error we will complete post-hooks
		recoveredResp, recoveredErr := pipeline.RunPostHooks(&ctx, nil, &bifrostErrVal, len(*bifrost.plugins.Load()))
		bifrost.releaseChannelMessage(msg)
		if recoveredErr != nil {
			return nil, recoveredErr
		}
		if recoveredResp != nil {
			return newBifrostMessageChan(recoveredResp), nil
		}
		return nil, &bifrostErrVal
	}
}

// requestWorker handles incoming requests from the queue for a specific provider.
// It manages retries, error handling, and response processing.
func (bifrost *Bifrost) requestWorker(provider schemas.Provider, config *schemas.ProviderConfig, queue chan *ChannelMessage) {
	defer func() {
		if waitGroupValue, ok := bifrost.waitGroups.Load(provider.GetProviderKey()); ok {
			waitGroup := waitGroupValue.(*sync.WaitGroup)
			waitGroup.Done()
		}
	}()

	for req := range queue {
		var result *schemas.BifrostResponse
		var stream chan *schemas.BifrostStream
		var bifrostError *schemas.BifrostError
		var err error

		// Determine the base provider type for key requirement checks
		baseProvider := provider.GetProviderKey()
		if cfg := config.CustomProviderConfig; cfg != nil && cfg.BaseProviderType != "" {
			baseProvider = cfg.BaseProviderType
		}

		key := schemas.Key{}
		if providerRequiresKey(baseProvider) {
			// Use the custom provider name for actual key selection, but pass base provider type for key validation
			key, err = bifrost.selectKeyFromProviderForModel(&req.Context, provider.GetProviderKey(), req.Model, baseProvider)
			if err != nil {
				bifrost.logger.Warn("error selecting key for model %s: %v", req.Model, err)
				req.Err <- schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Message: err.Error(),
						Error:   err,
					},
				}
				continue
			}
			req.Context = context.WithValue(req.Context, schemas.BifrostContextKeySelectedKey, key.ID)
		}

		// Track attempts
		var attempts int

		// Create plugin pipeline for streaming requests outside retry loop to prevent leaks
		var postHookRunner schemas.PostHookRunner
		if IsStreamRequestType(req.RequestType) {
			pipeline := bifrost.getPluginPipeline()
			defer bifrost.releasePluginPipeline(pipeline)

			postHookRunner = func(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				resp, bifrostErr := pipeline.RunPostHooks(ctx, result, err, len(*bifrost.plugins.Load()))
				if bifrostErr != nil {
					return nil, bifrostErr
				}
				return resp, nil
			}
		}

		// Execute request with retries
		for attempts = 0; attempts <= config.NetworkConfig.MaxRetries; attempts++ {
			if attempts > 0 {
				// Log retry attempt
				bifrost.logger.Info("retrying request (attempt %d/%d) for model %s: %s", attempts, config.NetworkConfig.MaxRetries, req.Model, bifrostError.Error.Message)

				// Calculate and apply backoff
				backoff := calculateBackoff(attempts-1, config)
				time.Sleep(backoff)
			}

			bifrost.logger.Debug("attempting request for provider %s", provider.GetProviderKey())

			// Attempt the request
			if IsStreamRequestType(req.RequestType) {
				stream, bifrostError = handleProviderStreamRequest(provider, req, key, postHookRunner)
				if bifrostError != nil && !bifrostError.IsBifrostError {
					break // Don't retry client errors
				}
			} else {
				result, bifrostError = handleProviderRequest(provider, req, key)
				if bifrostError != nil {
					break // Don't retry client errors
				}
			}

			bifrost.logger.Debug("request for provider %s completed", provider.GetProviderKey())

			// Check if successful or if we should retry
			if bifrostError == nil ||
				bifrostError.IsBifrostError ||
				(bifrostError.StatusCode != nil && !retryableStatusCodes[*bifrostError.StatusCode]) ||
				(bifrostError.Error.Type != nil && *bifrostError.Error.Type == schemas.RequestCancelled) {
				break
			}
		}

		if bifrostError != nil {
			// Add retry information to error
			if attempts > 0 {
				bifrost.logger.Warn("request failed after %d %s", attempts, map[bool]string{true: "retries", false: "retry"}[attempts > 1])
			}
			bifrostError.ExtraFields = schemas.BifrostErrorExtraFields{
				Provider:       provider.GetProviderKey(),
				ModelRequested: req.Model,
				RequestType:    req.RequestType,
			}

			// Send error with context awareness to prevent deadlock
			select {
			case req.Err <- *bifrostError:
				// Error sent successfully
			case <-req.Context.Done():
				// Client no longer listening, log and continue
				bifrost.logger.Debug("Client context cancelled while sending error response")
			case <-time.After(5 * time.Second):
				// Timeout to prevent indefinite blocking
				bifrost.logger.Warn("Timeout while sending error response, client may have disconnected")
			}
		} else {
			if IsStreamRequestType(req.RequestType) {
				// Send stream with context awareness to prevent deadlock
				select {
				case req.ResponseStream <- stream:
					// Stream sent successfully
				case <-req.Context.Done():
					// Client no longer listening, log and continue
					bifrost.logger.Debug("Client context cancelled while sending stream response")
				case <-time.After(5 * time.Second):
					// Timeout to prevent indefinite blocking
					bifrost.logger.Warn("Timeout while sending stream response, client may have disconnected")
				}
			} else {
				result.ExtraFields.RequestType = req.RequestType
				result.ExtraFields.Provider = provider.GetProviderKey()
				result.ExtraFields.ModelRequested = req.Model

				// Send response with context awareness to prevent deadlock
				select {
				case req.Response <- result:
					// Response sent successfully
				case <-req.Context.Done():
					// Client no longer listening, log and continue
					bifrost.logger.Debug("Client context cancelled while sending response")
				case <-time.After(5 * time.Second):
					// Timeout to prevent indefinite blocking
					bifrost.logger.Warn("Timeout while sending response, client may have disconnected")
				}
			}
		}
	}

	bifrost.logger.Debug("worker for provider %s exiting...", provider.GetProviderKey())
}

// handleProviderRequest handles the request to the provider based on the request type
func handleProviderRequest(provider schemas.Provider, req *ChannelMessage, key schemas.Key) (*schemas.BifrostResponse, *schemas.BifrostError) {
	switch req.RequestType {
	case schemas.TextCompletionRequest:
		return provider.TextCompletion(req.Context, key, req.BifrostRequest.TextCompletionRequest)
	case schemas.ChatCompletionRequest:
		return provider.ChatCompletion(req.Context, key, req.BifrostRequest.ChatRequest)
	case schemas.ResponsesRequest:
		return provider.Responses(req.Context, key, req.BifrostRequest.ResponsesRequest)
	case schemas.EmbeddingRequest:
		return provider.Embedding(req.Context, key, req.BifrostRequest.EmbeddingRequest)
	case schemas.SpeechRequest:
		return provider.Speech(req.Context, key, req.BifrostRequest.SpeechRequest)
	case schemas.TranscriptionRequest:
		return provider.Transcription(req.Context, key, req.BifrostRequest.TranscriptionRequest)
	default:
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("unsupported request type: %s", req.RequestType),
			},
		}
	}
}

// handleProviderStreamRequest handles the stream request to the provider based on the request type
func handleProviderStreamRequest(provider schemas.Provider, req *ChannelMessage, key schemas.Key, postHookRunner schemas.PostHookRunner) (chan *schemas.BifrostStream, *schemas.BifrostError) {
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
	default:
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("unsupported request type: %s", req.RequestType),
			},
		}
	}
}

// PLUGIN MANAGEMENT

// RunPreHooks executes PreHooks in order, tracks how many ran, and returns the final request, any short-circuit decision, and the count.
func (p *PluginPipeline) RunPreHooks(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, int) {
	var shortCircuit *schemas.PluginShortCircuit
	var err error
	for i, plugin := range p.plugins {
		p.logger.Debug("running pre-hook for plugin %s", plugin.GetName())
		req, shortCircuit, err = plugin.PreHook(ctx, req)
		if err != nil {
			p.preHookErrors = append(p.preHookErrors, err)
			p.logger.Warn("error in PreHook for plugin %s: %v", plugin.GetName(), err)
		}
		p.executedPreHooks = i + 1
		if shortCircuit != nil {
			return req, shortCircuit, p.executedPreHooks // short-circuit: only plugins up to and including i ran
		}
	}
	return req, nil, p.executedPreHooks
}

// RunPostHooks executes PostHooks in reverse order for the plugins whose PreHook ran.
// Accepts the response and error, and allows plugins to transform either (e.g., recover from error, or invalidate a response).
// Returns the final response and error after all hooks. If both are set, error takes precedence unless error is nil.
// runFrom is the count of plugins whose PreHooks ran; PostHooks will run in reverse from index (runFrom - 1) down to 0
func (p *PluginPipeline) RunPostHooks(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, runFrom int) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Defensive: ensure count is within valid bounds
	if runFrom < 0 {
		runFrom = 0
	}
	if runFrom > len(p.plugins) {
		runFrom = len(p.plugins)
	}
	var err error
	for i := runFrom - 1; i >= 0; i-- {
		plugin := p.plugins[i]
		p.logger.Debug("running post-hook for plugin %s", plugin.GetName())
		resp, bifrostErr, err = plugin.PostHook(ctx, resp, bifrostErr)
		if err != nil {
			p.postHookErrors = append(p.postHookErrors, err)
			p.logger.Warn("error in PostHook for plugin %s: %v", plugin.GetName(), err)
		}
		// If a plugin recovers from an error (sets bifrostErr to nil and sets resp), allow that
		// If a plugin invalidates a response (sets resp to nil and sets bifrostErr), allow that
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

// resetPluginPipeline resets a PluginPipeline instance for reuse
func (p *PluginPipeline) resetPluginPipeline() {
	p.executedPreHooks = 0
	p.preHookErrors = p.preHookErrors[:0]
	p.postHookErrors = p.postHookErrors[:0]
}

// getPluginPipeline gets a PluginPipeline from the pool and configures it
func (bifrost *Bifrost) getPluginPipeline() *PluginPipeline {
	pipeline := bifrost.pluginPipelinePool.Get().(*PluginPipeline)
	pipeline.plugins = *bifrost.plugins.Load()
	pipeline.logger = bifrost.logger
	pipeline.resetPluginPipeline()
	return pipeline
}

// releasePluginPipeline returns a PluginPipeline to the pool
func (bifrost *Bifrost) releasePluginPipeline(pipeline *PluginPipeline) {
	pipeline.resetPluginPipeline()
	bifrost.pluginPipelinePool.Put(pipeline)
}

// resetBifrostRequest resets a BifrostRequest instance for reuse
func resetBifrostRequest(req *schemas.BifrostRequest) {
	req.Provider = ""
	req.Model = ""
	req.Fallbacks = nil
	req.RequestType = ""
	req.TextCompletionRequest = nil
	req.ChatRequest = nil
	req.ResponsesRequest = nil
	req.EmbeddingRequest = nil
	req.SpeechRequest = nil
	req.TranscriptionRequest = nil
}

// getBifrostRequest gets a BifrostRequest from the pool
func (bifrost *Bifrost) getBifrostRequest() *schemas.BifrostRequest {
	req := bifrost.bifrostRequestPool.Get().(*schemas.BifrostRequest)
	resetBifrostRequest(req)
	return req
}

// releaseBifrostRequest returns a BifrostRequest to the pool
func (bifrost *Bifrost) releaseBifrostRequest(req *schemas.BifrostRequest) {
	resetBifrostRequest(req)
	bifrost.bifrostRequestPool.Put(req)
}

// POOL & RESOURCE MANAGEMENT

// getChannelMessage gets a ChannelMessage from the pool and configures it with the request.
// It also gets response and error channels from their respective pools.
func (bifrost *Bifrost) getChannelMessage(req schemas.BifrostRequest) *ChannelMessage {
	// Get channels from pool
	responseChan := bifrost.responseChannelPool.Get().(chan *schemas.BifrostResponse)
	errorChan := bifrost.errorChannelPool.Get().(chan schemas.BifrostError)

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
	msg := bifrost.channelMessagePool.Get().(*ChannelMessage)
	msg.BifrostRequest = req
	msg.Response = responseChan
	msg.Err = errorChan

	// Conditionally allocate ResponseStream for streaming requests only
	if IsStreamRequestType(req.RequestType) {
		responseStreamChan := bifrost.responseStreamPool.Get().(chan chan *schemas.BifrostStream)
		// Clear any previous values to avoid leaking between requests
		select {
		case <-responseStreamChan:
		default:
		}
		msg.ResponseStream = responseStreamChan
	}

	return msg
}

// releaseChannelMessage returns a ChannelMessage and its channels to their respective pools.
func (bifrost *Bifrost) releaseChannelMessage(msg *ChannelMessage) {
	// Put channels back in pools
	bifrost.responseChannelPool.Put(msg.Response)
	bifrost.errorChannelPool.Put(msg.Err)

	// Return ResponseStream to pool if it was used
	if msg.ResponseStream != nil {
		// Drain any remaining channels to prevent memory leaks
		select {
		case <-msg.ResponseStream:
		default:
		}
		bifrost.responseStreamPool.Put(msg.ResponseStream)
	}

	// Release of Bifrost Request is handled in handle methods as they are required for fallbacks

	// Clear references and return to pool
	msg.Response = nil
	msg.ResponseStream = nil
	msg.Err = nil
	bifrost.channelMessagePool.Put(msg)
}

// selectKeyFromProviderForModel selects an appropriate API key for a given provider and model.
// It uses weighted random selection if multiple keys are available.
func (bifrost *Bifrost) selectKeyFromProviderForModel(ctx *context.Context, providerKey schemas.ModelProvider, model string, baseProviderType schemas.ModelProvider) (schemas.Key, error) {
	// Check if key has been set in the context explicitly
	if ctx != nil {
		key, ok := (*ctx).Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
		if ok {
			return key, nil
		}
	}

	keys, err := bifrost.account.GetKeysForProvider(ctx, providerKey)
	if err != nil {
		return schemas.Key{}, err
	}

	if len(keys) == 0 {
		return schemas.Key{}, fmt.Errorf("no keys found for provider: %v and model: %s", providerKey, model)
	}

	// filter out keys which dont support the model, if the key has no models, it is supported for all models
	var supportedKeys []schemas.Key
	for _, key := range keys {
		modelSupported := (slices.Contains(key.Models, model) && (strings.TrimSpace(key.Value) != "" || canProviderKeyValueBeEmpty(baseProviderType))) || len(key.Models) == 0

		// Additional deployment checks for Azure and Bedrock
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
		}

		if modelSupported && deploymentSupported {
			supportedKeys = append(supportedKeys, key)
		}
	}

	if len(supportedKeys) == 0 {
		if baseProviderType == schemas.Azure || baseProviderType == schemas.Bedrock {
			return schemas.Key{}, fmt.Errorf("no keys found that support model/deployment: %s", model)
		}
		return schemas.Key{}, fmt.Errorf("no keys found that support model: %s", model)
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

func WeightedRandomKeySelector(ctx *context.Context, keys []schemas.Key, providerKey schemas.ModelProvider, model string) (schemas.Key, error) {
	// Use a weighted random selection based on key weights
	totalWeight := 0
	for _, key := range keys {
		totalWeight += int(key.Weight * 100) // Convert float to int for better performance
	}

	// Use a fast random number generator
	randomSource := rand.New(rand.NewSource(time.Now().UnixNano()))
	randomValue := randomSource.Intn(totalWeight)

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

	// Close all provider queues to signal workers to stop
	bifrost.requestQueues.Range(func(key, value interface{}) bool {
		close(value.(chan *ChannelMessage))
		return true
	})

	// Wait for all workers to exit
	bifrost.waitGroups.Range(func(key, value interface{}) bool {
		waitGroup := value.(*sync.WaitGroup)
		waitGroup.Wait()
		return true
	})

	// Cleanup MCP manager
	if bifrost.mcpManager != nil {
		err := bifrost.mcpManager.cleanup()
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("Error cleaning up MCP manager: %s", err.Error()))
		}
	}

	// Cleanup plugins
	for _, plugin := range *bifrost.plugins.Load() {
		err := plugin.Cleanup()
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("Error cleaning up plugin: %s", err.Error()))
		}
	}
	bifrost.logger.Info("all request channels closed")
}
