package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/maximhq/bifrost/plugins/otel"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// capturePluginsStore records the last config passed to UpdatePlugin so tests
// can assert that config merging occurred correctly.
type capturePluginsStore struct {
	configstore.ConfigStore
	existingPlugin  *configstoreTables.TablePlugin
	capturedConfig  map[string]any
	capturedEnabled bool
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
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("PUT")
	ctx.Request.SetBody(raw)
	ctx.SetUserValue("name", "otel")
	return ctx
}

// buildCreateRequest creates a POST /api/plugins fasthttp context.
func buildCreateRequest(t *testing.T, body any) *fasthttp.RequestCtx {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetBody(raw)
	return ctx
}

// TestCreatePlugin_RejectsCustomPathWhenAuthBypassed verifies that POST /api/plugins
// refuses to create a custom (non-builtin) plugin with a Path - the .so that would get
// dlopen()'d - when the request was let through by the auth middleware's fail-open
// bypass (dashboard auth disabled/unconfigured), and that no DB write happens.
func TestCreatePlugin_RejectsCustomPathWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &capturePluginsStore{}
	h := &PluginsHandler{
		pluginsLoader: noopPluginsLoader{},
		configStore:   store,
	}

	path := "/tmp/evil.so"
	ctx := buildCreateRequest(t, map[string]any{
		"name":    "my-custom-test-plugin",
		"enabled": true,
		"path":    path,
	})
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	h.createPlugin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if store.existingPlugin != nil {
		t.Error("CreatePlugin should not have been called on the config store")
	}
}

// TestCreatePlugin_AllowsCustomPathWhenNotBypassed verifies the same request succeeds
// for a genuinely authenticated caller (BifrostContextKeyAuthBypassed not set).
func TestCreatePlugin_AllowsCustomPathWhenNotBypassed(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &capturePluginsStore{}
	h := &PluginsHandler{
		pluginsLoader: noopPluginsLoader{},
		configStore:   store,
	}

	path := "/opt/bifrost/plugins/trusted.so"
	ctx := buildCreateRequest(t, map[string]any{
		"name":    "my-custom-test-plugin",
		"enabled": false,
		"path":    path,
	})

	h.createPlugin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if store.existingPlugin == nil {
		t.Fatal("CreatePlugin was not called on the config store")
	}
	if store.existingPlugin.Path == nil || *store.existingPlugin.Path != path {
		t.Errorf("stored plugin path = %v, want %v", store.existingPlugin.Path, path)
	}
}

// TestUpdatePlugin_RejectsCustomPathWhenAuthBypassed verifies the matching guard on
// PUT /api/plugins/{name}.
func TestUpdatePlugin_RejectsCustomPathWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &capturePluginsStore{
		existingPlugin: &configstoreTables.TablePlugin{
			Name:     "my-custom-test-plugin",
			Enabled:  false,
			IsCustom: true,
		},
	}
	h := &PluginsHandler{
		pluginsLoader: noopPluginsLoader{},
		configStore:   store,
	}

	ctx := buildUpdateRequest(t, map[string]any{
		"enabled": true,
		"path":    "/tmp/evil.so",
	})
	ctx.SetUserValue("name", "my-custom-test-plugin")
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	h.updatePlugin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if store.capturedConfig != nil || store.capturedEnabled {
		t.Error("UpdatePlugin should not have been called on the config store")
	}
}

