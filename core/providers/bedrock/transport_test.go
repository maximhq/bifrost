package bedrock

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateTestCACert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "testca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return string(certPEM)
}

func TestBedrockTransportHTTP2Config(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			MaxConnsPerHost:                5000,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)
	require.NotNil(t, provider)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok, "transport should be *http.Transport")

	assert.Equal(t, 5000, transport.MaxConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConns)
	assert.True(t, transport.ForceAttemptHTTP2)
}

func TestBedrockTransportCustomMaxConns(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			MaxConnsPerHost:                50,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)

	assert.Equal(t, 50, transport.MaxConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConns)
}

func TestBedrockTransportDefaultMaxConns(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			// MaxConnsPerHost left as 0 — should default to 5000
		},
	}
	config.CheckAndSetDefaults()

	assert.Equal(t, schemas.DefaultMaxConnsPerHost, config.NetworkConfig.MaxConnsPerHost)

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)

	assert.Equal(t, schemas.DefaultMaxConnsPerHost, transport.MaxConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConns)
}

func TestBedrockTransportTLSInsecureSkipVerify(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			InsecureSkipVerify:             true,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	assert.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	// ForceAttemptHTTP2 should still be true even with custom TLS config
	assert.True(t, transport.ForceAttemptHTTP2)
}

func TestBedrockTransportTLSCACert(t *testing.T) {
	testCACert := generateTestCACert(t)

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			CACertPEM:                      testCACert,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	assert.NotNil(t, transport.TLSClientConfig.RootCAs)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	assert.True(t, transport.ForceAttemptHTTP2)
}

func TestBedrockTransportDefaultTLS(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			// No TLS settings — should use system defaults
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	// No custom TLS config should be set
	assert.Nil(t, transport.TLSClientConfig)
	// EnforceHTTP2 not set — ForceAttemptHTTP2 should be false
	assert.False(t, transport.ForceAttemptHTTP2)
}

func TestBedrockTransportEnforceHTTP2(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.True(t, transport.ForceAttemptHTTP2)
	// TLSNextProto should NOT be set when HTTP/2 is enforced, allowing ALPN negotiation
	assert.Nil(t, transport.TLSNextProto)
}

func TestBedrockTransportEnforceHTTP2Disabled(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			EnforceHTTP2:                   false,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.False(t, transport.ForceAttemptHTTP2)
	// TLSNextProto must be set to empty map to truly disable HTTP/2 ALPN negotiation
	assert.NotNil(t, transport.TLSNextProto)
	assert.Empty(t, transport.TLSNextProto)
}
