package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProxyConfig_Redacted_PasswordFullyOpaque ensures proxy passwords never use
// partial masking in API-style redaction output.
func TestProxyConfig_Redacted_PasswordFullyOpaque(t *testing.T) {
	literal := "mysecretpassword-should-not-leak-substrings"
	pc := &ProxyConfig{
		Type:     HTTPProxy,
		URL:      NewSecretVar("http://proxy.example.com:8080"),
		Username: NewSecretVar("proxyuser"),
		Password: NewSecretVar(literal),
	}

	red := pc.Redacted()
	if red == nil || red.Password == nil {
		t.Fatalf("expected redacted password, got red=%v", red)
	}
	if red.Password.Val != "<REDACTED>" {
		t.Errorf("password Val: want %q, got %q", "<REDACTED>", red.Password.Val)
	}
	if strings.Contains(red.Password.Val, "mysecret") || strings.Contains(red.Password.Val, "substring") {
		t.Errorf("password redaction leaked substring: %q", red.Password.Val)
	}

	data, err := json.Marshal(red.Password)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), literal) {
		t.Errorf("JSON leaked literal password: %s", data)
	}
}
