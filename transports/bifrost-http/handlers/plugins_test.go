package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// capturePluginsStore records the last config passed to UpdatePlugin so tests
// can assert that config merging occurred correctly.
type capturePluginsStore struct {
	configstore.ConfigStore
	existingPlugin    *configstoreTables.TablePlugin
	capturedConfig    map[string]any
	capturedEnabled   bool
	capturedPath      *string
	capturedPlacement *schemas.PluginPlacement
	capturedOrder     *int
}

func (s *capturePluginsStore) GetPlugin(_ context.Context, name string) (*configstoreTables.TablePlugin, error) {
	if s.existingPlugin != nil && s.existingPlugin.Name == name {
		return s.existingPlugin, nil
	}
	return nil, configstore.ErrNotFound
}

func (s *capturePluginsStore) UpdatePlugin(_ context.Context, plugin *configstoreTables.TablePlugin, _ ...*gorm.DB) error {
	if cfg, ok := plugin.Config.(map[string]any); ok {
		s.capturedConfig = cfg
	}
	s.capturedEnabled = plugin.Enabled
	s.capturedPath = plugin.Path
	s.capturedPlacement = plugin.Placement
	s.capturedOrder = plugin.Order
	return nil
}

func (s *capturePluginsStore) CreatePlugin(_ context.Context, plugin *configstoreTables.TablePlugin, _ ...*gorm.DB) error {
	s.existingPlugin = plugin
	return nil
}

// noopPluginsLoader satisfies the PluginsLoader interface without doing anything.
type noopPluginsLoader struct{}

