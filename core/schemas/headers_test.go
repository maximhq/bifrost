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
