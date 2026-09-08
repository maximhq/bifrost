package logging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// servedModelPricingJSON holds the Azure Model Router surcharge row and the model
// it routes to. Model Router is the one case where pricing reads the served model,
// so it is the case the recalc parity test below exercises.
const servedModelPricingJSON = `{
  "model-router": {"provider": "azure", "mode": "chat", "input_cost_per_token": 1.4e-07},
  "gpt-4.1-mini": {"provider": "azure", "mode": "chat", "input_cost_per_token": 4e-07, "output_cost_per_token": 1.6e-06}
}`

func newServedModelPlugin(t *testing.T) *LoggerPlugin {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(path, []byte(servedModelPricingJSON), 0o600); err != nil {
		t.Fatalf("write pricing file: %v", err)
	}
	ds := datasheet.New(nil, testLogger{}, datasheet.Config{URL: "file://" + path})
	if err := ds.LoadFromURLIntoMemory(context.Background()); err != nil {
		t.Fatalf("load pricing datasheet: %v", err)
	}
	return &LoggerPlugin{
		store:          newTestStore(t),
		pricingManager: modelcatalog.NewTestCatalogWithDatasheet(ds),
		logger:         testLogger{},
	}
}

func TestApplyServedModel(t *testing.T) {
	tests := []struct {
		name   string
		entry  *logstore.Log
		result *schemas.BifrostResponse
		want   *string
	}{
		{
			name:   "records the model Azure Model Router routed to",
			entry:  &logstore.Log{Provider: string(schemas.Azure), Model: "model-router"},
			result: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Model: "gpt-4.1-mini"}},
			want:   schemas.Ptr("gpt-4.1-mini"),
		},
		{
			name:   "records the dated snapshot an OpenAI floating alias resolved to",
			entry:  &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-5.5"},
			result: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Model: "gpt-5.5-2026-07-15"}},
			want:   schemas.Ptr("gpt-5.5-2026-07-15"),
		},
		{
			name:  "reads a Responses reply",
			entry: &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-5.5"},
			result: &schemas.BifrostResponse{
				ResponsesResponse: &schemas.BifrostResponsesResponse{Model: "gpt-5.5-2026-07-15"},
			},
			want: schemas.Ptr("gpt-5.5-2026-07-15"),
		},
		{
			name:  "reads the envelope on a streamed Responses reply",
			entry: &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-5.5"},
			result: &schemas.BifrostResponse{
				ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
					Response: &schemas.BifrostResponsesResponse{Model: "gpt-5.5-2026-07-15"},
				},
			},
			want: schemas.Ptr("gpt-5.5-2026-07-15"),
		},
		{
			name:   "stays unset when the provider echoes what was requested",
			entry:  &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"},
			result: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Model: "gpt-4o"}},
			want:   nil,
		},
		{
			name:   "stays unset when the response carries no model",
			entry:  &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"},
			result: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}},
			want:   nil,
		},
		{
			name:   "stays unset for a response type that has no model",
			entry:  &logstore.Log{Provider: string(schemas.OpenAI), Model: "whisper-1"},
			result: &schemas.BifrostResponse{TranscriptionResponse: &schemas.BifrostTranscriptionResponse{}},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyServedModel(tt.entry, tt.result)
			got := tt.entry.ServedModel
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("served_model = %q, want unset", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("served_model unset, want %q", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Fatalf("served_model = %q, want %q", *got, *tt.want)
			}
		})
	}
}

// TestCalculateCostForLog_AzureModelRouterMatchesLive is the pricing reason to
// persist the served model. A Model Router row bills the router's own surcharge
// plus the model it routed to, and pricing reads the served model off the response
// body — which a recalc has to rebuild from columns. Without it the recalc silently
// drops the underlying model's leg and bills only the surcharge.
func TestCalculateCostForLog_AzureModelRouterMatchesLive(t *testing.T) {
	plugin := newServedModelPlugin(t)

	const promptTokens, completionTokens = 10000, 2000
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}

	want := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4.1-mini",
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.Azure, Model: "model-router"},
			},
		},
	}, string(schemas.Azure))

	// Surcharge:  10000 * 1.4e-07                = 0.0014
	// Underlying: 10000 * 4e-07 + 2000 * 1.6e-06 = 0.0072
	const wantCost = 0.0086
	if diff := want - wantCost; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("live cost = %v, want %v", want, wantCost)
	}

	entry := &logstore.Log{
		ID:               "req-model-router",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.Azure),
		Model:            "model-router",
		ServedModel:      schemas.Ptr("gpt-4.1-mini"),
		Status:           "success",
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		TokenUsageParsed: usage,
	}

	got, err := plugin.calculateCostForLog(entry)
	if err != nil {
		t.Fatalf("calculateCostForLog() error = %v", err)
	}
	if diff := got - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("recalculated cost = %v, want %v (live)", got, want)
	}
}

// TestCalculateCostForLog_ServedModelDoesNotRepriceOrdinaryRows guards the blast
// radius of restoring the served model on every reconstructed response: pricing
// reads the response body's model only in the Model Router split, so an OpenAI row
// that resolved to a dated snapshot must still price at the model that was asked for.
func TestCalculateCostForLog_ServedModelDoesNotRepriceOrdinaryRows(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	const promptTokens, completionTokens = 1000, 500
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
	newEntry := func() *logstore.Log {
		return &logstore.Log{
			ID:               "req-openai-snapshot",
			Timestamp:        time.Now().UTC(),
			Object:           string(schemas.ChatCompletionRequest),
			Provider:         string(schemas.OpenAI),
			Model:            "gpt-4o",
			Status:           "success",
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
			TokenUsageParsed: usage,
		}
	}

	baseline, err := plugin.calculateCostForLog(newEntry())
	if err != nil {
		t.Fatalf("calculateCostForLog() error = %v", err)
	}
	if baseline <= 0 {
		t.Fatalf("baseline cost must be positive for the comparison to mean anything, got %v", baseline)
	}

	entry := newEntry()
	// A snapshot name with no pricing row of its own: if it ever became a pricing
	// candidate, the row would reprice to zero rather than to gpt-4o rates.
	entry.ServedModel = schemas.Ptr("gpt-4o-2099-01-01")

	got, err := plugin.calculateCostForLog(entry)
	if err != nil {
		t.Fatalf("calculateCostForLog() error = %v", err)
	}
	if diff := got - baseline; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost with served_model = %v, want %v (unchanged)", got, baseline)
	}
}
