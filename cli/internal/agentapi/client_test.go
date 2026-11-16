package agentapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/cli/internal/client"
)

// roundTripFunc adapts a function into an HTTP transport for agent API tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip executes an in-memory agent API exchange.
func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

// TestAgentAPIContracts verifies user-scoped endpoints, bearer auth, and exact counters.
func TestAgentAPIContracts(t *testing.T) {
	transport := client.New("https://gateway.example", client.Credentials{AgentToken: "ck-bf-agent-test"}, time.Second)
	transport.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer ck-bf-agent-test" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		body := ""
		switch request.URL.Path {
		case "/api/agent/virtual-keys":
			body = `{"virtual_keys":[{"id":"vk-1","name":"Engineering","is_active":true}],"selected_virtual_key_id":"vk-1"}`
		case "/api/agent/usage-summary":
			body = `{"budget":{"used":5,"limit":10,"available":5},"budgets":[],"rate_limits":[{"id":"rate-1","tokens":{"used":9007199254740993,"limit":9007199254740999,"available":6}}]}`
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	api := New(transport)
	keys, err := api.ListVirtualKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.VirtualKeys) != 1 || keys.VirtualKeys[0].ID != "vk-1" || keys.SelectedVirtualKeyID != "vk-1" {
		t.Fatalf("keys = %#v", keys)
	}
	summary, err := api.GetUsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Budget.Used != 5 || len(summary.RateLimits) != 1 || summary.RateLimits[0].Tokens.Used != 9007199254740993 {
		t.Fatalf("summary = %#v", summary)
	}
}
