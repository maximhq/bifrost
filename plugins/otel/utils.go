package otel

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/attribute"
)

// createTLSConfig builds a tls.Config with a custom CA certificate pool loaded from the given path.
func createTLSConfig(caCertPath string, insecure bool) (*tls.Config, error) {
	// TLS priority: custom CA > system roots > insecure
	var tlsConfig *tls.Config
	if caCertPath != "" {
		// Validate the CA cert path to prevent path traversal attacks
		if err := validateCACertPath(caCertPath); err != nil {
			return nil, errors.Wrap(err, "CA cert path validation fails")
		}
		caCert, err := os.ReadFile(caCertPath)
		if err != nil {
			return nil, err
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("invalid cert provided")
		}
		tlsConfig = &tls.Config{
			RootCAs:    caCertPool,
			MinVersion: tls.VersionTLS12,
		}
	} else if insecure {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true, // #nosec G402
			MinVersion:         tls.VersionTLS12,
		}
	} else {
		// Use system root CAs with MinVersion
		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
	return tlsConfig, nil
}

// validateCACertPath validates the CA certificate path to prevent path traversal attacks.
// It ensures the path is absolute, cleaned of traversal sequences, and exists as a regular file.
func validateCACertPath(certPath string) error {
	if certPath == "" {
		return nil
	}

	// Clean the path to resolve any .. or . components
	cleanPath := filepath.Clean(certPath)

	// Require absolute paths to prevent relative path attacks
	if !filepath.IsAbs(cleanPath) {
		return fmt.Errorf("TLS CA cert path must be absolute: %s", certPath)
	}

	// Check that the cleaned path doesn't differ significantly from input
	// (indicates attempted traversal)
	if cleanPath != filepath.Clean(filepath.FromSlash(certPath)) {
		return fmt.Errorf("invalid TLS CA cert path: %s", certPath)
	}

	// Verify the file exists and is not a symlink
	info, err := os.Lstat(cleanPath)
	if err != nil {
		return fmt.Errorf("TLS CA cert path not accessible: %w", err)
	}
	// Reject symlinks to prevent symlink-based path traversal
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("TLS CA cert path cannot be a symlink: %s", certPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("TLS CA cert path is not a regular file: %s", certPath)
	}

	return nil
}

// resolveEnvHeaders resolves header values that use the "env." prefix by substituting
// with the corresponding environment variable. Returns an error if a referenced
// environment variable is empty or not set.
func resolveEnvHeaders(headers map[string]string) error {
	for key, value := range headers {
		if envVar, ok := strings.CutPrefix(value, "env."); ok {
			resolved, exists := os.LookupEnv(envVar)
			if !exists {
				return fmt.Errorf("environment variable %s not found", envVar)
			}
			headers[key] = resolved
		}
	}
	return nil
}

// mapToAttributes transforms a mapping of string keys and values to OTEL attributes
func mapToAttributes(data map[string]string) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	for key, value := range data {
		attrs = append(attrs, attribute.String(key, value))
	}
	return attrs
}
