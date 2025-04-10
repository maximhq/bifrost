package bifrost

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/maximhq/bifrost/providers"
)

type RequestType string

const (
	TextCompletionRequest RequestType = "text_completion"
	ChatCompletionRequest RequestType = "chat_completion"
)

type ChannelMessage struct {
	interfaces.BifrostRequest
	Response chan *interfaces.BifrostResponse
	Err      chan interfaces.BifrostError
	Type     RequestType
}

// Bifrost manages providers and maintains infinite open channels
type Bifrost struct {
	account             interfaces.Account
	providers           []interfaces.Provider // list of processed providers
	plugins             []interfaces.Plugin
	requestQueues       map[interfaces.SupportedModelProvider]chan ChannelMessage // provider request queues
	waitGroups          map[interfaces.SupportedModelProvider]*sync.WaitGroup
	channelMessagePool  sync.Pool // Pool for ChannelMessage objects
	responseChannelPool sync.Pool // Pool for response channels
	errorChannelPool    sync.Pool // Pool for error channels
	logger              interfaces.Logger
}

func (bifrost *Bifrost) createProviderFromProviderKey(providerKey interfaces.SupportedModelProvider, config *interfaces.ProviderConfig) (interfaces.Provider, error) {
	switch providerKey {
	case interfaces.OpenAI:
		return providers.NewOpenAIProvider(config, bifrost.logger), nil
	case interfaces.Anthropic:
		return providers.NewAnthropicProvider(config, bifrost.logger), nil
	case interfaces.Bedrock:
		return providers.NewBedrockProvider(config), nil
	case interfaces.Cohere:
		return providers.NewCohereProvider(config), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

func (bifrost *Bifrost) prepareProvider(providerKey interfaces.SupportedModelProvider, config *interfaces.ProviderConfig) error {
	providerConfig, err := bifrost.account.GetConfigForProvider(providerKey)
	if err != nil {
		return fmt.Errorf("failed to get config for provider: %v", err)
	}

	// Check if the provider has any keys
	keys, err := bifrost.account.GetKeysForProvider(providerKey)
	if err != nil || len(keys) == 0 {
		return fmt.Errorf("failed to get keys for provider: %v", err)
	}

	queue := make(chan ChannelMessage, providerConfig.ConcurrencyAndBufferSize.BufferSize) // Buffered channel per provider

	bifrost.requestQueues[providerKey] = queue

	// Start specified number of workers
	bifrost.waitGroups[providerKey] = &sync.WaitGroup{}

	provider, err := bifrost.createProviderFromProviderKey(providerKey, config)
	if err != nil {
		return fmt.Errorf("failed to get provider for the given key: %v", err)
	}

	for range providerConfig.ConcurrencyAndBufferSize.Concurrency {
		bifrost.waitGroups[providerKey].Add(1)
		go bifrost.processRequests(provider, queue)
	}

	return nil
}

// Initializes infinite listening channels for each provider
func Init(account interfaces.Account, plugins []interfaces.Plugin, logger interfaces.Logger) (*Bifrost, error) {
	bifrost := &Bifrost{
		account:       account,
		plugins:       plugins,
		waitGroups:    make(map[interfaces.SupportedModelProvider]*sync.WaitGroup),
		requestQueues: make(map[interfaces.SupportedModelProvider]chan ChannelMessage),
	}

	// Initialize object pools
	bifrost.channelMessagePool = sync.Pool{
		New: func() interface{} {
			return &ChannelMessage{}
		},
	}
	bifrost.responseChannelPool = sync.Pool{
		New: func() interface{} {
			return make(chan *interfaces.BifrostResponse, 1)
		},
	}
	bifrost.errorChannelPool = sync.Pool{
		New: func() interface{} {
			return make(chan interfaces.BifrostError, 1)
		},
	}

	// Prewarm pools with multiple objects
	for range 2500 {
		// Create and put new objects directly into pools
		bifrost.channelMessagePool.Put(&ChannelMessage{})
		bifrost.responseChannelPool.Put(make(chan *interfaces.BifrostResponse, 1))
		bifrost.errorChannelPool.Put(make(chan interfaces.BifrostError, 1))
	}

	providerKeys, err := bifrost.account.GetInitiallyConfiguredProviders()
	if err != nil {
		return nil, err
	}

	if logger == nil {
		logger = NewDefaultLogger(interfaces.LogLevelInfo)
	}
	bifrost.logger = logger

	// Create buffered channels for each provider and start workers
	for _, providerKey := range providerKeys {
		config, err := bifrost.account.GetConfigForProvider(providerKey)
		if err != nil {
			bifrost.logger.Warn(fmt.Sprintf("failed to get config for provider, skipping init: %v", err))
			continue
		}

		if err := bifrost.prepareProvider(providerKey, config); err != nil {
			bifrost.logger.Warn(fmt.Sprintf("failed to prepare provider: %v", err))
		}
	}

	return bifrost, nil
}

// getChannelMessage gets a ChannelMessage from the pool
func (bifrost *Bifrost) getChannelMessage(req interfaces.BifrostRequest, reqType RequestType) *ChannelMessage {
	// Get channels from pool
	responseChan := bifrost.responseChannelPool.Get().(chan *interfaces.BifrostResponse)
	errorChan := bifrost.errorChannelPool.Get().(chan interfaces.BifrostError)

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

// releaseChannelMessage returns a ChannelMessage and its channels to the pool
func (bifrost *Bifrost) releaseChannelMessage(msg *ChannelMessage) {
	// Put channels back in pools
	bifrost.responseChannelPool.Put(msg.Response)
	bifrost.errorChannelPool.Put(msg.Err)

	// Clear references and return to pool
	msg.Response = nil
	msg.Err = nil
	bifrost.channelMessagePool.Put(msg)
}

func (bifrost *Bifrost) SelectKeyFromProviderForModel(providerKey interfaces.SupportedModelProvider, model string) (string, error) {
	keys, err := bifrost.account.GetKeysForProvider(providerKey)
	if err != nil {
		return "", err
	}

	if len(keys) == 0 {
		return "", fmt.Errorf("no keys found for provider: %v", providerKey)
	}

	// filter out keys which dont support the model
	var supportedKeys []interfaces.Key
	for _, key := range keys {
		if slices.Contains(key.Models, model) {
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

// calculateBackoff implements exponential backoff with jitter
func (bifrost *Bifrost) calculateBackoff(attempt int, config *interfaces.ProviderConfig) time.Duration {
	// Calculate an exponential backoff: initial * 2^attempt
	backoff := config.NetworkConfig.RetryBackoffInitial * time.Duration(1<<uint(attempt))
	if backoff > config.NetworkConfig.RetryBackoffMax {
		backoff = config.NetworkConfig.RetryBackoffMax
	}

	// Add jitter (±20%)
	jitter := float64(backoff) * (0.8 + 0.4*rand.Float64())

	return time.Duration(jitter)
}

func (bifrost *Bifrost) processRequests(provider interfaces.Provider, queue chan ChannelMessage) {
	defer bifrost.waitGroups[provider.GetProviderKey()].Done()

	for req := range queue {
		var result *interfaces.BifrostResponse
		var bifrostError *interfaces.BifrostError

		key, err := bifrost.SelectKeyFromProviderForModel(provider.GetProviderKey(), req.Model)
		if err != nil {
			req.Err <- interfaces.BifrostError{
				IsBifrostError: false,
				Error: interfaces.ErrorField{
					Message: err.Error(),
					Error:   err,
				},
			}
			continue
		}

		config, err := bifrost.account.GetConfigForProvider(provider.GetProviderKey())
		if err != nil {
			req.Err <- interfaces.BifrostError{
				IsBifrostError: false,
				Error: interfaces.ErrorField{
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

			// Attempt the request
			if req.Type == TextCompletionRequest {
				if req.Input.TextCompletionInput == nil {
					bifrostError = &interfaces.BifrostError{
						IsBifrostError: false,
						Error: interfaces.ErrorField{
							Message: "text not provided for text completion request",
						},
					}
					break // Don't retry client errors
				} else {
					result, bifrostError = provider.TextCompletion(req.Model, key, *req.Input.TextCompletionInput, req.Params)
				}
			} else if req.Type == ChatCompletionRequest {
				if req.Input.ChatCompletionInput == nil {
					bifrostError = &interfaces.BifrostError{
						IsBifrostError: false,
						Error: interfaces.ErrorField{
							Message: "chats not provided for chat completion request",
						},
					}
					break // Don't retry client errors
				} else {
					result, bifrostError = provider.ChatCompletion(req.Model, key, *req.Input.ChatCompletionInput, req.Params)
				}
			}

			// Check if successful or if we should retry
			if bifrostError == nil ||
				//TODO should have a better way to check for only network errors
				bifrostError.IsBifrostError || // Only retry non-bifrost errors
				attempts == config.NetworkConfig.MaxRetries {
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

func (bifrost *Bifrost) GetConfiguredProviderFromProviderKey(key interfaces.SupportedModelProvider) (interfaces.Provider, error) {
	for _, provider := range bifrost.providers {
		if provider.GetProviderKey() == key {
			return provider, nil
		}
	}

	return nil, fmt.Errorf("no provider found for key: %s", key)
}

func (bifrost *Bifrost) GetProviderQueue(providerKey interfaces.SupportedModelProvider) (chan ChannelMessage, error) {
	var queue chan ChannelMessage
	var exists bool

	if queue, exists = bifrost.requestQueues[providerKey]; !exists {
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

func (bifrost *Bifrost) TextCompletionRequest(providerKey interfaces.SupportedModelProvider, req *interfaces.BifrostRequest, ctx context.Context) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	if req == nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: "bifrost request cannot be nil",
			},
		}
	}

	queue, err := bifrost.GetProviderQueue(providerKey)
	if err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: err.Error(),
			},
		}
	}

	for _, plugin := range bifrost.plugins {
		req, err = plugin.PreHook(&ctx, req)
		if err != nil {
			return nil, &interfaces.BifrostError{
				IsBifrostError: false,
				Error: interfaces.ErrorField{
					Message: err.Error(),
				},
			}
		}
	}

	if req == nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: "bifrost request after plugin hooks cannot be nil",
			},
		}
	}

	// Get a ChannelMessage from the pool
	msg := bifrost.getChannelMessage(*req, TextCompletionRequest)
	queue <- *msg

	// Handle response
	var result *interfaces.BifrostResponse
	select {
	case result = <-msg.Response:
		// Run plugins in reverse order
		for i := len(bifrost.plugins) - 1; i >= 0; i-- {
			result, err = bifrost.plugins[i].PostHook(&ctx, result)
			if err != nil {
				bifrost.releaseChannelMessage(msg)
				return nil, &interfaces.BifrostError{
					IsBifrostError: false,
					Error: interfaces.ErrorField{
						Message: err.Error(),
					},
				}
			}
		}
	case err := <-msg.Err:
		bifrost.releaseChannelMessage(msg)
		return nil, &err
	}

	// Return message to pool
	bifrost.releaseChannelMessage(msg)
	return result, nil
}

func (bifrost *Bifrost) ChatCompletionRequest(providerKey interfaces.SupportedModelProvider, req *interfaces.BifrostRequest, ctx context.Context) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	if req == nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: "bifrost request cannot be nil",
			},
		}
	}

	queue, err := bifrost.GetProviderQueue(providerKey)
	if err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: err.Error(),
			},
		}
	}

	for _, plugin := range bifrost.plugins {
		req, err = plugin.PreHook(&ctx, req)
		if err != nil {
			return nil, &interfaces.BifrostError{
				IsBifrostError: false,
				Error: interfaces.ErrorField{
					Message: err.Error(),
				},
			}
		}
	}

	if req == nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: "bifrost request after plugin hooks cannot be nil",
			},
		}
	}

	// Get a ChannelMessage from the pool
	msg := bifrost.getChannelMessage(*req, ChatCompletionRequest)
	queue <- *msg

	// Handle response
	var result *interfaces.BifrostResponse
	select {
	case result = <-msg.Response:
		// Run plugins in reverse order
		for i := len(bifrost.plugins) - 1; i >= 0; i-- {
			result, err = bifrost.plugins[i].PostHook(&ctx, result)
			if err != nil {
				bifrost.releaseChannelMessage(msg)
				return nil, &interfaces.BifrostError{
					IsBifrostError: false,
					Error: interfaces.ErrorField{
						Message: err.Error(),
					},
				}
			}
		}

	case err := <-msg.Err:
		bifrost.releaseChannelMessage(msg)
		return nil, &err
	}

	// Return message to pool
	bifrost.releaseChannelMessage(msg)
	return result, nil
}

// Shutdown gracefully stops all workers when triggered
func (bifrost *Bifrost) Shutdown() {
	bifrost.logger.Info("[BIFROST] Graceful Shutdown Initiated - Closing all request channels...")

	// Close all provider queues to signal workers to stop
	for _, queue := range bifrost.requestQueues {
		close(queue)
	}

	// Wait for all workers to exit
	for _, waitGroup := range bifrost.waitGroups {
		waitGroup.Wait()
	}
}

// Cleanup handles SIGINT (Ctrl+C) to exit cleanly
func (bifrost *Bifrost) Cleanup() {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	<-signalChan       // Wait for interrupt signal
	bifrost.Shutdown() // Gracefully shut down
}
