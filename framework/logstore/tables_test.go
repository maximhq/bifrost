package logstore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSerializeFieldsDenormalizesCostBreakdown(t *testing.T) {
	log := &Log{
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{
				CachedReadTokens: 400,
			},
			Cost: &schemas.BifrostCost{
				InputCost:  0.0021,
				OutputCost: 0.0075,
				InputCostDetails: &schemas.InputCostDetails{
					TextCost:       0.00165,
					CachedReadCost: 0.00045,
				},
				AdditionalCost: 0.0003,
				AdditionalCostDetails: &schemas.AdditionalCostDetails{
					GuardrailCost: 0.0003,
				},
				TotalCost: 0.0099,
			},
		},
	}
	require.NoError(t, log.SerializeFields())

	// Token denorm still works.
	assert.Equal(t, 1000, log.PromptTokens)
	assert.Equal(t, 400, log.CachedReadTokens)
	// Input/output/additional cost split is denormalized for SQL aggregation.
	assert.InDelta(t, 0.0021, log.InputCost, 1e-12)
	assert.InDelta(t, 0.0075, log.OutputCost, 1e-12)
	assert.InDelta(t, 0.0003, log.AdditionalCost, 1e-12)
}

func TestSerializeFieldsLeavesCostBreakdownZeroWhenNoCost(t *testing.T) {
	log := &Log{
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}
	require.NoError(t, log.SerializeFields())
	assert.Zero(t, log.InputCost)
	assert.Zero(t, log.OutputCost)
	assert.Zero(t, log.AdditionalCost)
}

// TestSerializeFieldsAttributesOpaqueTotalToInput covers providers that report
// only a total with no per-category split (xAI usd-ticks, Runware): the
// denormalized columns must still reconcile to the cost column by attributing
// the total to the input side.
func TestSerializeFieldsAttributesOpaqueTotalToInput(t *testing.T) {
	total := 0.0042
	log := &Log{
		Cost: &total,
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens: 100,
			// Opaque total only, no input/output/additional split.
			Cost: &schemas.BifrostCost{TotalCost: total},
		},
	}
	require.NoError(t, log.SerializeFields())
	assert.InDelta(t, total, log.InputCost, 1e-12)
	assert.Zero(t, log.OutputCost)
	assert.Zero(t, log.AdditionalCost)
	assert.InDelta(t, total, log.InputCost+log.OutputCost+log.AdditionalCost, 1e-12)
}

// TestSerializeFieldsOpaqueTotalNoUsageCarrier covers the same opaque-total
// reconciliation when there is no usage carrier at all (e.g. OCR rows before the
// plugin denormalizes): the total on the cost column still lands on input.
func TestSerializeFieldsOpaqueTotalNoUsageCarrier(t *testing.T) {
	total := 0.01
	log := &Log{Cost: &total}
	require.NoError(t, log.SerializeFields())
	assert.InDelta(t, total, log.InputCost, 1e-12)
}

func TestDeserializeFieldsReconstructsTokenUsageFromDenormalizedColumns(t *testing.T) {
	log := &Log{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.TokenUsageParsed)
	assert.Equal(t, 10, log.TokenUsageParsed.PromptTokens)
	assert.Equal(t, 5, log.TokenUsageParsed.CompletionTokens)
	assert.Equal(t, 15, log.TokenUsageParsed.TotalTokens)
	assert.Nil(t, log.TokenUsageParsed.PromptTokensDetails)
}

func TestDeserializeFieldsPrefersSerializedTokenUsage(t *testing.T) {
	log := &Log{
		TokenUsage:       `{"prompt_tokens":99,"completion_tokens":1,"total_tokens":100}`,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.TokenUsageParsed)
	assert.Equal(t, 99, log.TokenUsageParsed.PromptTokens)
	assert.Equal(t, 1, log.TokenUsageParsed.CompletionTokens)
	assert.Equal(t, 100, log.TokenUsageParsed.TotalTokens)
}

func TestDeserializeFieldsDoesNotReconstructTokenUsageWhenSerializedValueIsMalformed(t *testing.T) {
	log := &Log{
		TokenUsage:       `{"prompt_tokens":`,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}
	require.NoError(t, log.DeserializeFields())
	assert.Nil(t, log.TokenUsageParsed)
}

func TestDeserializeFieldsSkipsTokenUsageReconstructionWhenAllZero(t *testing.T) {
	log := &Log{}
	require.NoError(t, log.DeserializeFields())
	assert.Nil(t, log.TokenUsageParsed)
}

