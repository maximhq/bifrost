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
	Type           schemas.RequestType
}

// Bifrost manages providers and maintains specified open channels for concurrent processing.
// It handles request routing, provider management, and response processing.
type Bifrost struct {
	ctx                 context.Context
	account             schemas.Account  // account interface
	plugins             []schemas.Plugin // list of plugins
	requestQueues       sync.Map         // provider request queues (thread-safe)
	waitGroups          sync.Map         // wait groups for each provider (thread-safe)
	providerMutexes     sync.Map         // mutexes for each provider to prevent concurrent updates (thread-safe)
	channelMessagePool  sync.Pool        // Pool for ChannelMessage objects, initial pool size is set in Init
	responseChannelPool sync.Pool        // Pool for response channels, initial pool size is set in Init
	errorChannelPool    sync.Pool        // Pool for error channels, initial pool size is set in Init
	responseStreamPool  sync.Pool        // Pool for response stream channels, initial pool size is set in Init
	pluginPipelinePool  sync.Pool        // Pool for PluginPipeline objects
	logger              schemas.Logger   // logger instance, default logger is used if not provided
	mcpManager          *MCPManager      // MCP integration manager (nil if MCP not configured)
	dropExcessRequests  atomic.Bool      // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
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
		plugins:       config.Plugins,
		requestQueues: sync.Map{},
		waitGroups:    sync.Map{},
	}
	bifrost.dropExcessRequests.Store(config.DropExcessRequests)

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

// PUBLIC API METHODS

// TextCompletionRequest sends a text completion request to the specified provider.
func (bifrost *Bifrost) TextCompletionRequest(ctx context.Context, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req.Input.TextCompletionInput == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "text not provided for text completion request",
			},
		}
	}

	return bifrost.handleRequest(ctx, req, schemas.TextCompletionRequest)
}

// ChatCompletionRequest sends a chat completion request to the specified provider.
func (bifrost *Bifrost) ChatCompletionRequest(ctx context.Context, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req.Input.ChatCompletionInput == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "chats not provided for chat completion request",
			},
		}
	}

	return bifrost.handleRequest(ctx, req, schemas.ChatCompletionRequest)
}

// ChatCompletionStreamRequest sends a chat completion stream request to the specified provider.
func (bifrost *Bifrost) ChatCompletionStreamRequest(ctx context.Context, req *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if req.Input.ChatCompletionInput == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "chats not provided for chat completion request",
			},
		}
	}

	return bifrost.handleStreamRequest(ctx, req, schemas.ChatCompletionStreamRequest)
}

// EmbeddingRequest sends an embedding request to the specified provider.
func (bifrost *Bifrost) EmbeddingRequest(ctx context.Context, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req.Input.EmbeddingInput == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "embedding input not provided for embedding request",
			},
		}
	}

	return bifrost.handleRequest(ctx, req, schemas.EmbeddingRequest)
}

// SpeechRequest sends a speech request to the specified provider.
func (bifrost *Bifrost) SpeechRequest(ctx context.Context, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req.Input.SpeechInput == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "speech input not provided for speech request",
			},
		}
	}

	return bifrost.handleRequest(ctx, req, schemas.SpeechRequest)
}

// SpeechStreamRequest sends a speech stream request to the specified provider.
func (bifrost *Bifrost) SpeechStreamRequest(ctx context.Context, req *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if req.Input.SpeechInput == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "speech input not provided for speech stream request",
			},
		}
	}

	return bifrost.handleStreamRequest(ctx, req, schemas.SpeechStreamRequest)
}

// TranscriptionRequest sends a transcription request to the specified provider.
func (bifrost *Bifrost) TranscriptionRequest(ctx context.Context, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req.Input.TranscriptionInput == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "transcription input not provided for transcription request",
			},
		}
	}

	return bifrost.handleRequest(ctx, req, schemas.TranscriptionRequest)
}

