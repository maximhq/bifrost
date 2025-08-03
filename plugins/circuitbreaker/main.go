// Package circuitbreaker provides a circuit breaker plugin for the Bifrost system.
// The circuit breaker monitors request failures and slow calls to automatically
// open the circuit when thresholds are exceeded, preventing cascading failures.
//
// Configuration:
// The plugin accepts a CircuitBreakerConfig and automatically applies sensible defaults
// for any invalid or missing configuration values. This makes the plugin robust and
// user-friendly, as it will work even with incomplete or invalid configurations.
//
// Default Configuration Values:
// - FailureRateThreshold: 0.5 (50% failure rate threshold)
// - SlowCallRateThreshold: 0.5 (50% slow call rate threshold)
// - SlowCallDurationThreshold: 5 seconds
// - MinimumNumberOfCalls: 10 (minimum calls before evaluation)
// - SlidingWindowType: "count-based"
// - SlidingWindowSize: 100 (number of calls in window)
// - PermittedNumberOfCallsInHalfOpenState: 5
// - MaxWaitDurationInHalfOpenState: 60 seconds
//
// Usage:
//
//	config := CircuitBreakerConfig{
//	    FailureRateThreshold: 0.3,  // Only valid values need to be specified
//	    // Other values will use defaults
//	}
//	plugin, err := NewCircuitBreakerPlugin(config)
//
// The plugin will log any default values that were applied during initialization.
package circuitbreaker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

const PluginName = "bifrost-circuit-breaker"

// CircuitState represents the current state of a circuit breaker.
type CircuitState int32

const (
	StateClosed CircuitState = iota
	StateOpen
	StateHalfOpen
)

// String returns the string representation of the circuit state
func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// SlidingWindowType defines the type of sliding window used for metrics collection.
type SlidingWindowType string

const (
	CountBased SlidingWindowType = "count-based"
	TimeBased  SlidingWindowType = "time-based"
)

// CallResult tracks the result of a single API call for circuit breaker evaluation.
type CallResult struct {
	Duration   time.Duration // Duration of the call
	Success    bool          // Whether the call was successful
	Timestamp  time.Time     // When the call was made
	IsSlowCall bool          // Whether the call exceeded the slow call threshold
}

// SlidingWindow defines the interface for collecting and analyzing call metrics
// over a sliding window of time or count.
type SlidingWindow interface {
	RecordCall(result CallResult)
	GetMetrics() WindowMetrics
	Reset()
}

// WindowMetrics contains aggregated metrics for the sliding window.
type WindowMetrics struct {
	TotalCalls   int     // Total number of calls in the window
	FailedCalls  int     // Number of failed calls in the window
	SlowCalls    int     // Number of slow calls in the window
	FailureRate  float64 // Failure rate as a percentage (0.0 to 1.0)
	SlowCallRate float64 // Slow call rate as a percentage (0.0 to 1.0)
}

// CountBasedWindow implements a count-based sliding window that maintains
// a fixed number of most recent call results.
type CountBasedWindow struct {
	mu       sync.RWMutex
	calls    []CallResult
	maxSize  int
	position int
	full     bool
}

// TimeBasedWindow implements a time-based sliding window that maintains
// call results within a specified time duration.
type TimeBasedWindow struct {
	mu                    sync.RWMutex
	calls                 []CallResult
	windowDuration        time.Duration
	lastCleanup           time.Time // Last time cleanup was performed
	cleanupThreshold      int       // Number of calls before triggering cleanup
	maxCallsBeforeCleanup int       // Maximum calls to accumulate before forcing cleanup
}

// ProviderCircuitState maintains the circuit breaker state for a specific provider.
// It uses atomic operations for thread-safe state transitions and mutex-protected
// sliding window access.
type ProviderCircuitState struct {
	// Atomic variables for thread-safe access
	state                  int32 // Current circuit state
	stateTransitionTime    int64 // Unix nano timestamp of last state transition
	halfOpenCallsPermitted int32 // Number of calls allowed in half-open state
	halfOpenCallsAttempted int32 // Number of calls attempted in half-open state
	inFlightCalls          int32 // Number of calls currently in progress
	halfOpenSuccesses      int32 // Number of successful calls in half-open state

	// Protected by mutex
	mu            sync.RWMutex
	slidingWindow SlidingWindow
}

