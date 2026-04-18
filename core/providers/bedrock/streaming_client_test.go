package bedrock

import (
	"net/http"
	"testing"
	"time"
)

func TestStreamingHTTPClient_DisablesTotalTimeout(t *testing.T) {
	t.Parallel()

	baseTransport := http.DefaultTransport
	provider := &BedrockProvider{
		client: &http.Client{
			Transport: baseTransport,
			Timeout:   120 * time.Second,
		},
	}

	streamClient := provider.streamingHTTPClient()
	if streamClient.Timeout != 0 {
		t.Fatalf("streaming client timeout = %v, want 0", streamClient.Timeout)
	}
	if streamClient.Transport != baseTransport {
		t.Fatal("streaming client should reuse the provider transport")
	}
}
