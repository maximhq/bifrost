package schemas

import (
	"encoding/json"
	"strings"
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

// TestNetworkConfigRedacted_MasksExtraHeaders checks that every literal header value is
// fully redacted in the redacted copy and its JSON (a nine-byte credential must not leak
// its prefix or suffix), that env references stay visible, and that the live config is
// left untouched.
func TestNetworkConfigRedacted_MasksExtraHeaders(t *testing.T) {
	t.Setenv("BF_TEST_CLIENT_SECRET", "s3cr3t-from-env")
	const secret = "oauth-client-secret-value"
	const nineByte = "abc123xyz"
	nc := &NetworkConfig{ExtraHeaders: map[string]SecretVar{
		"X-Client-Secret": *NewSecretVar(secret),
		"X-Nine-Byte":     *NewSecretVar(nineByte),
		"X-Short":         *NewSecretVar("abc"),
		"X-From-Env":      *NewSecretVar("env.BF_TEST_CLIENT_SECRET"),
	}}

	data, err := json.Marshal(nc.Redacted())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out struct {
		ExtraHeaders map[string]string `json:"extra_headers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, name := range []string{"X-Client-Secret", "X-Nine-Byte", "X-Short"} {
		if got := out.ExtraHeaders[name]; got != "<REDACTED>" {
			t.Errorf("%s: got %q, want <REDACTED>", name, got)
		}
	}
	if got := out.ExtraHeaders["X-From-Env"]; got != "env.BF_TEST_CLIENT_SECRET" {
		t.Errorf("env header should show its reference, got %q", got)
	}
	for _, leaked := range []string{secret, "abc1", "3xyz", "s3cr3t-from-env"} {
		if strings.Contains(string(data), leaked) {
			t.Errorf("redacted JSON leaks %q: %s", leaked, data)
		}
	}
	if got := nc.ExtraHeaders["X-Client-Secret"]; got.GetValue() != secret {
		t.Fatalf("live config mutated: %q", got.GetValue())
	}
}

// echoRedacted replays what the UI sends back: the redacted config as JSON, decoded again.
func echoRedacted(t *testing.T, raw map[string]SecretVar) map[string]SecretVar {
	t.Helper()
	sent, err := json.Marshal((&NetworkConfig{ExtraHeaders: raw}).Redacted())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var echoed NetworkConfig
	if err := json.Unmarshal(sent, &echoed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return echoed.ExtraHeaders
}

// TestRestoreRedactedExtraHeaders replays the UI save flow: the redacted config goes out
// as JSON and comes back unchanged or edited. Untouched headers restore to their stored
// values, and a mask that cannot be matched to a stored header by name is rejected.
func TestRestoreRedactedExtraHeaders(t *testing.T) {
	t.Setenv("BF_TEST_CLIENT_SECRET", "s3cr3t-from-env")
	raw := map[string]SecretVar{
		"Authorization":   *NewSecretVar("Bearer dapi-token"),
		"X-Client-Secret": *NewSecretVar("oauth-client-secret-value"),
		"X-Client-Id":     *NewSecretVar("client-id-1234567"),
		"X-From-Env":      *NewSecretVar("env.BF_TEST_CLIENT_SECRET"),
	}

	t.Run("unchanged names and values restore without error", func(t *testing.T) {
		restored, err := RestoreRedactedExtraHeaders(echoRedacted(t, raw), raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for name, value := range raw {
			got := restored[name]
			if got.GetValue() != value.GetValue() {
				t.Errorf("%s: got %q, want %q", name, got.GetValue(), value.GetValue())
			}
		}
		if got := restored["X-From-Env"]; !got.IsFromEnv() {
			t.Errorf("X-From-Env lost its env reference")
		}
	})

	t.Run("edited and new values are kept as sent", func(t *testing.T) {
		echoed := echoRedacted(t, raw)
		echoed["X-Client-Id"] = *NewSecretVar("new-client-id")
		echoed["X-User-Email"] = *NewSecretVar("ops@example.com")
		restored, err := RestoreRedactedExtraHeaders(echoed, raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := restored["X-Client-Id"]; got.GetValue() != "new-client-id" {
			t.Errorf("X-Client-Id: got %q", got.GetValue())
		}
		if got := restored["X-User-Email"]; got.GetValue() != "ops@example.com" {
			t.Errorf("X-User-Email: got %q", got.GetValue())
		}
		if got := restored["X-Client-Secret"]; got.GetValue() != "oauth-client-secret-value" {
			t.Errorf("X-Client-Secret: got %q", got.GetValue())
		}
	})

	t.Run("renamed header with a masked value is rejected", func(t *testing.T) {
		for _, rename := range []struct{ from, to string }{
			{"X-Client-Secret", "X-Upstream-Secret"},
			{"Authorization", "authorization"},
		} {
			echoed := echoRedacted(t, raw)
			echoed[rename.to] = echoed[rename.from]
			delete(echoed, rename.from)
			_, err := RestoreRedactedExtraHeaders(echoed, raw)
			if err == nil || !strings.Contains(err.Error(), rename.to) {
				t.Errorf("%s -> %s: expected an error naming %s, got %v", rename.from, rename.to, rename.to, err)
			}
		}
	})

	t.Run("masked value on a new provider is rejected", func(t *testing.T) {
		_, err := RestoreRedactedExtraHeaders(map[string]SecretVar{"X-Client-Secret": *NewSecretVar("<REDACTED>")}, nil)
		if err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("removed header stays removed", func(t *testing.T) {
		restored, err := RestoreRedactedExtraHeaders(map[string]SecretVar{}, raw)
		if err != nil || len(restored) != 0 {
			t.Fatalf("got %v, %v", restored, err)
		}
	})
}
