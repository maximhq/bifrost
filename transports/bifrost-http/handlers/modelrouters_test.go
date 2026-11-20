package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
	"gorm.io/gorm"
)

// modelRouterManagerFake records what the model router endpoints serve.
type modelRouterManagerFake struct {
	mockRoutingManager
	reloadedRouters []string
	reloadedTargets []string
	reloadedRouting []string
	removedRouters  []string
	serveErr        error // what every reload and removal answers, after recording the call
}

// ReloadModelRouterTargets records a targets reload.
func (m *modelRouterManagerFake) ReloadModelRouterTargets(_ context.Context, id string) error {
	m.reloadedTargets = append(m.reloadedTargets, id)
	return m.serveErr
}

// ReloadModelRouter records a router reload.
func (m *modelRouterManagerFake) ReloadModelRouter(_ context.Context, id string) error {
	m.reloadedRouters = append(m.reloadedRouters, id)
	return m.serveErr
}

// RemoveModelRouter records a router removal.
func (m *modelRouterManagerFake) RemoveModelRouter(_ context.Context, id string) error {
	m.removedRouters = append(m.removedRouters, id)
	return m.serveErr
}

// ReloadModelRouterRouting records a routing reload.
func (m *modelRouterManagerFake) ReloadModelRouterRouting(_ context.Context, id string) error {
	m.reloadedRouting = append(m.reloadedRouting, id)
	return m.serveErr
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
	// A router whose config cannot be read gets no generated tier text, rather
	// than text rendered from the built-in defaults as if it were its own.
	status, body = call(t, h.getModelRouterStatus, "", map[string]string{"router_id": "unreadable"})
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.NotContains(t, body, "suggestion_default_tier_prompt")
	assert.NotContains(t, body, "suggestion_default_tier_criteria")
	status, body = call(t, h.getModelRouterStatus, "", map[string]string{"router_id": "support"})
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, suggest.DefaultInstructions(), body["suggestion_default_instructions"])
	assert.Equal(t, suggest.DefaultTierInstructions(), body["suggestion_default_tier_instructions"],
		"the decision tier step's question editor is seeded from the shipped instructions")
	// A router back on the shipped phrases has no examples to send.
	routerDefaults, _ := body["suggestion_default_tier_prompt"].(string)
	assert.NotContains(t, routerDefaults, "administrator assigned")

	// The suggestions tier block round-trips with its models filled in, and a
	// misspelled tier setting is refused.
	tiered := `{"id":"tiered","name":"Tiered","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},` +
		`"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"},` +
		`"suggestions":{"enabled":true,"provider":"openai","model":"gpt-4.1-mini","tier":{"classifier":"decision","instructions":"judge the work",` +
		`"prompt":"Refunds are SIMPLE.","criteria":{"SIMPLE":{"definition":"One short answer.","examples":["where is my refund"]}}}}}`
	status, body = call(t, h.createModelRouter, tiered, nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	suggestions, _ := body["suggestions"].(map[string]any)
	assert.Equal(t, map[string]any{"classifier": "decision", "provider": string(configstore.DefaultComplexityDecisionProvider),
		"model": configstore.DefaultComplexityDecisionModel, "instructions": "judge the work", "prompt": "Refunds are SIMPLE.",
		"criteria": map[string]any{"SIMPLE": map[string]any{"definition": "One short answer.", "examples": []any{"where is my refund"}}}}, suggestions["tier"])
	status, body = call(t, h.getModelRouter, "", map[string]string{"router_id": "tiered"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	suggestions, _ = body["suggestions"].(map[string]any)
	tierBack, _ := suggestions["tier"].(map[string]any)
	assert.Equal(t, "Refunds are SIMPLE.", tierBack["prompt"], "the edited text is persisted, not only echoed")
	assert.NotNil(t, tierBack["criteria"])

	// The generated tier text is rendered from this router: its own phrases
	// (s, m, c) are the examples, in the prompt and in each tier's criteria.
	status, body = call(t, h.getModelRouterStatus, "", map[string]string{"router_id": "tiered"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	tierPrompt, _ := body["suggestion_default_tier_prompt"].(string)
	assert.True(t, strings.HasPrefix(tierPrompt, suggest.DefaultTierInstructions()), tierPrompt)
	assert.True(t, strings.HasSuffix(tierPrompt, "SIMPLE:\n- s\nMEDIUM:\n- m\nCOMPLEX:\n- c"), tierPrompt)
	criteria, _ := body["suggestion_default_tier_criteria"].(map[string]any)
	require.Len(t, criteria, 3)
	medium, _ := criteria["MEDIUM"].(map[string]any)
	assert.Equal(t, []any{"m"}, medium["examples"])
	assert.NotEmpty(t, medium["definition"])
	status, _ = call(t, h.createModelRouter, strings.Replace(tiered, `"classifier"`, `"mode"`, 1), nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status)

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

// TestModelRouterListAcrossRouters walks several routers through the API: each
// router created without routing starts off and global, one priority after the
// last, a deleted router's priority is not handed out again, and the list
// answers oldest first with every router's own routing and targets.
func TestModelRouterListAcrossRouters(t *testing.T) {
	h, _, _ := modelRouterHandler(t)

	// The ids are not in alphabetical order, so the order asserted below is the
	// order of creation and not the order of the ids.
	for priority, id := range []string{"zulu", "mike", "alpha"} {
		body := modelRouterOK(t, h.createModelRouter, routerBody(id, "Router "+id), nil)
		assert.Equal(t, map[string]any{"enabled": false, "scope": "global", "priority": float64(priority)}, body["routing"],
			"router %s starts off, global and after the routers before it", id)
		assert.NotContains(t, body, "targets", "a new router has no targets")
	}
	modelRouterOK(t, h.setModelRouterTargets, `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]}`, modelRouterPath("zulu"))
	modelRouterOK(t, h.setModelRouterRouting, `{"enabled":true,"cel_expression":"model == \"alpha-assistant\""}`, modelRouterPath("alpha"))

	modelRouterOK(t, h.deleteModelRouter, "", modelRouterPath("mike"))
	body := modelRouterOK(t, h.createModelRouter, routerBody("bravo", "Router bravo"), nil)
	assert.Equal(t, map[string]any{"enabled": false, "scope": "global", "priority": float64(3)}, body["routing"],
		"a new router goes after the last global router; the priority the deleted one held stays free")

	// A save keeps a router's place in the list.
	modelRouterOK(t, h.updateModelRouter, `{"name":"Router zulu renamed"}`, modelRouterPath("zulu"))

	want := []map[string]any{
		{"id": "zulu", "name": "Router zulu renamed",
			"routing": map[string]any{"enabled": false, "scope": "global", "priority": float64(0)},
			"targets": []any{map[string]any{"tier": "SIMPLE", "provider": "openai", "model": "gpt-4o-mini", "weight": float64(1)}}},
		{"id": "alpha", "name": "Router alpha",
			"routing": map[string]any{"enabled": true, "scope": "global", "cel_expression": `model == "alpha-assistant"`, "priority": float64(2)},
			"targets": nil},
		{"id": "bravo", "name": "Router bravo",
			"routing": map[string]any{"enabled": false, "scope": "global", "priority": float64(3)},
			"targets": nil},
	}
	routers := modelRouterList(t, h)
	require.Len(t, routers, len(want), "the deleted router is not listed")
	for i, expected := range want {
		listed, _ := routers[i].(map[string]any)
		for field, value := range expected {
			assert.Equal(t, value, listed[field], "routers[%d].%s", i, field)
		}
	}
}

// TestModelRouterDeleteFreesNameAndPriority pins that a deleted router leaves
// nothing behind: a new router can take its id, its name and its priority, and
// does not inherit the targets the deleted one had. Deleting a router that does
// not exist asks the manager for nothing.
func TestModelRouterDeleteFreesNameAndPriority(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)
	modelRouterOK(t, h.createModelRouter, routerBody("billing", "Billing"), nil)
	modelRouterOK(t, h.setModelRouterTargets, `{"targets":[{"tier":"COMPLEX","provider":"anthropic","model":"claude-opus","weight":1}]}`, modelRouterPath("billing"))
	again := modelRouterBody("billing", "Billing", `"routing":{"enabled":true,"scope":"global","priority":1}`)

	status, body := call(t, h.createModelRouter, again, nil)
	require.Equal(t, fasthttp.StatusConflict, status, "the id, the name and priority 1 are all held: %v", body)

	modelRouterOK(t, h.deleteModelRouter, "", modelRouterPath("billing"))
	status, _ = call(t, h.getModelRouter, "", modelRouterPath("billing"))
	assert.Equal(t, fasthttp.StatusNotFound, status, "a deleted router cannot be read")

	body = modelRouterOK(t, h.createModelRouter, again, nil)
	assert.Equal(t, "Billing", body["name"], "the deleted router's name is free")
	assert.Equal(t, map[string]any{"enabled": true, "scope": "global", "priority": float64(1)}, body["routing"], "the deleted router's priority is free")
	assert.NotContains(t, body, "targets", "the deleted router's targets do not come back with its id")

	manager.removedRouters = nil
	status, _ = call(t, h.deleteModelRouter, "", modelRouterPath("never-created"))
	assert.Equal(t, fasthttp.StatusNotFound, status)
	assert.Empty(t, manager.removedRouters, "a delete that finds no router removes nothing from the manager")
}

// TestModelRouterCreateAppliesRouting pins that routing sent in a create body is
// stored with the router: whether it is on, its condition with the builder
// state, its scope and its priority. A priority is unique per scope, so two
// scopes can each hold the same one, and a scope's entity has to exist, except
// a user, whose id the gateway cannot look up.
func TestModelRouterCreateAppliesRouting(t *testing.T) {
	h, _, store := modelRouterHandler(t)
	require.NoError(t, store.CreateTeam(context.Background(), &tables.TableTeam{ID: "team-1", Name: "Team 1"}))

	cases := []struct {
		name    string
		id      string
		routing string
		want    map[string]any
	}{
		{
			name:    "global with a condition",
			id:      "global-router",
			routing: `{"enabled":true,"scope":"global","cel_expression":"model == \"gpt-4o\"","query":{"combinator":"and"},"priority":7}`,
			want: map[string]any{"enabled": true, "scope": "global", "cel_expression": `model == "gpt-4o"`,
				"query": map[string]any{"combinator": "and"}, "priority": float64(7)},
		},
		{
			name:    "scoped to a team that exists, on a priority a global router holds",
			id:      "team-router",
			routing: `{"enabled":true,"scope":"team","scope_id":"team-1","priority":7}`,
			want:    map[string]any{"enabled": true, "scope": "team", "scope_id": "team-1", "priority": float64(7)},
		},
		{
			name:    "scoped to a user, whose id is not looked up",
			id:      "user-router",
			routing: `{"scope":"user","scope_id":"user-123","priority":0}`,
			want:    map[string]any{"enabled": false, "scope": "user", "scope_id": "user-123", "priority": float64(0)},
		},
		{
			name:    "no scope is global",
			id:      "scopeless-router",
			routing: `{"enabled":true,"priority":8}`,
			want:    map[string]any{"enabled": true, "scope": "global", "priority": float64(8)},
		},
		{
			name:    "a global scope drops the scope_id it is sent",
			id:      "dropped-scope-id",
			routing: `{"scope":"global","scope_id":"team-1","priority":9}`,
			want:    map[string]any{"enabled": false, "scope": "global", "priority": float64(9)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := modelRouterOK(t, h.createModelRouter, modelRouterBody(tc.id, tc.name, `"routing":`+tc.routing), nil)
			assert.Equal(t, tc.want, body["routing"])
			assert.Equal(t, tc.want, modelRouterRead(t, h, tc.id)["routing"], "the routing is stored, not only echoed")
		})
	}
}

// TestModelRouterCreateRefusals pins each create the API refuses and its
// status, and that a refused create leaves no trace: no router is stored, the
// routers already there are untouched, and the manager is not asked to serve
// anything.
func TestModelRouterCreateRefusals(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, modelRouterBody("support", "Support", `"routing":{"enabled":true,"scope":"global","priority":4}`), nil)
	before := modelRouterList(t, h)
	manager.reloadedRouters = nil

	withRouting := func(routing string) string {
		return modelRouterBody("fresh", "Fresh", `"routing":`+routing)
	}
	cases := []struct {
		name    string
		body    string
		status  int
		message string // a part of the answer that tells the user what to fix
	}{
		{"a condition that does not compile", withRouting(`{"scope":"global","cel_expression":"model ==","priority":9}`), fasthttp.StatusBadRequest, "invalid CEL expression"},
		{"a condition that uses complexity_tier", withRouting(`{"scope":"global","cel_expression":"complexity_tier == \"SIMPLE\"","priority":9}`), fasthttp.StatusBadRequest, "complexity_tier"},
		{"an unknown scope", withRouting(`{"scope":"org","priority":9}`), fasthttp.StatusBadRequest, "invalid scope"},
		{"a team scope without scope_id", withRouting(`{"scope":"team","priority":9}`), fasthttp.StatusBadRequest, "scope_id is required"},
		{"a team scope with a blank scope_id", withRouting(`{"scope":"team","scope_id":"  ","priority":9}`), fasthttp.StatusBadRequest, "scope_id is required"},
		{"a team that does not exist", withRouting(`{"scope":"team","scope_id":"no-such-team","priority":9}`), fasthttp.StatusBadRequest, "team 'no-such-team' not found"},
		{"a customer that does not exist", withRouting(`{"scope":"customer","scope_id":"no-such-customer","priority":9}`), fasthttp.StatusBadRequest, "customer 'no-such-customer' not found"},
		{"a virtual key that does not exist", withRouting(`{"scope":"virtual_key","scope_id":"no-such-key","priority":9}`), fasthttp.StatusBadRequest, "virtual key 'no-such-key' not found"},
		{"a negative priority", withRouting(`{"scope":"global","priority":-1}`), fasthttp.StatusBadRequest, "priority must be 0 or greater"},
		{"a priority another router in the scope holds", withRouting(`{"scope":"global","priority":4}`), fasthttp.StatusConflict, "use a different priority"},
		{"a name another router has", modelRouterBody("fresh", "Support"), fasthttp.StatusConflict, `already exists: a model router with id "fresh" or name "Support"`},
		{"an id another router has", modelRouterBody("support", "Fresh"), fasthttp.StatusConflict, `already exists: a model router with id "support" or name "Fresh"`},
		{"the reserved id", modelRouterBody("defaults", "Fresh"), fasthttp.StatusBadRequest, "reserved"},
		{"an id with a slash", modelRouterBody("fresh/router", "Fresh"), fasthttp.StatusBadRequest, "may contain only letters, digits, '-' and '_'"},
		{"an id with a space", modelRouterBody("fresh router", "Fresh"), fasthttp.StatusBadRequest, "may contain only letters, digits, '-' and '_'"},
		{"an id longer than 64 characters", modelRouterBody(strings.Repeat("a", 65), "Fresh"), fasthttp.StatusBadRequest, "at most 64 characters"},
		{"no name", `{"id":"fresh",` + modelRouterPhrases + `}`, fasthttp.StatusBadRequest, "requires a name"},
		{"no phrases", `{"id":"fresh","name":"Fresh"}`, fasthttp.StatusBadRequest, "keyword lists must be non-empty"},
		{"an unknown field", modelRouterBody("fresh", "Fresh", `"priority":1`), fasthttp.StatusBadRequest, "unknown field"},
		{"an unknown routing field", withRouting(`{"scope":"global","prioritiy":9}`), fasthttp.StatusBadRequest, "unknown field"},
		{"a second JSON value after the router", modelRouterBody("fresh", "Fresh") + ` {"id":"second"}`, fasthttp.StatusBadRequest, "multiple JSON values"},
		{"a body that is not JSON", `fresh`, fasthttp.StatusBadRequest, "Invalid request payload"},
		{"an empty body", ``, fasthttp.StatusBadRequest, "Invalid request payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, h.createModelRouter, tc.body, nil)
			assert.Equal(t, tc.status, status, "%v", body)
			message := modelRouterErrorMessage(body)
			assert.Contains(t, message, tc.message)
			if !strings.Contains(tc.message, "priority") {
				assert.NotContains(t, message, "use a different priority", "only a taken priority is answered with the priority hint")
			}
			assert.Equal(t, before, modelRouterList(t, h), "a refused create stores nothing and changes no other router")
			assert.Empty(t, manager.reloadedRouters, "a refused create serves nothing")
		})
	}
}

// TestModelRouterCreateIgnoresTargetsAndGeneratesID pins that a create body's
// targets are dropped, since targets have their own endpoint, that a router
// sent without an id (or with a blank one) gets a generated id, and that the
// manager is asked to serve the router under the id it was stored with.
func TestModelRouterCreateIgnoresTargetsAndGeneratesID(t *testing.T) {
	h, manager, store := modelRouterHandler(t)
	targets := `"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]`

	generated := map[string]bool{}
	cases := []struct {
		name string
		body string
	}{
		{"an omitted id", modelRouterBody("", "Omitted", targets)},
		{"a blank id", `{"id":"  ","name":"Blank",` + modelRouterPhrases + `,` + targets + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager.reloadedRouters = nil
			body := modelRouterOK(t, h.createModelRouter, tc.body, nil)
			id, _ := body["id"].(string)
			require.NotEmpty(t, strings.TrimSpace(id), "the answer names the generated id")
			assert.False(t, generated[id], "every generated id is new")
			generated[id] = true
			assert.Equal(t, []string{id}, manager.reloadedRouters, "the manager serves the router under its generated id")
			assert.NotContains(t, body, "targets", "targets in a create body are ignored")

			stored, err := store.GetModelRouter(context.Background(), id)
			require.NoError(t, err, "the router is stored under the generated id")
			assert.Empty(t, stored.Targets)
			assert.Empty(t, manager.reloadedTargets, "a create never serves targets")
		})
	}
}

// TestModelRouterUpdate pins what PUT /{router_id} changes and what it leaves
// alone: only the router the path names is written, a name another router has
// is refused, a description sent empty is cleared, a body with any classifier
// setting replaces the whole config, and targets and routing in the body are
// ignored without even being checked. A refused update changes nothing and
// serves nothing.
func TestModelRouterUpdate(t *testing.T) {
	h, manager, store := modelRouterHandler(t)
	require.NoError(t, store.CreateTeam(context.Background(), &tables.TableTeam{ID: "team-1", Name: "Team 1"}))
	modelRouterOK(t, h.createModelRouter, modelRouterBody("support", "Support", `"description":"Support desk"`,
		`"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"}`,
		`"routing":{"enabled":true,"scope":"team","scope_id":"team-1","cel_expression":"model == \"support-assistant\"","priority":2}`), nil)
	modelRouterOK(t, h.setModelRouterTargets, `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]}`, modelRouterPath("support"))
	modelRouterOK(t, h.createModelRouter, modelRouterBody("billing", "Billing", `"description":"Billing desk"`), nil)

	refused := []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{"the name of another router", `{"name":"Billing"}`, fasthttp.StatusConflict, "already exists"},
		{"tier boundaries out of order", `{` + modelRouterPhrases + `,"tier_boundaries":{"simple_medium":0.9,"medium_complex":0.1}}`, fasthttp.StatusBadRequest, "tier boundaries must satisfy"},
		{"an unknown classifier", `{` + modelRouterPhrases + `,"classifier":"bogus"}`, fasthttp.StatusBadRequest, "complexity classifier must be"},
		{"a classifier setting without the phrases, which the config it replaces had", `{"classifier":"semantic"}`, fasthttp.StatusBadRequest, "keyword lists must be non-empty"},
		{"an unknown field", `{"name":"Renamed","priority":1}`, fasthttp.StatusBadRequest, "unknown field"},
		{"a second JSON value after the router", `{"name":"Renamed"} {"name":"Again"}`, fasthttp.StatusBadRequest, "multiple JSON values"},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			before := modelRouterList(t, h)
			manager.reloadedRouters = nil
			status, body := call(t, h.updateModelRouter, tc.body, modelRouterPath("support"))
			assert.Equal(t, tc.status, status, "%v", body)
			assert.Contains(t, modelRouterErrorMessage(body), tc.message)
			assert.Equal(t, before, modelRouterList(t, h), "a refused update leaves every router as it was")
			assert.Empty(t, manager.reloadedRouters, "a refused update serves nothing")
		})
	}

	t.Run("its own name is not a conflict", func(t *testing.T) {
		body := modelRouterOK(t, h.updateModelRouter, `{"name":"Support"}`, modelRouterPath("support"))
		assert.Equal(t, "Support", body["name"])
	})

	t.Run("an id in the body never redirects the write", func(t *testing.T) {
		billing := modelRouterRead(t, h, "billing")
		manager.reloadedRouters = nil
		body := modelRouterOK(t, h.updateModelRouter, `{"id":"billing","name":"Support team"}`, modelRouterPath("support"))
		assert.Equal(t, "support", body["id"], "the path names the router")
		assert.Equal(t, "Support team", body["name"])
		assert.Equal(t, billing, modelRouterRead(t, h, "billing"), "the router the body names is untouched")
		assert.Equal(t, []string{"support"}, manager.reloadedRouters, "only the router the path names is served again")
	})

	t.Run("targets and routing in the body are ignored, not checked", func(t *testing.T) {
		before := modelRouterRead(t, h, "support")
		manager.reloadedTargets, manager.reloadedRouting = nil, nil
		// Each would be refused on its own endpoint: the weights do not sum to 1,
		// the scope is unknown and the priority is negative.
		body := modelRouterOK(t, h.updateModelRouter, `{"name":"Support team","targets":[{"tier":"COMPLEX","model":"other","weight":0.5}],`+
			`"routing":{"enabled":false,"scope":"org","priority":-1}}`, modelRouterPath("support"))
		assert.Equal(t, before["targets"], body["targets"], "the stored targets are kept")
		assert.Equal(t, before["routing"], body["routing"], "the stored routing is kept")
		assert.Equal(t, before, modelRouterRead(t, h, "support"))
		assert.Empty(t, manager.reloadedTargets, "an update never serves targets")
		assert.Empty(t, manager.reloadedRouting, "an update never serves routing")
	})

	t.Run("an empty description clears it", func(t *testing.T) {
		body := modelRouterOK(t, h.updateModelRouter, `{"description":""}`, modelRouterPath("support"))
		assert.NotContains(t, body, "description", "a description sent empty is cleared, where an omitted one is kept")
		assert.Equal(t, "Support team", body["name"], "the omitted name is kept")
		assert.NotContains(t, modelRouterRead(t, h, "support"), "description", "the cleared description is stored")
		assert.Equal(t, "Billing desk", modelRouterRead(t, h, "billing")["description"], "another router's description is untouched")
	})

	t.Run("classifier settings replace the whole config", func(t *testing.T) {
		before := modelRouterRead(t, h, "support")
		require.Contains(t, before, "semantic", "the router was created with a semantic block")
		body := modelRouterOK(t, h.updateModelRouter, `{"keywords":{"simple_keywords":["s2"],"medium_keywords":["m2"],"complex_keywords":["c2"]}}`, modelRouterPath("support"))
		assert.Equal(t, map[string]any{"simple_keywords": []any{"s2"}, "medium_keywords": []any{"m2"}, "complex_keywords": []any{"c2"}}, body["keywords"])
		assert.NotContains(t, body, "semantic", "a section the new config leaves out is not carried over from the stored one")
		assert.Equal(t, before["name"], body["name"], "the omitted name is kept")
		assert.Equal(t, before["targets"], body["targets"], "a config save keeps the targets")
		assert.Equal(t, before["routing"], body["routing"], "a config save keeps the routing")
		assert.Equal(t, body, modelRouterRead(t, h, "support"), "the new config is stored, not only echoed")
	})
}

