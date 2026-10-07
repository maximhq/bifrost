package handlers

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
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
	capturedPlugin  *configstoreTables.TablePlugin
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
	s.capturedPlugin = plugin
	s.existingPlugin = plugin
	return nil
}

func (s *capturePluginsStore) GetPlugins(_ context.Context) ([]*configstoreTables.TablePlugin, error) {
	if s.existingPlugin == nil {
		return nil, nil
	}
	return []*configstoreTables.TablePlugin{s.existingPlugin}, nil
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

type orderingPluginsLoader struct {
	noopPluginsLoader
	reloaded  bool
	placement *schemas.PluginPlacement
	order     *int
}

func (l *orderingPluginsLoader) ReloadPlugin(_ context.Context, _ string, _ *string, _ any, placement *schemas.PluginPlacement, order *int) error {
	l.reloaded = true
	l.placement = placement
	l.order = order
	return nil
}

// TestPluginsHandler_PersistsEffectiveOrder pins the ordering metadata passed to
// storage, hot reload, and API responses for both create and update requests.
func TestPluginsHandler_PersistsEffectiveOrder(t *testing.T) {
	SetLogger(&mockLogger{})
	tests := []struct {
		name          string
		plugin        string
		placement     *schemas.PluginPlacement
		order         *int
		wantPlacement *schemas.PluginPlacement
		wantOrder     *int
	}{
		{"builtin_override", "telemetry", schemas.Ptr(schemas.PluginPlacementPostBuiltin), schemas.Ptr(99), schemas.Ptr(schemas.PluginPlacementBuiltin), schemas.Ptr(1)},
		{"routing_override", "routing", schemas.Ptr(schemas.PluginPlacementPreBuiltin), schemas.Ptr(-1), schemas.Ptr(schemas.PluginPlacementBuiltin), schemas.Ptr(5)},
		{"builtin_round_trip", "otel", schemas.Ptr(schemas.PluginPlacementBuiltin), schemas.Ptr(6), schemas.Ptr(schemas.PluginPlacementBuiltin), schemas.Ptr(6)},
		{"builtin_omitted", "maxim", nil, nil, schemas.Ptr(schemas.PluginPlacementBuiltin), schemas.Ptr(9)},
		{"resolver_override", "model-catalog-resolver", schemas.Ptr(schemas.PluginPlacementPreBuiltin), schemas.Ptr(-1), schemas.Ptr(schemas.PluginPlacementPostBuiltin), schemas.Ptr(math.MaxInt)},
		{"custom_explicit", "custom-order-test", schemas.Ptr(schemas.PluginPlacementPreBuiltin), schemas.Ptr(99), schemas.Ptr(schemas.PluginPlacementPreBuiltin), schemas.Ptr(99)},
		{"custom_omitted", "custom-order-test", nil, nil, nil, nil},
	}
	for _, operation := range []string{"create", "update"} {
		for _, enabled := range []bool{true, false} {
			state := "enabled"
			if !enabled {
				state = "disabled"
			}
			for _, tt := range tests {
				t.Run(operation+"/"+state+"/"+tt.name, func(t *testing.T) {
					store := &capturePluginsStore{}
					if operation == "update" {
						store.existingPlugin = &configstoreTables.TablePlugin{Name: tt.plugin}
					}
					loader := &orderingPluginsLoader{}
					h := &PluginsHandler{configStore: store, pluginsLoader: loader}
					var ctx *fasthttp.RequestCtx
					if operation == "create" {
						ctx = buildCreateRequest(t, CreatePluginRequest{Name: tt.plugin, Enabled: enabled, Placement: tt.placement, Order: tt.order})
						h.createPlugin(ctx)
						require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), "%s", ctx.Response.Body())
					} else {
						ctx = buildUpdateRequest(t, UpdatePluginRequest{Enabled: enabled, Placement: tt.placement, Order: tt.order})
						ctx.SetUserValue("name", tt.plugin)
						h.updatePlugin(ctx)
						require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "%s", ctx.Response.Body())
						require.NotNil(t, store.capturedPlugin)
					}
					require.NotNil(t, store.existingPlugin)
					require.Equal(t, tt.wantPlacement, store.existingPlugin.Placement, "stored placement")
					require.Equal(t, tt.wantOrder, store.existingPlugin.Order, "stored order")
					require.Equal(t, enabled, loader.reloaded)
					if enabled {
						require.Equal(t, tt.wantPlacement, loader.placement, "reload placement")
						require.Equal(t, tt.wantOrder, loader.order, "reload order")
					}
					var response struct {
						Plugin PluginResponse `json:"plugin"`
					}
					require.NoError(t, json.Unmarshal(ctx.Response.Body(), &response))
					require.Equal(t, tt.wantPlacement, response.Plugin.Placement, "response placement")
					require.Equal(t, tt.wantOrder, response.Plugin.Order, "response order")
				})
			}
		}
	}
}