// CircuitBreakerConfig contains all configuration parameters for the circuit breaker.
type CircuitBreakerConfig struct {
	FailureRateThreshold                  float64           // Failure rate threshold (0.0 to 1.0)
	SlowCallRateThreshold                 float64           // Slow call rate threshold (0.0 to 1.0)
	SlowCallDurationThreshold             time.Duration     // Duration threshold for slow calls
	MinimumNumberOfCalls                  int               // Minimum calls before evaluation
	SlidingWindowType                     SlidingWindowType // Type of sliding window
	SlidingWindowSize                     int               // Size of sliding window
	PermittedNumberOfCallsInHalfOpenState int               // Calls allowed in half-open state
	MaxWaitDurationInHalfOpenState        time.Duration     // Wait time before half-open transition
	Logger                                schemas.Logger    // Logger for circuit breaker, use default logger if not provided
}

// CircuitBreaker implements the Bifrost plugin interface to provide circuit breaker
// functionality. It maintains separate circuit states for each provider and uses
// sliding windows to track call metrics.
type CircuitBreaker struct {
	config CircuitBreakerConfig

	// Per-provider circuit states
	mu        sync.RWMutex
	providers map[schemas.ModelProvider]*ProviderCircuitState
}

// CircuitBreakerMetrics provides observability data for a circuit breaker instance.
type CircuitBreakerMetrics struct {
	State                  CircuitState // Current circuit state
	FailureRate            float64      // Current failure rate
	SlowCallRate           float64      // Current slow call rate
	TotalCalls             int          // Total calls in window
	FailedCalls            int          // Failed calls in window
	SlowCalls              int          // Slow calls in window
	StateTransitionTime    time.Time    // Time of last state transition
	InFlightCalls          int          // Currently in-flight calls
	HalfOpenCallsAttempted int          // Calls attempted in half-open state
	HalfOpenCallsPermitted int          // Calls permitted in half-open state
	HalfOpenSuccesses      int          // Successful calls in half-open state
}

// Context keys for storing circuit breaker data in request context.
type contextKey string

const (
	callStartTimeKey contextKey = "circuitbreaker_call_start_time"
	circuitStateKey  contextKey = "circuitbreaker_circuit_state"
	providerKey      contextKey = "circuitbreaker_provider"
)

// NewCircuitBreakerPlugin creates a new circuit breaker plugin with the given configuration.
// It validates the configuration and uses default values for any invalid parameters.
func NewCircuitBreakerPlugin(config CircuitBreakerConfig) (*CircuitBreaker, error) {
	// Apply default values for invalid configurations
	validatedConfig, appliedDefaults := ValidateConfigWithDefaults(config)

	// Log any defaults that were applied using the configured logger
	if len(appliedDefaults) > 0 && validatedConfig.Logger != nil {
		validatedConfig.Logger.Info(fmt.Sprintf("Circuit breaker plugin: Applied %d default values", len(appliedDefaults)))
		for _, defaultMsg := range appliedDefaults {
			validatedConfig.Logger.Info(fmt.Sprintf("  - %s", defaultMsg))
		}
	}

	return &CircuitBreaker{
		config:    validatedConfig,
		providers: make(map[schemas.ModelProvider]*ProviderCircuitState),
	}, nil
}

// GetName returns the plugin name for identification.
func (p *CircuitBreaker) GetName() string {
	return PluginName
}

