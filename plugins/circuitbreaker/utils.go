package circuitbreaker

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// DefaultConfig returns a default circuit breaker configuration
func DefaultConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureRateThreshold:                  0.5,              // 50% failure rate threshold
		SlowCallRateThreshold:                 0.5,              // 50% slow call rate threshold
		SlowCallDurationThreshold:             5 * time.Second,  // 5 seconds
		MinimumNumberOfCalls:                  10,               // Minimum 10 calls before evaluation
		SlidingWindowType:                     CountBased,       // Count-based sliding window
		SlidingWindowSize:                     100,              // 100 calls window
		PermittedNumberOfCallsInHalfOpenState: 5,                // 5 calls in half-open state
		MaxWaitDurationInHalfOpenState:        60 * time.Second, // 60 seconds wait time
	}
}

// ValidateConfigWithDefaults validates the configuration and returns information about what defaults were applied.
// It returns the validated config and a slice of strings describing what defaults were applied.
func ValidateConfigWithDefaults(config CircuitBreakerConfig) (CircuitBreakerConfig, []string) {
	defaults := DefaultConfig()
	validated := config
	var appliedDefaults []string

	// Apply defaults for invalid values and track what was applied
	if validated.FailureRateThreshold < 0 || validated.FailureRateThreshold > 1 {
		validated.FailureRateThreshold = defaults.FailureRateThreshold
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("failure rate threshold set to default: %f", defaults.FailureRateThreshold))
	}
	if validated.SlowCallRateThreshold < 0 || validated.SlowCallRateThreshold > 1 {
		validated.SlowCallRateThreshold = defaults.SlowCallRateThreshold
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("slow call rate threshold set to default: %f", defaults.SlowCallRateThreshold))
	}
	if validated.SlowCallDurationThreshold <= 0 {
		validated.SlowCallDurationThreshold = defaults.SlowCallDurationThreshold
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("slow call duration threshold set to default: %v", defaults.SlowCallDurationThreshold))
	}
	if validated.MinimumNumberOfCalls <= 0 {
		validated.MinimumNumberOfCalls = defaults.MinimumNumberOfCalls
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("minimum number of calls set to default: %d", defaults.MinimumNumberOfCalls))
	}
	if validated.SlidingWindowSize <= 0 {
		validated.SlidingWindowSize = defaults.SlidingWindowSize
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("sliding window size set to default: %d", defaults.SlidingWindowSize))
	}
	if validated.PermittedNumberOfCallsInHalfOpenState <= 0 {
		validated.PermittedNumberOfCallsInHalfOpenState = defaults.PermittedNumberOfCallsInHalfOpenState
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("permitted calls in half-open state set to default: %d", defaults.PermittedNumberOfCallsInHalfOpenState))
	}
	if validated.MaxWaitDurationInHalfOpenState <= 0 {
		validated.MaxWaitDurationInHalfOpenState = defaults.MaxWaitDurationInHalfOpenState
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("max wait duration in half-open state set to default: %v", defaults.MaxWaitDurationInHalfOpenState))
	}

	// Validate sliding window type
	if validated.SlidingWindowType != CountBased && validated.SlidingWindowType != TimeBased {
		validated.SlidingWindowType = defaults.SlidingWindowType
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("sliding window type set to default: %s", defaults.SlidingWindowType))
	}
	if validated.Logger == nil {
		validated.Logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
		appliedDefaults = append(appliedDefaults, fmt.Sprintf("logger set to default with level: %s", schemas.LogLevelInfo))
	}

	return validated, appliedDefaults
}

// GetState returns the current circuit breaker state for a provider
func (p *CircuitBreaker) GetState(provider schemas.ModelProvider) CircuitState {
	state := p.getOrCreateProviderState(provider)
	currentState := CircuitState(atomic.LoadInt32(&state.state))
	return currentState
}

// ForceOpen forces the circuit breaker to open state for a provider
func (p *CircuitBreaker) ForceOpen(provider schemas.ModelProvider) error {
	state, exists := p.GetProviderState(provider)
	if !exists {
		return fmt.Errorf("provider %s not found", provider)
	}

	p.transitionState(state, StateOpen)
	return nil
}

// ForceClose forces the circuit breaker to closed state for a provider
func (p *CircuitBreaker) ForceClose(provider schemas.ModelProvider) error {
	state, exists := p.GetProviderState(provider)
	if !exists {
		return fmt.Errorf("provider %s not found", provider)
	}

	p.transitionState(state, StateClosed)
	return nil
}

// Reset resets the circuit breaker state for a provider
func (p *CircuitBreaker) Reset(provider schemas.ModelProvider) error {
	state, exists := p.GetProviderState(provider)
	if !exists {
		return fmt.Errorf("provider %s not found", provider)
	}

	// Reset to closed state and clear all metrics
	p.transitionState(state, StateClosed)
	return nil
}

// IsServerError checks if a BifrostError represents a server error (5xx status code)
func IsServerError(bifrostErr *schemas.BifrostError) bool {
	if bifrostErr == nil || bifrostErr.StatusCode == nil {
		return false
	}
	statusCode := *bifrostErr.StatusCode
	return statusCode >= 500 && statusCode < 600
}

// IsRateLimitExceeded checks if a BifrostError represents a "Too Many Requests" (429 status code)
func IsRateLimitExceeded(bifrostErr *schemas.BifrostError) bool {
	if bifrostErr == nil || bifrostErr.StatusCode == nil {
		return false
	}
	statusCode := *bifrostErr.StatusCode
	return statusCode == 429
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
// using periodic cleanup to improve performance for high-frequency calls.
func (w *TimeBasedWindow) RecordCall(result CallResult) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.calls = append(w.calls, result)

	// Trigger cleanup based on conditions:
	// 1. If we've exceeded the maximum calls threshold
	// 2. If we've reached the cleanup threshold and enough time has passed
	// 3. If we've accumulated too many calls
	if len(w.calls) >= w.maxCallsBeforeCleanup ||
		(len(w.calls) >= w.cleanupThreshold && time.Since(w.lastCleanup) > w.windowDuration/4) {
		w.cleanupExpiredEntries()
	}
}

// cleanupExpiredEntries removes expired call results from the sliding window.
func (w *TimeBasedWindow) cleanupExpiredEntries() {
	cutoffTime := time.Now().Add(-w.windowDuration)
	trimIdx := 0

	// Find the first call that's still within the window
	for i, call := range w.calls {
		if call.Timestamp.After(cutoffTime) {
			trimIdx = i
			break
		}
	}

	// Remove expired entries
	if trimIdx > 0 {
		w.calls = w.calls[trimIdx:]
	}

	w.lastCleanup = time.Now()
}


// GetMetrics calculates and returns metrics for the time-based sliding window.
func (w *TimeBasedWindow) GetMetrics() WindowMetrics {
	// Trigger cleanup if needed before calculating metrics
	w.mu.Lock()
	if len(w.calls) > 0 && (len(w.calls) >= w.maxCallsBeforeCleanup || time.Since(w.lastCleanup) > w.windowDuration/2) {
		w.cleanupExpiredEntries()
	}
	w.mu.Unlock()

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
	w.lastCleanup = time.Now()
}

// Utility functions for context extraction
func GetCallStartTime(ctx context.Context) time.Time {
	if startTime, ok := ctx.Value(callStartTimeKey).(time.Time); ok {
		return startTime
	}
	return time.Now() // Fallback
}

func GetCircuitState(ctx context.Context) *ProviderCircuitState {
	if state, ok := ctx.Value(circuitStateKey).(*ProviderCircuitState); ok {
		return state
	}
	return nil
}
