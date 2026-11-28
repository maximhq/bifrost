package network

import (
	"crypto/tls"
	"crypto/x509"
	"errors"

	"github.com/maximhq/bifrost/core/schemas"
)

// ErrInvalidMCPCACertPEM is returned when MCPTLSConfig.CACertPEM holds no valid PEM certificate.
var ErrInvalidMCPCACertPEM = errors.New("failed to parse MCP CA certificate PEM")

// BuildMCPTLSConfig constructs the *tls.Config for an MCP client's TLS settings.
// It returns (nil, nil) when tlsCfg is nil or carries neither InsecureSkipVerify
// nor a non-empty CACertPEM. InsecureSkipVerify takes precedence over CACertPEM
// when both are set.
func BuildMCPTLSConfig(tlsCfg *schemas.MCPTLSConfig) (*tls.Config, error) {
	if tlsCfg == nil {
		return nil, nil
	}
	if tlsCfg.InsecureSkipVerify {
		return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, nil
	}
	if tlsCfg.CACertPEM == nil {
		return nil, nil
	}
	caPEM := tlsCfg.CACertPEM.GetValue()
	if caPEM == "" {
		return nil, nil
	}
	rootCAs, err := x509.SystemCertPool()
	if err != nil || rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	if !rootCAs.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, ErrInvalidMCPCACertPEM
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: rootCAs}, nil
}
