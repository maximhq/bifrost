package circuitbreaker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestCircuitBreakerBasicFunctionality tests basic circuit breaker operations
func TestCircuitBreakerBasicFunctionality(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureRateThreshold:                  0.5,
		SlowCallRateThreshold:                 0.5,
		SlowCallDurationThreshold:             100 * time.Millisecond,
		MinimumNumberOfCalls:                  5,
		SlidingWindowType:                     CountBased,
		SlidingWindowSize:                     10,
		PermittedNumberOfCallsInHalfOpenState: 2,
		MaxWaitDurationInHalfOpenState:        1 * time.Second,
	}

	cb, err := NewCircuitBreakerPlugin(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Test initial state
	if cb.GetName() != PluginName {
		t.Errorf("Expected plugin name %s, got %s", PluginName, cb.GetName())
	}

	// Test default config
	defaultCB := NewDefaultCircuitBreakerPlugin()
	if defaultCB == nil {
		t.Error("Default circuit breaker should not be nil")
	}
}

// TestCircuitBreakerStateTransitions tests state transitions from closed to open to half-open
func TestCircuitBreakerStateTransitions(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureRateThreshold:                  0.5,
		SlowCallRateThreshold:                 0.5,
		SlowCallDurationThreshold:             100 * time.Millisecond,
		MinimumNumberOfCalls:                  5,
		SlidingWindowType:                     CountBased,
		SlidingWindowSize:                     10,
		PermittedNumberOfCallsInHalfOpenState: 2,
		MaxWaitDurationInHalfOpenState:        100 * time.Millisecond, // Short for testing
	}

	cb, err := NewCircuitBreakerPlugin(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}
	provider := schemas.Ollama

	// Clean up any existing state
	cb.Cleanup()

	// Test initial state should be closed
	state, exists := cb.GetState(provider)
	if !exists {
		t.Error("Provider state should exist after first access")
	}
	if state != StateClosed {
		t.Errorf("Expected initial state CLOSED, got %s", state)
	}

	// Simulate failures to trigger open state
	for i := 0; i < 5; i++ {
		ctx := context.Background()
		req := &schemas.BifrostRequest{
			Provider: provider,
			Model:    "test-model",
		}

		// PreHook should succeed
		_, _, err := cb.PreHook(&ctx, req)
		if err != nil {
			t.Errorf("PreHook failed on iteration %d: %v", i, err)
		}

		// PostHook with server error (5xx)
		serverError := &schemas.BifrostError{
			StatusCode: &[]int{500}[0],
			Error: schemas.ErrorField{
				Message: "Internal Server Error",
			},
		}

		_, _, err = cb.PostHook(&ctx, nil, serverError)
		if err != nil {
			t.Errorf("PostHook failed on iteration %d: %v", i, err)
		}

		// Check state after each iteration
		currentState, _ := cb.GetState(provider)
		t.Logf("State after iteration %d: %s", i, currentState)
	}

	// Make one more failure to ensure circuit is open
	ctx := context.Background()
	req := &schemas.BifrostRequest{
		Provider: provider,
		Model:    "test-model",
	}

	// This PreHook should fail because circuit is now open
	_, shortCircuit, err := cb.PreHook(&ctx, req)
	if err != nil {
		t.Errorf("PreHook should not return error, got: %v", err)
	}
	if shortCircuit == nil {
		t.Error("Expected PreHook to return PluginShortCircuit when circuit is open")
	} else if shortCircuit.Error == nil {
		t.Error("Expected PluginShortCircuit to contain error when circuit is open")
	} else {
		t.Logf("Got expected PluginShortCircuit when circuit is open: %v", shortCircuit.Error.Error.Message)
	}

	// Check if circuit is now open
	state, _ = cb.GetState(provider)
	if state != StateOpen {
		t.Errorf("Expected state OPEN after failures, got %s", state)
	}

	// Test that requests are blocked when circuit is open (immediately after opening)
	ctx = context.Background()
	req = &schemas.BifrostRequest{
		Provider: provider,
		Model:    "test-model",
	}

	// Check state before trying to make request
	stateBefore, _ := cb.GetState(provider)
	t.Logf("State before blocked request: %s", stateBefore)

	_, shortCircuit, err = cb.PreHook(&ctx, req)
	if err != nil {
		t.Errorf("PreHook should not return error, got: %v", err)
	}
	if shortCircuit == nil {
		t.Error("Expected PluginShortCircuit when circuit is open")
	} else if shortCircuit.Error == nil {
		t.Error("Expected PluginShortCircuit to contain error when circuit is open")
	} else {
		t.Logf("Got expected PluginShortCircuit when circuit is open: %v", shortCircuit.Error.Error.Message)
	}

	// Wait for half-open transition
	time.Sleep(150 * time.Millisecond)

	// Test half-open state - should succeed now
	_, shortCircuit, err = cb.PreHook(&ctx, req)
	if err != nil {
		t.Errorf("PreHook should succeed in half-open state: %v", err)
	}
	if shortCircuit != nil {
		t.Error("Expected no PluginShortCircuit in half-open state when call is permitted")
	}

	// PostHook with success
	_, _, err = cb.PostHook(&ctx, &schemas.BifrostResponse{}, nil)
	if err != nil {
		t.Errorf("PostHook failed: %v", err)
	}

	// Check if circuit is still half-open (need more successful calls to close)
	state, _ = cb.GetState(provider)
	if state != StateHalfOpen {
		t.Errorf("Expected state HALF_OPEN after one successful call, got %s", state)
	}

	// Make one more successful call to close the circuit
	ctx2 := context.Background()
	req2 := &schemas.BifrostRequest{
		Provider: provider,
		Model:    "test-model",
	}

	_, shortCircuit, err = cb.PreHook(&ctx2, req2)
	if err != nil {
		t.Errorf("PreHook should succeed in half-open state: %v", err)
	}
	if shortCircuit != nil {
		t.Error("Expected no PluginShortCircuit in half-open state when call is permitted")
	}

	_, _, err = cb.PostHook(&ctx2, &schemas.BifrostResponse{}, nil)
	if err != nil {
		t.Errorf("PostHook failed: %v", err)
	}

	// Now circuit should be closed
	state, _ = cb.GetState(provider)
	if state != StateClosed {
		t.Errorf("Expected state CLOSED after two successful calls, got %s", state)
	}
}

