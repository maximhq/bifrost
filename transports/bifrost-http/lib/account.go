// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/joho/godotenv"
	"github.com/maximhq/bifrost/core/schemas"
)

// BaseAccount implements the Account interface for Bifrost.
// It manages provider configurations and API keys.
type BaseAccount struct {
	Config ConfigMap  // Map of provider configurations
	mu     sync.Mutex // Mutex to protect Config access
}

// GetConfiguredProviders returns a list of all configured providers.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	baseAccount.mu.Lock()
	defer baseAccount.mu.Unlock()

	providers := make([]schemas.ModelProvider, 0, len(baseAccount.Config))
	for provider := range baseAccount.Config {
		providers = append(providers, provider)
	}
	return providers, nil
}

// GetKeysForProvider returns the API keys configured for a specific provider.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetKeysForProvider(providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	baseAccount.mu.Lock()
	defer baseAccount.mu.Unlock()

	return baseAccount.Config[providerKey].Keys, nil
}

// GetConfigForProvider returns the complete configuration for a specific provider.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	baseAccount.mu.Lock()
	defer baseAccount.mu.Unlock()

	config, exists := baseAccount.Config[providerKey]
	if !exists {
		return nil, errors.New("config for provider not found")
	}

	providerConfig := &schemas.ProviderConfig{}

	if config.NetworkConfig != nil {
		providerConfig.NetworkConfig = *config.NetworkConfig
	}

	if config.MetaConfig != nil {
		providerConfig.MetaConfig = *config.MetaConfig
	}

	if config.ConcurrencyAndBufferSize != nil {
		providerConfig.ConcurrencyAndBufferSize = *config.ConcurrencyAndBufferSize
	}

	return providerConfig, nil
}

// readKeys reads environment variables from a .env file and updates the provider configurations.
// It replaces values starting with "env." in the config with actual values from the environment.
// Returns an error if any required environment variable is missing.
func (baseAccount *BaseAccount) ReadKeys(envLocation string) error {
	envVars, err := godotenv.Read(envLocation)
	if err != nil {
		return fmt.Errorf("failed to read .env file: %w", err)
	}

	// Helper function to check and replace env values
	replaceEnvValue := func(value string) (string, error) {
		if strings.HasPrefix(value, "env.") {
			envKey := strings.TrimPrefix(value, "env.")
			if envValue, exists := envVars[envKey]; exists {
				return envValue, nil
			}
			return "", fmt.Errorf("environment variable %s not found in .env file", envKey)
		}
		return value, nil
	}

	// Helper function to recursively check and replace env values in a struct
	var processStruct func(interface{}) error
	processStruct = func(v interface{}) error {
		val := reflect.ValueOf(v)

		// Dereference pointer if present
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}

		// Handle interface types
		if val.Kind() == reflect.Interface {
			val = val.Elem()
			// If the interface value is a pointer, dereference it
			if val.Kind() == reflect.Ptr {
				val = val.Elem()
			}
		}

		if val.Kind() != reflect.Struct {
			return nil
		}

		typ := val.Type()
		for i := 0; i < val.NumField(); i++ {
			field := val.Field(i)
			fieldType := typ.Field(i)

			// Skip unexported fields
			if !field.CanSet() {
				continue
			}

			switch field.Kind() {
			case reflect.String:
				if field.CanSet() {
					value := field.String()
					if strings.HasPrefix(value, "env.") {
						newValue, err := replaceEnvValue(value)
						if err != nil {
							return fmt.Errorf("field %s: %w", fieldType.Name, err)
						}
						field.SetString(newValue)
					}
				}
			case reflect.Interface:
				if !field.IsNil() {
					if err := processStruct(field.Interface()); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	// Lock the config map for the entire update operation
	baseAccount.mu.Lock()
	defer baseAccount.mu.Unlock()

	// Check and replace values in provider configs
	for provider, config := range baseAccount.Config {
		// Check keys
		for i, key := range config.Keys {
			newValue, err := replaceEnvValue(key.Value)
			if err != nil {
				return fmt.Errorf("provider %s: %w", provider, err)
			}
			config.Keys[i].Value = newValue
		}

		// Check meta config if it exists
		if config.MetaConfig != nil {
			if err := processStruct(config.MetaConfig); err != nil {
				return fmt.Errorf("provider %s: %w", provider, err)
			}
		}

		baseAccount.Config[provider] = config
	}

	return nil
}