// TestModelRouterRoutingScopeChanges pins what a routing update answers when
// the scope changes and the body sends no scope_id: the stored scope_id is the
// one checked against the new scope. So global to team is refused for want of
// an id, team to virtual key or customer is refused because the team's id names
// no such entity, team to user is accepted with the team's id as the user's id
// (user ids are not looked up), and team to global drops the id. A router
// cannot move into a scope where another router holds its priority.
func TestModelRouterRoutingScopeChanges(t *testing.T) {
	h, _, store := modelRouterHandler(t)
	require.NoError(t, store.CreateTeam(context.Background(), &tables.TableTeam{ID: "team-1", Name: "Team 1"}))
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)
	modelRouterOK(t, h.createModelRouter, routerBody("billing", "Billing"), nil) // global, priority 1
	support := modelRouterPath("support")
	stored := func() any {
		t.Helper()
		return modelRouterRead(t, h, "support")["routing"]
	}
	refused := func(payload string, wantStatus int, wantMessage, why string) {
		t.Helper()
		before := stored()
		status, body := call(t, h.setModelRouterRouting, payload, support)
		assert.Equal(t, wantStatus, status, "%s: %v", why, body)
		assert.Contains(t, modelRouterErrorMessage(body), wantMessage, why)
		assert.Equal(t, before, stored(), "a refused routing update leaves the stored routing as it was")
	}
	saved := func(payload string) any {
		t.Helper()
		routing := modelRouterOK(t, h.setModelRouterRouting, payload, support)["routing"]
		assert.Equal(t, routing, stored(), "the routing is stored, not only echoed")
		return routing
	}

	refused(`{"scope":"team"}`, fasthttp.StatusBadRequest, "scope_id is required", "global to team needs the team's id")
	assert.Equal(t, map[string]any{"enabled": true, "scope": "team", "scope_id": "team-1", "priority": float64(0)},
		saved(`{"enabled":true,"scope":"team","scope_id":"team-1"}`))

	refused(`{"scope":"virtual_key"}`, fasthttp.StatusBadRequest, "virtual key 'team-1' not found", "the kept team id names no virtual key")
	refused(`{"scope":"customer"}`, fasthttp.StatusBadRequest, "customer 'team-1' not found", "the kept team id names no customer")
	refused(`{"scope":"team","scope_id":""}`, fasthttp.StatusBadRequest, "scope_id is required", "an empty scope_id is not a team")

	assert.Equal(t, map[string]any{"enabled": true, "scope": "user", "scope_id": "team-1", "priority": float64(0)},
		saved(`{"scope":"user"}`), "team to user keeps the team's id as the user's id")
	assert.Equal(t, map[string]any{"enabled": true, "scope": "team", "scope_id": "team-1", "priority": float64(0)},
		saved(`{"scope":"team"}`), "and back to team with the id it kept")

	assert.Equal(t, map[string]any{"enabled": true, "scope": "global", "priority": float64(0)},
		saved(`{"scope":"global"}`), "team to global drops the scope_id")
	refused(`{"scope":"team"}`, fasthttp.StatusBadRequest, "scope_id is required", "the dropped team id is not remembered")
	assert.Equal(t, map[string]any{"enabled": true, "scope": "global", "priority": float64(0)},
		saved(`{"scope":"global","scope_id":"team-1"}`), "a scope_id sent with a global scope is dropped")
	saved(`{"scope":"team","scope_id":"team-1"}`)
	assert.Equal(t, map[string]any{"enabled": true, "scope": "global", "priority": float64(0)},
		saved(`{"scope":""}`), "an empty scope is global, so sending one moves a team's router to global")

	// Priority 1 is free among the team's routers and taken among the global ones.
	assert.Equal(t, map[string]any{"enabled": true, "scope": "team", "scope_id": "team-1", "priority": float64(1)},
		saved(`{"scope":"team","scope_id":"team-1","priority":1}`), "a priority is unique per scope")
	refused(`{"scope":"global"}`, fasthttp.StatusConflict, "use a different priority", "billing holds priority 1 among the global routers")
	assert.Equal(t, map[string]any{"enabled": true, "scope": "global", "priority": float64(5)},
		saved(`{"scope":"global","priority":5}`), "a move that also sends a free priority is accepted")
}