// TestCircuitBreakerRecovery tests recovery from open state to closed state
func TestCircuitBreakerRecovery(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureRateThreshold:                  0.5,
		SlowCallRateThreshold:                 0.5,
		SlowCallDurationThreshold:             100 * time.Millisecond,
		MinimumNumberOfCalls:                  5,
		SlidingWindowType:                     CountBased,
		SlidingWindowSize:                     10,
		PermittedNumberOfCallsInHalfOpenState: 2,
		MaxWaitDurationInHalfOpenState:        100 * time.Millisecond,
	}

	cb, err := NewCircuitBreakerPlugin(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}
	provider := schemas.Ollama

	// Clean up any existing state
	cb.Cleanup()

	// First, open the circuit
	for i := 0; i < 6; i++ {
		ctx := context.Background()
		req := &schemas.BifrostRequest{
			Provider: provider,
			Model:    "test-model",
		}

		cb.PreHook(&ctx, req)
		serverError := &schemas.BifrostError{
			StatusCode: &[]int{500}[0],
			Error: schemas.ErrorField{
				Message: "Internal Server Error",
			},
		}
		cb.PostHook(&ctx, nil, serverError)
	}

	// Verify circuit is open
	state, _ := cb.GetState(provider)
	if state != StateOpen {
		t.Errorf("Expected state OPEN, got %s", state)
	}

	// Wait for half-open transition
	time.Sleep(150 * time.Millisecond)

	// Make successful calls in half-open state
	for range 2 {
		ctx := context.Background()
		req := &schemas.BifrostRequest{
			Provider: provider,
			Model:    "test-model",
		}

		cb.PreHook(&ctx, req)
		cb.PostHook(&ctx, &schemas.BifrostResponse{}, nil)
	}

	// Verify circuit is now closed
	state, _ = cb.GetState(provider)
	if state != StateClosed {
		t.Errorf("Expected state CLOSED after recovery, got %s", state)
	}
}

