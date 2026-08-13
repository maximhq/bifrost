package semanticcache

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// directKeyNamespaceContext returns a test context carrying the supplied
// Direct Key value.
func directKeyNamespaceContext(rawKey string) *schemas.BifrostContext {
	ctx := newBaseTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{
		ID:    "header-provided",
		Value: schemas.SecretVar{Val: rawKey},
	})
	return ctx
}

// directKeyNamespaceConfig builds a cache config that references the named
// environment variable without embedding its secret value.
func directKeyNamespaceConfig(t *testing.T, envName string) *Config {
	t.Helper()
	var config Config
	payload := `{"default_cache_key":"shared-default","direct_key_cache_secret_env":"` + envName + `"}`
	if err := json.Unmarshal([]byte(payload), &config); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return &config
}

// TestResolveCacheKey_DirectKeyUsesStableHMAC verifies deterministic, opaque
// SHA-256 namespaces for repeated use of the same Direct Key.
func TestResolveCacheKey_DirectKeyUsesStableHMAC(t *testing.T) {
	t.Setenv("TEST_DIRECT_KEY_CACHE_SECRET", "namespace-secret-at-least-32-bytes")
	plugin := &Plugin{config: directKeyNamespaceConfig(t, "TEST_DIRECT_KEY_CACHE_SECRET")}

	first, ok := plugin.resolveCacheKey(directKeyNamespaceContext("sk-user-a"))
	if !ok {
		t.Fatal("expected direct key caching to be enabled")
	}
	second, ok := plugin.resolveCacheKey(directKeyNamespaceContext("sk-user-a"))
	if !ok {
		t.Fatal("expected repeated direct key caching to be enabled")
	}

	if first != second {
		t.Fatalf("expected stable namespace, got %q and %q", first, second)
	}
	if !strings.HasPrefix(first, "direct-key-hmac-v1:") {
		t.Fatalf("expected HMAC namespace, got %q", first)
	}
	if len(strings.TrimPrefix(first, "direct-key-hmac-v1:")) != 64 {
		t.Fatalf("expected SHA-256 hex digest, got %q", first)
	}
	if strings.Contains(first, "sk-user-a") {
		t.Fatal("namespace contains the raw direct key")
	}
}

// TestResolveCacheKey_DirectKeySeparatesKeys verifies that distinct Direct
// Keys cannot share a cache namespace.
func TestResolveCacheKey_DirectKeySeparatesKeys(t *testing.T) {
	t.Setenv("TEST_DIRECT_KEY_CACHE_SECRET", "namespace-secret-at-least-32-bytes")
	plugin := &Plugin{config: directKeyNamespaceConfig(t, "TEST_DIRECT_KEY_CACHE_SECRET")}

	keyA, ok := plugin.resolveCacheKey(directKeyNamespaceContext("sk-user-a"))
	if !ok {
		t.Fatal("expected Key A caching to be enabled")
	}
	keyB, ok := plugin.resolveCacheKey(directKeyNamespaceContext("sk-user-b"))
	if !ok {
		t.Fatal("expected Key B caching to be enabled")
	}
	if keyA == keyB {
		t.Fatal("different direct keys resolved to the same cache namespace")
	}
}

// TestResolveCacheKey_DirectKeySeparatesSecrets verifies that rotating the
// server secret produces a distinct cache namespace.
func TestResolveCacheKey_DirectKeySeparatesSecrets(t *testing.T) {
	t.Setenv("TEST_DIRECT_KEY_CACHE_SECRET_A", "namespace-secret-a-at-least-32-bytes")
	t.Setenv("TEST_DIRECT_KEY_CACHE_SECRET_B", "namespace-secret-b-at-least-32-bytes")

	keyA, ok := (&Plugin{config: directKeyNamespaceConfig(t, "TEST_DIRECT_KEY_CACHE_SECRET_A")}).resolveCacheKey(directKeyNamespaceContext("sk-user-a"))
	if !ok {
		t.Fatal("expected namespace using secret A")
	}
	keyB, ok := (&Plugin{config: directKeyNamespaceConfig(t, "TEST_DIRECT_KEY_CACHE_SECRET_B")}).resolveCacheKey(directKeyNamespaceContext("sk-user-a"))
	if !ok {
		t.Fatal("expected namespace using secret B")
	}
	if keyA == keyB {
		t.Fatal("different HMAC secrets resolved to the same cache namespace")
	}
}

// TestResolveCacheKey_DirectKeyOverridesClientAndDefaultKeys verifies that a
// server-derived Direct Key namespace takes precedence over untrusted keys.
func TestResolveCacheKey_DirectKeyOverridesClientAndDefaultKeys(t *testing.T) {
	t.Setenv("TEST_DIRECT_KEY_CACHE_SECRET", "namespace-secret-at-least-32-bytes")
	plugin := &Plugin{config: directKeyNamespaceConfig(t, "TEST_DIRECT_KEY_CACHE_SECRET")}
	ctx := directKeyNamespaceContext("sk-user-a")
	ctx.SetValue(CacheKey, "attacker-selected")

	cacheKey, ok := plugin.resolveCacheKey(ctx)
	if !ok {
		t.Fatal("expected direct key caching to be enabled")
	}
	if !strings.HasPrefix(cacheKey, "direct-key-hmac-v1:") {
		t.Fatalf("expected direct key namespace to win, got %q", cacheKey)
	}
	if cacheKey == "attacker-selected" || cacheKey == "shared-default" {
		t.Fatalf("untrusted cache key won precedence: %q", cacheKey)
	}
}

