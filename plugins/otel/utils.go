package otel

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// injectEnvToHeaders converts any headers that start with "env." with their corresponding environment variable value
// errors out if any environment variable is not found
func injectEnvToHeaders(headers map[string]string) error {
	if headers == nil {
		return nil
	}
	for k, v := range headers {
		if envKey, ok := strings.CutPrefix(v, "env."); ok {
			envVal, found := os.LookupEnv(envKey)
			if !found {
				return fmt.Errorf("environment variable %s not found", envKey)
			}
			headers[k] = envVal
		}
	}
	return nil
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

	// Verify the file exists and is not a symlink
	info, err := os.Lstat(cleanPath)
	if err != nil {
		return fmt.Errorf("TLS CA cert path not accessible: %w", err)
	}
	// Reject symlinks to prevent symlink-based path traversal
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("TLS CA cert path cannot be a symlink: %s", certPath)
	}
	// Ensure path is a regular file, not directories, sockets, pipes, devices, etc.
	if !info.Mode().IsRegular() {
		return fmt.Errorf("TLS CA cert path is not a regular file: %s", certPath)
	}

	return nil
}

// Builds a TLS config with custom CA, insecure mode, or system roots CAs
// - use a custom CA pool if tlsCACert is provided
// - otherwise skip verification if insecureMode is enabled
// - otherwise use the system root CAs
func buildTLSConfig(tlsCACert string, insecureMode bool) (*tls.Config, error) {
	cfg := tls.Config{
		InsecureSkipVerify: false,
		MinVersion:         tls.VersionTLS12,
	}

	// TLS priority: custom CA > system roots > insecure
	if tlsCACert != "" {
		if err := validateCACertPath(tlsCACert); err != nil {
			return nil, err
		}
		caCert, err := os.ReadFile(tlsCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to load provided CA cert: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to add provided CA cert")
		}
		cfg.RootCAs = caCertPool
	} else if insecureMode {
		cfg.InsecureSkipVerify = true // #nosec G402
	}

	return &cfg, nil
}