func (noopPluginsLoader) ReloadPlugin(_ context.Context, _ string, _ *string, _ any, _ *schemas.PluginPlacement, _ *int) error {
	return nil
}
func (noopPluginsLoader) RemovePlugin(_ context.Context, _ string) error { return nil }
func (noopPluginsLoader) GetPluginStatus(_ context.Context) map[string]schemas.PluginStatus {
	return nil
}
func (noopPluginsLoader) GetLoadedPluginNames() []string { return nil }
func (noopPluginsLoader) NormalizePluginConfig(_ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

func (noopPluginsLoader) ExpandPluginConfigForAPI(_ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

// buildUpdateRequest creates a PUT /api/plugins/{name} fasthttp context.
func buildUpdateRequest(t *testing.T, body any) *fasthttp.RequestCtx {
	t.Helper()
	return buildUpdateRequestFor(t, "otel", body)
}

// buildUpdateRequestFor is buildUpdateRequest for a named plugin. Custom plugins need
// their own name because built-ins have their path forced to nil by design.
func buildUpdateRequestFor(t *testing.T, name string, body any) *fasthttp.RequestCtx {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("PUT")
	ctx.Request.SetBody(raw)
	ctx.SetUserValue("name", name)
	return ctx
}

// TestUpdatePlugin_ConfigMerge verifies that updatePlugin merges the incoming
// config over the existing DB config, preserving fields the caller did not send.
// This is critical for the plugin_span_filter field: the OTEL config form in the
// UI does not send plugin_span_filter, so it must survive a save without being wiped.
// TestRestoreRedacted_OTELProfilesHeaders covers the two gaps that broke OTEL header
// round-trips after the multi-profile change: (1) headers live inside the `profiles`
// array (slice traversal), and (2) header values are plain redacted strings, not EnvVar
// objects. Saving a config whose headers came back redacted must not overwrite the
// stored credentials.
func TestRestoreRedacted_OTELProfilesHeaders(t *testing.T) {
	realAuth := "Basic-REAL-SUPER-SECRET-VALUE"
	realVersion := "4"
	maskedAuth := schemas.NewSecretVar(realAuth).Redacted().GetValue()       // long -> first4 + **** + last4
	maskedVersion := schemas.NewSecretVar(realVersion).Redacted().GetValue() // "4" -> "*"

	mkConfig := func(auth, version string) map[string]any {
		return map[string]any{
			"profiles": []any{
				map[string]any{
					"service_name": "langfuse",
					"headers": map[string]any{
						"Authorization":                auth,
						"x-langfuse-ingestion-version": version,
					},
				},
			},
		}
	}

	existing := mkConfig(realAuth, realVersion)
	incoming := mkConfig(maskedAuth, maskedVersion) // what the UI sends back after a redacted GET

	got := restoreRedactedFromExisting(incoming, existing)
	headers := got["profiles"].([]any)[0].(map[string]any)["headers"].(map[string]any)

	if headers["Authorization"] != realAuth {
		t.Errorf("Authorization not restored: got %q, want %q", headers["Authorization"], realAuth)
	}
	if headers["x-langfuse-ingestion-version"] != realVersion {
		t.Errorf("version not restored: got %q, want %q", headers["x-langfuse-ingestion-version"], realVersion)
	}

	// A genuinely changed (non-redacted) header value must pass through untouched.
	changed := mkConfig("Basic-A-BRAND-NEW-KEY-VALUE-1234", "3")
	got2 := restoreRedactedFromExisting(changed, existing)
	headers2 := got2["profiles"].([]any)[0].(map[string]any)["headers"].(map[string]any)
	if headers2["Authorization"] != "Basic-A-BRAND-NEW-KEY-VALUE-1234" {
		t.Errorf("new Authorization should pass through, got %q", headers2["Authorization"])
	}
	if headers2["x-langfuse-ingestion-version"] != "3" {
		t.Errorf("new version should pass through, got %q", headers2["x-langfuse-ingestion-version"])
	}

	// An intentional env.* reference (e.g. credential rotation) must pass through.
	// NewSecretVar parses the "env." prefix as FromEnv=true, which IsRedacted reports as
	// redacted; the IsFromEnv guard must let it through rather than restoring the stored value.
	rotated := mkConfig("env.NEW_TOKEN", "env.NEW_VERSION")
	got3 := restoreRedactedFromExisting(rotated, existing)
	headers3 := got3["profiles"].([]any)[0].(map[string]any)["headers"].(map[string]any)
	if headers3["Authorization"] != "env.NEW_TOKEN" {
		t.Errorf("env.* Authorization should pass through, got %q", headers3["Authorization"])
	}
	if headers3["x-langfuse-ingestion-version"] != "env.NEW_VERSION" {
		t.Errorf("env.* version should pass through, got %q", headers3["x-langfuse-ingestion-version"])
	}
}

// TestRestoreRedacted_KafkaSecretVarObjects covers the Kafka connector shape: secrets are
// stored in the DB as plain strings, but the redacted GET returns plain-text SecretVars as
// value-only objects ({"value": "supe…cret"} — ref/type are omitempty). Saving that back
// must restore the stored string, not persist the mask.
func TestRestoreRedacted_KafkaSecretVarObjects(t *testing.T) {
	realPassword := "REAL-KAFKA-SASL-PASSWORD-123"
	realCACert := "-----BEGIN CERTIFICATE-----\nREAL\n-----END CERTIFICATE-----"
	maskedPassword := schemas.NewSecretVar(realPassword).Redacted().GetValue()
	maskedCACert := schemas.NewSecretVar(realCACert).Redacted().GetValue()

	// What MarshalForStorage stored in the DB.
	existing := map[string]any{
		"brokers": []any{"localhost:9092"},
		"ca_cert": realCACert,
		"sasl": map[string]any{
			"mechanism": "PLAIN",
			"username":  "kafka-user",
			"password":  realPassword,
		},
	}
	// What the UI sends back after a redacted GET, untouched.
	incoming := map[string]any{
		"brokers": []any{"localhost:9092"},
		"ca_cert": map[string]any{"value": maskedCACert},
		"sasl": map[string]any{
			"mechanism": "PLAIN",
			"username":  map[string]any{"value": "kafka-user"},
			"password":  map[string]any{"value": maskedPassword},
		},
	}

	got := restoreRedactedFromExisting(incoming, existing)
	if got["ca_cert"] != realCACert {
		t.Errorf("ca_cert not restored: got %v, want %q", got["ca_cert"], realCACert)
	}
	sasl := got["sasl"].(map[string]any)
	if sasl["password"] != realPassword {
		t.Errorf("sasl.password not restored: got %v, want %q", sasl["password"], realPassword)
	}
	// A non-redacted value-only object (username shown in clear) must pass through.
	if u, ok := sasl["username"].(map[string]any); !ok || u["value"] != "kafka-user" {
		t.Errorf("sasl.username should pass through unchanged, got %v", sasl["username"])
	}

	// A genuinely rotated password ({"value": ..., "ref": ""} as SecretVarInput emits)
	// must pass through, not be clobbered by the stored value.
	rotated := map[string]any{
		"sasl": map[string]any{
			"password": map[string]any{"value": "A-BRAND-NEW-PASSWORD-5678", "ref": ""},
		},
	}
	got2 := restoreRedactedFromExisting(rotated, existing)
	p, ok := got2["sasl"].(map[string]any)["password"].(map[string]any)
	if !ok || p["value"] != "A-BRAND-NEW-PASSWORD-5678" {
		t.Errorf("new password should pass through, got %v", got2["sasl"].(map[string]any)["password"])
	}

	// Switching to an env reference must pass through (intentional update).
	envRef := map[string]any{
		"sasl": map[string]any{
			"password": map[string]any{"value": "", "ref": "env.KAFKA_PASSWORD", "type": "env"},
		},
	}
	got3 := restoreRedactedFromExisting(envRef, existing)
	p3, ok := got3["sasl"].(map[string]any)["password"].(map[string]any)
	if !ok || p3["ref"] != "env.KAFKA_PASSWORD" {
		t.Errorf("env ref password should pass through, got %v", got3["sasl"].(map[string]any)["password"])
	}
}

// TestRestoreRedacted_FullyRedactedSentinel covers the telemetry (Prometheus push gateway)
// password shape: FullyRedacted() returns the fixed "<REDACTED>" sentinel instead of the
// prefix/suffix mask, and the stored value is a plain string.
func TestRestoreRedacted_FullyRedactedSentinel(t *testing.T) {
	realPassword := "REAL-PUSHGATEWAY-PASSWORD"
	sentinel := schemas.NewSecretVar(realPassword).FullyRedacted().GetValue()

	existing := map[string]any{
		"push_gateway": map[string]any{
			"basic_auth": map[string]any{"username": "pgw-user", "password": realPassword},
		},
	}
	incoming := map[string]any{
		"push_gateway": map[string]any{
			"basic_auth": map[string]any{
				"username": map[string]any{"value": "pgw-user"},
				"password": map[string]any{"value": sentinel},
			},
		},
	}

	got := restoreRedactedFromExisting(incoming, existing)
	ba := got["push_gateway"].(map[string]any)["basic_auth"].(map[string]any)
	if ba["password"] != realPassword {
		t.Errorf("sentinel password not restored: got %v, want %q", ba["password"], realPassword)
	}
}

func TestUpdatePlugin_ConfigMerge(t *testing.T) {
	SetLogger(&mockLogger{})

	spanFilter := map[string]any{
		"mode":    "exclude",
		"plugins": []any{"logging", "compat"},
	}
	existingConfig := map[string]any{
		"collector_url":      "localhost:4317",
		"trace_type":         "genai_extension",
		"protocol":           "grpc",
		"plugin_span_filter": spanFilter,
	}

	store := &capturePluginsStore{
		existingPlugin: &configstoreTables.TablePlugin{
			Name:    "otel",
			Enabled: true,
			Config:  existingConfig,
		},
	}

	h := &PluginsHandler{
		pluginsLoader: noopPluginsLoader{},
		configStore:   store,
	}

	// The UI OTEL form sends only the base fields — no plugin_span_filter.
	reqBody := map[string]any{
		"enabled": true,
		"config": map[string]any{
			"collector_url": "new-collector:4317",
			"trace_type":    "open_inference",
			"protocol":      "grpc",
		},
	}

	ctx := buildUpdateRequest(t, reqBody)
	h.updatePlugin(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	// The merged config must contain both the updated base fields AND the preserved filter.
	if store.capturedConfig == nil {
		t.Fatal("UpdatePlugin was not called")
	}
	if got := store.capturedConfig["collector_url"]; got != "new-collector:4317" {
		t.Errorf("collector_url = %v, want new-collector:4317", got)
	}
	if got := store.capturedConfig["trace_type"]; got != "open_inference" {
		t.Errorf("trace_type = %v, want open_inference", got)
	}
	if _, ok := store.capturedConfig["plugin_span_filter"]; !ok {
		t.Error("plugin_span_filter was wiped from the config; merge logic is broken")
	}
}

// TestUpdatePlugin_ConfigMerge_NewPlugin verifies that when no existing plugin
// is found in the DB (first save), the incoming config is used as-is.
func TestUpdatePlugin_ConfigMerge_NewPlugin(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &capturePluginsStore{existingPlugin: nil}
	h := &PluginsHandler{
		pluginsLoader: noopPluginsLoader{},
		configStore:   store,
	}

	reqBody := map[string]any{
		"enabled": true,
		"config": map[string]any{
			"collector_url": "localhost:4317",
			"trace_type":    "genai_extension",
			"protocol":      "grpc",
		},
	}

	ctx := buildUpdateRequest(t, reqBody)
	h.updatePlugin(ctx)

	// Should succeed even when no existing plugin is found (creates then updates).
	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

// namedPluginsLoader is a noopPluginsLoader that returns a fixed set of loaded
// plugin names, used to assert the getLoadedPlugins response contract.
type namedPluginsLoader struct {
	noopPluginsLoader
	names []string
}

func (l namedPluginsLoader) GetLoadedPluginNames() []string { return l.names }

// TestGetLoadedPlugins verifies that getLoadedPlugins returns the loader's plugin
// names under the "plugins" JSON key, locking the response shape the UI depends on.
func TestGetLoadedPlugins(t *testing.T) {
	want := []string{"logging", "telemetry", "enterprise-governance"}
	h := &PluginsHandler{
		pluginsLoader: namedPluginsLoader{names: want},
		configStore:   nil,
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	h.getLoadedPlugins(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	var response struct {
		Plugins []string `json:"plugins"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(response.Plugins) != len(want) {
		t.Fatalf("expected %d plugins, got %d: %v", len(want), len(response.Plugins), response.Plugins)
	}
	for i, name := range want {
		if response.Plugins[i] != name {
			t.Errorf("plugins[%d] = %q, want %q", i, response.Plugins[i], name)
		}
	}
}

// TestUpdatePlugin_PreservesPathWhenOmitted verifies that a PUT which does not carry
// `path` leaves the stored path intact. The update-plugin sheet in the UI sends only
// `enabled` and `config`; clearing the path on such a request turned a custom plugin
// into a built-in lookup, which then failed with "unknown built-in plugin" while the
// already loaded instance kept running with its old config.
func TestUpdatePlugin_PreservesPathWhenOmitted(t *testing.T) {
	SetLogger(&mockLogger{})

	storedPath := "/app/plugins/custom.so"
	placement := schemas.PluginPlacementPreBuiltin
	order := 3

	store := &capturePluginsStore{
		existingPlugin: &configstoreTables.TablePlugin{
			Name:      "custom-probe",
			Enabled:   true,
			Config:    map[string]any{"tag": "one"},
			Path:      &storedPath,
			Placement: &placement,
			Order:     &order,
			IsCustom:  true,
		},
	}

	h := &PluginsHandler{
		pluginsLoader: noopPluginsLoader{},
		configStore:   store,
	}

	// Only enabled and config, exactly what the update-plugin sheet sends.
	ctx := buildUpdateRequestFor(t, "custom-probe", map[string]any{
		"enabled": true,
		"config":  map[string]any{"tag": "two"},
	})
	h.updatePlugin(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if store.capturedPath == nil {
		t.Fatal("path was cleared by an update that did not send it")
	}
	if *store.capturedPath != storedPath {
		t.Errorf("path = %q, want %q", *store.capturedPath, storedPath)
	}
	if store.capturedPlacement == nil || *store.capturedPlacement != placement {
		t.Errorf("placement = %v, want %v", store.capturedPlacement, placement)
	}
	if store.capturedOrder == nil || *store.capturedOrder != order {
		t.Errorf("order = %v, want %d", store.capturedOrder, order)
	}
	if got := store.capturedConfig["tag"]; got != "two" {
		t.Errorf("tag = %v, want two", got)
	}
}

// TestUpdatePlugin_ClearsPathWhenExplicitlyEmpty verifies that a caller can still
// detach a path by sending it as an empty string, which the handler normalizes to nil.
func TestUpdatePlugin_ClearsPathWhenExplicitlyEmpty(t *testing.T) {
	SetLogger(&mockLogger{})

	storedPath := "/app/plugins/custom.so"
	store := &capturePluginsStore{
		existingPlugin: &configstoreTables.TablePlugin{
			Name:     "custom-probe",
			Enabled:  true,
			Config:   map[string]any{},
			Path:     &storedPath,
			IsCustom: true,
		},
	}

	h := &PluginsHandler{
		pluginsLoader: noopPluginsLoader{},
		configStore:   store,
	}

	ctx := buildUpdateRequestFor(t, "custom-probe", map[string]any{
		"enabled": true,
		"path":    "",
		"config":  map[string]any{},
	})
	h.updatePlugin(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if store.capturedPath != nil {
		t.Errorf("path = %q, want nil after an explicit empty path", *store.capturedPath)
	}
}

// TestUpdatePlugin_PreservesPathWhenNull verifies that an explicit JSON null for `path`
// is treated the same as omitting it. Null would otherwise persist a nil path and send
// a custom plugin back down the built-in branch, which is the same failure as omitting
// the field. An empty string remains the way to detach a path.
func TestUpdatePlugin_PreservesPathWhenNull(t *testing.T) {
	SetLogger(&mockLogger{})

	storedPath := "/app/plugins/custom.so"
	store := &capturePluginsStore{
		existingPlugin: &configstoreTables.TablePlugin{
			Name:     "custom-probe",
			Enabled:  true,
			Config:   map[string]any{},
			Path:     &storedPath,
			IsCustom: true,
		},
	}

	h := &PluginsHandler{
		pluginsLoader: noopPluginsLoader{},
		configStore:   store,
	}

	ctx := buildUpdateRequestFor(t, "custom-probe", map[string]any{
		"enabled": true,
		"path":    nil,
		"config":  map[string]any{},
	})
	h.updatePlugin(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if store.capturedPath == nil {
		t.Fatal("path was cleared by an explicit null; null must behave like an omitted field")
	}
	if *store.capturedPath != storedPath {
		t.Errorf("path = %q, want %q", *store.capturedPath, storedPath)
	}
}
