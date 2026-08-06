package gigachat

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	t.Run("ReusesTLSClientWithCABundleUntilProviderReload", testGigaChatReusesTLSClientWithCABundleUntilProviderReload)
	t.Run("CachesTLSClientConcurrently", testGigaChatCachesTLSClientConcurrently)
	t.Run("BuildsTLSClientWithCertificate", testGigaChatBuildsTLSClientWithCertificate)
	t.Run("ReusesTLSClientWithCertificateUntilProviderReload", testGigaChatReusesTLSClientWithCertificateUntilProviderReload)
	t.Run("RejectsMissingCertificatePair", testGigaChatRejectsMissingCertificatePair)
	t.Run("PassthroughEarlyCloseFinalizesOnce", testGigaChatPassthroughEarlyCloseFinalizesOnce)
	t.Run("AttachmentCacheLifecycle", testGigaChatAttachmentCacheLifecycle)
}

type gigaChatCountingReadCloser struct {
	io.Reader
	closeCalls atomic.Int32
}

func (reader *gigaChatCountingReadCloser) Close() error {
	reader.closeCalls.Add(1)
	return nil
}

func testGigaChatPassthroughEarlyCloseFinalizesOnce(t *testing.T) {
	t.Parallel()

	ctx := testBifrostContext()
	underlying := &gigaChatCountingReadCloser{Reader: strings.NewReader("incomplete")}
	var finalizerCalls atomic.Int32
	reader := &gigaChatPassthroughReadCloser{
		ReadCloser: underlying,
		ctx:        ctx,
		postHookSpanFinalizer: func(context.Context) {
			finalizerCalls.Add(1)
		},
	}

	buffer := make([]byte, 1)
	if _, err := reader.Read(buffer); err != nil {
		t.Fatalf("failed to read passthrough prefix: %v", err)
	}

	closeErrors := make(chan error, 2)
	for range 2 {
		go func() {
			closeErrors <- reader.Close()
		}()
	}
	for range 2 {
		if err := <-closeErrors; err != nil {
			t.Fatalf("passthrough close failed: %v", err)
		}
	}

	if got := underlying.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying close calls mismatch: got %d, want 1", got)
	}
	if got := finalizerCalls.Load(); got != 1 {
		t.Fatalf("finalizer calls mismatch: got %d, want 1", got)
	}
	if ended, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); ended {
		t.Fatal("early passthrough close must not mark the stream complete")
	}
}

func testGigaChatAttachmentCacheLifecycle(t *testing.T) {
	t.Parallel()

	manager := newGigaChatAttachmentCacheManager()
	defer func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if manager.sweepTimer != nil {
			manager.sweepTimer.Stop()
			manager.sweepTimer = nil
		}
	}()

	provider := &GigaChatProvider{attachmentCache: manager}
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	request := &schemas.BifrostChatRequest{}
	fileID := "uploaded-file"
	replacement := schemas.ChatContentBlock{
		Type: schemas.ChatContentBlockTypeFile,
		File: &schemas.ChatInputFile{FileID: &fileID},
	}
	provider.setCachedGigaChatChatAttachment(ctx, schemas.Key{}, request, 0, 0, replacement)

	cacheID, ok := ctx.Value(gigaChatAttachmentCacheKey).(string)
	if !ok || cacheID == "" {
		t.Fatalf("context must store only a cache ID, got %#v", ctx.Value(gigaChatAttachmentCacheKey))
	}
	manager.mu.Lock()
	entry := manager.entries[cacheID]
	manager.mu.Unlock()
	if entry == nil {
		t.Fatal("provider-owned attachment cache entry is missing")
	}
	childCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	childCacheID, _ := childCtx.Value(gigaChatAttachmentCacheKey).(string)
	if childCacheID != cacheID {
		t.Fatalf("derived context did not inherit cache ID: got %q, want %q", childCacheID, cacheID)
	}
	if cached, found := provider.getCachedGigaChatChatAttachment(childCtx, schemas.Key{}, request, 0, 0); !found || cached.File == nil || cached.File.FileID == nil || *cached.File.FileID != fileID {
		t.Fatalf("derived context did not reuse cached attachment: %#v, found=%v", cached, found)
	}
	manager.mu.Lock()
	entryCount := len(manager.entries)
	manager.mu.Unlock()
	if entryCount != 1 {
		t.Fatalf("derived context created or evicted a cache bucket: got %d entries, want 1", entryCount)
	}

	entry.cache.mu.Lock()
	for key, attachment := range entry.cache.chat {
		attachment.expiresAt = time.Time{}
		entry.cache.chat[key] = attachment
	}
	entry.cache.mu.Unlock()
	if cached, found := provider.getCachedGigaChatChatAttachment(ctx, schemas.Key{}, request, 0, 0); found {
		t.Fatalf("expired attachment remained reusable: %#v", cached)
	}
	entry.cache.mu.Lock()
	remainingAttachments := len(entry.cache.chat)
	entry.cache.mu.Unlock()
	if remainingAttachments != 0 {
		t.Fatalf("expired attachment records were not pruned: %d remain", remainingAttachments)
	}

	_, writer := manager.cacheForWrite(ctx)
	if writer == nil {
		t.Fatal("failed to register in-flight attachment cache writer")
	}
	manager.mu.Lock()
	if manager.sweepTimer != nil {
		manager.sweepTimer.Stop()
		manager.sweepTimer = nil
	}
	manager.mu.Unlock()
	manager.sweep()
	manager.mu.Lock()
	entriesDuringWrite := len(manager.entries)
	manager.mu.Unlock()
	if entriesDuringWrite != 1 {
		t.Fatalf("sweep evicted a cache with an in-flight writer: got %d entries, want 1", entriesDuringWrite)
	}

	manager.finishWrite(writer)
	manager.mu.Lock()
	if manager.sweepTimer != nil {
		manager.sweepTimer.Stop()
		manager.sweepTimer = nil
	}
	manager.mu.Unlock()
	manager.sweep()
	manager.mu.Lock()
	remainingEntries := len(manager.entries)
	manager.mu.Unlock()
	if remainingEntries != 0 {
		t.Fatalf("empty context cache was not pruned: %d entries remain", remainingEntries)
	}
	cancel()
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

