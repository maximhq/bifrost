package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// testLogger is a minimal logger for tests that implements schemas.Logger.
type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// validTestCertPEM returns a minimal valid PEM-encoded CA certificate for testing.
func validTestCertPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	block := &pem.Block{Type: "CERTIFICATE", Bytes: certDER}
	return string(pem.EncodeToMemory(block))
}

func TestConfigureTLS_ReturnsUnchangedWhenNeitherSet(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}

	result := ConfigureTLS(client, schemas.NetworkConfig{}, logger)

	if result != client {
		t.Error("ConfigureTLS should return the same client when neither InsecureSkipVerify nor CACertPEM is set")
	}
	if client.TLSConfig != nil {
		t.Error("TLSConfig should remain nil when no TLS options are set")
	}
}

func TestConfigureTLS_SetsInsecureSkipVerify(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}

	result := ConfigureTLS(client, schemas.NetworkConfig{InsecureSkipVerify: true}, logger)

	if result != client {
		t.Error("ConfigureTLS should return the same client")
	}
	if client.TLSConfig == nil {
		t.Fatal("TLSConfig should be set when InsecureSkipVerify is true")
	}
	if !client.TLSConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true")
	}
}

func TestConfigureTLS_AppliesCACertPEM(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}
	caPEM := validTestCertPEM(t)

	result := ConfigureTLS(client, schemas.NetworkConfig{CACertPEM: caPEM}, logger)

	if result != client {
		t.Error("ConfigureTLS should return the same client")
	}
	if client.TLSConfig == nil {
		t.Fatal("TLSConfig should be set when CACertPEM is provided")
	}
	if client.TLSConfig.RootCAs == nil {
		t.Error("RootCAs should be set when CACertPEM is provided")
	}
}

func TestConfigureTLS_HandlesInvalidCACertPEM(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}

	result := ConfigureTLS(client, schemas.NetworkConfig{CACertPEM: "not-valid-pem"}, logger)

	if result != client {
		t.Error("ConfigureTLS should return the same client even when CACertPEM is invalid")
	}
	// Invalid PEM logs warning and skips RootCAs; TLSConfig may still be set with MinVersion
	if client.TLSConfig != nil && client.TLSConfig.RootCAs != nil {
		t.Error("RootCAs should not be set when CACertPEM is invalid")
	}
}

func TestConfigureTLS_MergesWithExistingTLSConfig(t *testing.T) {
	// Simulate client that already has TLSConfig from ConfigureProxy
	existingRootCAs, _ := x509.SystemCertPool()
	if existingRootCAs == nil {
		existingRootCAs = x509.NewCertPool()
	}
	client := &fasthttp.Client{
		TLSConfig: &tls.Config{
			RootCAs:    existingRootCAs,
			MinVersion: tls.VersionTLS12,
		},
	}
	logger := testLogger{}
	caPEM := validTestCertPEM(t)

	result := ConfigureTLS(client, schemas.NetworkConfig{CACertPEM: caPEM}, logger)

	if result != client {
		t.Error("ConfigureTLS should return the same client")
	}
	if client.TLSConfig == nil {
		t.Fatal("TLSConfig should remain set")
	}
	if client.TLSConfig.RootCAs == nil {
		t.Error("RootCAs should be set (merged with existing)")
	}
}

func TestConfigureTLS_InsecureSkipVerifyAndCACertPEM(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}
	caPEM := validTestCertPEM(t)

	result := ConfigureTLS(client, schemas.NetworkConfig{
		InsecureSkipVerify: true,
		CACertPEM:          caPEM,
	}, logger)

	if result != client {
		t.Error("ConfigureTLS should return the same client")
	}
	if client.TLSConfig == nil {
		t.Fatal("TLSConfig should be set")
	}
	if !client.TLSConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true when both options are set")
	}
	if client.TLSConfig.RootCAs == nil {
		t.Error("RootCAs should be set when CACertPEM is provided")
	}
}
