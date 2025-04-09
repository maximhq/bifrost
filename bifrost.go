package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/maximhq/bifrost/providers"
)

// Metrics to track timing
type RequestMetrics struct {
	TotalTime        time.Duration `json:"total_time"`
	QueueWaitTime    time.Duration `json:"queue_wait_time"`
	KeySelectionTime time.Duration `json:"key_selection_time"`
	ProviderTime     time.Duration `json:"provider_time"`
	PluginPreTime    time.Duration `json:"plugin_pre_time"`
	PluginPostTime   time.Duration `json:"plugin_post_time"`
	RequestCount     int64         `json:"request_count"`
	ErrorCount       int64         `json:"error_count"`
}

type RequestType string

const (
	TextCompletionRequest RequestType = "text_completion"
	ChatCompletionRequest RequestType = "chat_completion"
)

type ChannelMessage struct {
	interfaces.BifrostRequest
	Response  chan *interfaces.BifrostResponse
	Err       chan interfaces.BifrostError
	Type      RequestType
	Timestamp time.Time
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
	metrics             RequestMetrics
	metricsMutex        sync.RWMutex

	// Pool usage counters
	channelMessageGets       atomic.Int64
	channelMessagePuts       atomic.Int64
	channelMessageCreations  atomic.Int64
	responseChannelGets      atomic.Int64
	responseChannelPuts      atomic.Int64
	responseChannelCreations atomic.Int64
	errorChannelGets         atomic.Int64
	errorChannelPuts         atomic.Int64
	errorChannelCreations    atomic.Int64
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

// Init initializes a new Bifrost instance with the given account
func Init(config interfaces.BifrostConfig) (*Bifrost, error) {
	if config.Account == nil {
		return nil, fmt.Errorf("account is required to initialize Bifrost")
	}

	debug.SetGCPercent(-1)

	bifrost := &Bifrost{
		account:       config.Account,
		plugins:       config.Plugins,
		waitGroups:    make(map[interfaces.SupportedModelProvider]*sync.WaitGroup),
		requestQueues: make(map[interfaces.SupportedModelProvider]chan ChannelMessage),
	}

	// Initialize object pools
	bifrost.channelMessagePool = sync.Pool{
		New: func() interface{} {
			bifrost.channelMessageCreations.Add(1)
			return &ChannelMessage{}
		},
	}
	bifrost.responseChannelPool = sync.Pool{
		New: func() interface{} {
			bifrost.responseChannelCreations.Add(1)
			return make(chan *interfaces.BifrostResponse, 1)
		},
	}
	bifrost.errorChannelPool = sync.Pool{
		New: func() interface{} {
			bifrost.errorChannelCreations.Add(1)
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

	if config.Logger == nil {
		config.Logger = NewDefaultLogger(interfaces.LogLevelInfo)
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
			bifrost.logger.Warn(fmt.Sprintf("failed to prepare provider: %v", err))
		}
	}

	return bifrost, nil
}

// getChannelMessage gets a ChannelMessage from the pool
func (bifrost *Bifrost) getChannelMessage(req interfaces.BifrostRequest, reqType RequestType) *ChannelMessage {
	// Get channels from pool
	bifrost.responseChannelGets.Add(1)
	responseChan := bifrost.responseChannelPool.Get().(chan *interfaces.BifrostResponse)

	bifrost.errorChannelGets.Add(1)
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
	bifrost.channelMessageGets.Add(1)
	msg := bifrost.channelMessagePool.Get().(*ChannelMessage)
	msg.BifrostRequest = req
	msg.Response = responseChan
	msg.Err = errorChan
	msg.Type = reqType
	msg.Timestamp = time.Now()

	return msg
}

// releaseChannelMessage returns a ChannelMessage and its channels to the pool
func (bifrost *Bifrost) releaseChannelMessage(msg *ChannelMessage) {
	// Put channels back in pools
	bifrost.responseChannelPuts.Add(1)
	bifrost.responseChannelPool.Put(msg.Response)

	bifrost.errorChannelPuts.Add(1)
	bifrost.errorChannelPool.Put(msg.Err)

	// Clear references and return to pool
	msg.Response = nil
	msg.Err = nil
	bifrost.channelMessagePuts.Add(1)
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

func (bifrost *Bifrost) recordError(queueWaitTime, keySelectTime, providerTime, pluginPreTime, pluginPostTime time.Duration) {
	bifrost.metricsMutex.Lock()
	defer bifrost.metricsMutex.Unlock()

	atomic.AddInt64(&bifrost.metrics.RequestCount, 1)
	atomic.AddInt64(&bifrost.metrics.ErrorCount, 1)

	totalTime := queueWaitTime + keySelectTime + providerTime + pluginPreTime + pluginPostTime
	bifrost.metrics.QueueWaitTime = (bifrost.metrics.QueueWaitTime*time.Duration(bifrost.metrics.RequestCount-1) + queueWaitTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.KeySelectionTime = (bifrost.metrics.KeySelectionTime*time.Duration(bifrost.metrics.RequestCount-1) + keySelectTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.ProviderTime = (bifrost.metrics.ProviderTime*time.Duration(bifrost.metrics.RequestCount-1) + providerTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.PluginPreTime = (bifrost.metrics.PluginPreTime*time.Duration(bifrost.metrics.RequestCount-1) + pluginPreTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.PluginPostTime = (bifrost.metrics.PluginPostTime*time.Duration(bifrost.metrics.RequestCount-1) + pluginPostTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.TotalTime = (bifrost.metrics.TotalTime*time.Duration(bifrost.metrics.RequestCount-1) + totalTime) / time.Duration(bifrost.metrics.RequestCount)
}

func (bifrost *Bifrost) processRequests(provider interfaces.Provider, queue chan ChannelMessage) {
	defer bifrost.waitGroups[provider.GetProviderKey()].Done()

	for req := range queue {
		startTime := time.Now()
		queueWaitTime := startTime.Sub(req.Timestamp)

		var result *interfaces.BifrostResponse
		var bifrostError *interfaces.BifrostError

		// Track key selection time
		keySelectStart := time.Now()
		key, err := bifrost.SelectKeyFromProviderForModel(provider.GetProviderKey(), req.Model)
		keySelectTime := time.Since(keySelectStart)

		if err != nil {
			bifrost.recordError(queueWaitTime, keySelectTime, 0, 0, 0)
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

		// Track provider processing time
		providerStart := time.Now()

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

		providerTime := time.Since(providerStart)

		totalTime := time.Since(startTime)
		bifrost.recordMetrics(queueWaitTime, keySelectTime, providerTime, 0, 0, totalTime, bifrostError == nil)

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

func (bifrost *Bifrost) recordMetrics(queueWaitTime, keySelectTime, providerTime, pluginPreTime, pluginPostTime, totalTime time.Duration, success bool) {
	bifrost.metricsMutex.Lock()
	defer bifrost.metricsMutex.Unlock()

	atomic.AddInt64(&bifrost.metrics.RequestCount, 1)
	if !success {
		atomic.AddInt64(&bifrost.metrics.ErrorCount, 1)
	}

	bifrost.metrics.QueueWaitTime = (bifrost.metrics.QueueWaitTime*time.Duration(bifrost.metrics.RequestCount-1) + queueWaitTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.KeySelectionTime = (bifrost.metrics.KeySelectionTime*time.Duration(bifrost.metrics.RequestCount-1) + keySelectTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.ProviderTime = (bifrost.metrics.ProviderTime*time.Duration(bifrost.metrics.RequestCount-1) + providerTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.PluginPreTime = (bifrost.metrics.PluginPreTime*time.Duration(bifrost.metrics.RequestCount-1) + pluginPreTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.PluginPostTime = (bifrost.metrics.PluginPostTime*time.Duration(bifrost.metrics.RequestCount-1) + pluginPostTime) / time.Duration(bifrost.metrics.RequestCount)
	bifrost.metrics.TotalTime = (bifrost.metrics.TotalTime*time.Duration(bifrost.metrics.RequestCount-1) + totalTime) / time.Duration(bifrost.metrics.RequestCount)
}

func (bifrost *Bifrost) GetMetrics() RequestMetrics {
	bifrost.metricsMutex.RLock()
	defer bifrost.metricsMutex.RUnlock()
	return bifrost.metrics
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
	startTime := time.Now()

	if req == nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: "bifrost request cannot be nil",
			},
		}
	}

	// Track queue acquisition time
	queueStart := time.Now()
	queue, err := bifrost.GetProviderQueue(providerKey)
	queueTime := time.Since(queueStart)

	if err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: err.Error(),
			},
		}
	}

	// Track plugin pre-hook time
	pluginPreStart := time.Now()
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
	pluginPreTime := time.Since(pluginPreStart)

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
		// Track plugin post-hook time
		pluginPostStart := time.Now()
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
		pluginPostTime := time.Since(pluginPostStart)
		totalTime := time.Since(startTime)
		bifrost.recordMetrics(0, 0, 0, pluginPreTime, pluginPostTime, totalTime, true)

	case err := <-msg.Err:
		bifrost.releaseChannelMessage(msg)
		totalTime := time.Since(startTime)
		bifrost.recordMetrics(queueTime, 0, 0, pluginPreTime, 0, totalTime, false)
		return nil, &err
	}

	// Add bifrost metrics to the response
	if rawResponse, ok := result.ExtraFields.RawResponse.(map[string]interface{}); ok {
		rawResponse["bifrost_timings"] = bifrost.GetMetrics()
		result.ExtraFields.RawResponse = rawResponse
	}

	// Return message to pool
	bifrost.releaseChannelMessage(msg)
	return result, nil
}

func (bifrost *Bifrost) ChatCompletionRequest(providerKey interfaces.SupportedModelProvider, req *interfaces.BifrostRequest, ctx context.Context) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	startTime := time.Now()

	if req == nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: "bifrost request cannot be nil",
			},
		}
	}

	// Track queue acquisition time
	queueStart := time.Now()
	queue, err := bifrost.GetProviderQueue(providerKey)
	queueTime := time.Since(queueStart)

	if err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: err.Error(),
			},
		}
	}

	// Track plugin pre-hook time
	pluginPreStart := time.Now()
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
	pluginPreTime := time.Since(pluginPreStart)

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
		// Track plugin post-hook time
		pluginPostStart := time.Now()
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
		pluginPostTime := time.Since(pluginPostStart)
		totalTime := time.Since(startTime)
		bifrost.recordMetrics(0, 0, 0, pluginPreTime, pluginPostTime, totalTime, true)

	case err := <-msg.Err:
		bifrost.releaseChannelMessage(msg)
		totalTime := time.Since(startTime)
		bifrost.recordMetrics(queueTime, 0, 0, pluginPreTime, 0, totalTime, false)
		return nil, &err
	}

	// Add bifrost metrics to the response
	if rawResponse, ok := result.ExtraFields.RawResponse.(map[string]interface{}); ok {
		rawResponse["bifrost_timings"] = bifrost.GetMetrics()
		result.ExtraFields.RawResponse = rawResponse
	}

	// Return message to pool
	bifrost.releaseChannelMessage(msg)
	return result, nil
}