// TestPluginConfigDialTargetsRequireGenuineAuth pins that a caller admitted without a credential
// check cannot make a built-in plugin send the gateway's secrets anywhere: an env./vault.
// reference anywhere in the config, or an outbound endpoint (the OTEL collector and metrics
// endpoints, the telemetry push gateway), may be set or changed only by a genuinely authenticated
// request. Values identical to the stored config, removals and other fields still pass, so a
// bypassed caller can toggle a plugin or edit it without moving its destination.
func TestPluginConfigDialTargetsRequireGenuineAuth(t *testing.T) {
	SetLogger(&mockLogger{})

	const collector = "https://otel.internal:4318"
	const realAuth = "Basic-REAL-SUPER-SECRET-VALUE"
	maskedAuth := schemas.NewSecretVar(realAuth).Redacted().GetValue()
	otelStored := map[string]any{"service_name": "bifrost", "collector_url": collector, "headers": map[string]any{"Authorization": realAuth}}
	telemetryStored := map[string]any{"custom_labels": []any{"team"}}

	cases := []struct {
		name      string
		plugin    string
		create    bool
		genuine   bool // a real credential, not the fail-open bypass
		stored    map[string]any
		config    map[string]any
		want      int
		wantNamed string
	}{
		{name: "create otel with a collector", plugin: "otel", create: true, config: map[string]any{"collector_url": collector}, want: fasthttp.StatusForbidden, wantNamed: "collector_url"},
		{name: "move the collector", plugin: "otel", stored: otelStored, config: map[string]any{"collector_url": "https://evil.example/v1/traces"}, want: fasthttp.StatusForbidden, wantNamed: "collector_url"},
		{name: "set a metrics endpoint", plugin: "otel", stored: otelStored, config: map[string]any{"metrics_enabled": true, "metrics_endpoint": "https://evil.example/v1/metrics"}, want: fasthttp.StatusForbidden, wantNamed: "metrics_endpoint"},
		{name: "add an env header", plugin: "otel", stored: otelStored, config: map[string]any{"headers": map[string]any{"Authorization": realAuth, "x-leak": "env.OPENAI_API_KEY"}}, want: fasthttp.StatusForbidden, wantNamed: "headers.x-leak"},
		{name: "add a vault header", plugin: "otel", stored: otelStored, config: map[string]any{"trace_headers": map[string]any{"x-leak": "vault.secret/data/openai#key"}}, want: fasthttp.StatusForbidden, wantNamed: "trace_headers.x-leak"},
		{name: "move the collector inside a profile", plugin: "otel", stored: otelStored, config: map[string]any{"profiles": []any{map[string]any{"service_name": "x", "collector_url": "https://evil.example"}}}, want: fasthttp.StatusForbidden, wantNamed: "profiles[0].collector_url"},
		{name: "set the push gateway", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"enabled": true, "push_gateway_url": "https://evil.example:9091"}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.push_gateway_url"},
		{name: "set an env basic-auth password", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": "env.PG_PASSWORD"}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.basic_auth.password"},
		{name: "same collector echoed back", plugin: "otel", stored: otelStored, config: map[string]any{"service_name": "renamed", "collector_url": collector}, want: fasthttp.StatusOK},
		{name: "masked header echoed back", plugin: "otel", stored: otelStored, config: map[string]any{"collector_url": collector, "headers": map[string]any{"Authorization": maskedAuth}}, want: fasthttp.StatusOK},
		{name: "collector removed", plugin: "otel", stored: otelStored, config: map[string]any{"collector_url": ""}, want: fasthttp.StatusOK},
		{name: "genuinely authenticated collector move", plugin: "otel", genuine: true, stored: otelStored, config: map[string]any{"collector_url": "https://otel.other.internal:4318"}, want: fasthttp.StatusOK},
		// The plugin is not loaded here, so nothing normalizes SecretVar-shaped objects into strings;
		// the plugin's loader still reads them, so the gate reads them too.
		{name: "collector as a SecretVar object on an unloaded plugin", plugin: "otel", stored: otelStored, config: map[string]any{"collector_url": map[string]any{"value": "https://evil.example/v1/traces"}}, want: fasthttp.StatusForbidden, wantNamed: "collector_url"},
		{name: "env reference in the legacy env_var shape", plugin: "otel", stored: otelStored, config: map[string]any{"headers": map[string]any{"Authorization": realAuth, "x-leak": map[string]any{"value": "", "env_var": "OPENAI_API_KEY", "from_env": true}}}, want: fasthttp.StatusForbidden, wantNamed: "headers.x-leak"},
		{name: "env reference as a SecretVar ref", plugin: "otel", stored: otelStored, config: map[string]any{"headers": map[string]any{"Authorization": realAuth, "x-leak": map[string]any{"value": "", "ref": "env.OPENAI_API_KEY", "type": "env"}}}, want: fasthttp.StatusForbidden, wantNamed: "headers.x-leak"},
		{name: "same collector echoed back as a SecretVar object", plugin: "otel", stored: otelStored, config: map[string]any{"collector_url": map[string]any{"value": collector}}, want: fasthttp.StatusOK},
		// The explicit type decides what a ref is; the env. prefix is only inferred when no type is given.
		{name: "env reference by explicit type without the prefix", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": map[string]any{"value": "", "ref": "OPENAI_API_KEY", "type": "env"}}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.basic_auth.password"},
		{name: "vault reference by explicit type without the prefix", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": map[string]any{"value": "", "ref": "secret/data/openai#key", "type": "vault"}}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.basic_auth.password"},
		// SecretVar.UnmarshalJSON accepts both object forms without a value field.
		{name: "typed env reference with no value field", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": map[string]any{"ref": "OPENAI_API_KEY", "type": "env"}}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.basic_auth.password"},
		{name: "legacy env_var reference with no value field", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": map[string]any{"env_var": "OPENAI_API_KEY", "from_env": true}}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.basic_auth.password"},
		// SecretVar.UnmarshalJSON ignores fields it does not know, so they cannot hide a reference.
		{name: "typed env reference with an extra field", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": map[string]any{"ref": "OPENAI_API_KEY", "type": "env", "extra": true}}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.basic_auth.password"},
		{name: "endpoint object with an extra field", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"push_gateway_url": map[string]any{"value": "https://evil.example:9091", "note": "x"}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.push_gateway_url"},
		// The explicit type decides, as SecretVar.UnmarshalJSON reads it: switching a referenced
		// endpoint to plain_text with a new value moves the destination.
		{name: "endpoint switched from a reference to plain_text", plugin: "telemetry", stored: map[string]any{"push_gateway": map[string]any{"push_gateway_url": map[string]any{"ref": "env.PUSH_GATEWAY_URL", "type": "env", "value": ""}}}, config: map[string]any{"push_gateway": map[string]any{"push_gateway_url": map[string]any{"ref": "env.PUSH_GATEWAY_URL", "type": "plain_text", "value": "https://evil.example:9091"}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.push_gateway_url"},
		// The same reference written as an object over a stored string is unchanged.
		{name: "unchanged reference written as an object", plugin: "otel", stored: map[string]any{"collector_url": "https://otel.internal:4318", "headers": map[string]any{"x-leak": "env.OPENAI_API_KEY"}}, config: map[string]any{"headers": map[string]any{"x-leak": map[string]any{"ref": "env.OPENAI_API_KEY", "type": "env"}}}, want: fasthttp.StatusOK},
		{name: "plain-text SecretVar with a ref-looking value", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": map[string]any{"value": "hunter2", "type": "plain_text"}}}}, want: fasthttp.StatusOK},
		// A header map is not a SecretVar because its header names happen to be SecretVar field
		// names: OTEL sends a header named ref and resolves its env. value like any other header.
		{name: "headers named like SecretVar fields", plugin: "otel", stored: otelStored, config: map[string]any{"headers": map[string]any{"ref": "env.OPENAI_API_KEY", "type": "plain_text"}}, want: fasthttp.StatusForbidden, wantNamed: "headers.ref"},
		// The typed loaders decode JSON keys case-insensitively, the way encoding/json and sonic do,
		// so a case variant of an endpoint key still sets the endpoint.
		{name: "push gateway under an uppercase key", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"enabled": true, "PUSH_GATEWAY_URL": "https://evil.example:9091"}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.PUSH_GATEWAY_URL"},
		{name: "collector under a mixed-case key", plugin: "otel", stored: otelStored, config: map[string]any{"Collector_URL": "https://evil.example/v1/traces"}, want: fasthttp.StatusForbidden, wantNamed: "Collector_URL"},
		{name: "push gateway under a case-folded key", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"pu\u017fh_gateway_url": "https://evil.example:9091"}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.pu\u017fh_gateway_url"},
		{name: "env reference under uppercase SecretVar fields", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": map[string]any{"value": "", "REF": "OPENAI_API_KEY", "TYPE": "env"}}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.basic_auth.password"},
		// Two keys folding to one field are decided by their order in the request, which the parsed
		// config no longer has, so the object counts as changed unless it is identical to the stored one.
		{name: "SecretVar field given twice in different case", plugin: "telemetry", stored: telemetryStored, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"username": "bifrost", "password": map[string]any{"ref": "PLAIN", "REF": "OPENAI_API_KEY", "type": "env"}}}}, want: fasthttp.StatusForbidden, wantNamed: "push_gateway.basic_auth.password"},
		{name: "duplicated SecretVar field echoed back unchanged", plugin: "telemetry", stored: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"password": map[string]any{"ref": "PLAIN", "REF": "OPENAI_API_KEY", "type": "env"}}}}, config: map[string]any{"push_gateway": map[string]any{"basic_auth": map[string]any{"password": map[string]any{"ref": "PLAIN", "REF": "OPENAI_API_KEY", "type": "env"}}}}, want: fasthttp.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &capturePluginsStore{}
			if tc.stored != nil {
				store.existingPlugin = &configstoreTables.TablePlugin{Name: tc.plugin, Enabled: true, Config: tc.stored}
			}
			h := &PluginsHandler{pluginsLoader: noopPluginsLoader{}, configStore: store}
			body := map[string]any{"enabled": true, "config": tc.config}
			var ctx *fasthttp.RequestCtx
			if tc.create {
				body["name"] = tc.plugin
				ctx = buildCreateRequest(t, body)
			} else {
				ctx = buildUpdateRequest(t, body)
				ctx.SetUserValue("name", tc.plugin)
			}
			if !tc.genuine {
				ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			}
			if tc.create {
				h.createPlugin(ctx)
			} else {
				h.updatePlugin(ctx)
			}

			got := ctx.Response.StatusCode()
			if tc.want == fasthttp.StatusOK && tc.create {
				tc.want = fasthttp.StatusCreated
			}
			if got != tc.want {
				t.Fatalf("status got %d, want %d; body=%s", got, tc.want, ctx.Response.Body())
			}
			if tc.want == fasthttp.StatusForbidden {
				if tc.wantNamed != "" && !bytes.Contains(ctx.Response.Body(), []byte(tc.wantNamed)) {
					t.Fatalf("the refusal must name %s, got %s", tc.wantNamed, ctx.Response.Body())
				}
				if tc.create && store.existingPlugin != nil {
					t.Fatal("a refused create must not reach the config store")
				}
				if !tc.create && store.capturedConfig != nil {
					t.Fatalf("a refused update must not reach the config store, stored %v", store.capturedConfig)
				}
				return
			}
			if !tc.create && store.capturedConfig == nil {
				t.Fatal("an admitted update must reach the config store")
			}
		})
	}
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

