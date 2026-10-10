package schemas

import "testing"

// TestRedactSensitiveHeaders pins that captured request headers exported to
// observability backends have credential-bearing values replaced (keys kept),
// covering the identity-aware-proxy headers IsSensitiveHeader now matches.
func TestRedactSensitiveHeaders(t *testing.T) {
	in := map[string]string{
		"cf-access-jwt-assertion": "eyJ.jwt.sig",
		"x-amzn-oidc-data":        "eyJ.jwt.sig",
		"authorization":           "Bearer sk-ant",
		"x-api-key":               "sk-key",
		"x-app":                   "cli",
		"anthropic-beta":          "interleaved-thinking-2025-05-14",
	}
	out := RedactSensitiveHeaders(in)

	redacted := []string{"cf-access-jwt-assertion", "x-amzn-oidc-data", "authorization", "x-api-key"}
	for _, k := range redacted {
		if out[k] != RedactedAttrValue {
			t.Errorf("%q = %q, want %q", k, out[k], RedactedAttrValue)
		}
	}
	if out["x-app"] != "cli" {
		t.Errorf("x-app = %q, want it preserved", out["x-app"])
	}
	if out["anthropic-beta"] != "interleaved-thinking-2025-05-14" {
		t.Errorf("anthropic-beta = %q, want it preserved", out["anthropic-beta"])
	}
}

// Redacting a nil map must not panic and returns nil.
func TestRedactSensitiveHeaders_Nil(t *testing.T) {
	if got := RedactSensitiveHeaders(nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// The virtual key travels in Bifrost's own x-bf-vk header, which no generic credential pattern
// names. It is a credential all the same and is redacted like Authorization, in any casing.
func TestRedactSensitiveHeaders_VirtualKeyHeader(t *testing.T) {
	for _, name := range []string{"x-bf-vk", "X-BF-VK", " x-bf-vk "} {
		if !IsSensitiveHeader(name) {
			t.Errorf("IsSensitiveHeader(%q) = false, want true", name)
		}
	}
	out := RedactSensitiveHeaders(map[string]string{"x-bf-vk": "sk-bf-secret", "x-bf-session-id": "sess-1"})
	if out["x-bf-vk"] != RedactedAttrValue {
		t.Errorf("x-bf-vk = %q, want %q", out["x-bf-vk"], RedactedAttrValue)
	}
	if out["x-bf-session-id"] != "sess-1" {
		t.Errorf("x-bf-session-id = %q, want it preserved", out["x-bf-session-id"])
	}
}
