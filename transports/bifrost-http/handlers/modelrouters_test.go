package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"gorm.io/gorm"
)

// modelRouterManagerFake records what the model router endpoints serve.
type modelRouterManagerFake struct {
	mockRoutingManager
	reloadedRouters []string
	reloadedTargets []string
	reloadedRouting []string
	removedRouters  []string
}

// ReloadModelRouterTargets records a targets reload.
func (m *modelRouterManagerFake) ReloadModelRouterTargets(_ context.Context, id string) error {
	m.reloadedTargets = append(m.reloadedTargets, id)
	return nil
}

// ReloadModelRouter records a router reload.
func (m *modelRouterManagerFake) ReloadModelRouter(_ context.Context, id string) error {
	m.reloadedRouters = append(m.reloadedRouters, id)
	return nil
}

// RemoveModelRouter records a router removal.
func (m *modelRouterManagerFake) RemoveModelRouter(_ context.Context, id string) error {
	m.removedRouters = append(m.removedRouters, id)
	return nil
}

// ReloadModelRouterRouting records a routing reload.
func (m *modelRouterManagerFake) ReloadModelRouterRouting(_ context.Context, id string) error {
	m.reloadedRouting = append(m.reloadedRouting, id)
	return nil
}

// RetryModelRouterWarmup restarts "failed", refuses any other served router,
// and serves neither "missing" nor "unserved".
func (m *modelRouterManagerFake) RetryModelRouterWarmup(_ context.Context, id string) (complexity.SemanticStatusInfo, bool, error) {
	switch id {
	case "missing", "unserved":
		return complexity.SemanticStatusInfo{}, false, configstore.ErrNotFound
	case "failed":
		return complexity.SemanticStatusInfo{State: complexity.SemanticStatusWarming}, true, nil
	}
	return complexity.SemanticStatusInfo{State: complexity.SemanticStatusReady}, false, nil
}

// GetModelRouterStatus reports every known router as disabled. "missing"
// and "unserved" are routers the plugin does not serve.
func (m *modelRouterManagerFake) GetModelRouterStatus(_ context.Context, id string) (complexity.SemanticStatusInfo, complexity.LLMStatusInfo, error) {
	if id == "missing" || id == "unserved" {
		return complexity.SemanticStatusInfo{}, complexity.LLMStatusInfo{}, configstore.ErrNotFound
	}
	return complexity.SemanticStatusInfo{State: complexity.SemanticStatusDisabled}, complexity.LLMStatusInfo{State: complexity.LLMStatusDisabled}, nil
}

// modelRouterHandler builds a routing handler over a real SQLite store.
func modelRouterHandler(t *testing.T) (*RoutingHandler, *modelRouterManagerFake, configstore.ConfigStore) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &modelRouterManagerFake{}
	return &RoutingHandler{configStore: store, routingManager: manager}, manager, store
}

// call runs a handler with a body and path parameters and returns the status
// and decoded JSON body.
func call(t *testing.T, handler fasthttp.RequestHandler, body string, params map[string]string) (int, map[string]any) {
	t.Helper()
	ctx := newTestRequestCtx(body)
	for key, value := range params {
		ctx.SetUserValue(key, value)
	}
	handler(ctx)
	var decoded map[string]any
	_ = json.Unmarshal(ctx.Response.Body(), &decoded)
	return ctx.Response.StatusCode(), decoded
}

// routerBody is a router payload with one phrase per tier.
func routerBody(id, name string) string {
	return `{"id":"` + id + `","name":"` + name + `","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`
}

