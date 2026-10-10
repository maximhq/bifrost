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