// TestResolveCacheKey_DirectKeyWithoutSecretFailsClosed verifies that an unset
// namespace secret disables caching for Direct Key requests.
func TestResolveCacheKey_DirectKeyWithoutSecretFailsClosed(t *testing.T) {
	const envName = "BIFROST_TEST_MISSING_DIRECT_KEY_SECRET"
	previous, existed := os.LookupEnv(envName)
	if err := os.Unsetenv(envName); err != nil {
		t.Fatalf("unset test env: %v", err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(envName, previous)
		} else {
			_ = os.Unsetenv(envName)
		}
	})

	plugin := &Plugin{config: directKeyNamespaceConfig(t, envName)}
	ctx := directKeyNamespaceContext("sk-user-a")
	ctx.SetValue(CacheKey, "attacker-selected")

	cacheKey, ok := plugin.resolveCacheKey(ctx)
	if ok || cacheKey != "" {
		t.Fatalf("expected missing secret to disable caching, got key=%q ok=%v", cacheKey, ok)
	}
}

// TestResolveCacheKey_DirectKeyWithoutConfigFailsClosed verifies that missing
// namespace configuration disables caching for Direct Key requests.
func TestResolveCacheKey_DirectKeyWithoutConfigFailsClosed(t *testing.T) {
	plugin := &Plugin{config: &Config{DefaultCacheKey: "shared-default"}}
	ctx := directKeyNamespaceContext("sk-user-a")
	ctx.SetValue(CacheKey, "attacker-selected")

	cacheKey, ok := plugin.resolveCacheKey(ctx)
	if ok || cacheKey != "" {
		t.Fatalf("expected missing HMAC config to disable caching, got key=%q ok=%v", cacheKey, ok)
	}
}

// TestResolveCacheKey_DirectKeyWithShortSecretFailsClosed verifies that a weak
// namespace secret disables caching for Direct Key requests.
func TestResolveCacheKey_DirectKeyWithShortSecretFailsClosed(t *testing.T) {
	t.Setenv("TEST_SHORT_DIRECT_KEY_CACHE_SECRET", "too-short")
	plugin := &Plugin{config: directKeyNamespaceConfig(t, "TEST_SHORT_DIRECT_KEY_CACHE_SECRET")}
	ctx := directKeyNamespaceContext("sk-user-a")
	ctx.SetValue(CacheKey, "attacker-selected")

	cacheKey, ok := plugin.resolveCacheKey(ctx)
	if ok || cacheKey != "" {
		t.Fatalf("expected short secret to disable caching, got key=%q ok=%v", cacheKey, ok)
	}
}

// TestResolveCacheKey_EmptyDirectKeyFailsClosed verifies that an empty Direct
// Key cannot fall back to a shared cache namespace.
func TestResolveCacheKey_EmptyDirectKeyFailsClosed(t *testing.T) {
	t.Setenv("TEST_DIRECT_KEY_CACHE_SECRET", "namespace-secret-at-least-32-bytes")
	plugin := &Plugin{config: directKeyNamespaceConfig(t, "TEST_DIRECT_KEY_CACHE_SECRET")}
	ctx := directKeyNamespaceContext("")
	ctx.SetValue(CacheKey, "attacker-selected")

	cacheKey, ok := plugin.resolveCacheKey(ctx)
	if ok || cacheKey != "" {
		t.Fatalf("expected empty direct key to disable caching, got key=%q ok=%v", cacheKey, ok)
	}
}

// TestResolveCacheKey_NonDirectRequestKeepsExistingPrecedence verifies that
// ordinary requests retain the existing per-request cache-key behavior.
func TestResolveCacheKey_NonDirectRequestKeepsExistingPrecedence(t *testing.T) {
	plugin := &Plugin{config: directKeyNamespaceConfig(t, "UNUSED_DIRECT_KEY_CACHE_SECRET")}
	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, "per-request")

	cacheKey, ok := plugin.resolveCacheKey(ctx)
	if !ok || cacheKey != "per-request" {
		t.Fatalf("expected per-request key, got key=%q ok=%v", cacheKey, ok)
	}
}

// TestConfig_DirectKeyCacheSecretEnvDoesNotSerializeSecret verifies that only
// the environment variable name, never its secret value, is serialized.
func TestConfig_DirectKeyCacheSecretEnvDoesNotSerializeSecret(t *testing.T) {
	t.Setenv("BIFROST_CACHE_NAMESPACE_SECRET", "must-not-be-serialized")
	config := directKeyNamespaceConfig(t, "BIFROST_CACHE_NAMESPACE_SECRET")

	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if !strings.Contains(string(encoded), "BIFROST_CACHE_NAMESPACE_SECRET") {
		t.Fatalf("serialized config lost the environment variable name: %s", encoded)
	}
	if strings.Contains(string(encoded), "must-not-be-serialized") {
		t.Fatal("serialized config contains the HMAC secret value")
	}
}