// GetAllStats returns all statistics including request metrics and pool usage
func (bifrost *Bifrost) GetAllStats() map[string]interface{} {
	stats := make(map[string]interface{})

	// Add request metrics
	metrics := bifrost.GetMetrics()
	stats["request_metrics"] = map[string]interface{}{
		"total_time":         metrics.TotalTime.String(),
		"queue_wait_time":    metrics.QueueWaitTime.String(),
		"key_selection_time": metrics.KeySelectionTime.String(),
		"provider_time":      metrics.ProviderTime.String(),
		"plugin_pre_time":    metrics.PluginPreTime.String(),
		"plugin_post_time":   metrics.PluginPostTime.String(),
		"request_count":      metrics.RequestCount,
		"error_count":        metrics.ErrorCount,
		"error_rate":         fmt.Sprintf("%.2f%%", float64(metrics.ErrorCount)/float64(metrics.RequestCount)*100),
	}

	// Add pool usage statistics
	stats["pool_stats"] = bifrost.GetPoolStats()

	return stats
}

// GetPoolStats returns statistics about object pool usage
func (bifrost *Bifrost) GetPoolStats() map[string]interface{} {
	stats := make(map[string]interface{})

	// Add channel message pool stats
	stats["channel_message_pool"] = map[string]int64{
		"gets":      bifrost.channelMessageGets.Load(),
		"puts":      bifrost.channelMessagePuts.Load(),
		"creations": bifrost.channelMessageCreations.Load(),
	}

	// Add response channel pool stats
	stats["response_channel_pool"] = map[string]int64{
		"gets":      bifrost.responseChannelGets.Load(),
		"puts":      bifrost.responseChannelPuts.Load(),
		"creations": bifrost.responseChannelCreations.Load(),
	}

	// Add error channel pool stats
	stats["error_channel_pool"] = map[string]int64{
		"gets":      bifrost.errorChannelGets.Load(),
		"puts":      bifrost.errorChannelPuts.Load(),
		"creations": bifrost.errorChannelCreations.Load(),
	}

	// Add provider-specific pool stats
	providerStats := providers.GetPoolStats()
	for k, v := range providerStats {
		stats[k] = v
	}

	return stats
}

// Shutdown gracefully stops all workers when triggered
func (bifrost *Bifrost) Shutdown() {
	bifrost.logger.Info("[BIFROST] Graceful Shutdown Initiated - Closing all request channels...")

	stats := bifrost.GetAllStats()
	statsJSON, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		bifrost.logger.Info(fmt.Sprintf("[BIFROST] Stats collection failed: %v", err))
	} else {
		bifrost.logger.Info(fmt.Sprintf("[BIFROST] Statistics:\n%s", statsJSON))
	}

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
