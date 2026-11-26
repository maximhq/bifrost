package handlers

import (
	"context"
	"fmt"
	"github.com/maximhq/bifrost/framework/configstore"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// modelRouterClassifierFake answers each statement with the tier named in
// tiers, by text; a text not listed gets no tier and a semantic near miss.
type modelRouterClassifierFake struct {
	mockRoutingManager
	tiers    map[string]string
	routerID string
	inputs   []routing.ModelRouterClassifyInput
}

// ClassifyWithModelRouter records the call and classifies from the fake's table.
func (m *modelRouterClassifierFake) ClassifyWithModelRouter(_ context.Context, routerID string, inputs []routing.ModelRouterClassifyInput) (routing.ModelRouterClassifyResult, error) {
	m.routerID, m.inputs = routerID, inputs
	if routerID == "missing" || routerID == "unserved" {
		return routing.ModelRouterClassifyResult{}, routing.ErrUnknownComplexityRouter
	}
	if len(inputs) == 0 {
		return routing.ModelRouterClassifyResult{}, fmt.Errorf("%w: send between 1 and 100 inputs", routing.ErrInvalidModelRouterClassify)
	}
	results := make([]routing.ModelRouterClassification, len(inputs))
	for i, in := range inputs {
		if tier, ok := m.tiers[in.Text]; ok {
			score := 0.95
			results[i] = routing.ModelRouterClassification{Tier: tier, Mechanism: complexity.MechanismSemantic, Score: &score, MatchedExemplar: "exemplar for " + tier, Trace: []schemas.RoutingEngineLogEntry{}}
			continue
		}
		results[i] = routing.ModelRouterClassification{
			Mechanism:    complexity.MechanismSkipped,
			SemanticMiss: &routing.ModelRouterSemanticMiss{Tier: complexity.TierMedium, Score: 0.4, MinSimilarity: 0.7},
			Trace:        []schemas.RoutingEngineLogEntry{},
		}
	}
	return routing.ModelRouterClassifyResult{RouterID: routerID, RouterName: "Support", Classifier: complexity.ClassifierSemantic, Results: results}, nil
}

// TestClassifyWithModelRouterScoresLabelledStatements sends a labelled page
// and gets every statement back in order with its id, text, label and
// classification, plus a summary with per-tier counts, accuracy over the
// labelled statements, and a confusion matrix.
func TestClassifyWithModelRouterScoresLabelledStatements(t *testing.T) {
	SetLogger(&mockLogger{})
	fake := &modelRouterClassifierFake{tiers: map[string]string{"hi": complexity.TierSimple, "refactor": complexity.TierMedium, "design": complexity.TierMedium}}
	h := &RoutingHandler{routingManager: fake}

	status, body := call(t, h.classifyWithModelRouter, `{"inputs":[
		{"id":"a","text":"hi","expected_tier":"simple"},
		{"id":"b","text":"refactor","previous_user_messages":["context"],"expected_tier":"MEDIUM"},
		{"id":"c","text":"design","expected_tier":"COMPLEX"},
		{"text":"unlabelled"}
	]}`, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)

	assert.Equal(t, "support", fake.routerID)
	require.Len(t, fake.inputs, 4)
	assert.Equal(t, []string{"context"}, fake.inputs[1].PreviousUserMessages)
	assert.Equal(t, "Support", body["router_name"])
	assert.Equal(t, "semantic", body["classifier"])

	results := body["results"].([]any)
	require.Len(t, results, 4)
	first := results[0].(map[string]any)
	assert.EqualValues(t, 0, first["index"])
	assert.Equal(t, "a", first["id"])
	assert.Equal(t, "hi", first["text"])
	assert.Equal(t, "SIMPLE", first["expected_tier"], "a label is normalised to upper case")
	assert.Equal(t, "SIMPLE", first["tier"])
	assert.Equal(t, true, first["correct"])
	assert.Equal(t, "exemplar for SIMPLE", first["matched_exemplar"], "the classification is flattened into the result")
	assert.Equal(t, false, results[2].(map[string]any)["correct"])
	last := results[3].(map[string]any)
	assert.NotContains(t, last, "tier", "a statement with no tier has none")
	assert.NotContains(t, last, "correct", "an unlabelled statement is not scored")
	assert.Equal(t, map[string]any{"tier": "MEDIUM", "score": 0.4, "min_similarity": 0.7}, last["semantic_miss"])

	summary := body["summary"].(map[string]any)
	assert.EqualValues(t, 4, summary["total"])
	assert.Equal(t, map[string]any{"SIMPLE": 1.0, "MEDIUM": 2.0, "COMPLEX": 0.0, "UNCLASSIFIED": 1.0}, summary["tiers"])
	assert.EqualValues(t, 3, summary["labeled"])
	assert.EqualValues(t, 2, summary["correct"])
	assert.InDelta(t, 2.0/3.0, summary["accuracy"], 1e-9)
	assert.Equal(t, map[string]any{
		"SIMPLE":  map[string]any{"SIMPLE": 1.0},
		"MEDIUM":  map[string]any{"MEDIUM": 1.0},
		"COMPLEX": map[string]any{"MEDIUM": 1.0},
	}, summary["confusion"])
}

// TestClassifyWithModelRouterUnlabelledHasNoAccuracy leaves accuracy and the
// confusion matrix out when no statement is labelled.
func TestClassifyWithModelRouterUnlabelledHasNoAccuracy(t *testing.T) {
	SetLogger(&mockLogger{})
	h := &RoutingHandler{routingManager: &modelRouterClassifierFake{}}
	status, body := call(t, h.classifyWithModelRouter, `{"inputs":[{"text":"anything"}]}`, map[string]string{"router_id": "default"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	summary := body["summary"].(map[string]any)
	assert.EqualValues(t, 0, summary["labeled"])
	assert.NotContains(t, summary, "accuracy")
	assert.NotContains(t, summary, "confusion")
}

// TestClassifyWithModelRouterErrors maps each refusal to its status: an
// unknown router is 404 and a saved one the plugin does not serve 503; a bad
// label, a misspelled field, or a page the plugin rejects is 400; a
// deployment without the classifier is 503.
func TestClassifyWithModelRouterErrors(t *testing.T) {
	SetLogger(&mockLogger{})
	saved := suggestionsConfigStore{routers: map[string]*configstore.ModelRouter{"unserved": {ID: "unserved", Name: "Unserved"}}}
	h := &RoutingHandler{configStore: saved, routingManager: &modelRouterClassifierFake{}}
	for name, tc := range map[string]struct {
		router string
		body   string
		status int
	}{
		"unknown router":           {"missing", `{"inputs":[{"text":"hi"}]}`, fasthttp.StatusNotFound},
		"saved router, not loaded": {"unserved", `{"inputs":[{"text":"hi"}]}`, fasthttp.StatusServiceUnavailable},
		"invalid label":            {"default", `{"inputs":[{"text":"hi","expected_tier":"HARD"}]}`, fasthttp.StatusBadRequest},
		"misspelled field":         {"default", `{"inputs":[{"text":"hi","expected_teir":"SIMPLE"}]}`, fasthttp.StatusBadRequest},
		"empty page":               {"default", `{"inputs":[]}`, fasthttp.StatusBadRequest},
		"not json":                 {"default", `inputs`, fasthttp.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := call(t, h.classifyWithModelRouter, tc.body, map[string]string{"router_id": tc.router})
			assert.Equal(t, tc.status, status, "%v", body)
		})
	}

	bare := &RoutingHandler{routingManager: &mockRoutingManager{}}
	status, _ := call(t, bare.classifyWithModelRouter, `{"inputs":[{"text":"hi"}]}`, map[string]string{"router_id": "default"})
	assert.Equal(t, fasthttp.StatusServiceUnavailable, status)
}
