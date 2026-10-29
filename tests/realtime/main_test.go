// Package realtime is the end-to-end suite for the Realtime API (/v1/realtime) through a running
// Bifrost gateway, against the real providers: OpenAI's realtime models, the same models on Azure
// OpenAI, and Amazon Nova 2 Sonic on Bedrock. Each test drives a session the way an app does and checks what the gateway relays,
// refuses and logs. See README.md.
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
	gatewayUp = gatewayHealthy()
	if !gatewayUp {
		if os.Getenv("REALTIME_REQUIRE_GATEWAY") != "" {
			fmt.Printf("realtime e2e: gateway at %s is not reachable\n", gatewayURL)
			os.Exit(1)
		}
		fmt.Printf("realtime e2e: gateway at %s is not reachable; every test will skip\n", gatewayURL)
	}
	os.Exit(m.Run())
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