// A plugin that loaded despite its row saying disabled must surface as enabled=false
// with the runtime status, not be masked as "disabled". Masking it is what hid the
// enterprise loader ignoring the row: the API reported disabled while every pod ran it.
func TestBuildPluginResponseSurfacesRowRuntimeDivergence(t *testing.T) {
	h := &PluginsHandler{}
	row := &configstoreTables.TablePlugin{Name: "datadog", Enabled: false}

	t.Run("disabled row with no runtime entry reads as disabled", func(t *testing.T) {
		got := h.buildPluginResponseWithStatuses(row, map[string]schemas.PluginStatus{})
		if got.Enabled {
			t.Errorf("Enabled = true, want false")
		}
		if got.Status.Status != schemas.PluginStatusDisabled {
			t.Errorf("Status = %q, want %q", got.Status.Status, schemas.PluginStatusDisabled)
		}
	})

	t.Run("disabled row that is actually loaded reads as active", func(t *testing.T) {
		got := h.buildPluginResponseWithStatuses(row, map[string]schemas.PluginStatus{
			"datadog": {Name: "datadog", Status: schemas.PluginStatusActive},
		})
		if got.Enabled {
			t.Errorf("Enabled = true, want false (the row still says disabled)")
		}
		if got.Status.Status != schemas.PluginStatusActive {
			t.Errorf("Status = %q, want %q so the contradiction is visible", got.Status.Status, schemas.PluginStatusActive)
		}
	})
}

