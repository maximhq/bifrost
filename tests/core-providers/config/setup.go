// Package main provides comprehensive test utilities and configurations for the Bifrost system.
// It includes comprehensive test implementations covering all major AI provider scenarios,
// including text completion, chat, tool calling, image processing, and end-to-end workflows.
package config

import (
	"context"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// getBifrost initializes and returns a Bifrost instance for comprehensive testing.
// It sets up the comprehensive test account, plugin, and logger configuration.
//
// Returns:
//   - *bifrost.Bifrost: A configured Bifrost instance ready for comprehensive testing
//   - error: Any error that occurred during Bifrost initialization
//
// The function:
//  1. Loads environment variables
//  2. Creates a comprehensive test account instance
//  3. Configures Bifrost with the account and default logger
func getBifrost() (*bifrost.Bifrost, error) {
	account := ComprehensiveTestAccount{}

	// Initialize Bifrost
	b, err := bifrost.Init(schemas.BifrostConfig{
		Account: &account,
		Plugins: nil,
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		return nil, err
	}

	return b, nil
}

// SetupTest initializes a test environment with timeout context
func SetupTest() (*bifrost.Bifrost, context.Context, context.CancelFunc, error) {
	client, err := getBifrost()
	if err != nil {
		return nil, nil, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second) // 5 minutes for comprehensive tests
	return client, ctx, cancel, nil
}