// TestDeserializeFieldsCostBreakdownGraftsDetailFromPayload covers a normal
// hydrated row: the top-level split comes from the columns and the finer detail
// objects are grafted from token_usage.cost, which reconciles with them.
func TestDeserializeFieldsCostBreakdownGraftsDetailFromPayload(t *testing.T) {
	total := 0.0099
	log := &Log{
		Cost:           &total,
		InputCost:      0.0021,
		OutputCost:     0.0075,
		AdditionalCost: 0.0003,
		TokenUsage: `{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,` +
			`"cost":{"input_cost":0.0021,"input_cost_details":{"cached_read_cost":0.00045},` +
			`"output_cost":0.0075,"additional_cost":0.0003,` +
			`"additional_cost_details":{"guardrail_cost":0.0003},"total_cost":0.0099}}`,
	}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.CostBreakdown)
	assert.InDelta(t, 0.0021, log.CostBreakdown.InputCost, 1e-12)
	assert.InDelta(t, 0.0075, log.CostBreakdown.OutputCost, 1e-12)
	assert.InDelta(t, 0.0003, log.CostBreakdown.AdditionalCost, 1e-12)
	assert.InDelta(t, 0.0099, log.CostBreakdown.TotalCost, 1e-12)
	require.NotNil(t, log.CostBreakdown.InputCostDetails)
	assert.InDelta(t, 0.00045, log.CostBreakdown.InputCostDetails.CachedReadCost, 1e-12)
	require.NotNil(t, log.CostBreakdown.AdditionalCostDetails)
	assert.InDelta(t, 0.0003, log.CostBreakdown.AdditionalCostDetails.GuardrailCost, 1e-12)
}

// TestDeserializeFieldsCostBreakdownPrefersColumnsOnReprice covers a repriced row:
// BulkUpdateCost refreshes the columns and the cost column but leaves the
// token_usage blob stale. The top-level split must track the fresh columns, and
// stale detail must NOT be grafted since it no longer reconciles.
func TestDeserializeFieldsCostBreakdownPrefersColumnsOnReprice(t *testing.T) {
	total := 0.02 // repriced upward
	log := &Log{
		Cost:           &total,
		InputCost:      0.005,
		OutputCost:     0.015,
		AdditionalCost: 0,
		// Stale payload from before the reprice.
		TokenUsage: `{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,` +
			`"cost":{"input_cost":0.0021,"input_cost_details":{"cached_read_cost":0.00045},` +
			`"output_cost":0.0075,"total_cost":0.0099}}`,
	}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.CostBreakdown)
	assert.InDelta(t, 0.005, log.CostBreakdown.InputCost, 1e-12)
	assert.InDelta(t, 0.015, log.CostBreakdown.OutputCost, 1e-12)
	assert.InDelta(t, total, log.CostBreakdown.TotalCost, 1e-12)
	// Stale detail is dropped because it no longer matches the column split.
	assert.Nil(t, log.CostBreakdown.InputCostDetails)
	assert.Nil(t, log.CostBreakdown.OutputCostDetails)
}

// TestDeserializeFieldsCostBreakdownOpaqueTotal covers opaque-total providers
// (xAI, Runware): the whole total is attributed to input in the columns, and no
// detail is grafted since the payload's zero input does not reconcile.
func TestDeserializeFieldsCostBreakdownOpaqueTotal(t *testing.T) {
	total := 0.0042
	log := &Log{
		Cost:      &total,
		InputCost: 0.0042, // SerializeFields attributes the opaque total to input
	}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.CostBreakdown)
	assert.InDelta(t, total, log.CostBreakdown.InputCost, 1e-12)
	assert.Zero(t, log.CostBreakdown.OutputCost)
	assert.InDelta(t, total, log.CostBreakdown.TotalCost, 1e-12)
	assert.Nil(t, log.CostBreakdown.InputCostDetails)
}

// TestDeserializeFieldsCostBreakdownLegacyTotalOnly covers rows written before the
// split columns existed: only the cost column is set, so the total is attributed
// to input and the breakdown still reconciles.
func TestDeserializeFieldsCostBreakdownLegacyTotalOnly(t *testing.T) {
	total := 0.0123
	log := &Log{Cost: &total}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.CostBreakdown)
	assert.InDelta(t, total, log.CostBreakdown.InputCost, 1e-12)
	assert.Zero(t, log.CostBreakdown.OutputCost)
	assert.Zero(t, log.CostBreakdown.AdditionalCost)
	assert.InDelta(t, total, log.CostBreakdown.TotalCost, 1e-12)
}