// getOrCreateProviderState retrieves or creates the circuit breaker state for a provider.
// It uses a double-checked locking pattern to ensure thread-safe state creation.
func (p *CircuitBreaker) getOrCreateProviderState(provider schemas.ModelProvider) *ProviderCircuitState {
	// Fast path: Try to get existing state with read lock
	p.mu.RLock()
	if state, exists := p.providers[provider]; exists {
		p.mu.RUnlock()
		return state
	}
	p.mu.RUnlock()

	// Slow path: Provider doesn't exist, need to create it
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check: Another goroutine might have created it while we waited for the lock
	if state, exists := p.providers[provider]; exists {
		return state
	}

	// Create new circuit breaker state for this provider
	newState := &ProviderCircuitState{
		state:                  int32(StateClosed),
		stateTransitionTime:    time.Now().UnixNano(),
		halfOpenCallsPermitted: int32(p.config.PermittedNumberOfCallsInHalfOpenState),
		halfOpenCallsAttempted: 0,
		inFlightCalls:          0,
		halfOpenSuccesses:      0,
		slidingWindow:          p.createSlidingWindow(),
	}

	p.providers[provider] = newState
	return newState
}

// createSlidingWindow creates a new sliding window based on the configuration type.
func (p *CircuitBreaker) createSlidingWindow() SlidingWindow {
	switch p.config.SlidingWindowType {
	case CountBased:
		return newCountBasedWindow(p.config.SlidingWindowSize)
	case TimeBased:
		return newTimeBasedWindow(time.Duration(p.config.SlidingWindowSize) * time.Second)
	default:
		return newCountBasedWindow(p.config.SlidingWindowSize)
	}
}

// NewCountBasedWindow creates a new count-based sliding window with the specified size.
func newCountBasedWindow(size int) *CountBasedWindow {
	return &CountBasedWindow{
		calls:    make([]CallResult, size),
		maxSize:  size,
		position: 0,
		full:     false,
	}
}

// NewTimeBasedWindow creates a new time-based sliding window with the specified duration.
func newTimeBasedWindow(duration time.Duration) *TimeBasedWindow {
	return &TimeBasedWindow{
		calls:                 make([]CallResult, 0),
		windowDuration:        duration,
		lastCleanup:           time.Now(),
		cleanupThreshold:      10,  // Trigger cleanup every 10 calls
		maxCallsBeforeCleanup: 100, // Force cleanup after 100 calls
	}
}

// GetProviderState safely retrieves an existing provider state (read-only).
// Returns the state and a boolean indicating if the provider exists.
func (p *CircuitBreaker) GetProviderState(provider schemas.ModelProvider) (*ProviderCircuitState, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	state, exists := p.providers[provider]
	return state, exists
}

// PreHook implements the Plugin interface and is called before each request.
// It checks the circuit breaker state and either allows the request to proceed
// or short-circuits with an error if the circuit is open or half-open limits are exceeded.
func (p *CircuitBreaker) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("request cannot be nil")
	}

	provider := req.Provider
	circuitState := p.getOrCreateProviderState(provider)

	// Get current state atomically
	currentState := CircuitState(atomic.LoadInt32(&circuitState.state))

	// Handle based on current state
	switch currentState {
	case StateOpen:
		// Check if wait duration has passed
		if !p.shouldTransitionToHalfOpen(circuitState) {
			// Short-circuit with error - allow fallbacks to other providers
			return req, &schemas.PluginShortCircuit{
				Error: &schemas.BifrostError{
					Error: schemas.ErrorField{
						Message: fmt.Sprintf("Service temporarily unavailable: %s circuit breaker is OPEN due to high failure rate (%0.2f%%). Circuit will attempt recovery in %s. Please retry later or use an alternative provider.", provider, p.config.FailureRateThreshold*100, p.config.MaxWaitDurationInHalfOpenState),
						Type:    bifrost.Ptr("circuitbreaker_open"),
					},
				},
			}, nil
		}
		// Transition to half-open and continue
		p.transitionToHalfOpen(circuitState)
		fallthrough

	case StateHalfOpen:
		// Check if we're within permitted call limit
		if !p.canMakeHalfOpenCall(circuitState) {
			// Short-circuit with error - allow fallbacks to other providers
			return req, &schemas.PluginShortCircuit{
				Error: &schemas.BifrostError{
					Error: schemas.ErrorField{
						Message: fmt.Sprintf("Service testing capacity: %s circuit breaker is in HALF_OPEN state with limited capacity (%d/%d calls). Please retry in a moment or use an alternative provider.", provider, atomic.LoadInt32(&circuitState.halfOpenCallsAttempted), atomic.LoadInt32(&circuitState.halfOpenCallsPermitted)),
						Type:    bifrost.Ptr("circuitbreaker_half_open_limit"),
					},
				},
			}, nil
		}

	case StateClosed:
		// Allow request to proceed
	}

	// Increment in-flight counter
	atomic.AddInt32(&circuitState.inFlightCalls, 1)

	// Store call start time and circuit state in context
	*ctx = context.WithValue(*ctx, callStartTimeKey, time.Now())
	*ctx = context.WithValue(*ctx, circuitStateKey, circuitState)
	*ctx = context.WithValue(*ctx, providerKey, provider)

	return req, nil, nil
}

