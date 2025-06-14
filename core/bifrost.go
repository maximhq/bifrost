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
	"time"

	"github.com/maximhq/bifrost/core/providers"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// RequestType represents the type of request being made to a provider.
type RequestType string

const (
	TextCompletionRequest RequestType = "text_completion"
	ChatCompletionRequest RequestType = "chat_completion"
)

// ChannelMessage represents a message passed through the request channel.
// It contains the request, response and error channels, and the request type.
type ChannelMessage struct {
	schemas.BifrostRequest
	Context  context.Context
	Response chan *schemas.BifrostResponse
	Err      chan schemas.BifrostError
	Type     RequestType
}

// Bifrost manages providers and maintains sepcified open channels for concurrent processing.
// It handles request routing, provider management, and response processing.
type Bifrost struct {
	account             schemas.Account                               // account interface
	providers           []schemas.Provider                            // list of processed providers
	plugins             []schemas.Plugin                              // list of plugins
	requestQueues       map[schemas.ModelProvider]chan ChannelMessage // provider request queues
	waitGroups          map[schemas.ModelProvider]*sync.WaitGroup     // wait groups for each provider
	channelMessagePool  sync.Pool                                     // Pool for ChannelMessage objects, initial pool size is set in Init
	responseChannelPool sync.Pool                                     // Pool for response channels, initial pool size is set in Init
	errorChannelPool    sync.Pool                                     // Pool for error channels, initial pool size is set in Init
	logger              schemas.Logger                                // logger instance, default logger is used if not provided
	dropExcessRequests  bool                                          // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
	backgroundCtx       context.Context                               // Shared background context for nil context handling
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

// NewPluginPipeline creates a new pipeline for a given plugin slice and logger.
func NewPluginPipeline(plugins []schemas.Plugin, logger schemas.Logger) *PluginPipeline {
	return &PluginPipeline{
		plugins: plugins,
		logger:  logger,
	}
}

// RunPreHooks executes PreHooks in order, tracks how many ran, and returns the final request, any short-circuit response, and the count.
func (p *PluginPipeline) RunPreHooks(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.BifrostResponse, int) {
	var resp *schemas.BifrostResponse
	var err error
	for i, plugin := range p.plugins {
		req, resp, err = plugin.PreHook(ctx, req)
		if err != nil {
			p.preHookErrors = append(p.preHookErrors, err)
			p.logger.Warn(fmt.Sprintf("Error in PreHook for plugin %s: %v", plugin.GetName(), err))
		}
		p.executedPreHooks = i + 1
		if resp != nil {
			return req, resp, p.executedPreHooks // short-circuit: only plugins up to and including i ran
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
			p.logger.Warn(fmt.Sprintf("Error in PostHook for plugin %s: %v", plugin.GetName(), err))
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

// createProviderFromProviderKey creates a new provider instance based on the provider key.
// It returns an error if the provider is not supported.
func (bifrost *Bifrost) createProviderFromProviderKey(providerKey schemas.ModelProvider, config *schemas.ProviderConfig) (schemas.Provider, error) {
	switch providerKey {
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
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

// prepareProvider sets up a provider with its configuration, keys, and worker channels.
// It initializes the request queue and starts worker goroutines for processing requests.
func (bifrost *Bifrost) prepareProvider(providerKey schemas.ModelProvider, config *schemas.ProviderConfig) error {
	providerConfig, err := bifrost.account.GetConfigForProvider(providerKey)
	if err != nil {
		return fmt.Errorf("failed to get config for provider: %v", err)
	}

	// Check if the provider has any keys (skip vertex)
	if providerKey != schemas.Vertex {
		keys, err := bifrost.account.GetKeysForProvider(providerKey)
		if err != nil || len(keys) == 0 {
			return fmt.Errorf("failed to get keys for provider: %v", err)
		}
	}

	queue := make(chan ChannelMessage, providerConfig.ConcurrencyAndBufferSize.BufferSize) // Buffered channel per provider

	bifrost.requestQueues[providerKey] = queue

	// Start specified number of workers
	bifrost.waitGroups[providerKey] = &sync.WaitGroup{}

	provider, err := bifrost.createProviderFromProviderKey(providerKey, config)
	if err != nil {
		return fmt.Errorf("failed to create provider for the given key: %v", err)
	}

	for range providerConfig.ConcurrencyAndBufferSize.Concurrency {
		bifrost.waitGroups[providerKey].Add(1)
		go bifrost.requestWorker(provider, queue)
	}

	return nil
}

// Init initializes a new Bifrost instance with the given configuration.
// It sets up the account, plugins, object pools, and initializes providers.
// Returns an error if initialization fails.
// Initial Memory Allocations happens here as per the initial pool size.
func Init(config schemas.BifrostConfig) (*Bifrost, error) {
	if config.Account == nil {
		return nil, fmt.Errorf("account is required to initialize Bifrost")
	}

	bifrost := &Bifrost{
		account:            config.Account,
		plugins:            config.Plugins,
		waitGroups:         make(map[schemas.ModelProvider]*sync.WaitGroup),
		requestQueues:      make(map[schemas.ModelProvider]chan ChannelMessage),
		dropExcessRequests: config.DropExcessRequests,
		backgroundCtx:      context.Background(),
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

	// Prewarm pools with multiple objects
	for range config.InitialPoolSize {
		// Create and put new objects directly into pools
		bifrost.channelMessagePool.Put(&ChannelMessage{})
		bifrost.responseChannelPool.Put(make(chan *schemas.BifrostResponse, 1))
		bifrost.errorChannelPool.Put(make(chan schemas.BifrostError, 1))
	}

	providerKeys, err := bifrost.account.GetConfiguredProviders()
	if err != nil {
		return nil, err
	}

	if config.Logger == nil {
		config.Logger = NewDefaultLogger(schemas.LogLevelInfo)
	}
	bifrost.logger = config.Logger

	// Create buffered channels for each provider and start workers
	for _, providerKey := range providerKeys {
		config, err := bifrost.account.GetConfigForProvider(providerKey)
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("failed to get config for provider, skipping init: %v", err))
			continue
		}

		if err := bifrost.prepareProvider(providerKey, config); err != nil {
			bifrost.logger.Warn(fmt.Sprintf("failed to prepare provider %s: %v", providerKey, err))
		}
	}

	return bifrost, nil
}

// getChannelMessage gets a ChannelMessage from the pool and configures it with the request.
// It also gets response and error channels from their respective pools.
func (bifrost *Bifrost) getChannelMessage(req schemas.BifrostRequest, reqType RequestType) *ChannelMessage {
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

	return msg
}

// releaseChannelMessage returns a ChannelMessage and its channels to their respective pools.
func (bifrost *Bifrost) releaseChannelMessage(msg *ChannelMessage) {
	// Put channels back in pools
	bifrost.responseChannelPool.Put(msg.Response)
	bifrost.errorChannelPool.Put(msg.Err)

	// Clear references and return to pool
	msg.Response = nil
	msg.Err = nil
	bifrost.channelMessagePool.Put(msg)
}

// selectKeyFromProviderForModel selects an appropriate API key for a given provider and model.
// It uses weighted random selection if multiple keys are available.
func (bifrost *Bifrost) selectKeyFromProviderForModel(providerKey schemas.ModelProvider, model string) (string, error) {
	keys, err := bifrost.account.GetKeysForProvider(providerKey)
	if err != nil {
		return "", err
	}

	if len(keys) == 0 {
		return "", fmt.Errorf("no keys found for provider: %v", providerKey)
	}

	// filter out keys which dont support the model
	var supportedKeys []schemas.Key
	for _, key := range keys {
		if slices.Contains(key.Models, model) && strings.TrimSpace(key.Value) != "" {
			supportedKeys = append(supportedKeys, key)
		}
	}

	if len(supportedKeys) == 0 {
		return "", fmt.Errorf("no keys found that support model: %s", model)
	}

	if len(supportedKeys) == 1 {
		return supportedKeys[0].Value, nil
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
			return key.Value, nil
		}
	}

	// Fallback to first key if something goes wrong
	return supportedKeys[0].Value, nil
}

// Define a set of retryable status codes
var retryableStatusCodes = map[int]bool{
	500: true, // Internal Server Error
	502: true, // Bad Gateway
	503: true, // Service Unavailable
	504: true, // Gateway Timeout
	429: true, // Too Many Requests
}

// calculateBackoff implements exponential backoff with jitter for retry attempts.
func (bifrost *Bifrost) calculateBackoff(attempt int, config *schemas.ProviderConfig) time.Duration {
	// Calculate an exponential backoff: initial * 2^attempt
	backoff := min(config.NetworkConfig.RetryBackoffInitial*time.Duration(1<<uint(attempt)), config.NetworkConfig.RetryBackoffMax)

	// Add jitter (±20%)
	jitter := float64(backoff) * (0.8 + 0.4*rand.Float64())

	return time.Duration(jitter)
}

// requestWorker handles incoming requests from the queue for a specific provider.
// It manages retries, error handling, and response processing.
func (bifrost *Bifrost) requestWorker(provider schemas.Provider, queue chan ChannelMessage) {
	defer bifrost.waitGroups[provider.GetProviderKey()].Done()

	for req := range queue {
		var result *schemas.BifrostResponse
		var bifrostError *schemas.BifrostError
		var err error

		key := ""
		if provider.GetProviderKey() != schemas.Vertex {
			key, err = bifrost.selectKeyFromProviderForModel(provider.GetProviderKey(), req.Model)
			if err != nil {
				bifrost.logger.Warn(fmt.Sprintf("Error selecting key for model %s: %v", req.Model, err))
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

		config, err := bifrost.account.GetConfigForProvider(provider.GetProviderKey())
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("Error getting config for provider %s: %v", provider.GetProviderKey(), err))
			req.Err <- schemas.BifrostError{
				IsBifrostError: false,
				Error: schemas.ErrorField{
					Message: err.Error(),
					Error:   err,
				},
			}
			continue
		}

		// Track attempts
		var attempts int

		// Execute request with retries
		for attempts = 0; attempts <= config.NetworkConfig.MaxRetries; attempts++ {
			if attempts > 0 {
				// Log retry attempt
				bifrost.logger.Info(fmt.Sprintf(
					"Retrying request (attempt %d/%d) for model %s: %s",
					attempts, config.NetworkConfig.MaxRetries, req.Model,
					bifrostError.Error.Message,
				))

				// Calculate and apply backoff
				backoff := bifrost.calculateBackoff(attempts-1, config)
				time.Sleep(backoff)
			}

			bifrost.logger.Debug(fmt.Sprintf("Attempting request for provider %s", provider.GetProviderKey()))

			// Attempt the request
			if req.Type == TextCompletionRequest {
				if req.Input.TextCompletionInput == nil {
					bifrostError = &schemas.BifrostError{
						IsBifrostError: false,
						Error: schemas.ErrorField{
							Message: "text not provided for text completion request",
						},
					}
					break // Don't retry client errors
				} else {
					result, bifrostError = provider.TextCompletion(req.Context, req.Model, key, *req.Input.TextCompletionInput, req.Params)
				}
			} else if req.Type == ChatCompletionRequest {
				if req.Input.ChatCompletionInput == nil {
					bifrostError = &schemas.BifrostError{
						IsBifrostError: false,
						Error: schemas.ErrorField{
							Message: "chats not provided for chat completion request",
						},
					}
					break // Don't retry client errors
				} else {
					result, bifrostError = provider.ChatCompletion(req.Context, req.Model, key, *req.Input.ChatCompletionInput, req.Params)
				}
			}

			bifrost.logger.Debug(fmt.Sprintf("Request for provider %s completed", provider.GetProviderKey()))

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
				bifrost.logger.Warn(fmt.Sprintf("Request failed after %d %s",
					attempts,
					map[bool]string{true: "retries", false: "retry"}[attempts > 1]))
			}
			req.Err <- *bifrostError
		} else {
			req.Response <- result
		}
	}

	bifrost.logger.Debug(fmt.Sprintf("Worker for provider %s exiting...", provider.GetProviderKey()))
}

// GetConfiguredProviderFromProviderKey returns the provider instance for a given provider key.
// Uses the GetProviderKey method of the provider interface to find the provider.
func (bifrost *Bifrost) GetConfiguredProviderFromProviderKey(key schemas.ModelProvider) (schemas.Provider, error) {
	for _, provider := range bifrost.providers {
		if provider.GetProviderKey() == key {
			return provider, nil
		}
	}

	return nil, fmt.Errorf("no provider found for key: %s", key)
}

// getProviderQueue returns the request queue for a given provider key.
// If the queue doesn't exist, it creates one at runtime and initializes the provider,
// given the provider config is provided in the account interface implementation.
func (bifrost *Bifrost) getProviderQueue(providerKey schemas.ModelProvider) (chan ChannelMessage, error) {
	var queue chan ChannelMessage
	var exists bool

	if queue, exists = bifrost.requestQueues[providerKey]; !exists {
		bifrost.logger.Debug(fmt.Sprintf("Creating new request queue for provider %s at runtime", providerKey))

		config, err := bifrost.account.GetConfigForProvider(providerKey)
		if err != nil {
			return nil, fmt.Errorf("failed to get config for provider: %v", err)
		}

		if err := bifrost.prepareProvider(providerKey, config); err != nil {
			return nil, err
		}

		queue = bifrost.requestQueues[providerKey]
	}

	return queue, nil
}

// TextCompletionRequest sends a text completion request to the specified provider.
// It handles plugin hooks, request validation, response processing, and fallback providers.
// If the primary provider fails, it will try each fallback provider in order until one succeeds.
func (bifrost *Bifrost) TextCompletionRequest(ctx context.Context, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "bifrost request cannot be nil",
			},
		}
	}

	if req.Provider == "" {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "provider is required",
			},
		}
	}

	if req.Model == "" {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "model is required",
			},
		}
	}

	// Try the primary provider first
	primaryResult, primaryErr := bifrost.tryTextCompletion(req, ctx)
	if primaryErr == nil {
		return primaryResult, nil
	}

	if primaryErr.Error.Type != nil && *primaryErr.Error.Type == schemas.RequestCancelled {
		return nil, primaryErr
	}

	// If primary provider failed and we have fallbacks, try them in order
	if len(req.Fallbacks) > 0 {
		for _, fallback := range req.Fallbacks {
			// Check if we have config for this fallback provider
			_, err := bifrost.account.GetConfigForProvider(fallback.Provider)
			if err != nil {
				bifrost.logger.Warn(fmt.Sprintf("Config not found for provider %s, skipping fallback: %v", fallback.Provider, err))
				continue
			}

			// Create a new request with the fallback model
			fallbackReq := *req
			fallbackReq.Model = fallback.Model

			// Try the fallback provider
			result, fallbackErr := bifrost.tryTextCompletion(&fallbackReq, ctx)
			if fallbackErr == nil {
				bifrost.logger.Info(fmt.Sprintf("Successfully used fallback provider %s with model %s", fallback.Provider, fallback.Model))
				return result, nil
			}
			if fallbackErr.Error.Type != nil && *fallbackErr.Error.Type == schemas.RequestCancelled {
				return nil, fallbackErr
			}

			bifrost.logger.Warn(fmt.Sprintf("Fallback provider %s failed: %s", fallback.Provider, fallbackErr.Error.Message))
		}
	}

	// All providers failed, return the original error
	return nil, primaryErr
}

