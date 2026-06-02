package gigachat

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestGigachat(t *testing.T) {
	t.Parallel()

	t.Run("NewProvider", testNewGigaChatProvider)
	t.Run("TrimBaseURL", testNewGigaChatProviderTrimsBaseURL)
	t.Run("UnsupportedOperation", testGigaChatProviderUnsupportedOperation)
	t.Run("ChatCompletion", testGigaChatChatCompletion)
	t.Run("ListModels", testGigaChatListModels)
	t.Run("Embedding", testGigaChatEmbedding)
	t.Run("ResponsesRequestConversion", testGigaChatResponsesRequestConversion)
	t.Run("Responses", testGigaChatResponses)
	t.Run("ResponsesStream", testGigaChatResponsesStream)
	t.Run("Tools", testGigaChatTools)
	t.Run("Errors", testGigaChatErrors)
	t.Run("BuildsTLSClientWithCABundle", testGigaChatBuildsTLSClientWithCABundle)
	t.Run("BuildsTLSClientWithCertificate", testGigaChatBuildsTLSClientWithCertificate)
	t.Run("RejectsMissingCertificatePair", testGigaChatRejectsMissingCertificatePair)
	t.Run("RejectsEncryptedKeyPassword", testGigaChatRejectsEncryptedKeyPassword)
}

func testNewGigaChatProvider(t *testing.T) {
	t.Parallel()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}
	if provider.GetProviderKey() != schemas.GigaChat {
		t.Fatalf("provider key mismatch: got %q, want %q", provider.GetProviderKey(), schemas.GigaChat)
	}
	if provider.networkConfig.BaseURL != gigaChatDefaultBaseURL {
		t.Fatalf("base URL mismatch: got %q, want %q", provider.networkConfig.BaseURL, gigaChatDefaultBaseURL)
	}
	if provider.client == nil {
		t.Fatal("client is nil")
	}
	if provider.streamingClient == nil {
		t.Fatal("streaming client is nil")
	}
}

func testNewGigaChatProviderTrimsBaseURL(t *testing.T) {
	t.Parallel()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: "https://api.giga.chat/v1/",
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	if provider.networkConfig.BaseURL != "https://api.giga.chat/v1" {
		t.Fatalf("base URL mismatch: got %q", provider.networkConfig.BaseURL)
	}
}

func testGigaChatProviderUnsupportedOperation(t *testing.T) {
	t.Parallel()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	response, bifrostErr := provider.TextCompletion(nil, schemas.Key{}, &schemas.BifrostTextCompletionRequest{})
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil {
		t.Fatal("expected unsupported operation error, got nil")
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("unexpected error code: %#v", bifrostErr.Error)
	}
	if bifrostErr.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q, want %q", bifrostErr.ExtraFields.Provider, schemas.GigaChat)
	}
	if bifrostErr.ExtraFields.RequestType != schemas.TextCompletionRequest {
		t.Fatalf("request type mismatch: got %q, want %q", bifrostErr.ExtraFields.RequestType, schemas.TextCompletionRequest)
	}
	if !strings.Contains(bifrostErr.Error.Message, "gigachat provider") {
		t.Fatalf("unexpected error message: %q", bifrostErr.Error.Message)
	}
}

func testGigaChatBuildsTLSClientWithCABundle(t *testing.T) {
	t.Parallel()

	certPEM, _ := generateGigaChatTestCertificate(t)
	caBundleFile := writeGigaChatTestFile(t, "ca.pem", certPEM)

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	client, err := buildGigaChatTLSClient(provider.client, &schemas.GigaChatKeyConfig{CABundleFile: caBundleFile})
	if err != nil {
		t.Fatalf("buildGigaChatTLSClient returned error: %v", err)
	}
	if client == provider.client {
		t.Fatal("expected a cloned client when TLS material is configured")
	}
	if client.TLSConfig == nil || client.TLSConfig.RootCAs == nil {
		t.Fatalf("expected RootCAs to be configured, got %#v", client.TLSConfig)
	}
	if provider.client.TLSConfig != nil && provider.client.TLSConfig.RootCAs != nil {
		t.Fatal("base client TLS config was mutated")
	}
	if client.MaxConnsPerHost != provider.client.MaxConnsPerHost {
		t.Fatalf("MaxConnsPerHost mismatch: got %d, want %d", client.MaxConnsPerHost, provider.client.MaxConnsPerHost)
	}
	if client.ConnPoolStrategy != fasthttp.FIFO {
		t.Fatalf("ConnPoolStrategy mismatch: got %v", client.ConnPoolStrategy)
	}
}

func testGigaChatBuildsTLSClientWithCertificate(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := generateGigaChatTestCertificate(t)
	certFile := writeGigaChatTestFile(t, "client.pem", certPEM)
	keyFile := writeGigaChatTestFile(t, "client.key", keyPEM)

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	client, err := buildGigaChatTLSClient(provider.client, &schemas.GigaChatKeyConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	if err != nil {
		t.Fatalf("buildGigaChatTLSClient returned error: %v", err)
	}
	if client.TLSConfig == nil || len(client.TLSConfig.Certificates) != 1 {
		t.Fatalf("expected one client certificate, got %#v", client.TLSConfig)
	}
}

func testGigaChatRejectsMissingCertificatePair(t *testing.T) {
	t.Parallel()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	_, err = buildGigaChatTLSClient(provider.client, &schemas.GigaChatKeyConfig{CertFile: "client.pem"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cert_file and gigachat_key_config.key_file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatRejectsEncryptedKeyPassword(t *testing.T) {
	t.Parallel()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	_, err = buildGigaChatTLSClient(provider.client, &schemas.GigaChatKeyConfig{
		CertFile:        "client.pem",
		KeyFile:         "client.key",
		KeyFilePassword: schemas.NewEnvVar("super-secret-password"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "encrypted gigachat_key_config.key_file is not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "super-secret-password") {
		t.Fatalf("secret leaked in error: %v", err)
	}
}

func generateGigaChatTestCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "gigachat-test",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	keyDER := x509.MarshalPKCS1PrivateKey(privateKey)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func writeGigaChatTestFile(t *testing.T, name string, contents []byte) string {
	t.Helper()

	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	return path
}