// A lone "*" is a legitimate header pattern, but IsRedacted treats an all-asterisk
// string as a redaction placeholder, so restoring array elements swapped it for the
// stored pattern and request_headers:["*"] captured nothing.
func TestRestoreRedactedValueKeepsStringListEntries(t *testing.T) {
	for _, tc := range []struct {
		label    string
		incoming []any
		existing []any
		want     []any
	}{
		{"lone star survives", []any{"*"}, []any{"x-unused"}, []any{"*"}},
		{"star alongside another", []any{"x-other-c", "*"}, []any{"x-unused"}, []any{"x-other-c", "*"}},
		{"short asterisk run survives", []any{"**"}, []any{"x-bench-*"}, []any{"**"}},
		{"ordinary patterns unchanged", []any{"x-bench-*"}, []any{"x-old"}, []any{"x-bench-*"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got, ok := restoreRedactedValue(tc.incoming, tc.existing).([]any)
			if !ok {
				t.Fatalf("got %T, want []any", got)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("element %d = %v, want %v (the stored value must not replace a list entry)", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Secrets are still restored when they arrive as objects in an array, which is the only
// shape a credential can take inside a list.
func TestRestoreRedactedValueStillRestoresObjectElements(t *testing.T) {
	incoming := []any{map[string]any{"value": "****"}}
	existing := []any{map[string]any{"value": "real-secret"}}

	got := restoreRedactedValue(incoming, existing).([]any)
	m, ok := got[0].(map[string]any)
	if !ok {
		t.Fatalf("element 0 = %T, want a map", got[0])
	}
	if m["value"] != "real-secret" {
		t.Errorf("value = %v, want the stored secret preserved", m["value"])
	}
}

// rollbackPluginsStore records every row the handler writes to the plugin table, so a test can see
// what an update wrote and what it restored afterwards. failCreate makes every CreatePlugin call
// fail, which is how a restore fails.
type rollbackPluginsStore struct {
	configstore.ConfigStore
	existing   *configstoreTables.TablePlugin
	updates    []configstoreTables.TablePlugin
	created    []configstoreTables.TablePlugin
	deleted    []string
	failCreate bool
}

func (s *rollbackPluginsStore) GetPlugin(_ context.Context, name string) (*configstoreTables.TablePlugin, error) {
	if s.existing != nil && s.existing.Name == name {
		return s.existing, nil
	}
	return nil, configstore.ErrNotFound
}

func (s *rollbackPluginsStore) CreatePlugin(_ context.Context, plugin *configstoreTables.TablePlugin, _ ...*gorm.DB) error {
	if s.failCreate {
		return errors.New("database is unavailable")
	}
	created := *plugin
	s.created = append(s.created, created)
	s.existing = &created
	return nil
}

func (s *rollbackPluginsStore) ExecuteTransaction(_ context.Context, fn func(tx *gorm.DB) error) error {
	return fn(nil)
}

func (s *rollbackPluginsStore) UpdatePlugin(_ context.Context, plugin *configstoreTables.TablePlugin, _ ...*gorm.DB) error {
	s.updates = append(s.updates, *plugin)
	return nil
}

func (s *rollbackPluginsStore) DeletePlugin(_ context.Context, name string, _ ...*gorm.DB) error {
	s.deleted = append(s.deleted, name)
	return nil
}

// failingReloadLoader refuses to load any plugin with err, as a plugin whose Init fails does.
type failingReloadLoader struct {
	noopPluginsLoader
	err error
}

func (l failingReloadLoader) ReloadPlugin(_ context.Context, _ string, _ *string, _ any, _ *schemas.PluginPlacement, _ *int) error {
	return l.err
}

// errorRecordingLogger keeps every Error log line so a test can assert on what was reported.
type errorRecordingLogger struct {
	mockLogger
	errors []string
}

func (l *errorRecordingLogger) Error(format string, args ...any) {
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}

// errNamespaceDimensionMismatch is what the semantic cache plugin's Init returns when its
// namespace already holds vectors of another size.
var errNamespaceDimensionMismatch = fmt.Errorf("failed to create namespace for semantic cache: %w",
	fmt.Errorf("%w: namespace %q already exists with dimension 1536 but config requires 3072", vectorstore.ErrDimensionMismatch, "Cache"))

func semanticCacheRow() *configstoreTables.TablePlugin {
	return &configstoreTables.TablePlugin{
		Name:    "semantic_cache",
		Enabled: true,
		Config:  map[string]any{"provider": "openai", "embedding_model": "text-embedding-3-small", "dimension": 1536},
	}
}

func semanticCacheUpdateRequest(t *testing.T) *fasthttp.RequestCtx {
	t.Helper()
	ctx := buildUpdateRequest(t, map[string]any{
		"enabled": true,
		"config":  map[string]any{"provider": "openai", "embedding_model": "text-embedding-3-large", "dimension": 3072},
	})
	ctx.SetUserValue("name", "semantic_cache")
	return ctx
}

// A plugin update the plugin cannot load with must leave the saved row as it was, so the saved
// config always describes the instance still running; and a configuration the store rejects is the
// caller's mistake, answered with 400 rather than 500.
func TestUpdatePlugin_LoadFailureRestoresPreviousRow(t *testing.T) {
	cases := []struct {
		name       string
		loadErr    error
		wantStatus int
	}{
		{"store failure answers 500", errors.New("vector store unreachable"), fasthttp.StatusInternalServerError},
		{"dimension mismatch answers 400", errNamespaceDimensionMismatch, fasthttp.StatusBadRequest},
		{"refused config answers 400", fmt.Errorf("%w: semantic_cache plugin requires 'embedding_model' when 'provider' is set", lib.ErrInvalidPluginConfig), fasthttp.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			previous := semanticCacheRow()
			store := &rollbackPluginsStore{existing: previous}
			h := &PluginsHandler{pluginsLoader: failingReloadLoader{err: tc.loadErr}, configStore: store}

			ctx := semanticCacheUpdateRequest(t)
			h.updatePlugin(ctx)

			if got := ctx.Response.StatusCode(); got != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", got, tc.wantStatus, ctx.Response.Body())
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
				t.Fatalf("decode error response %s: %v", ctx.Response.Body(), err)
			}
			if !strings.Contains(body.Error.Message, tc.loadErr.Error()) {
				t.Errorf("error message %q does not carry the load error %q", body.Error.Message, tc.loadErr)
			}
			if len(store.updates) != 1 {
				t.Fatalf("expected one write of the new row, got %d", len(store.updates))
			}
			if got := fmt.Sprint(store.updates[0].Config.(map[string]any)["dimension"]); got != "3072" {
				t.Fatalf("first write dimension = %s, want the requested 3072", got)
			}
			if len(store.deleted) != 1 || len(store.created) != 1 {
				t.Fatalf("expected the new row replaced by the previous one, got deletes %v and %d creates", store.deleted, len(store.created))
			}
			restored := store.created[0]
			if restored.Enabled != previous.Enabled || fmt.Sprint(restored.Config.(map[string]any)["dimension"]) != "1536" {
				t.Fatalf("restored row = enabled %v dimension %v, want the previous enabled %v dimension 1536",
					restored.Enabled, restored.Config.(map[string]any)["dimension"], previous.Enabled)
			}
		})
	}
}

// A failed update must leave the row exactly as it was. Restoring through UpdatePlugin cannot: it
// reads a nil placement or order as "keep the current value", so an update that added one would
// survive its own failure and take effect on the next restart or reload.
func TestUpdatePlugin_LoadFailureRestoresTheExactPreviousRow(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/config.db"},
	}, &mockLogger{})
	if err != nil {
		t.Fatalf("failed to open config store: %v", err)
	}
	if err := store.CreatePlugin(ctx, semanticCacheRow()); err != nil {
		t.Fatalf("failed to seed the plugin row: %v", err)
	}
	h := &PluginsHandler{pluginsLoader: failingReloadLoader{err: errNamespaceDimensionMismatch}, configStore: store}

	req := initedUpdateRequest(t, "semantic_cache", map[string]any{
		"enabled":   true,
		"config":    map[string]any{"provider": "openai", "embedding_model": "text-embedding-3-large", "dimension": 3072},
		"placement": "pre_builtin",
		"order":     5,
	})
	h.updatePlugin(req)

	if got := req.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", got, req.Response.Body())
	}
	row, err := store.GetPlugin(ctx, "semantic_cache")
	if err != nil {
		t.Fatalf("failed to read the plugin row: %v", err)
	}
	if row.Placement != nil {
		t.Fatalf("restored row kept the failed update's placement %q, want it unset", *row.Placement)
	}
	if row.Order != nil {
		t.Fatalf("restored row kept the failed update's order %d, want it unset", *row.Order)
	}
	if got := fmt.Sprint(row.Config.(map[string]any)["dimension"]); got != "1536" {
		t.Fatalf("restored row dimension = %s, want the previous 1536", got)
	}
}

// initedUpdateRequest builds a PUT for the named plugin initialised the way a server does, so a
// handler backed by a real store can use it as its request context.
func initedUpdateRequest(t *testing.T, name string, body any) *fasthttp.RequestCtx {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	var httpReq fasthttp.Request
	httpReq.Header.SetMethod("PUT")
	httpReq.SetBody(raw)
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&httpReq, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	ctx.SetUserValue("name", name)
	return ctx
}

// gatedReloadLoader holds the load of a semantic_cache config at dimension 3072 until release is
// closed and then refuses it, as a namespace of another size does; every other config loads.
type gatedReloadLoader struct {
	noopPluginsLoader
	entered chan struct{}
	release chan struct{}
}

func (l *gatedReloadLoader) ReloadPlugin(_ context.Context, _ string, _ *string, cfg any, _ *schemas.PluginPlacement, _ *int) error {
	if fmt.Sprint(cfg.(map[string]any)["dimension"]) != "3072" {
		return nil
	}
	close(l.entered)
	<-l.release
	return errNamespaceDimensionMismatch
}

// A failed update restores the row it found, so it must not interleave with another save of the
// same plugin: a second update that saves and loads while the first is still loading would have
// its saved config overwritten by the first one's restore, leaving the database describing a
// config that no longer runs.
func TestUpdatePlugin_ConcurrentSavesOfOnePluginDoNotInterleave(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/config.db"},
	}, &mockLogger{})
	if err != nil {
		t.Fatalf("failed to open config store: %v", err)
	}
	if err := store.CreatePlugin(ctx, semanticCacheRow()); err != nil {
		t.Fatalf("failed to seed the plugin row: %v", err)
	}
	loader := &gatedReloadLoader{entered: make(chan struct{}), release: make(chan struct{})}
	h := &PluginsHandler{pluginsLoader: loader, configStore: store}

	failing := initedUpdateRequest(t, "semantic_cache", map[string]any{
		"enabled": true,
		"config":  map[string]any{"provider": "openai", "embedding_model": "text-embedding-3-large", "dimension": 3072},
	})
	failingDone := make(chan struct{})
	go func() {
		defer close(failingDone)
		h.updatePlugin(failing)
	}()
	<-loader.entered

	succeeding := initedUpdateRequest(t, "semantic_cache", map[string]any{
		"enabled": true,
		"config":  map[string]any{"provider": "openai", "embedding_model": "text-embedding-3-small", "dimension": 512},
	})
	succeedingDone := make(chan struct{})
	go func() {
		defer close(succeedingDone)
		h.updatePlugin(succeeding)
	}()
	// Unserialized, the second save finishes while the first is still loading. Serialized, it
	// waits for the first, so give it a moment either way before letting the first fail.
	select {
	case <-succeedingDone:
	case <-time.After(500 * time.Millisecond):
	}
	close(loader.release)
	<-failingDone
	<-succeedingDone

	if got := failing.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("failing update status = %d, want 400: %s", got, failing.Response.Body())
	}
	if got := succeeding.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("succeeding update status = %d, want 200: %s", got, succeeding.Response.Body())
	}
	row, err := store.GetPlugin(ctx, "semantic_cache")
	if err != nil {
		t.Fatalf("failed to read the plugin row: %v", err)
	}
	if got := fmt.Sprint(row.Config.(map[string]any)["dimension"]); got != "512" {
		t.Fatalf("saved dimension = %s, want 512 from the update that loaded", got)
	}
}