// tryTextCompletion attempts a text completion request with a single provider.
// This is a helper function used by TextCompletionRequest to handle individual provider attempts.
func (bifrost *Bifrost) tryTextCompletion(req *schemas.BifrostRequest, ctx context.Context) (*schemas.BifrostResponse, *schemas.BifrostError) {
	queue, err := bifrost.getProviderQueue(req.Provider)
	if err != nil {
		return nil, newBifrostError(err)
	}

	pipeline := NewPluginPipeline(bifrost.plugins, bifrost.logger)
	preReq, preResp, preCount := pipeline.RunPreHooks(&ctx, req)
	if preResp != nil {
		resp, bifrostErr := pipeline.RunPostHooks(&ctx, preResp, nil, preCount)
		// If PostHooks recovered from error, return resp; if not, return error
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		return resp, nil
	}
	if preReq == nil {
		return nil, newBifrostErrorFromMsg("bifrost request after plugin hooks cannot be nil")
	}

	msg := bifrost.getChannelMessage(*preReq, TextCompletionRequest)
	msg.Context = ctx

	select {
	case queue <- *msg:
		// Message was sent successfully
	case <-ctx.Done():
		bifrost.releaseChannelMessage(msg)
		return nil, newBifrostErrorFromMsg("request cancelled while waiting for queue space")
	default:
		if bifrost.dropExcessRequests {
			bifrost.releaseChannelMessage(msg)
			bifrost.logger.Warn("Request dropped: queue is full, please increase the queue size or set dropExcessRequests to false")
			return nil, newBifrostErrorFromMsg("request dropped: queue is full")
		}
		if ctx == nil {
			ctx = bifrost.backgroundCtx
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

// ChatCompletionRequest sends a chat completion request to the specified provider.
// It handles plugin hooks, request validation, response processing, and fallback providers.
// If the primary provider fails, it will try each fallback provider in order until one succeeds.
func (bifrost *Bifrost) ChatCompletionRequest(ctx context.Context, req *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if req == nil {
		return nil, newBifrostErrorFromMsg("bifrost request cannot be nil")
	}

	if req.Provider == "" {
		return nil, newBifrostErrorFromMsg("provider is required")
	}

	if req.Model == "" {
		return nil, newBifrostErrorFromMsg("model is required")
	}

	// Try the primary provider first
	primaryResult, primaryErr := bifrost.tryChatCompletion(req, ctx)
	if primaryErr == nil {
		return primaryResult, nil
	}

	// If primary provider failed and we have fallbacks, try them in order
	if len(req.Fallbacks) > 0 {
		for _, fallback := range req.Fallbacks {
			// Check if we have config for this fallback provider
			_, err := bifrost.account.GetConfigForProvider(fallback.Provider)
			if err != nil {
				bifrost.logger.Warn(fmt.Sprintf("Skipping fallback provider %s: %v", fallback.Provider, err))
				continue
			}

			// Create a new request with the fallback model
			fallbackReq := *req
			fallbackReq.Model = fallback.Model

			// Try the fallback provider
			result, fallbackErr := bifrost.tryChatCompletion(&fallbackReq, ctx)
			if fallbackErr == nil {
				bifrost.logger.Info(fmt.Sprintf("Successfully used fallback provider %s with model %s", fallback.Provider, fallback.Model))
				return result, nil
			}
			bifrost.logger.Warn(fmt.Sprintf("Fallback provider %s failed: %v", fallback.Provider, fallbackErr.Error.Message))
		}
	}

	// All providers failed, return the original error
	return nil, primaryErr
}

// tryChatCompletion attempts a chat completion request with a single provider.
// This is a helper function used by ChatCompletionRequest to handle individual provider attempts.
func (bifrost *Bifrost) tryChatCompletion(req *schemas.BifrostRequest, ctx context.Context) (*schemas.BifrostResponse, *schemas.BifrostError) {
	queue, err := bifrost.getProviderQueue(req.Provider)
	if err != nil {
		return nil, newBifrostError(err)
	}

	pipeline := NewPluginPipeline(bifrost.plugins, bifrost.logger)
	preReq, preResp, preCount := pipeline.RunPreHooks(&ctx, req)
	if preResp != nil {
		resp, bifrostErr := pipeline.RunPostHooks(&ctx, preResp, nil, preCount)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		return resp, nil
	}
	if preReq == nil {
		return nil, newBifrostErrorFromMsg("bifrost request after plugin hooks cannot be nil")
	}

	msg := bifrost.getChannelMessage(*preReq, ChatCompletionRequest)
	msg.Context = ctx

	select {
	case queue <- *msg:
		// Message was sent successfully
	case <-ctx.Done():
		bifrost.releaseChannelMessage(msg)
		return nil, newBifrostErrorFromMsg("request cancelled while waiting for queue space")
	default:
		if bifrost.dropExcessRequests {
			bifrost.releaseChannelMessage(msg)
			bifrost.logger.Warn("Request dropped: queue is full, please increase the queue size or set dropExcessRequests to false")
			return nil, newBifrostErrorFromMsg("request dropped: queue is full")
		}
		if ctx == nil {
			ctx = bifrost.backgroundCtx
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

// Cleanup gracefully stops all workers when triggered.
// It closes all request channels and waits for workers to exit.
func (bifrost *Bifrost) Cleanup() {
	bifrost.logger.Info("Graceful Cleanup Initiated - Closing all request channels...")

	// Close all provider queues to signal workers to stop
	for _, queue := range bifrost.requestQueues {
		close(queue)
	}

	// Wait for all workers to exit
	for _, waitGroup := range bifrost.waitGroups {
		waitGroup.Wait()
	}

	// Cleanup plugins
	for _, plugin := range bifrost.plugins {
		err := plugin.Cleanup()
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("Error cleaning up plugin: %s", err.Error()))
		}
	}

	bifrost.logger.Info("Graceful Cleanup Completed")
}
