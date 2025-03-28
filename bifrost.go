package bifrost

import (
	"bifrost/interfaces"
	"bifrost/providers"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
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
	configs       map[interfaces.SupportedModelProvider]interfaces.ProviderConfig
	requestQueues map[interfaces.SupportedModelProvider]chan ChannelMessage // provider request queues
	wg            map[interfaces.SupportedModelProvider]*sync.WaitGroup
}

func createProviderFromProviderKey(providerKey interfaces.SupportedModelProvider, config *interfaces.ProviderConfig) (interfaces.Provider, error) {
	switch providerKey {
	case interfaces.OpenAI:
		return providers.NewOpenAIProvider(config), nil
	case interfaces.Anthropic:
		return providers.NewAnthropicProvider(config), nil
	case interfaces.Bedrock:
		return providers.NewBedrockProvider(config), nil
	case interfaces.Cohere:
		return providers.NewCohereProvider(config), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

func getConfigForProvider(providerKey interfaces.SupportedModelProvider, configs map[interfaces.SupportedModelProvider]interfaces.ProviderConfig) (*interfaces.ProviderConfig, error) {
	if config, ok := configs[providerKey]; ok {
		return &config, nil
	}

	return nil, fmt.Errorf("no config found for provider: %s", providerKey)
}

func (bifrost *Bifrost) prepareProvider(providerKey interfaces.SupportedModelProvider, config *interfaces.ProviderConfig) error {
	provider, err := createProviderFromProviderKey(providerKey, config)
	if err != nil {
		return fmt.Errorf("failed to get provider for the given key: %v", err)
	}

	concurrencyAndBuffer, err := bifrost.account.GetConcurrencyAndBufferSizeForProvider(provider)
	if err != nil {
		return fmt.Errorf("failed to get concurrency and buffer size for provider: %v", err)
	}

	// Check if the provider has any keys
	keys, err := bifrost.account.GetKeysForProvider(provider)
	if err != nil || len(keys) == 0 {
		return fmt.Errorf("failed to get keys for provider: %v", err)
	}

	queue := make(chan ChannelMessage, concurrencyAndBuffer.BufferSize) // Buffered channel per provider

	bifrost.requestQueues[provider.GetProviderKey()] = queue

	// Start specified number of workers
	bifrost.wg[provider.GetProviderKey()] = &sync.WaitGroup{}

	for i := 0; i < concurrencyAndBuffer.Concurrency; i++ {
		bifrost.wg[provider.GetProviderKey()].Add(1)
		go bifrost.processRequests(provider, queue)
	}

	return nil
}

// Initializes infinite listening channels for each provider
func Init(account interfaces.Account, plugins []interfaces.Plugin, configs map[interfaces.SupportedModelProvider]interfaces.ProviderConfig) (*Bifrost, error) {
	bifrost := &Bifrost{account: account, plugins: plugins}
	bifrost.wg = make(map[interfaces.SupportedModelProvider]*sync.WaitGroup)

	providerKeys, err := bifrost.account.GetInitiallyConfiguredProviderKeys()
	if err != nil {
		return nil, err
	}

	bifrost.requestQueues = make(map[interfaces.SupportedModelProvider]chan ChannelMessage)
	bifrost.configs = configs

	// Create buffered channels for each provider and start workers
	for _, providerKey := range providerKeys {
		config, err := getConfigForProvider(providerKey, configs)
		if err != nil {
			return nil, fmt.Errorf("failed to get config for provider: %v", err)
		}

		if err := bifrost.prepareProvider(providerKey, config); err != nil {
			fmt.Printf("failed to prepare provider: %v", err)
		}
	}

	return bifrost, nil
}

func (bifrost *Bifrost) SelectKeyFromProviderForModel(provider interfaces.Provider, model string) (string, error) {
	keys, err := bifrost.account.GetKeysForProvider(provider)
	if err != nil {
		return "", err
	}

	if len(keys) == 0 {
		return "", fmt.Errorf("no keys found for provider: %v", provider.GetProviderKey())
	}

	// filter out keys which dont support the model
	var supportedKeys []interfaces.Key
	for _, key := range keys {
		for _, supportedModel := range key.Models {
			if supportedModel == model {
				supportedKeys = append(supportedKeys, key)
				break
			}
		}
	}

	if len(supportedKeys) == 0 {
		return "", fmt.Errorf("no keys found supporting model: %s", model)
	}

	// Create a new random source
	ran := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Shuffle keys using the new random number generator
	ran.Shuffle(len(supportedKeys), func(i, j int) {
		supportedKeys[i], supportedKeys[j] = supportedKeys[j], supportedKeys[i]
	})

	// Compute the cumulative weight sum
	var totalWeight float64
	for _, key := range supportedKeys {
		totalWeight += key.Weight
	}

	// Generate a random number within total weight
	r := ran.Float64() * totalWeight
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
	defer bifrost.wg[provider.GetProviderKey()].Done()

	for req := range queue {
		var result *interfaces.BifrostResponse
		var err error

		key, err := bifrost.SelectKeyFromProviderForModel(provider, req.Model)
		if err != nil {
			req.Err <- err
			continue
		}

		if req.Type == TextCompletionRequest {
			result, err = provider.TextCompletion(req.Model, key, *req.Input.TextInput, req.Params)
		} else if req.Type == ChatCompletionRequest {
			result, err = provider.ChatCompletion(req.Model, key, *req.Input.ChatInput, req.Params)
		}

		if err != nil {
			req.Err <- err
		} else {
			req.Response <- result
		}
	}

	fmt.Println("Worker for provider", provider.GetProviderKey(), "exiting...")
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
		config, err := getConfigForProvider(providerKey, bifrost.configs)
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
	queue, err := bifrost.GetProviderQueue(providerKey)
	if err != nil {
		return nil, err
	}

	responseChan := make(chan *interfaces.BifrostResponse)
	errorChan := make(chan error)

	for _, plugin := range bifrost.plugins {
		ctx, req, err = plugin.PreHook(ctx, req)
		if err != nil {
			return nil, err
		}
	}

	queue <- ChannelMessage{
		BifrostRequest: *req,
		Response:       responseChan,
		Err:            errorChan,
		Type:           TextCompletionRequest,
	}

	select {
	case result := <-responseChan:
		for _, plugin := range bifrost.plugins {
			result, err = plugin.PostHook(ctx, result)

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
	queue, err := bifrost.GetProviderQueue(providerKey)
	if err != nil {
		return nil, err
	}

	responseChan := make(chan *interfaces.BifrostResponse)
	errorChan := make(chan error)

	for _, plugin := range bifrost.plugins {
		ctx, req, err = plugin.PreHook(ctx, req)
		if err != nil {
			return nil, err
		}
	}

	queue <- ChannelMessage{
		BifrostRequest: *req,
		Response:       responseChan,
		Err:            errorChan,
		Type:           ChatCompletionRequest,
	}

	select {
	case result := <-responseChan:
		for _, plugin := range bifrost.plugins {
			result, err = plugin.PostHook(ctx, result)

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
	fmt.Println("\n[Graceful Shutdown Initiated] Closing all request channels...")

	// Close all provider queues to signal workers to stop
	for _, queue := range bifrost.requestQueues {
		close(queue)
	}

	// Wait for all workers to exit
	for _, wg := range bifrost.wg {
		wg.Wait()
	}

	fmt.Println("Bifrost has shut down gracefully.")
}

// Cleanup handles SIGINT (Ctrl+C) to exit cleanly
func (bifrost *Bifrost) Cleanup() {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	<-signalChan       // Wait for interrupt signal
	bifrost.Shutdown() // Gracefully shut down
}