// TestCircuitBreakerSlowCalls tests slow call detection
func TestCircuitBreakerSlowCalls(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureRateThreshold:                  0.5,
		SlowCallRateThreshold:                 0.5,
		SlowCallDurationThreshold:             50 * time.Millisecond,
		MinimumNumberOfCalls:                  5,
		SlidingWindowType:                     CountBased,
		SlidingWindowSize:                     10,
		PermittedNumberOfCallsInHalfOpenState: 2,
		MaxWaitDurationInHalfOpenState:        1 * time.Second,
	}

	cb, err := NewCircuitBreakerPlugin(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}
	provider := schemas.Ollama

	// Make slow calls
	for range 6 {
		ctx := context.Background()
		req := &schemas.BifrostRequest{
			Provider: provider,
			Model:    "test-model",
		}

		cb.PreHook(&ctx, req)

		// Simulate slow call by adding delay
		time.Sleep(60 * time.Millisecond)

		cb.PostHook(&ctx, &schemas.BifrostResponse{}, nil)
	}

	// Circuit should be open due to slow calls
	state, _ := cb.GetState(provider)
	if state != StateOpen {
		t.Errorf("Expected state OPEN with slow calls, got %s", state)
	}
}

// TestCircuitBreakerMetrics tests metrics collection
func TestCircuitBreakerMetrics(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureRateThreshold:                  0.5,
		SlowCallRateThreshold:                 0.5,
		SlowCallDurationThreshold:             100 * time.Millisecond,
		MinimumNumberOfCalls:                  5,
		SlidingWindowType:                     CountBased,
		SlidingWindowSize:                     10,
		PermittedNumberOfCallsInHalfOpenState: 2,
		MaxWaitDurationInHalfOpenState:        1 * time.Second,
	}

	cb, err := NewCircuitBreakerPlugin(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}
	provider := schemas.Ollama

	// Make some calls
	for range 3 {
		ctx := context.Background()
		req := &schemas.BifrostRequest{
			Provider: provider,
			Model:    "test-model",
		}

		cb.PreHook(&ctx, req)
		cb.PostHook(&ctx, &schemas.BifrostResponse{}, nil)
	}

	// Make some failures
	for range 2 {
		ctx := context.Background()
		req := &schemas.BifrostRequest{
			Provider: provider,
			Model:    "test-model",
		}

		cb.PreHook(&ctx, req)
		serverError := &schemas.BifrostError{
			StatusCode: &[]int{500}[0],
			Error: schemas.ErrorField{
				Message: "Internal Server Error",
			},
		}
		cb.PostHook(&ctx, nil, serverError)
	}

	// Check metrics
	metrics, err := cb.GetMetrics(provider)
	if err != nil {
		t.Errorf("Failed to get metrics: %v", err)
	}

	if metrics.TotalCalls != 5 {
		t.Errorf("Expected 5 total calls, got %d", metrics.TotalCalls)
	}

	if metrics.FailedCalls != 2 {
		t.Errorf("Expected 2 failed calls, got %d", metrics.FailedCalls)
	}

	expectedFailureRate := 0.4 // 2/5
	if metrics.FailureRate != expectedFailureRate {
		t.Errorf("Expected failure rate %f, got %f", expectedFailureRate, metrics.FailureRate)
	}
}

// TestCircuitBreakerTimeBasedWindow tests time-based sliding window
func TestCircuitBreakerTimeBasedWindow(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureRateThreshold:                  0.5,
		SlowCallRateThreshold:                 0.5,
		SlowCallDurationThreshold:             100 * time.Millisecond,
		MinimumNumberOfCalls:                  5,
		SlidingWindowType:                     TimeBased,
		SlidingWindowSize:                     1, // 1 second window
		PermittedNumberOfCallsInHalfOpenState: 2,
		MaxWaitDurationInHalfOpenState:        1 * time.Second,
	}

	cb, err := NewCircuitBreakerPlugin(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}
	provider := schemas.Ollama

	// Make calls within the time window
	for range 5 {
		ctx := context.Background()
		req := &schemas.BifrostRequest{
			Provider: provider,
			Model:    "test-model",
		}

		cb.PreHook(&ctx, req)
		serverError := &schemas.BifrostError{
			StatusCode: &[]int{500}[0],
			Error: schemas.ErrorField{
				Message: "Internal Server Error",
			},
		}
		cb.PostHook(&ctx, nil, serverError)
	}

	// Circuit should be open
	state, _ := cb.GetState(provider)
	if state != StateOpen {
		t.Errorf("Expected state OPEN with time-based window, got %s", state)
	}

	// Wait for window to expire
	time.Sleep(1000 * time.Millisecond)

	// Check metrics - should be reset
	metrics, err := cb.GetMetrics(provider)
	if err != nil {
		t.Errorf("Failed to get metrics: %v", err)
	}

	fmt.Println("metrics", metrics.FailedCalls)
	if metrics.TotalCalls != 0 {
		t.Errorf("Expected 0 total calls after window expiry, got %d", metrics.TotalCalls)
	}
}
