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
	Err      chan error
	Type     RequestType
}

// Bifrost manages providers and maintains infinite open channels
type Bifrost struct {
	account       interfaces.Account
	providers     []interfaces.Provider // list of processed providers
	plugins       []interfaces.Plugin
	requestQueues map[interfaces.SupportedModelProvider]chan ChannelMessage // provider request queues
	waitGroups    map[interfaces.SupportedModelProvider]*sync.WaitGroup
	logger        interfaces.Logger
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
	bifrost := &Bifrost{account: account, plugins: plugins}
	bifrost.waitGroups = make(map[interfaces.SupportedModelProvider]*sync.WaitGroup)

	providerKeys, err := bifrost.account.GetInitiallyConfiguredProviders()
	if err != nil {
		return nil, err
	}

	if logger == nil {
		logger = NewDefaultLogger(interfaces.LogLevelInfo)
	}
	bifrost.logger = logger

	bifrost.requestQueues = make(map[interfaces.SupportedModelProvider]chan ChannelMessage)

	// Create buffered channels for each provider and start workers
	for _, providerKey := range providerKeys {
		config, err := bifrost.account.GetConfigForProvider(providerKey)
		if err != nil {
			return nil, fmt.Errorf("failed to get config for provider: %v", err)
		}

		if err := bifrost.prepareProvider(providerKey, config); err != nil {
			bifrost.logger.Warn(fmt.Sprintf("failed to prepare provider: %v", err))
		}
	}

	return bifrost, nil
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
		return "", fmt.Errorf("no keys found supporting model: %s", model)
	}

	// Create a new random source
	randomSource := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Shuffle keys using the new random number generator
	randomSource.Shuffle(len(supportedKeys), func(i, j int) {
		supportedKeys[i], supportedKeys[j] = supportedKeys[j], supportedKeys[i]
	})

	// Compute the cumulative weight sum
	var totalWeight float64
	for _, key := range supportedKeys {
		totalWeight += key.Weight
	}

	// Generate a random number within total weight
	r := randomSource.Float64() * totalWeight
	var cumulative float64

	// Select the key based on weighted probability
	for _, key := range supportedKeys {
		cumulative += key.Weight
		if r <= cumulative {
			return key.Value, nil
		}
	}

	// Fallback (should never happen)
	return supportedKeys[len(supportedKeys)-1].Value, nil
}

func (bifrost *Bifrost) processRequests(provider interfaces.Provider, queue chan ChannelMessage) {
	defer bifrost.waitGroups[provider.GetProviderKey()].Done()

	for req := range queue {
		var result *interfaces.BifrostResponse
		var err error

		key, err := bifrost.SelectKeyFromProviderForModel(provider.GetProviderKey(), req.Model)
		if err != nil {
			req.Err <- err
			continue
		}

		if req.Type == TextCompletionRequest {
			if req.Input.TextCompletionInput == nil {
				err = fmt.Errorf("text not provided for text completion request")
			} else {
				result, err = provider.TextCompletion(req.Model, key, *req.Input.TextCompletionInput, req.Params)
			}
		} else if req.Type == ChatCompletionRequest {
			if req.Input.ChatCompletionInput == nil {
				err = fmt.Errorf("chats not provided for chat completion request")
			} else {
				result, err = provider.ChatCompletion(req.Model, key, *req.Input.ChatCompletionInput, req.Params)
			}
		}

		if err != nil {
			req.Err <- err
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

func (bifrost *Bifrost) TextCompletionRequest(providerKey interfaces.SupportedModelProvider, req *interfaces.BifrostRequest, ctx context.Context) (*interfaces.BifrostResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("bifrost request cannot be nil")
	}

	queue, err := bifrost.GetProviderQueue(providerKey)
	if err != nil {
		return nil, err
	}

	responseChan := make(chan *interfaces.BifrostResponse)
	errorChan := make(chan error)

	for _, plugin := range bifrost.plugins {
		req, err = plugin.PreHook(&ctx, req)
		if err != nil {
			return nil, err
		}
	}

	if req == nil {
		return nil, fmt.Errorf("bifrost request after plugin hooks cannot be nil")
	}

	queue <- ChannelMessage{
		BifrostRequest: *req,
		Response:       responseChan,
		Err:            errorChan,
		Type:           TextCompletionRequest,
	}

	select {
	case result := <-responseChan:
		// Run plugins in reverse order
		for i := len(bifrost.plugins) - 1; i >= 0; i-- {
			result, err = bifrost.plugins[i].PostHook(&ctx, result)

			if err != nil {
				return nil, err
			}
		}

		return result, nil
	case err := <-errorChan:
		return nil, err
	}
}

func (bifrost *Bifrost) ChatCompletionRequest(providerKey interfaces.SupportedModelProvider, req *interfaces.BifrostRequest, ctx context.Context) (*interfaces.BifrostResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("bifrost request cannot be nil")
	}

	queue, err := bifrost.GetProviderQueue(providerKey)
	if err != nil {
		return nil, err
	}

	responseChan := make(chan *interfaces.BifrostResponse)
	errorChan := make(chan error)

	for _, plugin := range bifrost.plugins {
		req, err = plugin.PreHook(&ctx, req)
		if err != nil {
			return nil, err
		}
	}

	if req == nil {
		return nil, fmt.Errorf("bifrost request after pre plugin hooks cannot be nil")
	}

	queue <- ChannelMessage{
		BifrostRequest: *req,
		Response:       responseChan,
		Err:            errorChan,
		Type:           ChatCompletionRequest,
	}

	select {
	case result := <-responseChan:
		// Run plugins in reverse order
		for i := len(bifrost.plugins) - 1; i >= 0; i-- {
			result, err = bifrost.plugins[i].PostHook(&ctx, result)

			if err != nil {
				return nil, err
			}
		}

		return result, nil
	case err := <-errorChan:
		return nil, err
	}
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
