package circuitbreaker

import (
	"fmt"
	"sync/atomic"
	"time"

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

// NewDefaultCircuitBreakerPlugin creates a circuit breaker plugin with default configuration
func NewDefaultCircuitBreakerPlugin() *CircuitBreaker {
	cb, err := NewCircuitBreakerPlugin(DefaultConfig())
	if err != nil {
		// This should never happen with default config, but if it does, panic
		panic(fmt.Sprintf("failed to create circuit breaker with default config: %v", err))
	}
	return cb
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

	return validated, appliedDefaults
}

// GetState returns the current circuit breaker state for a provider
func (p *CircuitBreaker) GetState(provider schemas.ModelProvider) (CircuitState, bool) {
	state := p.getOrCreateProviderState(provider)
	currentState := CircuitState(atomic.LoadInt32(&state.state))
	return currentState, true
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