// TranscriptionStreamRequest sends a transcription stream request to the specified provider.
func (bifrost *Bifrost) TranscriptionStreamRequest(ctx context.Context, req *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if req.Input.TranscriptionInput == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "transcription input not provided for transcription stream request",
			},
		}
	}

	return bifrost.handleStreamRequest(ctx, req, schemas.TranscriptionStreamRequest)
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

	oldQueue := oldQueueValue.(chan ChannelMessage)

	bifrost.logger.Debug("gracefully stopping existing workers for provider %s", providerKey)

	// Step 1: Create new queue with updated buffer size
	newQueue := make(chan ChannelMessage, providerConfig.ConcurrencyAndBufferSize.BufferSize)

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
				go func(m ChannelMessage) {
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
							Error: schemas.ErrorField{
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

	for range providerConfig.ConcurrencyAndBufferSize.Concurrency {
		waitGroupValue, _ := bifrost.waitGroups.Load(providerKey)
		waitGroup := waitGroupValue.(*sync.WaitGroup)
		waitGroup.Add(1)
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
func (bifrost *Bifrost) RegisterMCPTool(name, description string, handler func(args any) (string, error), toolSchema schemas.Tool) error {
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
//   - schemas.BifrostMessage: Tool message with execution result
//   - schemas.BifrostError: Any execution error
func (bifrost *Bifrost) ExecuteMCPTool(ctx context.Context, toolCall schemas.ToolCall) (*schemas.BifrostMessage, *schemas.BifrostError) {
	if bifrost.mcpManager == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "MCP is not configured in this Bifrost instance",
			},
		}
	}

	result, err := bifrost.mcpManager.executeTool(ctx, toolCall)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
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

	queue := make(chan ChannelMessage, providerConfig.ConcurrencyAndBufferSize.BufferSize) // Buffered channel per provider

	bifrost.requestQueues.Store(providerKey, queue)

	// Start specified number of workers
	bifrost.waitGroups.Store(providerKey, &sync.WaitGroup{})

	provider, err := bifrost.createBaseProvider(providerKey, config)
	if err != nil {
		return fmt.Errorf("failed to create provider for the given key: %v", err)
	}

	for range providerConfig.ConcurrencyAndBufferSize.Concurrency {
		waitGroupValue, _ := bifrost.waitGroups.Load(providerKey)
		waitGroup := waitGroupValue.(*sync.WaitGroup)
		waitGroup.Add(1)
		go bifrost.requestWorker(provider, providerConfig, queue)
	}

	return nil
}

// getProviderQueue returns the request queue for a given provider key.
// If the queue doesn't exist, it creates one at runtime and initializes the provider,
// given the provider config is provided in the account interface implementation.
// This function uses read locks to prevent race conditions during provider updates.
func (bifrost *Bifrost) getProviderQueue(providerKey schemas.ModelProvider) (chan ChannelMessage, error) {
	// Use read lock to allow concurrent reads but prevent concurrent updates
	providerMutex := bifrost.getProviderMutex(providerKey)
	providerMutex.RLock()

	if queueValue, exists := bifrost.requestQueues.Load(providerKey); exists {
		queue := queueValue.(chan ChannelMessage)
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
		queue := queueValue.(chan ChannelMessage)
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
	queue := queueValue.(chan ChannelMessage)

	return queue, nil
}

// CORE INTERNAL LOGIC

// shouldTryFallbacks handles the primary error and returns true if we should proceed with fallbacks, false if we should return immediately
func (bifrost *Bifrost) shouldTryFallbacks(req *schemas.BifrostRequest, primaryErr *schemas.BifrostError) bool {
	// If no primary error, we succeeded
	if primaryErr == nil {
		return false
	}

	// Handle request cancellation
	if primaryErr.Error.Type != nil && *primaryErr.Error.Type == schemas.RequestCancelled {
		primaryErr.Provider = req.Provider
		return false
	}

	// Check if this is a short-circuit error that doesn't allow fallbacks
	// Note: AllowFallbacks = nil is treated as true (allow fallbacks by default)
	if primaryErr.AllowFallbacks != nil && !*primaryErr.AllowFallbacks {
		primaryErr.Provider = req.Provider
		return false
	}

	// If no fallbacks configured, return primary error
	if len(req.Fallbacks) == 0 {
		primaryErr.Provider = req.Provider
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
	fallbackReq.Provider = fallback.Provider
	fallbackReq.Model = fallback.Model
	return &fallbackReq
}

// shouldContinueWithFallbacks processes errors from fallback attempts
// Returns true if we should continue with more fallbacks, false if we should stop
func (bifrost *Bifrost) shouldContinueWithFallbacks(fallback schemas.Fallback, fallbackErr *schemas.BifrostError) bool {
	if fallbackErr.Error.Type != nil && *fallbackErr.Error.Type == schemas.RequestCancelled {
		fallbackErr.Provider = fallback.Provider
		return false
	}

	// Check if it was a short-circuit error that doesn't allow fallbacks
	if fallbackErr.AllowFallbacks != nil && !*fallbackErr.AllowFallbacks {
		fallbackErr.Provider = fallback.Provider
		return false
	}

	bifrost.logger.Warn(fmt.Sprintf("Fallback provider %s failed: %s", fallback.Provider, fallbackErr.Error.Message))
	return true
}

// handleRequest handles the request to the provider based on the request type
// It handles plugin hooks, request validation, response processing, and fallback providers.
// If the primary provider fails, it will try each fallback provider in order until one succeeds.
// It is the wrapper for all non-streaming public API methods.
func (bifrost *Bifrost) handleRequest(ctx context.Context, req *schemas.BifrostRequest, requestType schemas.RequestType) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := validateRequest(req); err != nil {
		err.Provider = req.Provider
		return nil, err
	}

	// Handle nil context early to prevent blocking
	if ctx == nil {
		ctx = bifrost.ctx
	}

	// Add request type to context
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyRequestType, requestType)

	// Try the primary provider first
	primaryResult, primaryErr := bifrost.tryRequest(req, ctx, requestType)

	// Check if we should proceed with fallbacks
	shouldTryFallbacks := bifrost.shouldTryFallbacks(req, primaryErr)
	if !shouldTryFallbacks {
		return primaryResult, primaryErr
	}

	// Try fallbacks in order
	for _, fallback := range req.Fallbacks {
		fallbackReq := bifrost.prepareFallbackRequest(req, fallback)
		if fallbackReq == nil {
			continue
		}

		// Try the fallback provider
		result, fallbackErr := bifrost.tryRequest(fallbackReq, ctx, requestType)
		if fallbackErr == nil {
			bifrost.logger.Info(fmt.Sprintf("Successfully used fallback provider %s with model %s", fallback.Provider, fallback.Model))
			return result, nil
		}

		// Check if we should continue with more fallbacks
		if !bifrost.shouldContinueWithFallbacks(fallback, fallbackErr) {
			return nil, fallbackErr
		}
	}

	primaryErr.Provider = req.Provider
	// All providers failed, return the original error
	return nil, primaryErr
}

// handleStreamRequest handles the stream request to the provider based on the request type
// It handles plugin hooks, request validation, response processing, and fallback providers.
// If the primary provider fails, it will try each fallback provider in order until one succeeds.
// It is the wrapper for all streaming public API methods.
func (bifrost *Bifrost) handleStreamRequest(ctx context.Context, req *schemas.BifrostRequest, requestType schemas.RequestType) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := validateRequest(req); err != nil {
		err.Provider = req.Provider
		return nil, err
	}

	// Handle nil context early to prevent blocking
	if ctx == nil {
		ctx = bifrost.ctx
	}

	// Add request type to context
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyRequestType, requestType)

	// Try the primary provider first
	primaryResult, primaryErr := bifrost.tryStreamRequest(req, ctx, requestType)

	// Check if we should proceed with fallbacks
	shouldTryFallbacks := bifrost.shouldTryFallbacks(req, primaryErr)
	if !shouldTryFallbacks {
		return primaryResult, primaryErr
	}

	// Try fallbacks in order
	for _, fallback := range req.Fallbacks {
		fallbackReq := bifrost.prepareFallbackRequest(req, fallback)
		if fallbackReq == nil {
			continue
		}

		// Try the fallback provider
		result, fallbackErr := bifrost.tryStreamRequest(fallbackReq, ctx, requestType)
		if fallbackErr == nil {
			bifrost.logger.Info(fmt.Sprintf("Successfully used fallback provider %s with model %s", fallback.Provider, fallback.Model))
			return result, nil
		}

		// Check if we should continue with more fallbacks
		if !bifrost.shouldContinueWithFallbacks(fallback, fallbackErr) {
			return nil, fallbackErr
		}
	}

	primaryErr.Provider = req.Provider
	// All providers failed, return the original error
	return nil, primaryErr
}