// TestModelRouterRoutingClearsConditionAndKeepsStoredOnRefusal pins that an
// empty cel_expression clears the condition and its builder state, so the
// router takes every request in its scope, and that a refused routing update
// (a taken priority is 409 and says to use a different one) leaves the stored
// routing as it was and serves nothing.
func TestModelRouterRoutingClearsConditionAndKeepsStoredOnRefusal(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, modelRouterBody("support", "Support",
		`"routing":{"enabled":true,"scope":"global","cel_expression":"model == \"support-assistant\"","query":{"combinator":"and"},"priority":4}`), nil)
	modelRouterOK(t, h.createModelRouter, modelRouterBody("billing", "Billing",
		`"routing":{"enabled":true,"scope":"global","cel_expression":"model == \"billing-assistant\"","query":{"combinator":"or"},"priority":6}`), nil)

	refused := []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{"a priority another router in the scope holds", `{"enabled":false,"priority":4}`, fasthttp.StatusConflict, "use a different priority"},
		{"a negative priority", `{"enabled":false,"priority":-1}`, fasthttp.StatusBadRequest, "priority must be 0 or greater"},
		{"a condition that does not compile", `{"enabled":false,"cel_expression":"model =="}`, fasthttp.StatusBadRequest, "invalid CEL expression"},
		{"a second JSON value after the routing", `{"enabled":false} {"priority":9}`, fasthttp.StatusBadRequest, "multiple JSON values"},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			before := modelRouterList(t, h)
			manager.reloadedRouting = nil
			status, body := call(t, h.setModelRouterRouting, tc.body, modelRouterPath("billing"))
			assert.Equal(t, tc.status, status, "%v", body)
			assert.Contains(t, modelRouterErrorMessage(body), tc.message)
			assert.Equal(t, before, modelRouterList(t, h), "the fields sent beside the refused one are not saved either")
			assert.Empty(t, manager.reloadedRouting, "a refused routing update serves nothing")
		})
	}

	t.Run("an empty condition clears it", func(t *testing.T) {
		body := modelRouterOK(t, h.setModelRouterRouting, `{"cel_expression":""}`, modelRouterPath("billing"))
		want := map[string]any{"enabled": true, "scope": "global", "priority": float64(6)}
		assert.Equal(t, want, body["routing"], "the condition and its builder state are gone, the rest is kept")
		assert.Equal(t, want, modelRouterRead(t, h, "billing")["routing"])
		assert.Contains(t, modelRouterRead(t, h, "support")["routing"], "cel_expression", "another router's condition is untouched")
	})
}

