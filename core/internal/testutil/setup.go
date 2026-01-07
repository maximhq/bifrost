// Package testutil provides comprehensive test utilities and configurations for the Bifrost system.
// It includes comprehensive test implementations covering all major AI provider scenarios,
// including text completion, chat, tool calling, image processing, and end-to-end workflows.
package testutil

import (
	"context"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Constants for test configuration
const (
	// TestTimeout defines the maximum duration for comprehensive tests
	// Set to 20 minutes to allow for complex multi-step operations
	TestTimeout = 20 * time.Minute
)

// getBifrost initializes and returns a Bifrost instance for comprehensive testing.
// It sets up the comprehensive test account, plugin, and logger configuration.
//
// Environment variables are expected to be set by the system or test runner before calling this function.
// The account configuration will read API keys and settings from these environment variables.
//
// Returns:
//   - *bifrost.Bifrost: A configured Bifrost instance ready for comprehensive testing
//   - error: Any error that occurred during Bifrost initialization
//
// The function:
//  1. Creates a comprehensive test account instance
//  2. Configures Bifrost with the account and default logger
func getBifrost(ctx context.Context) (*bifrost.Bifrost, error) {
	account := ComprehensiveTestAccount{}

	// Initialize Bifrost
	b, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account: &account,
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		return nil, err
	}

	return b, nil
}

// SetupTest initializes a test environment with timeout context
func SetupTest() (*bifrost.Bifrost, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	client, err := getBifrost(ctx)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}

	return client, ctx, cancel, nil
}