// An update that created the row itself (no row existed) removes it again when the plugin fails to
// load, so a failed first save leaves nothing behind.
func TestUpdatePlugin_LoadFailureWithoutPreviousRowRemovesIt(t *testing.T) {
	SetLogger(&mockLogger{})
	store := &rollbackPluginsStore{}
	h := &PluginsHandler{pluginsLoader: failingReloadLoader{err: errNamespaceDimensionMismatch}, configStore: store}

	ctx := semanticCacheUpdateRequest(t)
	h.updatePlugin(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", got, ctx.Response.Body())
	}
	if len(store.deleted) != 1 || store.deleted[0] != "semantic_cache" {
		t.Fatalf("expected the row the update created to be deleted, got deletes %v", store.deleted)
	}
}

// When the previous row cannot be restored, the saved config no longer matches the running
// instance; that must reach the server log as an error naming the plugin.
func TestUpdatePlugin_LogsErrorWhenRestoreFails(t *testing.T) {
	logger := &errorRecordingLogger{}
	SetLogger(logger)
	store := &rollbackPluginsStore{existing: semanticCacheRow(), failCreate: true}
	h := &PluginsHandler{pluginsLoader: failingReloadLoader{err: errNamespaceDimensionMismatch}, configStore: store}

	ctx := semanticCacheUpdateRequest(t)
	h.updatePlugin(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", got, ctx.Response.Body())
	}
	var logged bool
	for _, line := range logger.errors {
		if strings.Contains(line, "restore") && strings.Contains(line, "semantic_cache") && strings.Contains(line, "database is unavailable") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("expected an error log naming the plugin and the failed restore, got %q", logger.errors)
	}
}