// TestModelRouterReloadFailures pins what a write answers when it was saved but
// the manager could not serve it: 500 with a hint to restart, and the store
// keeps the write, so the saved state and the served state differ until then.
func TestModelRouterReloadFailures(t *testing.T) {
	h, manager, store := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)
	support := modelRouterPath("support")
	read := func(t *testing.T, id string) *configstore.ModelRouter {
		t.Helper()
		router, err := store.GetModelRouter(context.Background(), id)
		require.NoError(t, err)
		return router
	}
	builtIn := complexity.DefaultAnalyzerConfig()
	manager.reloadedRouters = nil
	manager.serveErr = errors.New("plugin is restarting")

	// In order: the delete comes last, once every other write has been made.
	steps := []struct {
		name    string
		handler fasthttp.RequestHandler
		body    string
		params  map[string]string
		stored  func(t *testing.T)
	}{
		{"create", h.createModelRouter, routerBody("late", "Late"), nil, func(t *testing.T) {
			late := read(t, "late")
			assert.Equal(t, "Late", late.Name, "the router is stored")
			assert.Equal(t, &configstore.ModelRouterRouting{Scope: "global", Priority: 1}, late.Routing)
			assert.Equal(t, []string{"late"}, manager.reloadedRouters)
			status, _ := call(t, h.createModelRouter, routerBody("late", "Late"), nil)
			assert.Equal(t, fasthttp.StatusConflict, status, "sending the create again is refused: the first one was stored")
		}},
		{"update", h.updateModelRouter, `{"name":"Renamed"}`, support, func(t *testing.T) {
			assert.Equal(t, "Renamed", read(t, "support").Name, "the new name is stored")
		}},
		{"targets", h.setModelRouterTargets, `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]}`, support, func(t *testing.T) {
			assert.Len(t, read(t, "support").Targets, 1, "the new targets are stored")
			assert.Equal(t, []string{"support"}, manager.reloadedTargets)
		}},
		{"routing", h.setModelRouterRouting, `{"enabled":true,"priority":5}`, support, func(t *testing.T) {
			assert.Equal(t, &configstore.ModelRouterRouting{Enabled: true, Scope: "global", Priority: 5}, read(t, "support").Routing, "the new routing is stored")
			assert.Equal(t, []string{"support"}, manager.reloadedRouting)
		}},
		{"reset", h.resetModelRouter, "", support, func(t *testing.T) {
			assert.Equal(t, builtIn.Normalized().Keywords, read(t, "support").Keywords, "the built-in phrases are stored")
		}},
		{"delete", h.deleteModelRouter, "", support, func(t *testing.T) {
			_, err := store.GetModelRouter(context.Background(), "support")
			assert.ErrorIs(t, err, configstore.ErrNotFound, "the router is deleted")
			assert.Equal(t, []string{"support"}, manager.removedRouters)
		}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			status, body := call(t, step.handler, step.body, step.params)
			assert.Equal(t, fasthttp.StatusInternalServerError, status, "%v", body)
			message := modelRouterErrorMessage(body)
			assert.Contains(t, message, "plugin is restarting", "the answer says why the router is not served")
			assert.Contains(t, message, "please restart bifrost to sync with the database")
			step.stored(t)
		})
	}
}