// tryRequest is a generic function that handles common request processing logic
// It consolidates queue setup, plugin pipeline execution, enqueue logic, and response handling
func (bifrost *Bifrost) tryRequest(req *schemas.BifrostRequest, ctx context.Context, requestType schemas.RequestType) (*schemas.BifrostResponse, *schemas.BifrostError) {
	queue, err := bifrost.getProviderQueue(req.Provider)
	if err != nil {
		return nil, newBifrostError(err)
	}

	// Add MCP tools to request if MCP is configured and requested
	if requestType != schemas.EmbeddingRequest &&
		requestType != schemas.SpeechRequest &&
		requestType != schemas.TranscriptionRequest &&
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

	msg := bifrost.getChannelMessage(*preReq, requestType)
	msg.Context = ctx

	select {
	case queue <- *msg:
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
		case queue <- *msg:
			// Message was sent successfully
		case <-ctx.Done():
			bifrost.releaseChannelMessage(msg)
			return nil, newBifrostErrorFromMsg("request cancelled while waiting for queue space")
		}
	}

	var result *schemas.BifrostResponse
	var resp *schemas.BifrostResponse
	select {
	case result = <-msg.Response:
		resp, bifrostErr := pipeline.RunPostHooks(&ctx, result, nil, len(bifrost.plugins))
		if bifrostErr != nil {
			bifrost.releaseChannelMessage(msg)
			return nil, bifrostErr
		}
		bifrost.releaseChannelMessage(msg)
		return resp, nil
	case bifrostErrVal := <-msg.Err:
		bifrostErrPtr := &bifrostErrVal
		resp, bifrostErrPtr = pipeline.RunPostHooks(&ctx, nil, bifrostErrPtr, len(bifrost.plugins))
		bifrost.releaseChannelMessage(msg)
		if bifrostErrPtr != nil {
			return nil, bifrostErrPtr
		}
		return resp, nil
	}
}