// TestModelRouterEndpoints walks a router through the API: none is listed
// before one is created, a router is created off, renamed and reset, and
// deleted. An update changes only what its body sends, so a rename keeps the
// stored config, and a reset answers with the targets it keeps.
func TestModelRouterEndpoints(t *testing.T) {
	h, manager, store := modelRouterHandler(t)

	status, body := call(t, h.listModelRouters, "", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Empty(t, body["routers"], "no router exists until one is created")
	assert.NotNil(t, body["routers"], "the list is empty, not null")

	status, body = call(t, h.createModelRouter, routerBody("support", "Support"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "Support", body["name"])
	assert.Equal(t, map[string]any{"enabled": false, "scope": "global", "priority": float64(0)}, body["routing"], "a new router starts off")
	assert.Equal(t, []string{"support"}, manager.reloadedRouters)

	status, _ = call(t, h.createModelRouter, routerBody("support", "Again"), nil)
	assert.Equal(t, fasthttp.StatusConflict, status)
	status, _ = call(t, h.createModelRouter, `{"id":"default","name":"Default","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`, nil)
	assert.Equal(t, fasthttp.StatusOK, status, "only `defaults` is reserved")
	status, _ = call(t, h.createModelRouter, `{"id":"defaults","name":"Defaults","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "GET /api/routing/model-routers/defaults serves the defaults, so no router can take that id")
	status, _ = call(t, h.createModelRouter, `{"name":"Typo","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},"semantik":{}}`, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a misspelled setting is rejected, not dropped")

	status, body = call(t, h.createModelRouter, `{"name":"Generated","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`, nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.NotEmpty(t, body["id"], "an omitted id is generated")

	status, body = call(t, h.updateModelRouter, routerBody("ignored", "Customer support"), map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "support", body["id"], "the path names the router")
	assert.Equal(t, "Customer support", body["name"])

	status, _ = call(t, h.updateModelRouter, routerBody("", "Nobody"), map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = call(t, h.updateModelRouter, `{"name":"Nobody"}`, map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	support := map[string]string{"router_id": "support"}
	status, body = call(t, h.updateModelRouter, `{"name":"Support","description":"Support desk","keywords":{"simple_keywords":["s2"],"medium_keywords":["m2"],"complex_keywords":["c2"]}}`, support)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	status, body = call(t, h.updateModelRouter, `{"name":"Customer support"}`, support)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "Customer support", body["name"])
	assert.Equal(t, "Support desk", body["description"], "an omitted description is kept")
	keywords, _ := body["keywords"].(map[string]any)
	assert.Equal(t, []any{"s2"}, keywords["simple_keywords"], "a rename keeps the stored config")
	status, body = call(t, h.updateModelRouter, `{"keywords":{"simple_keywords":["s3"],"medium_keywords":["m3"],"complex_keywords":["c3"]}}`, support)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "Customer support", body["name"], "a config save keeps the stored name")
	status, _ = call(t, h.updateModelRouter, `{"name":"  "}`, support)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a sent name is still validated")

	model := "gpt-4o-mini"
	require.NoError(t, store.SetModelRouterTargets(context.Background(), "support", []tables.TableModelRouterTarget{{Tier: "SIMPLE", Model: &model, Weight: 1}}))
	status, body = call(t, h.resetModelRouter, "", support)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "Customer support", body["name"], "a reset keeps the name")
	targets, _ := body["targets"].([]any)
	assert.Len(t, targets, 1, "a reset answers with the targets it keeps")

	status, _ = call(t, h.deleteModelRouter, "", map[string]string{"router_id": "default"})
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, []string{"default"}, manager.removedRouters)
	status, _ = call(t, h.deleteModelRouter, "", map[string]string{"router_id": "default"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, _ = call(t, h.getModelRouterStatus, "", map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = call(t, h.getModelRouterStatus, "", map[string]string{"router_id": "support"})
	assert.Equal(t, fasthttp.StatusOK, status)

	// A saved router the plugin does not serve (session routing without a KV
	// store, say) is reported as not loaded, not as missing.
	status, body = call(t, h.createModelRouter, routerBody("unserved", "Unserved"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	status, body = call(t, h.getModelRouterStatus, "", map[string]string{"router_id": "unserved"})
	assert.Equal(t, fasthttp.StatusServiceUnavailable, status, "%v", body)
	assert.Contains(t, fmt.Sprint(body), "not loaded")
}

// TestModelRouterTargetsEndpoint pins how a router's targets are set: on their
// own endpoint, validated, served without reloading the router's classifiers,
// and left alone by a save of the router's config.
func TestModelRouterTargetsEndpoint(t *testing.T) {
	h, manager, store := modelRouterHandler(t)
	status, body := call(t, h.createModelRouter, routerBody("support", "Support"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	manager.reloadedRouters = nil

	targets := `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1},{"tier":"COMPLEX","provider":"anthropic","model":"claude-opus","weight":1,"fallbacks":["openai/gpt-4o"]}]}`
	status, body = call(t, h.setModelRouterTargets, targets, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Len(t, body["targets"], 2)
	assert.Equal(t, []string{"support"}, manager.reloadedTargets)
	assert.Empty(t, manager.reloadedRouters, "new targets never reconfigure the classifiers")

	status, body = call(t, h.updateModelRouter, `{"name":"Support","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},"targets":[]}`, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Len(t, body["targets"], 2, "saving the router's config leaves its targets alone")

	// One refusal from the store's validator (its cases are unit-tested there),
	// the one only this endpoint makes, and bodies that do not say what the
	// targets are: each would otherwise read as no targets and clear them.
	invalid := map[string]string{
		"weights do not sum":         `{"targets":[{"tier":"SIMPLE","model":"a","weight":0.5}]}`,
		"config-only fallback field": `{"targets":[{"tier":"SIMPLE","model":"m","weight":1,"fallbacks":[{"provider":"openai","model":"gpt-4o","provider_key_name":"k"}]}]}`,
		"empty body object":          `{}`,
		"misspelled targets key":     `{"target":[{"tier":"SIMPLE","model":"m","weight":1}]}`,
		"null targets":               `{"targets":null}`,
		"unknown target field":       `{"targets":[{"tier":"SIMPLE","model":"m","weight":1,"ttft_timeout_ms":500}]}`,
	}
	for name, payload := range invalid {
		t.Run(name, func(t *testing.T) {
			status, _ := call(t, h.setModelRouterTargets, payload, map[string]string{"router_id": "support"})
			assert.Equal(t, fasthttp.StatusBadRequest, status)
		})
	}
	stored, err := store.GetModelRouter(context.Background(), "support")
	require.NoError(t, err)
	assert.Len(t, stored.Targets, 2, "a refused body leaves the targets as they were")

	status, _ = call(t, h.setModelRouterTargets, targets, map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, body = call(t, h.setModelRouterTargets, `{"targets":[]}`, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Nil(t, body["targets"], "no targets clears them")

	// Targets this version cannot read make the router unreadable; saving new
	// targets replaces them and repairs it, as saving or resetting its config does.
	status, body = call(t, h.setModelRouterTargets, targets, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	require.NoError(t, store.ExecuteTransaction(context.Background(), func(tx *gorm.DB) error {
		return tx.Model(&tables.TableModelRouterTarget{}).Where("router_id = ? AND tier = ?", "support", "COMPLEX").Update("fallbacks", `["broken`).Error
	}))
	_, err = store.GetModelRouter(context.Background(), "support")
	require.ErrorIs(t, err, configstore.ErrConfigUnreadable)
	status, body = call(t, h.setModelRouterTargets, targets, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Len(t, body["targets"], 2)
	_, err = store.GetModelRouter(context.Background(), "support")
	require.NoError(t, err, "the save repaired the router")
}

// TestModelRouterWarmupRetryEndpoint pins the retry answers, as
// TestRetryComplexitySemanticWarmup does for the Complexity Router page: a
// failed warmup restarts (202), any other state is refused (409), a router
// that is not saved is 404 and one saved but not served is 503.
func TestModelRouterWarmupRetryEndpoint(t *testing.T) {
	h, _, _ := modelRouterHandler(t)
	for _, id := range []string{"failed", "ready", "unserved"} {
		status, body := call(t, h.createModelRouter, routerBody(id, id), nil)
		require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	}

	status, body := call(t, h.retryModelRouterWarmup, "", map[string]string{"router_id": "failed"})
	require.Equal(t, fasthttp.StatusAccepted, status, "%v", body)
	assert.Equal(t, string(complexity.SemanticStatusWarming), body["state"])

	status, _ = call(t, h.retryModelRouterWarmup, "", map[string]string{"router_id": "ready"})
	assert.Equal(t, fasthttp.StatusConflict, status)
	status, _ = call(t, h.retryModelRouterWarmup, "", map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = call(t, h.retryModelRouterWarmup, "", map[string]string{"router_id": "unserved"})
	assert.Equal(t, fasthttp.StatusServiceUnavailable, status)
}

// TestModelRouterDefaultsEndpoint pins what a new router starts from: the
// built-in phrases and the guidance each model-based classifier sends.
func TestModelRouterDefaultsEndpoint(t *testing.T) {
	h, _, _ := modelRouterHandler(t)
	status, body := call(t, h.getModelRouterDefaults, "", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	config := body["config"].(map[string]any)
	keywords := config["keywords"].(map[string]any)
	assert.NotEmpty(t, keywords["simple_keywords"])
	assert.NotEmpty(t, keywords["complex_keywords"])
	assert.NotEmpty(t, body["llm_default_prompt"])
	assert.NotEmpty(t, body["decision_defaults"])
}

// TestModelRouterRoutingEndpoint pins how a router's routing is set: on its
// own endpoint, validated, served without reloading the router's classifiers,
// and left alone by a save of the router's config.
func TestModelRouterRoutingEndpoint(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	status, body := call(t, h.createModelRouter, routerBody("support", "Support"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	status, body = call(t, h.createModelRouter, routerBody("billing", "Billing"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, float64(1), body["routing"].(map[string]any)["priority"], "a new router goes after the last global one")
	manager.reloadedRouters = nil

	routing := `{"enabled":true,"scope":"global","cel_expression":"model == \"support-assistant\"","query":{"combinator":"and"},"priority":4}`
	status, body = call(t, h.setModelRouterRouting, routing, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	saved := body["routing"].(map[string]any)
	assert.Equal(t, true, saved["enabled"])
	assert.Equal(t, `model == "support-assistant"`, saved["cel_expression"])
	assert.Equal(t, map[string]any{"combinator": "and"}, saved["query"])
	assert.Equal(t, []string{"support"}, manager.reloadedRouting)
	assert.Empty(t, manager.reloadedRouters, "new routing never reconfigures the classifiers")

	status, body = call(t, h.updateModelRouter, `{"name":"Support","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},"routing":{"enabled":false,"scope":"global","priority":9}}`, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, true, body["routing"].(map[string]any)["enabled"], "saving the router's config leaves its routing alone")

	status, _ = call(t, h.setModelRouterRouting, `{"enabled":true,"scope":"global","priority":4}`, map[string]string{"router_id": "billing"})
	assert.Equal(t, fasthttp.StatusConflict, status, "priority 4 in global is taken")

	// One refusal from the condition check and one from the store's validator
	// (both unit-tested there), and the scope lookup only this endpoint makes.
	invalid := map[string]string{
		"complexity_tier in the condition": `{"scope":"global","cel_expression":"complexity_tier == \"SIMPLE\""}`,
		"unknown scope":                    `{"scope":"org"}`,
		"team without a team":              `{"scope":"team","scope_id":"no-such-team"}`,
	}
	for name, payload := range invalid {
		t.Run(name, func(t *testing.T) {
			status, _ := call(t, h.setModelRouterRouting, payload, map[string]string{"router_id": "billing"})
			assert.Equal(t, fasthttp.StatusBadRequest, status)
		})
	}

	status, _ = call(t, h.setModelRouterRouting, `{"scope":"global"}`, map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, body = call(t, h.createModelRouter, `{"id":"sales","name":"Sales","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},"routing":{"enabled":true,"scope":"global","priority":4}}`, nil)
	assert.Equal(t, fasthttp.StatusConflict, status, "a create with routing is checked too: %v", body)
}

// TestModelRouterRoutingEndpointUpdatesSentFields pins that a routing update
// changes only the fields its body sends, as a routing rule update does, so a
// switch that sends {"enabled": ...} never writes back a stale scope,
// condition or priority. A condition sent without its builder state clears the
// stored state, which described the old condition, and a misspelled field is
// refused rather than dropped.
func TestModelRouterRoutingEndpointUpdatesSentFields(t *testing.T) {
	h, _, _ := modelRouterHandler(t)
	status, body := call(t, h.createModelRouter, routerBody("support", "Support"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	routing := func(payload string) map[string]any {
		t.Helper()
		status, body := call(t, h.setModelRouterRouting, payload, map[string]string{"router_id": "support"})
		require.Equal(t, fasthttp.StatusOK, status, "%v", body)
		return body["routing"].(map[string]any)
	}

	routing(`{"enabled":true,"scope":"global","cel_expression":"model == \"support-assistant\"","query":{"combinator":"and"},"priority":4}`)

	saved := routing(`{"enabled":false}`)
	assert.Equal(t, false, saved["enabled"])
	assert.Equal(t, "global", saved["scope"])
	assert.Equal(t, `model == "support-assistant"`, saved["cel_expression"], "a toggle keeps the condition")
	assert.Equal(t, map[string]any{"combinator": "and"}, saved["query"], "a toggle keeps the builder state")
	assert.Equal(t, float64(4), saved["priority"], "a toggle keeps the priority")

	saved = routing(`{"priority":6}`)
	assert.Equal(t, false, saved["enabled"], "an omitted field keeps its stored value")
	assert.Equal(t, float64(6), saved["priority"])
	assert.Equal(t, map[string]any{"combinator": "and"}, saved["query"])

	saved = routing(`{"cel_expression":"model == \"other\""}`)
	assert.Equal(t, `model == "other"`, saved["cel_expression"])
	assert.Nil(t, saved["query"], "a new condition without builder state clears the old state")

	status, _ = call(t, h.setModelRouterRouting, `{"prioritiy":7}`, map[string]string{"router_id": "support"})
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a misspelled field is refused")
}

// modelRouterPhrases is the phrase block a router body needs: one phrase per tier.
const modelRouterPhrases = `"keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}`

// modelRouterBody is routerBody with extra top-level fields, each written as
// `"key":value`. An empty id is left out of the body.
func modelRouterBody(id, name string, fields ...string) string {
	parts := make([]string, 0, len(fields)+3)
	if id != "" {
		parts = append(parts, `"id":"`+id+`"`)
	}
	parts = append(parts, `"name":"`+name+`"`, modelRouterPhrases)
	parts = append(parts, fields...)
	return "{" + strings.Join(parts, ",") + "}"
}

// modelRouterPath is the path parameter that names a router.
func modelRouterPath(id string) map[string]string {
	return map[string]string{"router_id": id}
}

// modelRouterOK runs a handler that must answer 200 and returns its body.
func modelRouterOK(t *testing.T, handler fasthttp.RequestHandler, body string, params map[string]string) map[string]any {
	t.Helper()
	status, decoded := call(t, handler, body, params)
	require.Equal(t, fasthttp.StatusOK, status, "%v", decoded)
	return decoded
}

// modelRouterErrorMessage reads the message of an error answer.
func modelRouterErrorMessage(body map[string]any) string {
	errorField, _ := body["error"].(map[string]any)
	message, _ := errorField["message"].(string)
	return message
}

// modelRouterList returns what GET /api/routing/model-routers lists.
func modelRouterList(t *testing.T, h *RoutingHandler) []any {
	t.Helper()
	body := modelRouterOK(t, h.listModelRouters, "", nil)
	routers, _ := body["routers"].([]any)
	assert.Equal(t, float64(len(routers)), body["count"], "count is the number of routers listed")
	return routers
}

// modelRouterRead returns what GET /api/routing/model-routers/{router_id} answers.
func modelRouterRead(t *testing.T, h *RoutingHandler, id string) map[string]any {
	t.Helper()
	return modelRouterOK(t, h.getModelRouter, "", modelRouterPath(id))
}

// TestModelRouterScopeEntityRemoved pins what the API answers for a router
// whose scope names an entity that was deleted after the router was saved. The
// router stays stored and is still read and listed with the removed entity's
// id. As for a routing rule, an update that leaves the scope as stored does
// not look the entity up again, so the router can still be turned off,
// reordered and given another condition; an update that names the scope is
// checked, so the removed entity is refused; and moving the router to global
// is accepted.
func TestModelRouterScopeEntityRemoved(t *testing.T) {
	h, manager, store := modelRouterHandler(t)
	ctx := context.Background()
	active := true
	entities := []struct {
		scope    string
		id       string
		notFound string
		create   func() error
		remove   func() error
	}{
		{"team", "team-1", "team 'team-1' not found",
			func() error { return store.CreateTeam(ctx, &tables.TableTeam{ID: "team-1", Name: "Team 1"}) },
			func() error { return store.DeleteTeam(ctx, "team-1") }},
		{"virtual_key", "vk-1", "virtual key 'vk-1' not found",
			func() error {
				return store.CreateVirtualKey(ctx, &tables.TableVirtualKey{ID: "vk-1", Name: "Key 1", Value: *schemas.NewSecretVar("sk-bf-vk-1"), IsActive: &active})
			},
			func() error { return store.DeleteVirtualKey(ctx, "vk-1") }},
		{"customer", "customer-1", "customer 'customer-1' not found",
			func() error {
				return store.CreateCustomer(ctx, &tables.TableCustomer{ID: "customer-1", Name: "Customer 1"})
			},
			func() error { return store.DeleteCustomer(ctx, "customer-1") }},
	}
	for priority, entity := range entities {
		t.Run(entity.scope, func(t *testing.T) {
			id := "router-for-" + strings.ReplaceAll(entity.scope, "_", "-")
			path := modelRouterPath(id)
			scoped := map[string]any{"enabled": true, "scope": entity.scope, "scope_id": entity.id, "priority": float64(0)}
			require.NoError(t, entity.create())
			body := modelRouterOK(t, h.createModelRouter, modelRouterBody(id, "Router for "+entity.scope,
				`"routing":{"enabled":true,"scope":"`+entity.scope+`","scope_id":"`+entity.id+`","priority":0}`), nil)
			require.Equal(t, scoped, body["routing"])

			require.NoError(t, entity.remove())

			assert.Equal(t, scoped, modelRouterRead(t, h, id)["routing"], "the router is still read, scoped to the removed entity")
			var listed map[string]any
			for _, router := range modelRouterList(t, h) {
				if entry, _ := router.(map[string]any); entry["id"] == id {
					listed = entry
				}
			}
			assert.Equal(t, scoped, listed["routing"], "and still listed")

			// An update that leaves the scope as stored is not held up by the removed entity.
			manager.reloadedRouting = nil
			for _, payload := range []string{`{"enabled":false}`, `{"priority":3}`, `{"cel_expression":"model == \"gpt-4o\""}`} {
				status, body := call(t, h.setModelRouterRouting, payload, path)
				assert.Equal(t, fasthttp.StatusOK, status, "%s: %v", payload, body)
			}
			scoped = map[string]any{"enabled": false, "scope": entity.scope, "scope_id": entity.id, "priority": float64(3), "cel_expression": `model == "gpt-4o"`}
			assert.Equal(t, scoped, modelRouterRead(t, h, id)["routing"], "each update changed only what it sent and kept the scope")
			assert.Equal(t, []string{id, id, id}, manager.reloadedRouting, "and each one reached live routing")

			// An update that names the scope is checked, so the removed entity is refused.
			manager.reloadedRouting = nil
			for _, payload := range []string{
				`{"scope":"` + entity.scope + `"}`,
				`{"scope_id":"` + entity.id + `"}`,
				`{"enabled":true,"scope":"` + entity.scope + `","scope_id":"` + entity.id + `"}`,
			} {
				status, body := call(t, h.setModelRouterRouting, payload, path)
				assert.Equal(t, fasthttp.StatusBadRequest, status, "%s: %v", payload, body)
				assert.Contains(t, modelRouterErrorMessage(body), entity.notFound, payload)
			}
			assert.Equal(t, scoped, modelRouterRead(t, h, id)["routing"], "the refused updates changed nothing")
			assert.Empty(t, manager.reloadedRouting)

			// The router's other writes do not look the scope up.
			modelRouterOK(t, h.updateModelRouter, `{"name":"Renamed router for `+entity.scope+`"}`, path)
			modelRouterOK(t, h.setModelRouterTargets, `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]}`, path)
			assert.Equal(t, scoped, modelRouterRead(t, h, id)["routing"])

			// Each subtest's router takes its own priority among the global ones.
			body = modelRouterOK(t, h.setModelRouterRouting, fmt.Sprintf(`{"enabled":false,"scope":"global","priority":%d}`, priority), path)
			assert.Equal(t, map[string]any{"enabled": false, "scope": "global", "priority": float64(priority), "cel_expression": `model == "gpt-4o"`}, body["routing"],
				"moving the router to global is accepted and drops the removed entity's id")
		})
	}
}