// TestModelRouterStatusShape pins the fields GET /{router_id}/status answers
// for a served router beside the suggestion defaults TestModelRouterEndpoints
// covers: the semantic warmup state with its progress, the llm classifier's
// state, and the shipped guidance the editors are seeded from.
func TestModelRouterStatusShape(t *testing.T) {
	h, _, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)

	body := modelRouterOK(t, h.getModelRouterStatus, "", modelRouterPath("support"))
	assert.Equal(t, string(complexity.SemanticStatusDisabled), body["state"])
	assert.Equal(t, float64(0), body["loaded"])
	assert.Equal(t, float64(0), body["total"])
	assert.Equal(t, map[string]any{"state": string(complexity.LLMStatusDisabled)}, body["llm"])
	assert.NotEmpty(t, body["decision_defaults"])
	assert.NotEmpty(t, body["llm_default_prompt"])
}

// TestModelRouterResetKeepsRouting pins the parts of a reset that
// TestModelRouterEndpoints leaves out: the phrases go back to the built-in
// ones, the description and the routing are kept, the router is served again,
// and a reset of a router that does not exist is 404 and serves nothing.
func TestModelRouterResetKeepsRouting(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, modelRouterBody("support", "Support", `"description":"Support desk"`,
		`"routing":{"enabled":true,"scope":"global","cel_expression":"model == \"support-assistant\"","query":{"combinator":"and"},"priority":4}`), nil)
	// A stored router's phrases are normalized, so the built-in ones are
	// compared in that form and not as the defaults endpoint lists them.
	builtIn := complexity.DefaultAnalyzerConfig()
	encoded, err := json.Marshal(builtIn.Normalized().Keywords)
	require.NoError(t, err)
	var builtInPhrases any
	require.NoError(t, json.Unmarshal(encoded, &builtInPhrases))
	before := modelRouterRead(t, h, "support")
	require.NotEqual(t, builtInPhrases, before["keywords"])
	manager.reloadedRouters = nil

	body := modelRouterOK(t, h.resetModelRouter, "", modelRouterPath("support"))
	assert.Equal(t, builtInPhrases, body["keywords"], "the phrases are the built-in ones again")
	assert.Equal(t, "Support desk", body["description"], "a reset keeps the description")
	assert.Equal(t, before["routing"], body["routing"], "a reset keeps the routing")
	assert.Equal(t, body, modelRouterRead(t, h, "support"), "the reset is stored, not only echoed")
	assert.Equal(t, []string{"support"}, manager.reloadedRouters, "the reset router is served again")

	manager.reloadedRouters = nil
	status, _ := call(t, h.resetModelRouter, "", modelRouterPath("never-created"))
	assert.Equal(t, fasthttp.StatusNotFound, status)
	assert.Empty(t, manager.reloadedRouters, "a reset that finds no router serves nothing")
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