func testGigaChatReusesTLSClientWithCABundleUntilProviderReload(t *testing.T) {
	t.Parallel()

	certPEM1, _ := generateGigaChatTestCertificate(t)
	certPEM2, _ := generateGigaChatTestCertificate(t)
	caBundleFile := writeGigaChatTestFile(t, "ca.pem", certPEM1)

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	keyConfig := &schemas.GigaChatKeyConfig{CABundleFile: caBundleFile}
	client, err := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, keyConfig)
	if err != nil {
		t.Fatalf("getGigaChatTLSClient returned error: %v", err)
	}
	if client == provider.client {
		t.Fatal("expected a cloned client when TLS material is configured")
	}

	if err := os.WriteFile(caBundleFile, certPEM2, 0o600); err != nil {
		t.Fatalf("failed to rotate CA bundle file: %v", err)
	}

	reusedClient, err := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, keyConfig)
	if err != nil {
		t.Fatalf("getGigaChatTLSClient returned error after CA rotation: %v", err)
	}
	if reusedClient != client {
		t.Fatal("expected cached TLS client to be reused after CA bundle rotation")
	}

	if err := os.Remove(caBundleFile); err != nil {
		t.Fatalf("failed to remove CA bundle file: %v", err)
	}
	reusedClient, err = provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, keyConfig)
	if err != nil {
		t.Fatalf("cached TLS client lookup read removed CA bundle file: %v", err)
	}
	if reusedClient != client {
		t.Fatal("expected cached TLS client to be reused after CA bundle removal")
	}

	reloadedProvider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}
	if _, err := reloadedProvider.getGigaChatTLSClient(reloadedProvider.client, gigaChatTLSClientCacheDefault, keyConfig); err == nil {
		t.Fatal("expected provider reload to validate missing CA bundle file")
	} else if !strings.Contains(err.Error(), "ca_bundle_file") {
		t.Fatalf("unexpected missing CA error: %v", err)
	}
}

func testGigaChatCachesTLSClientConcurrently(t *testing.T) {
	t.Parallel()

	certPEM, _ := generateGigaChatTestCertificate(t)
	caBundleFile := writeGigaChatTestFile(t, "ca.pem", certPEM)
	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	const workerCount = 32
	keyConfig := &schemas.GigaChatKeyConfig{CABundleFile: caBundleFile}
	clients := make([]*fasthttp.Client, workerCount)
	errs := make([]error, workerCount)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			clients[i], errs[i] = provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, keyConfig)
		}()
	}
	close(start)
	workers.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("getGigaChatTLSClient call %d returned error: %v", i, err)
		}
	}
	cachedClient := clients[0]
	for _, client := range clients[1:] {
		if client != cachedClient {
			t.Fatal("concurrent cache misses returned different TLS clients")
		}
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

func testGigaChatReusesTLSClientWithCertificateUntilProviderReload(t *testing.T) {
	t.Parallel()

	certPEM1, keyPEM1 := generateGigaChatTestCertificate(t)
	certPEM2, keyPEM2 := generateGigaChatTestCertificate(t)
	certFile := writeGigaChatTestFile(t, "client.pem", certPEM1)
	keyFile := writeGigaChatTestFile(t, "client.key", keyPEM1)

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	keyConfig := &schemas.GigaChatKeyConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	}
	client, err := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, keyConfig)
	if err != nil {
		t.Fatalf("getGigaChatTLSClient returned error: %v", err)
	}

	if err := os.WriteFile(certFile, certPEM2, 0o600); err != nil {
		t.Fatalf("failed to rotate client certificate file: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM2, 0o600); err != nil {
		t.Fatalf("failed to rotate client key file: %v", err)
	}

	reusedClient, err := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, keyConfig)
	if err != nil {
		t.Fatalf("getGigaChatTLSClient returned error after certificate rotation: %v", err)
	}
	if reusedClient != client {
		t.Fatal("expected cached TLS client to be reused after certificate rotation")
	}

	if err := os.Remove(certFile); err != nil {
		t.Fatalf("failed to remove client certificate file: %v", err)
	}
	if err := os.Remove(keyFile); err != nil {
		t.Fatalf("failed to remove client key file: %v", err)
	}
	reusedClient, err = provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, keyConfig)
	if err != nil {
		t.Fatalf("cached TLS client lookup read removed certificate files: %v", err)
	}
	if reusedClient != client {
		t.Fatal("expected cached TLS client to be reused after certificate file removal")
	}

	reloadedProvider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}
	if _, err := reloadedProvider.getGigaChatTLSClient(reloadedProvider.client, gigaChatTLSClientCacheDefault, keyConfig); err == nil {
		t.Fatal("expected provider reload to validate missing certificate files")
	} else if !strings.Contains(err.Error(), "cert_file/key_file") {
		t.Fatalf("unexpected missing certificate error: %v", err)
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
