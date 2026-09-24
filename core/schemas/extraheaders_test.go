package schemas

import (
	"encoding/json"
	"testing"
)

// TestNetworkConfigExtraHeaders_LegacyStringMapLoads checks that a stored or
// config.json extra_headers map of plain strings still decodes, with env.* values
// resolved and kept as references.
func TestNetworkConfigExtraHeaders_LegacyStringMapLoads(t *testing.T) {
	t.Setenv("BF_TEST_CLIENT_SECRET", "s3cr3t-from-env")

	var nc NetworkConfig
	raw := `{"extra_headers":{"X-User-Email":"ops@example.com","X-Client-Secret":"env.BF_TEST_CLIENT_SECRET"}}`
	if err := json.Unmarshal([]byte(raw), &nc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	email := nc.ExtraHeaders["X-User-Email"]
	if email.GetValue() != "ops@example.com" || email.IsFromSecret() {
		t.Fatalf("literal header: got %+v", email)
	}
	secret := nc.ExtraHeaders["X-Client-Secret"]
	if !secret.IsFromEnv() || secret.GetValue() != "s3cr3t-from-env" {
		t.Fatalf("env header: got value %q fromEnv=%v", secret.GetValue(), secret.IsFromEnv())
	}
}

// TestNetworkConfigExtraHeaders_MarshalKeepsStringShape checks that headers serialize
// back to a plain string map and that an env-backed value is written as its reference,
// never as the resolved secret.
func TestNetworkConfigExtraHeaders_MarshalKeepsStringShape(t *testing.T) {
	t.Setenv("BF_TEST_CLIENT_SECRET", "s3cr3t-from-env")

	nc := NetworkConfig{ExtraHeaders: map[string]SecretVar{
		"X-User-Email":    *NewSecretVar("ops@example.com"),
		"X-Client-Secret": *NewSecretVar("env.BF_TEST_CLIENT_SECRET"),
	}}
	data, err := json.Marshal(nc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out struct {
		ExtraHeaders map[string]string `json:"extra_headers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("extra_headers is not a string map: %v (%s)", err, data)
	}
	if got := out.ExtraHeaders["X-User-Email"]; got != "ops@example.com" {
		t.Errorf("literal header: got %q", got)
	}
	if got := out.ExtraHeaders["X-Client-Secret"]; got != "env.BF_TEST_CLIENT_SECRET" {
		t.Errorf("env header should serialize as its reference, got %q", got)
	}
}

// TestNetworkConfigExtraHeaders_DecodesJSONEscapes checks that header values decode as
// JSON strings: an escaped slash or \u escape must yield the character, not the quoted
// JSON text, or the header fails upstream authentication.
func TestNetworkConfigExtraHeaders_DecodesJSONEscapes(t *testing.T) {
	var nc NetworkConfig
	raw := `{"extra_headers":{"Authorization":"Bearer a\/b","X-Name":"caf\u00e9 \ud83d\ude00","X-Quote":"say \"hi\""}}`
	if err := json.Unmarshal([]byte(raw), &nc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"Authorization": "Bearer a/b",
		"X-Name":        "café 😀",
		"X-Quote":       `say "hi"`,
	}
	for name, value := range want {
		got := nc.ExtraHeaders[name]
		if got.GetValue() != value {
			t.Errorf("%s: got %q, want %q", name, got.GetValue(), value)
		}
	}
}