// PostHook implements the Plugin interface and is called after each request.
// It records the call result, updates metrics, and evaluates state transitions
// based on the sliding window metrics.
func (p *CircuitBreaker) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// Extract data from context
	callStartTime := GetCallStartTime(*ctx)
	circuitState := GetCircuitState(*ctx)

	if circuitState == nil {
		// No circuit state found, return as-is
		return result, err, nil
	}

	// Calculate call duration
	callDuration := time.Since(callStartTime)

	// Determine if this is a server error (status code 5xx)
	isServerError := IsServerError(err)
	// Determine if this is a rate limit exceeded error (status code 429)
	isRateLimitExceeded := IsRateLimitExceeded(err)

	// Determine call result - count as failure for server errors (5xx) and rate limit exceeded (429)
	// Client errors (4xx) and other errors are considered successful for circuit breaker purposes
	callResult := CallResult{
		Duration:   callDuration,
		Success:    (err == nil && result != nil) || (!isServerError && !isRateLimitExceeded),
		Timestamp:  callStartTime,
		IsSlowCall: callDuration > p.config.SlowCallDurationThreshold,
	}

	// Record call result in sliding window
	circuitState.mu.Lock()
	circuitState.slidingWindow.RecordCall(callResult)
	metrics := circuitState.slidingWindow.GetMetrics()
	circuitState.mu.Unlock()

	// Evaluate state transition based on current state
	currentState := CircuitState(atomic.LoadInt32(&circuitState.state))
	newState := p.evaluateStateTransition(circuitState, currentState, metrics, callResult)

	// Perform state transition if needed
	if newState != currentState {
		p.transitionState(circuitState, newState)
	}

	// Decrement in-flight counter
	defer atomic.AddInt32(&circuitState.inFlightCalls, -1)

	return result, err, nil
}

// shouldTransitionToHalfOpen checks if enough time has passed to transition from open to half-open.
func (p *CircuitBreaker) shouldTransitionToHalfOpen(state *ProviderCircuitState) bool {
	transitionTime := atomic.LoadInt64(&state.stateTransitionTime)
	waitDuration := p.config.MaxWaitDurationInHalfOpenState
	return time.Since(time.Unix(0, transitionTime)) >= waitDuration
}

// canMakeHalfOpenCall checks if we can make a call in half-open state.
func (p *CircuitBreaker) canMakeHalfOpenCall(state *ProviderCircuitState) bool {
	permitted := atomic.LoadInt32(&state.halfOpenCallsPermitted)
	// Atomically increment attempted counter and check if within permitted
	attempted := atomic.AddInt32(&state.halfOpenCallsAttempted, 1)
	return attempted <= permitted
}