// tryStreamRequest is a generic function that handles common request processing logic
// It consolidates queue setup, plugin pipeline execution, enqueue logic, and response handling
func (bifrost *Bifrost) tryStreamRequest(req *schemas.BifrostRequest, ctx context.Context, requestType schemas.RequestType) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	queue, err := bifrost.getProviderQueue(req.Provider)
	if err != nil {
		return nil, newBifrostError(err)
	}

	// Add MCP tools to request if MCP is configured and requested
	if requestType != schemas.SpeechStreamRequest && requestType != schemas.TranscriptionStreamRequest && bifrost.mcpManager != nil {
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

	msg := bifrost.getChannelMessage(*preReq, requestType)
	msg.Context = ctx

	select {
	case queue <- *msg:
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
		case queue <- *msg:
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
		bifrost.releaseChannelMessage(msg)
		return nil, &bifrostErrVal
	}
}

// requestWorker handles incoming requests from the queue for a specific provider.
// It manages retries, error handling, and response processing.
func (bifrost *Bifrost) requestWorker(provider schemas.Provider, config *schemas.ProviderConfig, queue chan ChannelMessage) {
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
					Error: schemas.ErrorField{
						Message: err.Error(),
						Error:   err,
					},
				}
				continue
			}
		}

		// Track attempts
		var attempts int

		// Create plugin pipeline for streaming requests outside retry loop to prevent leaks
		var postHookRunner schemas.PostHookRunner
		if isStreamRequestType(req.Type) {
			pipeline := bifrost.getPluginPipeline()
			defer bifrost.releasePluginPipeline(pipeline)

			postHookRunner = func(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				resp, bifrostErr := pipeline.RunPostHooks(ctx, result, err, len(bifrost.plugins))
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
			if isStreamRequestType(req.Type) {
				stream, bifrostError = handleProviderStreamRequest(provider, &req, key, postHookRunner, req.Type)
				if bifrostError != nil && !bifrostError.IsBifrostError {
					break // Don't retry client errors
				}
			} else {
				result, bifrostError = handleProviderRequest(provider, &req, key, req.Type)
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
			if isStreamRequestType(req.Type) {
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
func handleProviderRequest(provider schemas.Provider, req *ChannelMessage, key schemas.Key, reqType schemas.RequestType) (*schemas.BifrostResponse, *schemas.BifrostError) {
	switch reqType {
	case schemas.TextCompletionRequest:
		return provider.TextCompletion(req.Context, req.Model, key, *req.Input.TextCompletionInput, req.Params)
	case schemas.ChatCompletionRequest:
		return provider.ChatCompletion(req.Context, req.Model, key, *req.Input.ChatCompletionInput, req.Params)
	case schemas.EmbeddingRequest:
		return provider.Embedding(req.Context, req.Model, key, req.Input.EmbeddingInput, req.Params)
	case schemas.SpeechRequest:
		return provider.Speech(req.Context, req.Model, key, req.Input.SpeechInput, req.Params)
	case schemas.TranscriptionRequest:
		return provider.Transcription(req.Context, req.Model, key, req.Input.TranscriptionInput, req.Params)
	default:
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: fmt.Sprintf("unsupported request type: %s", reqType),
			},
		}
	}
}

// handleProviderStreamRequest handles the stream request to the provider based on the request type
func handleProviderStreamRequest(provider schemas.Provider, req *ChannelMessage, key schemas.Key, postHookRunner schemas.PostHookRunner, reqType schemas.RequestType) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	switch reqType {
	case schemas.ChatCompletionStreamRequest:
		return provider.ChatCompletionStream(req.Context, postHookRunner, req.Model, key, *req.Input.ChatCompletionInput, req.Params)
	case schemas.SpeechStreamRequest:
		return provider.SpeechStream(req.Context, postHookRunner, req.Model, key, req.Input.SpeechInput, req.Params)
	case schemas.TranscriptionStreamRequest:
		return provider.TranscriptionStream(req.Context, postHookRunner, req.Model, key, req.Input.TranscriptionInput, req.Params)
	default:
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: fmt.Sprintf("unsupported request type: %s", reqType),
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
func (p *PluginPipeline) RunPostHooks(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, count int) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Defensive: ensure count is within valid bounds
	if count < 0 {
		count = 0
	}
	if count > len(p.plugins) {
		count = len(p.plugins)
	}
	var err error
	for i := count - 1; i >= 0; i-- {
		plugin := p.plugins[i]
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
		if resp != nil && bifrostErr.StatusCode == nil && bifrostErr.Error.Type == nil &&
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
	pipeline.plugins = bifrost.plugins
	pipeline.logger = bifrost.logger
	pipeline.resetPluginPipeline()
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
func (bifrost *Bifrost) getChannelMessage(req schemas.BifrostRequest, reqType schemas.RequestType) *ChannelMessage {
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
	msg.Type = reqType

	// Conditionally allocate ResponseStream for streaming requests only
	if isStreamRequestType(reqType) {
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
		return schemas.Key{}, fmt.Errorf("no keys found for provider: %v", providerKey)
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

	// Use a weighted random selection based on key weights
	totalWeight := 0
	for _, key := range supportedKeys {
		totalWeight += int(key.Weight * 100) // Convert float to int for better performance
	}

	// Use a fast random number generator
	randomSource := rand.New(rand.NewSource(time.Now().UnixNano()))
	randomValue := randomSource.Intn(totalWeight)

	// Select key based on weight
	currentWeight := 0
	for _, key := range supportedKeys {
		currentWeight += int(key.Weight * 100)
		if randomValue < currentWeight {
			return key, nil
		}
	}

	// Fallback to first key if something goes wrong
	return supportedKeys[0], nil
}

// Shutdown gracefully stops all workers when triggered.
// It closes all request channels and waits for workers to exit.
func (bifrost *Bifrost) Shutdown() {
	bifrost.logger.Info("closing all request channels...")

	// Close all provider queues to signal workers to stop
	bifrost.requestQueues.Range(func(key, value interface{}) bool {
		close(value.(chan ChannelMessage))
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
	for _, plugin := range bifrost.plugins {
		err := plugin.Cleanup()
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("Error cleaning up plugin: %s", err.Error()))
		}
	}
}