// routerHidingStore answers every router read as not found, as the enterprise
// data access control answers a router whose routing is outside the caller's
// scope. Its embedded store is nil, so a route that goes on to any other store
// method panics.
type routerHidingStore struct {
	configstore.ConfigStore
}

// GetModelRouter hides every router.
func (routerHidingStore) GetModelRouter(context.Context, string) (*configstore.ModelRouter, error) {
	return nil, configstore.ErrNotFound
}

// TestModelRouterRoutesRefuseARouterTheStoreHides pins that every route under
// /api/routing/model-routers/{router_id} answers 404 for a router the config
// store does not return to the caller, before its handler runs. Behind the
// enterprise data access control that is a router whose routing is outside
// the caller's scope. Status, warmup retry, classify and the suggestion routes
// act on the in-memory router, and delete, reset and add phrases write without
// reading the router first, so without the shared check each would act on a
// router the caller cannot see. The routes are read from the router, so a
// route added later is covered too.
func TestModelRouterRoutesRefuseARouterTheStoreHides(t *testing.T) {
	SetLogger(&mockLogger{})
	h := &RoutingHandler{configStore: routerHidingStore{}, routingManager: &suggestionManagerFake{}}
	r := router.New()
	h.RegisterRoutes(r)
	params := strings.NewReplacer("{router_id}", "hidden", "{suggestion_id}", "s-1", "{run_id}", "run-1")
	checked := 0
	for method, paths := range r.List() {
		for _, path := range paths {
			if !strings.Contains(path, "{router_id}") {
				continue
			}
			checked++
			uri := params.Replace(path)
			var code int
			var body map[string]any
			assert.NotPanics(t, func() { code, body = serve(t, r, method, uri, "{}") }, "%s %s reached its handler", method, uri)
			assert.Equal(t, fasthttp.StatusNotFound, code, "%s %s: %v", method, uri, body)
			assert.Equal(t, `model router "hidden" not found`, errorText(body), "%s %s", method, uri)
		}
	}
	assert.GreaterOrEqual(t, checked, 19, "every route that names a router was checked")
}

