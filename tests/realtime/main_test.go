// Package realtime is the end-to-end suite for the Realtime API (/v1/realtime) through a running
// Bifrost gateway, against the real providers: OpenAI's realtime models, the same models on Azure
// OpenAI, Amazon Nova 2 Sonic on Bedrock, ElevenLabs' Scribe and agents, and Soniox's real-time transcription. Each test drives a session the way an app does and checks what the gateway relays,
// refuses and logs. See README.md.
//
// Two upstream modes, set by REALTIME_UPSTREAM:
//
//	real (default)  the gateway runs on config.json and talks to the providers; every test runs.
//	fake            the suite hosts fake Gemini, Soniox and ElevenLabs upstreams, and the gateway runs on
//	                fake/config.json, which points those providers at them. Only the tests that start
//	                with requireUpstream run, and Vertex's cases skip because Vertex has no fake;
//	                the rest need a real provider.
package realtime

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	realUpstream = "real"
	fakeUpstream = "fake"
)

var (
	gatewayURL   string
	gatewayUp    bool
	upstreamMode string
	fakeAddr     string
	fake         *fakeLive // the fake Live upstream; nil unless upstreamMode is fake

	openaiModel     = envOr("REALTIME_OPENAI_MODEL", "gpt-realtime-mini")
	transcribeModel = envOr("REALTIME_TRANSCRIBE_MODEL", "gpt-4o-mini-transcribe")
	novaModel       = envOr("REALTIME_NOVA_MODEL", "amazon.nova-2-sonic-v1:0")

	azureModel           = envOr("REALTIME_AZURE_MODEL", "gpt-realtime")                 // an Azure deployment name
	azureTranscribeModel = envOr("REALTIME_AZURE_TRANSCRIBE_MODEL", "gpt-4o-transcribe") // an Azure deployment name
)

const (
	frameTimeout    = 10 * time.Second // for frames the gateway sends itself
	turnTimeout     = 45 * time.Second // for a provider to finish a response
	rowWaitTimeout  = 15 * time.Second
	rowSettleWindow = 1500 * time.Millisecond // longer than the logs store's batch interval
)

func TestMain(m *testing.M) {
	gatewayURL = strings.TrimSuffix(envOr("BIFROST_BASE_URL", "http://localhost:8080"), "/")
	upstreamMode = envOr("REALTIME_UPSTREAM", realUpstream)
	fakeAddr = envOr("REALTIME_FAKE_ADDR", "127.0.0.1:9421") // the address fake/config.json points at
	if upstreamMode != realUpstream && upstreamMode != fakeUpstream {
		fmt.Printf("realtime e2e: REALTIME_UPSTREAM is %q, want %q or %q\n", upstreamMode, realUpstream, fakeUpstream)
		os.Exit(1)
	}

	gatewayUp = gatewayHealthy()
	if !gatewayUp {
		if os.Getenv("REALTIME_REQUIRE_GATEWAY") != "" {
			fmt.Printf("realtime e2e: gateway at %s is not reachable\n", gatewayURL)
			os.Exit(1)
		}
		fmt.Printf("realtime e2e: gateway at %s is not reachable; every test will skip\n", gatewayURL)
	}
	if upstreamMode == fakeUpstream && gatewayUp {
		var err error
		if fake, err = startFakeLive(fakeAddr); err != nil {
			fmt.Printf("realtime e2e: cannot start the fake Live upstream on %s: %v\n", fakeAddr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if fake != nil {
		fake.Stop()
	}
	os.Exit(code)
}

// requireGateway skips when no gateway answers or when the upstream is fake: it opens the tests that
// need a real provider, and most of the suite does.
func requireGateway(t *testing.T) {
	t.Helper()
	if upstreamMode == fakeUpstream {
		t.Skip("needs a real provider; REALTIME_UPSTREAM=fake serves only Gemini, Soniox and ElevenLabs")
	}
	requireUpstream(t)
}

// requireUpstream skips when no gateway answers. A test that runs against the fake upstream and the
// real one starts with it; fake is non-nil exactly when the upstream is fake.
func requireUpstream(t *testing.T) {
	t.Helper()
	if !gatewayUp {
		t.Skipf("gateway at %s is not reachable", gatewayURL)
	}
}

// requireFake skips unless the upstream is the fake, for a scenario only the fake can script.
func requireFake(t *testing.T) {
	t.Helper()
	requireUpstream(t)
	if fake == nil {
		t.Skip("needs the fake upstream (REALTIME_UPSTREAM=fake)")
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
