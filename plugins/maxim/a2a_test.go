package maxim

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/maxim-go/logging"
)

// TestA2AExportWire verifies real SDK HTTP delivery, content suppression, task
// identity, streaming events and the absence of raw captures and LLM duplicates.
func TestA2AExportWire(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "content", true: "disabled"}[disabled], func(t *testing.T) {
			var mu sync.Mutex
			var payload strings.Builder
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/sdk/v3/log" || r.URL.Query().Get("id") != "repo" || r.Header.Get("x-maxim-api-key") != "synthetic-key" {
					t.Error("export did not use the shared SDK connection configuration")
				}
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				payload.Write(body)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			auto := false
			logger := logging.NewLogger(server.URL, "synthetic-key", &logging.LoggerConfig{Id: "repo", AutoFlush: &auto})
			plugin := &Plugin{defaultLogRepoID: "repo", loggers: map[string]*logging.Logger{"repo": logger}, loggerMutex: &sync.RWMutex{}}
			trace := schemas.NewA2AExportFixtureTrace()
			if disabled {
				trace.RootSpan.Attributes[schemas.AttrBifrostContentLoggingDisabled] = true
			}
			if err := plugin.Inject(context.Background(), trace); err != nil {
				t.Fatal(err)
			}
			logger.Flush()
			mu.Lock()
			wire := payload.String()
			mu.Unlock()
			if !strings.Contains(wire, "a2a.") || !strings.Contains(wire, schemas.AttrA2ATaskID) {
				t.Fatalf("A2A identity missing from wire: %s", wire)
			}
			if strings.Contains(wire, schemas.ExportFixtureRawSecret) {
				t.Fatal("raw bodies must not be exported")
			}
			if disabled && (strings.Contains(wire, schemas.AttrInputMessages) || strings.Contains(wire, schemas.AttrOutputMessages)) {
				t.Fatal("disabled content exported")
			}
			if !strings.Contains(wire, `2023-11-14T22:13:20Z`) || !strings.Contains(wire, `2023-11-14T22:13:21Z`) {
				t.Fatal("original timestamps missing")
			}
			if !disabled && !strings.Contains(wire, schemas.AttrOutputMessages) {
				t.Fatal("structured output missing")
			}
			if strings.Contains(wire, "generation{") {
				t.Fatal("A2A must not create LLM generations")
			}
		})
	}
}
