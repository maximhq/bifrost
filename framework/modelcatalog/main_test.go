package modelcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

type noOpLogger struct{}

func (noOpLogger) Debug(string, ...any)                   {}
func (noOpLogger) Info(string, ...any)                    {}
func (noOpLogger) Warn(string, ...any)                    {}
func (noOpLogger) Error(string, ...any)                   {}
func (noOpLogger) Fatal(string, ...any)                   {}
func (noOpLogger) SetLevel(schemas.LogLevel)              {}
func (noOpLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noOpLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestInitAutomaticSyncDisabledAllowsEmptyOfflineCatalog(t *testing.T) {
	t.Parallel()

	disabled := false
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	catalog, err := Init(context.Background(), &Config{
		AutomaticSyncEnabled: &disabled,
		PricingURL:           &server.URL,
		ModelParametersURL:   &server.URL,
		MCPLibraryURL:        &server.URL,
	}, nil, noOpLogger{})
	if err != nil {
		t.Fatalf("Init() with automatic sync disabled returned error: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Cleanup() })

	if catalog.syncTicker != nil {
		t.Fatal("automatic sync disabled but background sync ticker was started")
	}
	if catalog.automaticSyncEnabled {
		t.Fatal("catalog reports automatic sync enabled")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("automatic sync disabled but catalog made %d remote request(s)", got)
	}
	if models := catalog.GetModelsForProvider(schemas.OpenAI); len(models) != 0 {
		t.Fatalf("empty offline catalog returned models: %v", models)
	}
}