// routerLookupStore answers every router read with err.
type routerLookupStore struct {
	configstore.ConfigStore
	err error
}

// GetModelRouter answers with the store's error.
func (s routerLookupStore) GetModelRouter(context.Context, string) (*configstore.ModelRouter, error) {
	return nil, s.err
}

// TestVisibleModelRouterFailsClosed pins how the router check answers each
// lookup result. A router the store does not return is 404. Any other failed
// lookup is 500 and never reaches the handler, as getRoutingRule answers it: a
// read that failed cannot say whether the caller may see the router. A found
// router, and one found but stored in a form this version cannot read, reach
// the handler, which reports the latter itself (an unreadable router can still
// be deleted).
func TestVisibleModelRouterFailsClosed(t *testing.T) {
	SetLogger(&mockLogger{})
	cases := []struct {
		name        string
		err         error
		wantCode    int
		wantHandler bool
	}{
		{"found", nil, fasthttp.StatusOK, true},
		{"unreadable", fmt.Errorf("%w: unknown classifier", configstore.ErrConfigUnreadable), fasthttp.StatusOK, true},
		{"hidden or missing", configstore.ErrNotFound, fasthttp.StatusNotFound, false},
		{"lookup failed", errors.New("database is locked"), fasthttp.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &RoutingHandler{configStore: routerLookupStore{err: tc.err}}
			reached := false
			handler := h.visibleModelRouter("/api/routing/model-routers/{router_id}/status", func(*fasthttp.RequestCtx) { reached = true })
			code, _ := call(t, handler, "", map[string]string{"router_id": "support"})
			assert.Equal(t, tc.wantCode, code)
			assert.Equal(t, tc.wantHandler, reached, "handler reached")
		})
	}
}
