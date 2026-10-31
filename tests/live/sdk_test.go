package live

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// SDK compatibility: the OpenAI Python SDK's live client, run from python/ with pytest against
// the gateway's OpenAI drop-in route. The fake upstream, the pricing and the keys are this
// suite's, so the Python tests only need the gateway's address and a credential.

func TestSDK_OpenAIPython(t *testing.T) {
	requireGateway(t)
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv is not installed; the OpenAI Python SDK suite needs it")
	}
	env := append(os.Environ(),
		"BIFROST_BASE_URL="+gatewayURL,
		"LIVE_UPSTREAM="+upstreamMode,
		"LIVE_VOICE_MODEL="+voiceModel,
		"LIVE_BACKEND_MODEL="+backendModel,
		"LIVE_BACKEND_MODEL_2="+backendModel2,
	)
	if upstreamMode == fakeUpstream {
		vk := createVirtualKey(t, virtualKeySpec{})
		restricted := createVirtualKey(t, virtualKeySpec{allowedModels: []string{"gpt-4o-mini"}})
		env = append(env, "BIFROST_VK="+vk.Value, "BIFROST_VK_RESTRICTED="+restricted.Value)
	} else if envVirtualKey != "" {
		env = append(env, "BIFROST_VK="+envVirtualKey)
	}

	// A stalled uv or pytest must not outlive the Go test: the budget is the test's own deadline
	// with a margin to report, or ten minutes when the test runs without one.
	timeout := 10 * time.Minute
	if deadline, ok := t.Deadline(); ok {
		timeout = time.Until(deadline) - 30*time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, uv, "run", "--quiet", "pytest", "-q", "-x", "--timeout=120")
	cmd.Dir = "python"
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("pytest:\n%s", out)
	require.NoError(t, err, "the OpenAI Python SDK suite failed")
}