// A create whose config the store rejects is the caller's mistake too: 400, and the row is removed.
func TestCreatePlugin_DimensionMismatchAnswers400(t *testing.T) {
	SetLogger(&mockLogger{})
	store := &rollbackPluginsStore{}
	h := &PluginsHandler{pluginsLoader: failingReloadLoader{err: errNamespaceDimensionMismatch}, configStore: store}

	ctx := buildCreateRequest(t, map[string]any{
		"name":    "semantic_cache",
		"enabled": true,
		"config":  map[string]any{"provider": "openai", "embedding_model": "text-embedding-3-large", "dimension": 3072},
	})
	h.createPlugin(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", got, ctx.Response.Body())
	}
	if !strings.Contains(string(ctx.Response.Body()), "already exists with dimension 1536 but config requires 3072") {
		t.Errorf("response %s does not carry the mismatch", ctx.Response.Body())
	}
	if len(store.deleted) != 1 || store.deleted[0] != "semantic_cache" {
		t.Fatalf("expected the created row to be removed, got deletes %v", store.deleted)
	}
}

// otelMarshallerLoader is noopPluginsLoader with the real OTEL storage marshaller installed for
// "otel", as a loaded OTEL plugin would have it: a legacy single-profile config is normalized into
// profiles[0] on the way to storage.
type otelMarshallerLoader struct{ noopPluginsLoader }