// TestPluginsHandler_ReturnsEffectiveOrder covers legacy persisted values in
// both GET endpoints without mutating the row while constructing a response.
func TestPluginsHandler_ReturnsEffectiveOrder(t *testing.T) {
	SetLogger(&mockLogger{})
	tests := []struct {
		name          string
		wantPlacement schemas.PluginPlacement
		wantOrder     int
	}{
		{"telemetry", schemas.PluginPlacementBuiltin, 1},
		{"routing", schemas.PluginPlacementBuiltin, 5},
		{"model-catalog-resolver", schemas.PluginPlacementPostBuiltin, math.MaxInt},
		{"custom-order-test", schemas.PluginPlacementPostBuiltin, 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := &configstoreTables.TablePlugin{
				Name: tt.name, Enabled: true,
				Placement: schemas.Ptr(schemas.PluginPlacementPostBuiltin), Order: schemas.Ptr(99),
			}
			h := &PluginsHandler{configStore: &capturePluginsStore{existingPlugin: row}, pluginsLoader: noopPluginsLoader{}}
			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue("name", tt.name)
			h.getPlugin(ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "%s", ctx.Response.Body())
			var single PluginResponse
			require.NoError(t, json.Unmarshal(ctx.Response.Body(), &single))
			require.Equal(t, schemas.Ptr(tt.wantPlacement), single.Placement)
			require.Equal(t, schemas.Ptr(tt.wantOrder), single.Order)

			listCtx := &fasthttp.RequestCtx{}
			h.getPlugins(listCtx)
			require.Equal(t, fasthttp.StatusOK, listCtx.Response.StatusCode(), "%s", listCtx.Response.Body())
			var list struct {
				Plugins []PluginResponse `json:"plugins"`
			}
			require.NoError(t, json.Unmarshal(listCtx.Response.Body(), &list))
			require.Len(t, list.Plugins, 1)
			require.Equal(t, single.Placement, list.Plugins[0].Placement)
			require.Equal(t, single.Order, list.Plugins[0].Order)
			require.Equal(t, schemas.Ptr(schemas.PluginPlacementPostBuiltin), row.Placement, "GET must not mutate storage")
			require.Equal(t, schemas.Ptr(99), row.Order, "GET must not mutate storage")
		})
	}
}

