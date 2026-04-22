package async

import (
	"net/http"
	"strings"
	"testing"
)

// Auth tests cover every combination of VK presence at submit and poll time.
// All tests use chat_completions as a representative endpoint.

// assertPollSuccess fails the test unless the poll returned a success code (200 or 202).
func assertPollSuccess(t *testing.T, code int, body []byte) {
	t.Helper()
	if code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("expected 200/202, got %d: %s", code, body)
	}
}

// TestAuth_Submit_InvalidVK_Returns400 verifies that submitting with a VK value
// unknown to the governance store fails at submit time with 400.
// Requires BIFROST_VK to be set, which proves VK governance is active on the server.
func TestAuth_Submit_InvalidVK_Returns400(t *testing.T) {
	if cfg.VK == "" {
		t.Skip("BIFROST_VK not set — governance may not be active")
	}
	ec := chatCompletionCase()
	code, _, body := submitCase(t, ec, vkHeaders("sk-bf-nonexistent-key-for-auth-test"))
	if code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown VK on submit, got %d: %s", code, body)
	}
}

// TestAuth_VKScoped_SameKey_Succeeds submits with a VK and polls with the same VK.
func TestAuth_VKScoped_SameKey_Succeeds(t *testing.T) {
	if cfg.VK == "" {
		t.Skip("BIFROST_VK not set")
	}
	ec := chatCompletionCase()
	_, submitted, body := submitCase(t, ec, vkHeaders(cfg.VK))
	if submitted.ID == "" {
		t.Fatalf("submit returned no job id: %s", body)
	}

	pollPath := jobPollPath(ec.pollBase, submitted.ID)
	code, _, body := pollOnce(t, pollPath, vkHeaders(cfg.VK))
	assertPollSuccess(t, code, body)
}

// TestAuth_VKScoped_DifferentKey_Returns404 submits with VK1 and polls with VK2.
// The gateway must return 404 because the VK IDs will not match.
func TestAuth_VKScoped_DifferentKey_Returns404(t *testing.T) {
	if cfg.VK == "" || cfg.AltVK == "" {
		t.Skip("both BIFROST_VK and BIFROST_ALT_VK must be set")
	}
	ec := chatCompletionCase()
	_, submitted, body := submitCase(t, ec, vkHeaders(cfg.VK))
	if submitted.ID == "" {
		t.Fatalf("submit returned no job id: %s", body)
	}

	pollPath := jobPollPath(ec.pollBase, submitted.ID)
	code, _, _ := pollOnce(t, pollPath, vkHeaders(cfg.AltVK))
	if code != http.StatusNotFound {
		t.Errorf("expected 404 when polling with a different VK, got %d", code)
	}
}

// TestAuth_VKScoped_MissingKeyOnPoll_Returns404 submits with a VK and polls
// without one. The job stores a VirtualKeyID so the gateway requires a VK on poll.
func TestAuth_VKScoped_MissingKeyOnPoll_Returns404(t *testing.T) {
	if cfg.VK == "" {
		t.Skip("BIFROST_VK not set")
	}
	ec := chatCompletionCase()
	_, submitted, body := submitCase(t, ec, vkHeaders(cfg.VK))
	if submitted.ID == "" {
		t.Fatalf("submit returned no job id: %s", body)
	}

	pollPath := jobPollPath(ec.pollBase, submitted.ID)
	code, _, _ := pollOnce(t, pollPath, nil)
	if code != http.StatusNotFound {
		t.Errorf("expected 404 when polling a VK-scoped job without a VK, got %d", code)
	}
}

// TestAuth_PublicJob_AnonymousPoll_Succeeds submits without a VK (VirtualKeyID = nil)
// and polls without a VK. The VK check is skipped for public jobs.
func TestAuth_PublicJob_AnonymousPoll_Succeeds(t *testing.T) {
	ec := chatCompletionCase()
	_, submitted, body := submitCase(t, ec, nil)
	if submitted.ID == "" {
		t.Fatalf("submit returned no job id: %s", body)
	}

	pollPath := jobPollPath(ec.pollBase, submitted.ID)
	code, _, body := pollOnce(t, pollPath, nil)
	assertPollSuccess(t, code, body)
}

