package network

import (
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildMCPTLSConfig(t *testing.T) {
	cfg, err := BuildMCPTLSConfig(nil)
	require.NoError(t, err)
	assert.Nil(t, cfg)

	cfg, err = BuildMCPTLSConfig(&schemas.MCPTLSConfig{})
	require.NoError(t, err)
	assert.Nil(t, cfg)

	cfg, err = BuildMCPTLSConfig(&schemas.MCPTLSConfig{InsecureSkipVerify: true, CACertPEM: schemas.NewSecretVar("not-pem")})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.InsecureSkipVerify)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)

	_, err = BuildMCPTLSConfig(&schemas.MCPTLSConfig{CACertPEM: schemas.NewSecretVar("not-pem")})
	require.ErrorIs(t, err, ErrInvalidMCPCACertPEM)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	cfg, err = BuildMCPTLSConfig(&schemas.MCPTLSConfig{CACertPEM: schemas.NewSecretVar(ca)})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfg.InsecureSkipVerify)
	assert.NotNil(t, cfg.RootCAs)
}