// JSON clients commonly decode numbers as float64. In particular, JavaScript
// rounds the resolver's MaxInt order above the range accepted by Go's int.
func TestPluginsHandler_ResolverOrderJSONRoundTrip(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			store := &capturePluginsStore{existingPlugin: &configstoreTables.TablePlugin{
				Name: "model-catalog-resolver", Enabled: true,
				Placement: schemas.Ptr(schemas.PluginPlacementPostBuiltin), Order: schemas.Ptr(math.MaxInt),
			}}
			loader := &orderingPluginsLoader{}
			h := &PluginsHandler{configStore: store, pluginsLoader: loader}
			getCtx := &fasthttp.RequestCtx{}
			getCtx.SetUserValue("name", "model-catalog-resolver")
			h.getPlugin(getCtx)
			require.Equal(t, fasthttp.StatusOK, getCtx.Response.StatusCode())
			var body map[string]any
			require.NoError(t, json.Unmarshal(getCtx.Response.Body(), &body))
			if operation == "create" {
				store.existingPlugin = nil
				ctx := buildCreateRequest(t, body)
				h.createPlugin(ctx)
				require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), "%s", ctx.Response.Body())
			} else {
				ctx := buildUpdateRequest(t, body)
				ctx.SetUserValue("name", "model-catalog-resolver")
				h.updatePlugin(ctx)
				require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "%s", ctx.Response.Body())
			}
			require.Equal(t, schemas.Ptr(math.MaxInt), store.existingPlugin.Order)
			require.Equal(t, schemas.Ptr(schemas.PluginPlacementPostBuiltin), store.existingPlugin.Placement)
			require.True(t, loader.reloaded)
			require.Equal(t, schemas.Ptr(math.MaxInt), loader.order)
		})
	}
}

func TestPluginsHandler_ValidatesOnlyCustomOrder(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, operation := range []string{"create", "update"} {
		for _, rawOrder := range []string{"9223372036854776000", "1.5", `"invalid"`} {
			for _, name := range []string{"telemetry", "custom-order-test"} {
				t.Run(operation+"/"+name+"/"+rawOrder, func(t *testing.T) {
					store := &capturePluginsStore{}
					if operation == "update" {
						store.existingPlugin = &configstoreTables.TablePlugin{Name: name}
					}
					h := &PluginsHandler{configStore: store, pluginsLoader: noopPluginsLoader{}}
					body := map[string]any{"name": name, "enabled": false, "order": json.RawMessage(rawOrder)}
					var ctx *fasthttp.RequestCtx
					wantStatus := fasthttp.StatusOK
					if operation == "create" {
						ctx = buildCreateRequest(t, body)
						h.createPlugin(ctx)
						wantStatus = fasthttp.StatusCreated
					} else {
						ctx = buildUpdateRequest(t, body)
						ctx.SetUserValue("name", name)
						h.updatePlugin(ctx)
					}
					if name == "custom-order-test" {
						wantStatus = fasthttp.StatusBadRequest
						require.Nil(t, store.capturedPlugin, "invalid custom order must not be saved")
						if operation == "create" {
							require.Nil(t, store.existingPlugin)
						}
					} else {
						require.NotNil(t, store.existingPlugin)
						require.Equal(t, schemas.Ptr(1), store.existingPlugin.Order)
					}
					require.Equal(t, wantStatus, ctx.Response.StatusCode(), "%s", ctx.Response.Body())
				})
			}
		}
	}
}

func TestPluginsHandler_RejectsCustomBuiltinPlacement(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			store := &capturePluginsStore{}
			h := &PluginsHandler{configStore: store, pluginsLoader: noopPluginsLoader{}}
			var ctx *fasthttp.RequestCtx
			if operation == "create" {
				ctx = buildCreateRequest(t, CreatePluginRequest{Name: "custom-order-test", Placement: schemas.Ptr(schemas.PluginPlacementBuiltin)})
				h.createPlugin(ctx)
				require.Nil(t, store.existingPlugin)
			} else {
				store.existingPlugin = &configstoreTables.TablePlugin{Name: "custom-order-test"}
				ctx = buildUpdateRequest(t, UpdatePluginRequest{Placement: schemas.Ptr(schemas.PluginPlacementBuiltin)})
				ctx.SetUserValue("name", "custom-order-test")
				h.updatePlugin(ctx)
				require.Nil(t, store.capturedPlugin)
			}
			require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "%s", ctx.Response.Body())
		})
	}
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