// evaluateStateTransition determines if a state transition should occur based on
// current metrics and the last call result.
func (p *CircuitBreaker) evaluateStateTransition(state *ProviderCircuitState, currentState CircuitState, metrics WindowMetrics, lastCall CallResult) CircuitState {
	switch currentState {
	case StateClosed:
		// Check if failure rate or slow call rate exceeds thresholds
		if metrics.TotalCalls >= p.config.MinimumNumberOfCalls {
			if metrics.FailureRate >= p.config.FailureRateThreshold ||
				metrics.SlowCallRate >= p.config.SlowCallRateThreshold {
				return StateOpen
			}
		}
		return StateClosed

	case StateHalfOpen:
		// If last call failed (server error) or was slow, go back to open
		if !lastCall.Success || lastCall.IsSlowCall {
			return StateOpen
		}

		// If we've made all permitted calls successfully, close circuit
		if lastCall.Success && !lastCall.IsSlowCall {
			atomic.AddInt32(&state.halfOpenSuccesses, 1)
		}
		successes := atomic.LoadInt32(&state.halfOpenSuccesses)
		if successes >= int32(p.config.PermittedNumberOfCallsInHalfOpenState) {
			return StateClosed
		}
		return StateHalfOpen

	case StateOpen:
		// Should only transition via shouldTransitionToHalfOpen check
		return StateOpen
	}

	return currentState
}

// transitionState performs the actual state transition and resets relevant counters.
func (p *CircuitBreaker) transitionState(state *ProviderCircuitState, newState CircuitState) {
	atomic.StoreInt32(&state.state, int32(newState))
	atomic.StoreInt64(&state.stateTransitionTime, time.Now().UnixNano())

	// Reset counters based on new state
	switch newState {
	case StateClosed:
		atomic.StoreInt32(&state.halfOpenCallsAttempted, 0)
		state.mu.Lock()
		state.slidingWindow.Reset()
		state.mu.Unlock()
	case StateOpen:
		atomic.StoreInt32(&state.halfOpenCallsAttempted, 0)
	case StateHalfOpen:
		atomic.StoreInt32(&state.halfOpenCallsAttempted, 0)
	}

	atomic.StoreInt32(&state.halfOpenSuccesses, 0)
}

// transitionToHalfOpen transitions from open to half-open state.
func (p *CircuitBreaker) transitionToHalfOpen(state *ProviderCircuitState) {
	atomic.StoreInt32(&state.state, int32(StateHalfOpen))
	atomic.StoreInt64(&state.stateTransitionTime, time.Now().UnixNano())
	atomic.StoreInt32(&state.halfOpenCallsAttempted, 0)
}

// GetMetrics returns metrics for a specific provider.
func (p *CircuitBreaker) GetMetrics(provider schemas.ModelProvider) (*CircuitBreakerMetrics, error) {
	state, exists := p.GetProviderState(provider)
	if !exists {
		return nil, fmt.Errorf("provider %s not found", provider)
	}

	state.mu.RLock()
	metrics := state.slidingWindow.GetMetrics()
	state.mu.RUnlock()

	return &CircuitBreakerMetrics{
		State:                  CircuitState(atomic.LoadInt32(&state.state)),
		FailureRate:            metrics.FailureRate,
		SlowCallRate:           metrics.SlowCallRate,
		TotalCalls:             metrics.TotalCalls,
		FailedCalls:            metrics.FailedCalls,
		SlowCalls:              metrics.SlowCalls,
		StateTransitionTime:    time.Unix(0, atomic.LoadInt64(&state.stateTransitionTime)),
		InFlightCalls:          int(atomic.LoadInt32(&state.inFlightCalls)),
		HalfOpenCallsAttempted: int(atomic.LoadInt32(&state.halfOpenCallsAttempted)),
		HalfOpenCallsPermitted: int(atomic.LoadInt32(&state.halfOpenCallsPermitted)),
		HalfOpenSuccesses:      int(atomic.LoadInt32(&state.halfOpenSuccesses)),
	}, nil
}

// Cleanup implements the Plugin interface
func (p *CircuitBreaker) Cleanup() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear all provider states
	p.providers = make(map[schemas.ModelProvider]*ProviderCircuitState)
	return nil
}
