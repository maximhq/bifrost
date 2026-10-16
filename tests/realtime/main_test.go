// Package realtime is the end-to-end suite for the Realtime API (/v1/realtime) through a running
// Bifrost gateway. The gateway's OpenAI provider points at a fake upstream the suite runs, and each
// test scripts both ends of the conversation: what the client sends, what the provider answers,
// and what the gateway must relay, refuse and log in between. See README.md.
package realtime

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	gatewayURL string
	gatewayUp  bool
	upstream   *fakeUpstream
	suiteKey   virtualKey // shared by every test that does not need a key of its own
)

const (
	frameTimeout   = 10 * time.Second
	rowWaitTimeout = 15 * time.Second

	upstreamKey = "sk-fake-realtime-upstream" // the openai key in config.json
)

func TestMain(m *testing.M) {
	gatewayURL = strings.TrimSuffix(envOr("BIFROST_BASE_URL", "http://localhost:8080"), "/")
	gatewayUp = gatewayHealthy()
	if !gatewayUp {
		fmt.Printf("realtime e2e: gateway at %s is not reachable; every test will skip\n", gatewayURL)
		os.Exit(m.Run())
	}

	var err error
	if upstream, err = startFakeUpstream(envOr("REALTIME_FAKE_ADDR", "127.0.0.1:9412")); err != nil {
		fmt.Printf("realtime e2e: cannot start the fake upstream: %v\n", err)
		os.Exit(1)
	}
	if err := installPricing(); err != nil {
		fmt.Printf("realtime e2e: cannot install pricing overrides: %v\n", err)
		os.Exit(1)
	}
	if suiteKey, err = createSuiteKey(); err != nil {
		fmt.Printf("realtime e2e: cannot create the suite's virtual key: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	deleteVirtualKey(suiteKey.ID)
	if err := removePricing(); err != nil {
		fmt.Printf("realtime e2e: cannot remove pricing overrides: %v\n", err)
	}
	upstream.stop()
	os.Exit(code)
}

// requireGateway skips when no gateway answers; every test starts with it.
func requireGateway(t *testing.T) {
	t.Helper()
	if !gatewayUp {
		t.Skipf("gateway at %s is not reachable", gatewayURL)
	}
}

func gatewayHealthy() bool {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(gatewayURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