// TestDeserializeFieldsCostBreakdownFromColumns covers rows where token_usage.cost
// is gone (OCR, offloaded, content-hidden, rebuilt-stub, list rows): cost_breakdown
// is derived from the denormalized columns, carrying the top-level split but no
// per-category detail.
func TestDeserializeFieldsCostBreakdownFromColumns(t *testing.T) {
	total := 0.0099
	log := &Log{
		Cost:           &total,
		InputCost:      0.0021,
		OutputCost:     0.0075,
		AdditionalCost: 0.0003,
	}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.CostBreakdown)
	assert.InDelta(t, 0.0021, log.CostBreakdown.InputCost, 1e-12)
	assert.InDelta(t, 0.0075, log.CostBreakdown.OutputCost, 1e-12)
	assert.InDelta(t, 0.0003, log.CostBreakdown.AdditionalCost, 1e-12)
	assert.InDelta(t, total, log.CostBreakdown.TotalCost, 1e-12)
	assert.Nil(t, log.CostBreakdown.InputCostDetails)
}

// TestDeserializeFieldsCostBreakdownNilWhenNoCost: a row with no cost gets no
// cost_breakdown, so the field tracks the scalar cost field.
func TestDeserializeFieldsCostBreakdownNilWhenNoCost(t *testing.T) {
	log := &Log{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	require.NoError(t, log.DeserializeFields())
	assert.Nil(t, log.CostBreakdown)
}

// TestMCPToolLogGovernanceSetsRoundTrip covers the multi-valued attribution
// surviving storage. The ids and names are written as separate JSON columns but
// read back index-aligned, which is what lets DAC filter the two together.
func TestMCPToolLogGovernanceSetsRoundTrip(t *testing.T) {
	entry := &MCPToolLog{
		TeamIDsParsed: []string{"team1", "team2"}, TeamNamesParsed: []string{"Team One", "Team Two"},
		CustomerIDsParsed: []string{"cust1"}, CustomerNamesParsed: []string{"Customer One"},
		BusinessUnitIDsParsed: []string{"bu1"}, BusinessUnitNamesParsed: []string{"BU One"},
		BudgetIDsParsed: []string{"budget1"}, RateLimitIDsParsed: []string{"rl1"},
	}
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if entry.TeamIDs == nil || entry.TeamNames == nil || entry.BudgetIDs == nil {
		t.Fatalf("sets not written to their columns: %+v", entry)
	}

	stored := &MCPToolLog{
		TeamIDs: entry.TeamIDs, TeamNames: entry.TeamNames,
		CustomerIDs: entry.CustomerIDs, CustomerNames: entry.CustomerNames,
		BusinessUnitIDs: entry.BusinessUnitIDs, BusinessUnitNames: entry.BusinessUnitNames,
		BudgetIDs: entry.BudgetIDs, RateLimitIDs: entry.RateLimitIDs,
	}
	if err := stored.DeserializeFields(); err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	for _, field := range []struct {
		name      string
		got, want []string
	}{
		{"team_ids", stored.TeamIDsParsed, entry.TeamIDsParsed},
		{"team_names", stored.TeamNamesParsed, entry.TeamNamesParsed},
		{"customer_ids", stored.CustomerIDsParsed, entry.CustomerIDsParsed},
		{"customer_names", stored.CustomerNamesParsed, entry.CustomerNamesParsed},
		{"business_unit_ids", stored.BusinessUnitIDsParsed, entry.BusinessUnitIDsParsed},
		{"business_unit_names", stored.BusinessUnitNamesParsed, entry.BusinessUnitNamesParsed},
		{"budget_ids", stored.BudgetIDsParsed, entry.BudgetIDsParsed},
		{"rate_limit_ids", stored.RateLimitIDsParsed, entry.RateLimitIDsParsed},
	} {
		if len(field.got) != len(field.want) {
			t.Fatalf("%s = %v, want %v", field.name, field.got, field.want)
		}
		for i := range field.want {
			if field.got[i] != field.want[i] {
				t.Fatalf("%s = %v, want %v", field.name, field.got, field.want)
			}
		}
	}
}

// TestMCPToolLogGovernanceSetsTolerateCorruptJSON keeps one unreadable column
// from failing the whole read: the row is still worth serving without it.
func TestMCPToolLogGovernanceSetsTolerateCorruptJSON(t *testing.T) {
	corrupt := "{not json"
	entry := &MCPToolLog{TeamIDs: &corrupt}
	if err := entry.DeserializeFields(); err != nil {
		t.Fatalf("deserialize must not fail on a corrupt column: %v", err)
	}
	if entry.TeamIDsParsed != nil {
		t.Fatalf("team_ids = %v, want nil", entry.TeamIDsParsed)
	}
}
