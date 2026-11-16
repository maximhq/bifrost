package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPermanentConnectErrorsAreNotRetried: failures no retry can fix are
// permanent for the dial and for the checker's probe, through the wrapping they
// arrive in: a misconfigured client, an upstream speaking a protocol version
// this client does not support, and a server certificate this client does not
// trust. A refused connection stays retryable.
func TestPermanentConnectErrorsAreNotRetried(t *testing.T) {
	untrusted := &url.Error{Op: "Post", URL: "https://upstream.example/mcp", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}
	for name, err := range map[string]error{
		"misconfigured":         misconfiguredError{errors.New("environment variable X is not set for MCP client c")},
		"wrapped misconfigured": fmt.Errorf("failed to start transport: %w", misconfiguredError{errors.New("InProcess connection requires a server instance")}),
		"unsupported version":   mcp.UnsupportedProtocolVersionError{Version: "1999-01-01"},
		"untrusted certificate": transport.NewError(fmt.Errorf("failed to send request: %w", untrusted)),
	} {
		t.Run(name, func(t *testing.T) {
			if isTransientError(err) {
				t.Errorf("isTransientError(%q) = true, want false", err)
			}
			if isTransientProbeError(err) {
				t.Errorf("isTransientProbeError(%q) = true, want false", err)
			}
		})
	}
	if !isTransientError(errors.New("dial tcp 127.0.0.1:1: connect: connection refused")) {
		t.Error("a refused connection stays retryable")
	}
}

// TestAddClient_UntrustedServerCertificateFailsAtOnce: a sticky client whose
// server presents a certificate it does not trust fails its create on the
// first attempt, instead of retrying it for about half a minute.
func TestAddClient_UntrustedServerCertificateFailsAtOnce(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	start := time.Now()
	err := m.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:                     "untrusted-cert",
		Name:                   "untrusted_cert",
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(ts.URL),
		AuthType:               schemas.MCPAuthTypeNone,
		NeedsSessionStickiness: schemas.Ptr(true),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate")
	assert.Less(t, time.Since(start), 5*time.Second, "an untrusted certificate is not retried")
}

// TestAddClient_InvalidCACertificateIsRefused: a ca_cert_pem that does not
// parse is refused when the client is added, sticky or per call, before
// anything is dialled. A per-call client used to be accepted and then fail
// every call. The certificate is not checked when insecure_skip_verify is set,
// since every dial ignores it then.
func TestAddClient_InvalidCACertificateIsRefused(t *testing.T) {
	newConfig := func(sticky *bool, insecure bool) *schemas.MCPClientConfig {
		return &schemas.MCPClientConfig{
			ID:                     "bad-ca",
			Name:                   "bad_ca",
			ConnectionType:         schemas.MCPConnectionTypeHTTP,
			ConnectionString:       schemas.NewSecretVar("https://127.0.0.1:1/mcp"),
			AuthType:               schemas.MCPAuthTypeNone,
			NeedsSessionStickiness: sticky,
			TLSConfig:              &schemas.MCPTLSConfig{InsecureSkipVerify: insecure, CACertPEM: schemas.NewSecretVar("not-a-pem")},
		}
	}
	for name, sticky := range map[string]*bool{"sticky": schemas.Ptr(true), "per call": nil} {
		t.Run(name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			t.Cleanup(func() { _ = m.Cleanup() })
			start := time.Now()
			err := m.AddClient(context.Background(), newConfig(sticky, false))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "tls_config.ca_cert_pem is not a valid PEM certificate")
			assert.Less(t, time.Since(start), time.Second, "refused before any dial")
			_, registered := snapshotAddClientState(m, "bad-ca")
			assert.False(t, registered, "a refused client is not registered")
		})
	}
	t.Run("ignored with insecure_skip_verify", func(t *testing.T) {
		m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
		t.Cleanup(func() { _ = m.Cleanup() })
		require.NoError(t, m.AddClient(context.Background(), newConfig(nil, true)))
	})
}

// TestUpdateClient_InvalidCACertificateIsRefused: an edit that sets a
// ca_cert_pem that does not parse is refused and leaves the client as it was.
func TestUpdateClient_InvalidCACertificateIsRefused(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID:               "edit-bad-ca",
		Name:             "edit_bad_ca",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://127.0.0.1:1/mcp"),
		AuthType:         schemas.MCPAuthTypeNone,
	}
	require.NoError(t, m.AddClient(context.Background(), config))

	edited := *config
	edited.TLSConfig = &schemas.MCPTLSConfig{CACertPEM: schemas.NewSecretVar("not-a-pem")}
	err := m.UpdateClient(config.ID, &edited)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tls_config.ca_cert_pem is not a valid PEM certificate")

	state, ok := snapshotAddClientState(m, config.ID)
	require.True(t, ok)
	assert.Nil(t, state.ExecutionConfig.TLSConfig, "the refused edit changed nothing")
}
