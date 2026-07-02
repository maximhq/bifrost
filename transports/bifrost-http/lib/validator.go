// Package lib provides core functionality for the Bifrost HTTP service.
// This file contains JSON schema validation for config files.
package lib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	DefaultConfigSchemaURL   = "https://www.getbifrost.ai/schema"
	ConfigSchemaURLEnv       = "BIFROST_SCHEMA_URL"
	LegacyConfigSchemaURLEnv = "SCHEMA_URL"
)

func ConfigSchemaURL() string {
	if schemaLocation := configuredConfigSchemaLocation(); schemaLocation != "" {
		return schemaLocation
	}
	return DefaultConfigSchemaURL
}

func configuredConfigSchemaLocation() string {
	for _, envName := range []string{ConfigSchemaURLEnv, LegacyConfigSchemaURLEnv} {
		if schemaLocation := strings.TrimSpace(os.Getenv(envName)); schemaLocation != "" {
			return schemaLocation
		}
	}
	return ""
}

func loadSchemaFromLocation(location string) ([]byte, error) {
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		resp, err := http.Get(location)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	}

	path := location
	if strings.HasPrefix(location, "file://") {
		path = strings.TrimPrefix(location, "file://")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Clean(path)
	}
	return os.ReadFile(path)
}

func schemaLocationFromConfig(data []byte) string {
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return ""
	}
	schemaLocation, ok := config["$schema"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(schemaLocation)
}

// localSchemaCandidates lists paths (relative to CWD) where config.schema.json may be found
// when running from a source checkout. Checked in order before falling back to the remote URL.
var localSchemaCandidates = []string{
	"config.schema.json",            // running from transports/
	"../config.schema.json",         // running from transports/bifrost-http/
	"../../config.schema.json",      // running from transports/bifrost-http/lib/
	"transports/config.schema.json", // running from repo root
}

// tryLoadLocalSchema attempts to read config.schema.json from known local paths.
// Returns nil if none are found.
func tryLoadLocalSchema() []byte {
	for _, p := range localSchemaCandidates {
		data, err := os.ReadFile(p)
		if err == nil {
			return data
		}
	}
	return nil
}

// ValidateConfigSchema validates config data against the JSON schema.
// Returns nil if valid, or a formatted error describing all validation failures.
// An optional schemaOverride can be provided to use a local schema instead of loading from the configured location.
func ValidateConfigSchema(data []byte, schemaOverride ...[]byte) error {
	var configSchemaJSONBytes []byte
	if len(schemaOverride) > 0 && len(schemaOverride[0]) > 0 {
		configSchemaJSONBytes = schemaOverride[0]
	} else if schemaLocation := configuredConfigSchemaLocation(); schemaLocation != "" {
		var err error
		configSchemaJSONBytes, err = loadSchemaFromLocation(schemaLocation)
		if err != nil {
			return fmt.Errorf("failed to get config schema from %s: %w", schemaLocation, err)
		}
	} else if schemaLocation := schemaLocationFromConfig(data); schemaLocation != "" && schemaLocation != DefaultConfigSchemaURL {
		var err error
		configSchemaJSONBytes, err = loadSchemaFromLocation(schemaLocation)
		if err != nil {
			return fmt.Errorf("failed to get config schema from %s: %w", schemaLocation, err)
		}
	} else if localSchema := tryLoadLocalSchema(); localSchema != nil {
		// Prefer the local schema file from the source checkout when available.
		// This avoids validating against a potentially stale remote schema.
		configSchemaJSONBytes = localSchema
	} else {
		schemaURL := ConfigSchemaURL()
		var err error
		configSchemaJSONBytes, err = loadSchemaFromLocation(schemaURL)
		if err != nil {
			return fmt.Errorf("failed to get config schema from %s: %w", schemaURL, err)
		}
	}
	// Parse the schema JSON
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(configSchemaJSONBytes))
	if err != nil {
		return fmt.Errorf("failed to parse config schema JSON: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("config.schema.json", schemaDoc); err != nil {
		return fmt.Errorf("failed to add config schema resource: %w", err)
	}
	// Compile the schema
	compiledSchema, err := c.Compile("config.schema.json")
	if err != nil {
		return fmt.Errorf("failed to compile config schema: %w", err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	err = compiledSchema.Validate(v)
	if err == nil {
		return nil
	}
	// Format validation errors for better readability
	return formatValidationError(err)
}

// formatValidationError converts jsonschema validation errors into user-friendly messages
func formatValidationError(err error) error {
	validationErr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err
	}

	// Use the GoString format which provides detailed hierarchical output
	return fmt.Errorf("schema validation failed:\n%s", validationErr.GoString())
}
