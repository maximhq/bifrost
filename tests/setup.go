// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of interfaces, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"fmt"
	"log"
	"os"

	"github.com/maximhq/bifrost"
	"github.com/maximhq/bifrost/interfaces"

	"github.com/joho/godotenv"
	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

// loadEnv loads environment variables from a .env file into the process environment.
// It uses the godotenv package to load variables and fails if the .env file cannot be loaded.
//
// Environment Variables:
//   - .env file: Contains configuration values for the test environment
//
// Returns:
//   - None, but will log.Fatal if the .env file cannot be loaded
func loadEnv() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file:", err)
	}
}

// getPlugin initializes and returns a Plugin instance for testing purposes.
// It sets up the Maxim logger with configuration from environment variables.
//
// Environment Variables:
//   - MAXIM_API_KEY: API key for Maxim SDK authentication
//   - MAXIM_LOGGER_ID: ID for the Maxim logger instance
//
// Returns:
//   - interfaces.Plugin: A configured plugin instance for request/response tracing
//   - error: Any error that occurred during plugin initialization
func getPlugin() (interfaces.Plugin, error) {
	loadEnv()

	// check if Maxim Logger variables are set
	if os.Getenv("MAXIM_API_KEY") == "" {
		return nil, fmt.Errorf("MAXIM_API_KEY is not set, please set it in your .env file or pass nil in the Plugins field when initializing Bifrost")
	}

	if os.Getenv("MAXIM_LOGGER_ID") == "" {
		return nil, fmt.Errorf("MAXIM_LOGGER_ID is not set, please set it in your .env file or pass nil in the Plugins field when initializing Bifrost")
	}

	mx := maxim.Init(&maxim.MaximSDKConfig{ApiKey: os.Getenv("MAXIM_API_KEY")})

	logger, err := mx.GetLogger(&logging.LoggerConfig{Id: os.Getenv("MAXIM_LOGGER_ID")})
	if err != nil {
		return nil, err
	}

	plugin := &Plugin{logger}

	return plugin, nil
}

// getBifrost initializes and returns a Bifrost instance for testing.
// It sets up the test account, plugin, and logger configuration.
//
// Environment Variables:
//   - Uses environment variables loaded by loadEnv()
//
// Returns:
//   - *bifrost.Bifrost: A configured Bifrost instance ready for testing
//   - error: Any error that occurred during Bifrost initialization
//
// The function:
//  1. Loads environment variables
//  2. Creates a test account instance
//  3. Initializes a plugin for request tracing
//  4. Configures Bifrost with the account, plugin, and default logger
func getBifrost() (*bifrost.Bifrost, error) {
	loadEnv()

	account := BaseAccount{}

	// You can pass nil in the Plugins field if you don't want to use the implemented example plugin.
	plugin, err := getPlugin()
	if err != nil {
		fmt.Println("Error setting up the plugin:", err)
		return nil, err
	}

	// Initialize Bifrost
	b, err := bifrost.Init(interfaces.BifrostConfig{
		Account: &account,
		// Plugins: nil,
		Plugins: []interfaces.Plugin{plugin},
		Logger:  bifrost.NewDefaultLogger(interfaces.LogLevelInfo),
	})
	if err != nil {
		return nil, err
	}

	return b, nil
}