// TestAuth_PublicJob_VKPoll_Succeeds submits without a VK and polls with one.
// Per docs: "Jobs created without a virtual key are not virtual-key scoped, so they
// can be polled by any caller that passes your gateway auth/middleware checks."
func TestAuth_PublicJob_VKPoll_Succeeds(t *testing.T) {
	if cfg.VK == "" {
		t.Skip("BIFROST_VK not set")
	}
	ec := chatCompletionCase()
	_, submitted, body := submitCase(t, ec, nil)
	if submitted.ID == "" {
		t.Fatalf("submit returned no job id: %s", body)
	}

	pollPath := jobPollPath(ec.pollBase, submitted.ID)
	code, _, body := pollOnce(t, pollPath, vkHeaders(cfg.VK))
	assertPollSuccess(t, code, body)
}

// vkPrefixed returns true when vk begins with the governance virtual-key prefix "sk-bf-".
// Only keys with this prefix are recognised by the Authorization, x-api-key, and
// x-goog-api-key header paths in ConvertToBifrostContext.
func vkPrefixed(vk string) bool {
	return strings.HasPrefix(strings.ToLower(vk), "sk-bf-")
}

// TestAuth_BearerVK_SameKey_Succeeds submits with "Authorization: Bearer <vk>" and
// polls with the same header.  Verifies the Bearer token path in ConvertToBifrostContext.
func TestAuth_BearerVK_SameKey_Succeeds(t *testing.T) {
	if cfg.VK == "" || !vkPrefixed(cfg.VK) {
		t.Skip("BIFROST_VK not set or does not start with sk-bf- prefix")
	}
	ec := chatCompletionCase()
	headers := map[string]string{"Authorization": "Bearer " + cfg.VK}
	_, submitted, body := submitCase(t, ec, headers)
	if submitted.ID == "" {
		t.Fatalf("submit returned no job id: %s", body)
	}
	pollPath := jobPollPath(ec.pollBase, submitted.ID)
	code, _, body := pollOnce(t, pollPath, headers)
	assertPollSuccess(t, code, body)
}

// TestAuth_ApiKeyVK_SameKey_Succeeds submits with "x-api-key: <vk>" and polls with
// the same header.  Verifies the x-api-key path in ConvertToBifrostContext.
func TestAuth_ApiKeyVK_SameKey_Succeeds(t *testing.T) {
	if cfg.VK == "" || !vkPrefixed(cfg.VK) {
		t.Skip("BIFROST_VK not set or does not start with sk-bf- prefix")
	}
	ec := chatCompletionCase()
	headers := map[string]string{"x-api-key": cfg.VK}
	_, submitted, body := submitCase(t, ec, headers)
	if submitted.ID == "" {
		t.Fatalf("submit returned no job id: %s", body)
	}
	pollPath := jobPollPath(ec.pollBase, submitted.ID)
	code, _, body := pollOnce(t, pollPath, headers)
	assertPollSuccess(t, code, body)
}

// TestAuth_GoogApiKeyVK_SameKey_Succeeds submits with "x-goog-api-key: <vk>" and polls
// with the same header.  Verifies the x-goog-api-key path in ConvertToBifrostContext.
func TestAuth_GoogApiKeyVK_SameKey_Succeeds(t *testing.T) {
	if cfg.VK == "" || !vkPrefixed(cfg.VK) {
		t.Skip("BIFROST_VK not set or does not start with sk-bf- prefix")
	}
	ec := chatCompletionCase()
	headers := map[string]string{"x-goog-api-key": cfg.VK}
	_, submitted, body := submitCase(t, ec, headers)
	if submitted.ID == "" {
		t.Fatalf("submit returned no job id: %s", body)
	}
	pollPath := jobPollPath(ec.pollBase, submitted.ID)
	code, _, body := pollOnce(t, pollPath, headers)
	assertPollSuccess(t, code, body)
}
