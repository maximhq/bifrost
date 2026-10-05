// Package live is the end-to-end suite for GPT Live sessions through a running Bifrost gateway:
// WebSocket and WebRTC sessions, sidebands, recordings, billing, governance and logging.
//
// Two upstream modes:
//
//	fake (default)  the suite runs a fake OpenAI live server and the gateway must be started
//	                with this folder as APP_DIR, whose config points the openai provider at it.
//	real            the gateway points at api.openai.com with a real key; a smoke subset runs.
//
// See README.md for the commands.
package live

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	gatewayURL   string
	upstreamMode string
	fakeAddr     string
	fake         *fakeOpenAI
	gatewayUp    bool
	// envVirtualKey is BIFROST_VK: the credential every request presents when set. Fake mode
	// enforces auth, so tests mint their own keys; real mode uses this one when the gateway
	// enforces auth.
	envVirtualKey string
)

const (
	voiceModel     = "gpt-live-1"
	backendModel   = "gpt-5.6-luna"
	backendModel2  = "gpt-5.6-terra"
	fakeUpstream   = "fake"
	realUpstream   = "real"
	rowWaitTimeout = 15 * time.Second
	frameTimeout   = 10 * time.Second
)

func TestMain(m *testing.M) {
	gatewayURL = strings.TrimSuffix(envOr("BIFROST_BASE_URL", "http://localhost:8080"), "/")
	upstreamMode = envOr("LIVE_UPSTREAM", fakeUpstream)
	fakeAddr = envOr("LIVE_FAKE_ADDR", "127.0.0.1:9411")
	envVirtualKey = os.Getenv("BIFROST_VK")

	gatewayUp = gatewayHealthy()
	if !gatewayUp {
		fmt.Printf("live e2e: gateway at %s is not reachable; every test will skip\n", gatewayURL)
	}
	if upstreamMode == fakeUpstream && gatewayUp {
		var err error
		fake, err = startFakeOpenAI(fakeAddr)
		if err != nil {
			fmt.Printf("live e2e: cannot start the fake OpenAI server on %s: %v\n", fakeAddr, err)
			os.Exit(1)
		}
		if err := installFakePricing(); err != nil {
			fmt.Printf("live e2e: cannot install pricing overrides: %v\n", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if fake != nil {
		fake.Stop()
		if err := removeFakePricing(); err != nil {
			fmt.Printf("live e2e: cannot remove pricing overrides: %v\n", err)
		}
	}
	os.Exit(code)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func gatewayHealthy() bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(gatewayURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// requireGateway skips when no gateway answers.
func requireGateway(t *testing.T) {
	t.Helper()
	if !gatewayUp {
		t.Skipf("gateway at %s is not reachable", gatewayURL)
	}
}

// requireFake skips outside fake mode: the scenario drives the upstream.
func requireFake(t *testing.T) {
	t.Helper()
	requireGateway(t)
	if upstreamMode != fakeUpstream {
		t.Skip("needs the fake upstream (LIVE_UPSTREAM=fake)")
	}
}

// requireReal skips outside real mode: the scenario talks to OpenAI and costs money.
func requireReal(t *testing.T) {
	t.Helper()
	requireGateway(t)
	if upstreamMode != realUpstream {
		t.Skip("needs the real upstream (LIVE_UPSTREAM=real)")
	}
}

// marker names a test's session on the wire, in session.instructions, so the fake can hand the
// test its own session even while other tests run in parallel.
func marker(t *testing.T) string {
	return "live-e2e " + t.Name() + " " + fmt.Sprint(time.Now().UnixNano())
}