func (otelMarshallerLoader) NormalizePluginConfig(name string, config map[string]any) (map[string]any, error) {
	if name != otel.PluginName {
		return nil, nil
	}
	return (&otel.OtelPlugin{}).MarshalConfigForStorage(config)
}

// A plugin provisioned through config.json in the legacy single-profile shape is stored as written,
// while an update is normalized into profiles[0]. The stored config is read the same way before
// the two are compared, so a bypassed caller toggling the plugin or echoing its unchanged
// collector is not refused for a destination that did not move, and a moved collector still is.
func TestPluginConfigDialTargetsCompareNormalizedStoredConfig(t *testing.T) {
	SetLogger(&mockLogger{})
	const collector = "https://otel.internal:4318"
	legacy := map[string]any{"service_name": "bifrost", "collector_url": collector, "trace_type": "otel", "protocol": "http"}
	for _, tc := range []struct {
		name   string
		config map[string]any
		want   int
	}{
		{"toggle with no config", map[string]any{}, fasthttp.StatusOK},
		{"unchanged collector echoed back", map[string]any{"service_name": "bifrost", "collector_url": collector}, fasthttp.StatusOK},
		{"collector moved", map[string]any{"collector_url": "https://evil.example/v1/traces"}, fasthttp.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &capturePluginsStore{existingPlugin: &configstoreTables.TablePlugin{Name: otel.PluginName, Enabled: true, Config: legacy}}
			h := &PluginsHandler{pluginsLoader: otelMarshallerLoader{}, configStore: store}
			ctx := buildUpdateRequest(t, map[string]any{"enabled": false, "config": tc.config})
			ctx.SetUserValue("name", otel.PluginName)
			ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			h.updatePlugin(ctx)
			if got := ctx.Response.StatusCode(); got != tc.want {
				t.Fatalf("status got %d, want %d; body=%s", got, tc.want, ctx.Response.Body())
			}
		})
	}
}

