// Package circuitbreaker provides a circuit breaker plugin for the Bifrost system.
// The circuit breaker monitors request failures and slow calls to automatically
// open the circuit when thresholds are exceeded, preventing cascading failures.
package circuitbreaker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

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
	mu             sync.RWMutex
	calls          []CallResult
	windowDuration time.Duration
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
	callStartTimeKey contextKey = "circuit_breaker_call_start_time"
	circuitStateKey  contextKey = "circuit_breaker_circuit_state"
	providerKey      contextKey = "circuit_breaker_provider"
)

// NewCircuitBreakerPlugin creates a new circuit breaker plugin with the given configuration.
// It validates the configuration and returns an error if any parameters are invalid.
func NewCircuitBreakerPlugin(config CircuitBreakerConfig) (*CircuitBreaker, error) {
	if err := ValidateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid circuit breaker configuration: %w", err)
	}

	return &CircuitBreaker{
		config:    config,
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
		return NewCountBasedWindow(p.config.SlidingWindowSize)
	case TimeBased:
		return NewTimeBasedWindow(time.Duration(p.config.SlidingWindowSize) * time.Second)
	default:
		return NewCountBasedWindow(p.config.SlidingWindowSize)
	}
}

// NewCountBasedWindow creates a new count-based sliding window with the specified size.
func NewCountBasedWindow(size int) *CountBasedWindow {
	return &CountBasedWindow{
		calls:    make([]CallResult, size),
		maxSize:  size,
		position: 0,
		full:     false,
	}
}

// NewTimeBasedWindow creates a new time-based sliding window with the specified duration.
func NewTimeBasedWindow(duration time.Duration) *TimeBasedWindow {
	return &TimeBasedWindow{
		calls:          make([]CallResult, 0),
		windowDuration: duration,
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
// or returns an error if the circuit is open or half-open limits are exceeded.
func (p *CircuitBreaker) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.BifrostResponse, error) {
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
			return req, nil, fmt.Errorf("circuit breaker is OPEN for provider %s", provider)
		}
		// Transition to half-open and continue
		p.transitionToHalfOpen(circuitState)
		fallthrough

	case StateHalfOpen:
		// Check if we're within permitted call limit
		if !p.canMakeHalfOpenCall(circuitState) {
			return req, nil, fmt.Errorf("half-open call limit exceeded for provider %s", provider)
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
func (p *CircuitBreaker) PostHook(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// Extract data from context
	callStartTime := getCallStartTime(*ctx)
	circuitState := getCircuitState(*ctx)

	if circuitState == nil {
		// No circuit state found, return as-is
		return resp, bifrostErr, nil
	}

	// Calculate call duration
	callDuration := time.Since(callStartTime)

	// Determine if this is a server error (status code 5xx)
	isServerError := IsServerError(bifrostErr)

	// Determine call result - only count as failure for server errors (5xx)
	// Client errors (4xx) and other errors are considered successful for circuit breaker purposes
	callResult := CallResult{
		Duration:   callDuration,
		Success:    (bifrostErr == nil && resp != nil) || !isServerError,
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

	return resp, bifrostErr, nil
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

// RecordCall adds a new call result to the count-based sliding window.
func (w *CountBasedWindow) RecordCall(result CallResult) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.calls[w.position] = result
	w.position = (w.position + 1) % w.maxSize
	if !w.full && w.position == 0 {
		w.full = true
	}
}

// GetMetrics calculates and returns metrics for the count-based sliding window.
func (w *CountBasedWindow) GetMetrics() WindowMetrics {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var totalCalls, failedCalls, slowCalls int
	callCount := w.maxSize
	if !w.full {
		callCount = w.position
	}

	for i := 0; i < callCount; i++ {
		call := w.calls[i]
		totalCalls++
		if !call.Success {
			failedCalls++
		}
		if call.IsSlowCall {
			slowCalls++
		}
	}

	var failureRate, slowCallRate float64
	if totalCalls > 0 {
		failureRate = float64(failedCalls) / float64(totalCalls)
		slowCallRate = float64(slowCalls) / float64(totalCalls)
	}

	return WindowMetrics{
		TotalCalls:   totalCalls,
		FailedCalls:  failedCalls,
		SlowCalls:    slowCalls,
		FailureRate:  failureRate,
		SlowCallRate: slowCallRate,
	}
}

// Reset clears all call data from the count-based sliding window.
func (w *CountBasedWindow) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.calls = make([]CallResult, w.maxSize)
	w.position = 0
	w.full = false
}

// RecordCall adds a new call result to the time-based sliding window.
func (w *TimeBasedWindow) RecordCall(result CallResult) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.calls = append(w.calls, result)
	cutoffTime := time.Now().Add(-w.windowDuration)
	trimIdx := 0
	for i, call := range w.calls {
		if call.Timestamp.After(cutoffTime) {
			trimIdx = i
			break
		}
	}
	w.calls = w.calls[trimIdx:]
}

// GetMetrics calculates and returns metrics for the time-based sliding window.
func (w *TimeBasedWindow) GetMetrics() WindowMetrics {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var totalCalls, failedCalls, slowCalls int
	cutoffTime := time.Now().Add(-w.windowDuration)

	for _, call := range w.calls {
		if call.Timestamp.After(cutoffTime) {
			totalCalls++
			if !call.Success {
				failedCalls++
			}
			if call.IsSlowCall {
				slowCalls++
			}
		}
	}

	var failureRate, slowCallRate float64
	if totalCalls > 0 {
		failureRate = float64(failedCalls) / float64(totalCalls)
		slowCallRate = float64(slowCalls) / float64(totalCalls)
	}

	return WindowMetrics{
		TotalCalls:   totalCalls,
		FailedCalls:  failedCalls,
		SlowCalls:    slowCalls,
		FailureRate:  failureRate,
		SlowCallRate: slowCallRate,
	}
}

// Reset clears all call data from the time-based sliding window.
func (w *TimeBasedWindow) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.calls = make([]CallResult, 0)
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

// Utility functions for context extraction
func getCallStartTime(ctx context.Context) time.Time {
	if startTime, ok := ctx.Value(callStartTimeKey).(time.Time); ok {
		return startTime
	}
	return time.Now() // Fallback
}

func getCircuitState(ctx context.Context) *ProviderCircuitState {
	if state, ok := ctx.Value(circuitStateKey).(*ProviderCircuitState); ok {
		return state
	}
	return nil
}
