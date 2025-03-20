package bifrost

import (
	"bifrost/interfaces"
	"fmt"
	"log"
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

// Request represents a generic request for text or chat completion
type Request struct {
	Model string
	//* is this okay or should we do string | Message?
	Input    interface{}
	Params   *interfaces.ModelParameters
	Response chan *interfaces.CompletionResult
	Err      chan error
	Type     RequestType
}

// Bifrost manages providers and maintains infinite open channels
type Bifrost struct {
	account       interfaces.Account
	providers     []interfaces.Provider   // list of processed providers
	requestQueues map[string]chan Request // provider request queues
	wg            sync.WaitGroup
}

func (bifrost *Bifrost) prepareProvider(provider interfaces.Provider) error {
	concurrency, err := bifrost.account.GetConcurrencyAndBufferSizeForProvider(provider)
	if err != nil {
		log.Fatalf("Failed to get concurrency and buffer size for provider: %v", err)
		return err
	}

	// Check if the provider has any keys
	keys, err := bifrost.account.GetKeysForProvider(provider)
	if err != nil || len(keys) == 0 {
		log.Fatalf("Failed to get keys for provider: %v", err)
		return err
	}

	queue := make(chan Request, concurrency.BufferSize) // Buffered channel per provider
	bifrost.requestQueues[string(provider.GetProviderKey())] = queue

	// Start specified number of workers
	for i := 0; i < concurrency.Concurrency; i++ {
		bifrost.wg.Add(1)
		go bifrost.processRequests(provider, queue)
	}

	return nil
}

// Initializes infinite listening channels for each provider
func Init(account interfaces.Account) (*Bifrost, error) {
	bifrost := &Bifrost{account: account}

	providers, err := bifrost.account.GetInitiallyConfiguredProviders()
	if err != nil {
		log.Fatalf("Failed to get initially configured providers: %v", err)
		return nil, err
	}

	bifrost.requestQueues = make(map[string]chan Request)

	// Create buffered channels for each provider and start workers
	for _, provider := range providers {
		if err := bifrost.prepareProvider(provider); err != nil {
			log.Fatalf("Failed to prepare provider: %v", err)
			return nil, err
		}
	}

	return bifrost, nil
}

func (bifrost *Bifrost) SelectFromProviderKeys(provider interfaces.Provider, model string) (string, error) {
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

func (bifrost *Bifrost) processRequests(provider interfaces.Provider, queue chan Request) {
	defer bifrost.wg.Done()

	for req := range queue {
		var result *interfaces.CompletionResult
		var err error

		key, err := bifrost.SelectFromProviderKeys(provider, req.Model)
		if err != nil {
			req.Err <- err
			continue
		}

		if req.Type == TextCompletionRequest {
			result, err = provider.TextCompletion(req.Model, key, req.Input.(string), req.Params)
		} else if req.Type == ChatCompletionRequest {
			result, err = provider.ChatCompletion(req.Model, key, req.Input.([]interfaces.Message), req.Params)
		}

		if err != nil {
			req.Err <- err
		} else {
			req.Response <- result
		}
	}

	fmt.Println("Worker for provider", provider.GetProviderKey(), "exiting...")
}

func (bifrost *Bifrost) GetProviderFromProviderKey(key interfaces.SupportedModelProvider) (interfaces.Provider, error) {
	for _, provider := range bifrost.providers {
		if provider.GetProviderKey() == key {
			return provider, nil
		}
	}

	return nil, fmt.Errorf("no provider found for key: %s", key)
}

func (bifrost *Bifrost) GetProviderQueue(providerKey interfaces.SupportedModelProvider) (chan Request, error) {
	var queue chan Request
	var exists bool

	if queue, exists = bifrost.requestQueues[string(providerKey)]; !exists {
		provider, err := bifrost.GetProviderFromProviderKey(providerKey)
		if err != nil {
			return nil, err
		}

		if err := bifrost.prepareProvider(provider); err != nil {
			return nil, err
		}

		queue = bifrost.requestQueues[string(providerKey)]
	}

	return queue, nil
}

func (bifrost *Bifrost) TextCompletionRequest(providerKey interfaces.SupportedModelProvider, model, text string, params *interfaces.ModelParameters) (*interfaces.CompletionResult, error) {
	queue, err := bifrost.GetProviderQueue(providerKey)
	if err != nil {
		return nil, err
	}

	responseChan := make(chan *interfaces.CompletionResult)
	errorChan := make(chan error)

	queue <- Request{
		Model:    model,
		Input:    text,
		Params:   params,
		Response: responseChan,
		Err:      errorChan,
		Type:     TextCompletionRequest,
	}

	select {
	case result := <-responseChan:
		return result, nil
	case err := <-errorChan:
		return nil, err
	}
}

func (bifrost *Bifrost) ChatCompletionRequest(providerKey interfaces.SupportedModelProvider, model string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.CompletionResult, error) {
	queue, err := bifrost.GetProviderQueue(providerKey)
	if err != nil {
		return nil, err
	}

	responseChan := make(chan *interfaces.CompletionResult)
	errorChan := make(chan error)

	queue <- Request{
		Model:    model,
		Input:    messages,
		Params:   params,
		Response: responseChan,
		Err:      errorChan,
		Type:     ChatCompletionRequest,
	}

	// Wait for response
	select {
	case result := <-responseChan:
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
	bifrost.wg.Wait()

	fmt.Println("Bifrost has shut down gracefully.")
}

// Cleanup handles SIGINT (Ctrl+C) to exit cleanly
func (bifrost *Bifrost) Cleanup() {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	<-signalChan       // Wait for interrupt signal
	bifrost.Shutdown() // Gracefully shut down
}