// An update for a plugin with no row used to create an empty row before the request was read, so
// a refused update still left a row behind and a later create of that name met a conflict. A
// refused update now writes nothing.
func TestUpdatePluginRefusedForAMissingPluginWritesNothing(t *testing.T) {
	SetLogger(&mockLogger{})
	store := &capturePluginsStore{}
	h := &PluginsHandler{pluginsLoader: noopPluginsLoader{}, configStore: store}
	ctx := buildUpdateRequest(t, map[string]any{"enabled": true, "config": map[string]any{"collector_url": "https://evil.example/v1/traces"}})
	ctx.SetUserValue("name", "otel")
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
	h.updatePlugin(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if store.existingPlugin != nil {
		t.Fatalf("a refused update must not create the plugin row, created %+v", store.existingPlugin)
	}

	// A malformed body for a missing plugin writes nothing either.
	bad := &fasthttp.RequestCtx{}
	bad.Request.Header.SetMethod("PUT")
	bad.Request.SetBody([]byte("{not json"))
	bad.SetUserValue("name", "otel")
	h.updatePlugin(bad)
	if bad.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", bad.Response.StatusCode(), bad.Response.Body())
	}
	if store.existingPlugin != nil {
		t.Fatalf("a malformed update must not create the plugin row, created %+v", store.existingPlugin)
	}
}

// auditPluginsStore adds DeletePlugin to capturePluginsStore.
type auditPluginsStore struct{ capturePluginsStore }

func (s *auditPluginsStore) DeletePlugin(_ context.Context, _ string, _ ...*gorm.DB) error {
	return nil
}

// failingRemovePluginsLoader fails RemovePlugin the way a runtime that cannot stop a plugin would.
type failingRemovePluginsLoader struct{ noopPluginsLoader }

func (failingRemovePluginsLoader) RemovePlugin(_ context.Context, _ string) error {
	return errors.New("stop failed")
}

func TestUpdatePlugin_AuditsCreationWhenPluginMissing(t *testing.T) {
	SetLogger(&mockLogger{})
	h := &PluginsHandler{pluginsLoader: noopPluginsLoader{}, configStore: &capturePluginsStore{}}

	ctx := buildUpdateRequest(t, map[string]any{"enabled": true, "config": map[string]any{"collector_url": "localhost:4317", "trace_type": "genai_extension", "protocol": "grpc"}})
	h.updatePlugin(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	requireSingleAuditChange(t, ctx, "plugin.created")
}

func TestDeletePlugin_AuditsCommittedDeleteWhenRuntimeRemovalFails(t *testing.T) {
	SetLogger(&mockLogger{})
	store := &auditPluginsStore{capturePluginsStore{existingPlugin: &configstoreTables.TablePlugin{Name: "otel", Enabled: true}}}
	h := &PluginsHandler{pluginsLoader: failingRemovePluginsLoader{}, configStore: store}

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("name", "otel")
	h.deletePlugin(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected 500, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	requireSingleAuditChange(t, ctx, "plugin.deleted")
}

func TestUpdatePlugin_AuditsCommittedUpdateWhenRuntimeStopFails(t *testing.T) {
	SetLogger(&mockLogger{})
	store := &capturePluginsStore{existingPlugin: &configstoreTables.TablePlugin{Name: "otel", Enabled: true, Config: map[string]any{}}}
	h := &PluginsHandler{pluginsLoader: failingRemovePluginsLoader{}, configStore: store}

	ctx := buildUpdateRequest(t, map[string]any{"enabled": false})
	h.updatePlugin(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected 500, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	requireSingleAuditChange(t, ctx, "plugin.updated")
}
