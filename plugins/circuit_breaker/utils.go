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

// ValidateConfig validates the circuit breaker configuration
func ValidateConfig(config CircuitBreakerConfig) error {
	if config.FailureRateThreshold < 0 || config.FailureRateThreshold > 1 {
		return fmt.Errorf("failure rate threshold must be between 0 and 1, got %f", config.FailureRateThreshold)
	}
	if config.SlowCallRateThreshold < 0 || config.SlowCallRateThreshold > 1 {
		return fmt.Errorf("slow call rate threshold must be between 0 and 1, got %f", config.SlowCallRateThreshold)
	}
	if config.SlowCallDurationThreshold <= 0 {
		return fmt.Errorf("slow call duration threshold must be positive, got %v", config.SlowCallDurationThreshold)
	}
	if config.MinimumNumberOfCalls <= 0 {
		return fmt.Errorf("minimum number of calls must be positive, got %d", config.MinimumNumberOfCalls)
	}
	if config.SlidingWindowSize <= 0 {
		return fmt.Errorf("sliding window size must be positive, got %d", config.SlidingWindowSize)
	}
	if config.PermittedNumberOfCallsInHalfOpenState <= 0 {
		return fmt.Errorf("permitted number of calls in half-open state must be positive, got %d", config.PermittedNumberOfCallsInHalfOpenState)
	}
	if config.MaxWaitDurationInHalfOpenState <= 0 {
		return fmt.Errorf("max wait duration in half-open state must be positive, got %v", config.MaxWaitDurationInHalfOpenState)
	}

	// Validate sliding window type
	if config.SlidingWindowType != CountBased && config.SlidingWindowType != TimeBased {
		return fmt.Errorf("invalid sliding window type: %s, must be either %s or %s", config.SlidingWindowType, CountBased, TimeBased)
	}
	return nil
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
