package datasheet

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deepSeekPeakHours mirrors DeepSeek's published schedule: peak is Mon-Fri
// 01:00-04:00 and 06:00-10:00 UTC, and every other instant is off-peak.
func deepSeekPeakHours() *configstoreTables.PeakHoursSchedule {
	return &configstoreTables.PeakHoursSchedule{
		Timezone: "UTC",
		Windows: []configstoreTables.PeakHoursWindow{
			{Days: []int{1, 2, 3, 4, 5}, Start: "01:00", End: "04:00"},
			{Days: []int{1, 2, 3, 4, 5}, Start: "06:00", End: "10:00"},
		},
	}
}

// deepSeekPricing is a deepseek-v4-flash row: base rates are the PEAK prices
// and the multiplier halves them off-peak.
func deepSeekPricing() configstoreTables.TableModelPricing {
	return configstoreTables.TableModelPricing{
		Model:                   "deepseek-v4-flash",
		Provider:                "deepseek",
		Mode:                    "chat",
		InputCostPerToken:       bifrost.Ptr(0.00000044),
		OutputCostPerToken:      bifrost.Ptr(0.00000132),
		CacheReadInputTokenCost: bifrost.Ptr(0.000000014),
		OffPeakCostMultiplier:   bifrost.Ptr(0.5),
		PeakHours:               deepSeekPeakHours(),
	}
}

func utc(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return parsed
}

// TestOffPeak_DeepSeekSchedule walks DeepSeek's real windows. 2026-08-17 is a
// Monday, so 08-15 is Saturday and 08-16 is Sunday.
func TestOffPeak_DeepSeekSchedule(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): deepSeekPricing(),
	})

	// 1000 prompt + 1000 completion at peak:
	// 1000*0.00000044 + 1000*0.00000132 = 0.00176
	const peakCost = 0.00176

	cases := []struct {
		name     string
		at       string
		expected float64
	}{
		{"monday inside first peak window", "2026-08-17T02:00:00Z", peakCost},
		{"monday inside second peak window", "2026-08-17T09:59:00Z", peakCost},
		{"monday in the gap between windows", "2026-08-17T05:00:00Z", peakCost / 2},
		{"monday before the first window", "2026-08-17T00:30:00Z", peakCost / 2},
		{"monday after the last window", "2026-08-17T23:00:00Z", peakCost / 2},
		{"saturday at a peak-hour clock time", "2026-08-15T02:00:00Z", peakCost / 2},
		{"sunday at a peak-hour clock time", "2026-08-16T02:00:00Z", peakCost / 2},
		// Half-open [start, end): the start instant is peak, the end instant is not.
		{"exact window start is peak", "2026-08-17T01:00:00Z", peakCost},
		{"exact window end is off-peak", "2026-08-17T04:00:00Z", peakCost / 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", &schemas.BifrostLLMUsage{
				PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
			})
			cost := s.CalculateCost(resp, &LookupScopes{BilledAt: utc(t, tc.at)})
			assert.InDelta(t, tc.expected, cost, 1e-12)
		})
	}
}

// TestOffPeak_DiscountsCacheReads verifies the discount reaches the cache-hit
// rate, not just the plain input rate — DeepSeek halves all three categories.
func TestOffPeak_DiscountsCacheReads(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): deepSeekPricing(),
	})

	usage := &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 800},
	}

	peak := s.CalculateCostBreakdown(
		makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", usage),
		&LookupScopes{BilledAt: utc(t, "2026-08-17T02:00:00Z")},
	)
	off := s.CalculateCostBreakdown(
		makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", usage),
		&LookupScopes{BilledAt: utc(t, "2026-08-17T05:00:00Z")},
	)
	require.NotNil(t, peak)
	require.NotNil(t, off)
	require.NotNil(t, peak.InputCostDetails)
	require.NotNil(t, off.InputCostDetails)

	assert.Greater(t, peak.InputCostDetails.CachedReadCost, 0.0)
	assert.InDelta(t, peak.InputCostDetails.CachedReadCost/2, off.InputCostDetails.CachedReadCost, 1e-15)
	assert.InDelta(t, peak.InputCostDetails.TextCost/2, off.InputCostDetails.TextCost, 1e-15)
	assert.InDelta(t, peak.OutputCost/2, off.OutputCost, 1e-15)
	assert.InDelta(t, peak.TotalCost/2, off.TotalCost, 1e-15)
}

// TestOffPeak_FlatFeesAreNotDiscounted pins the exclusions: a per-request
// surcharge and a per-search-query fee are not usage charges, so an off-peak
// request pays them in full.
func TestOffPeak_FlatFeesAreNotDiscounted(t *testing.T) {
	pricing := deepSeekPricing()
	pricing.CostPerRequest = bifrost.Ptr(0.01)
	pricing.SearchContextCostPerQuery = bifrost.Ptr(0.005)

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): pricing,
	})

	queries := 2
	usage := &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{NumSearchQueries: &queries},
	}

	bd := s.CalculateCostBreakdown(
		makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", usage),
		&LookupScopes{BilledAt: utc(t, "2026-08-17T05:00:00Z")},
	)
	require.NotNil(t, bd)
	require.NotNil(t, bd.InputCostDetails)
	require.NotNil(t, bd.OutputCostDetails)

	// Flat fees at full price...
	assert.InDelta(t, 0.01, bd.InputCostDetails.RequestCost, 1e-12)
	assert.InDelta(t, 0.01, bd.OutputCostDetails.SearchQueriesCost, 1e-12)
	// ...token costs halved.
	assert.InDelta(t, 1000*0.00000044/2, bd.InputCostDetails.TextCost, 1e-15)
	assert.InDelta(t, 1000*0.00000132/2, bd.OutputCostDetails.TextCost, 1e-15)
	// Sides still reconcile.
	assert.InDelta(t, bd.InputCost+bd.OutputCost+bd.AdditionalCost, bd.TotalCost, 1e-12)
}

// TestOffPeak_FailsClosed covers every way the discount can fail to resolve.
// All of them must bill at the peak (higher) rate — base rates are the peak
// prices, so failing open would hand out a discount that was never configured.
func TestOffPeak_FailsClosed(t *testing.T) {
	const peakCost = 0.00176

	mutate := map[string]func(p *configstoreTables.TableModelPricing){
		"no multiplier": func(p *configstoreTables.TableModelPricing) { p.OffPeakCostMultiplier = nil },
		"no schedule":   func(p *configstoreTables.TableModelPricing) { p.PeakHours = nil },
		"empty windows": func(p *configstoreTables.TableModelPricing) {
			p.PeakHours = &configstoreTables.PeakHoursSchedule{Timezone: "UTC"}
		},
		"unknown timezone": func(p *configstoreTables.TableModelPricing) {
			p.PeakHours.Timezone = "Mars/Olympus_Mons"
		},
		"malformed clock": func(p *configstoreTables.TableModelPricing) {
			p.PeakHours.Windows = []configstoreTables.PeakHoursWindow{{Days: []int{1}, Start: "0100", End: "04:00"}}
		},
		"out of range hour": func(p *configstoreTables.TableModelPricing) {
			p.PeakHours.Windows = []configstoreTables.PeakHoursWindow{{Days: []int{1}, Start: "25:00", End: "26:00"}}
		},
		"no days": func(p *configstoreTables.TableModelPricing) {
			p.PeakHours.Windows = []configstoreTables.PeakHoursWindow{{Days: nil, Start: "01:00", End: "04:00"}}
		},
		"multiplier above one": func(p *configstoreTables.TableModelPricing) {
			p.OffPeakCostMultiplier = bifrost.Ptr(1.5)
		},
		"zero multiplier": func(p *configstoreTables.TableModelPricing) {
			p.OffPeakCostMultiplier = bifrost.Ptr(0.0)
		},
		"negative multiplier": func(p *configstoreTables.TableModelPricing) {
			p.OffPeakCostMultiplier = bifrost.Ptr(-0.5)
		},
	}

	for name, fn := range mutate {
		t.Run(name, func(t *testing.T) {
			pricing := deepSeekPricing()
			fn(&pricing)
			s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
				makeKey("deepseek-v4-flash", "deepseek", "chat"): pricing,
			})
			resp := makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", &schemas.BifrostLLMUsage{
				PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
			})
			// 05:00 Monday is off-peak under the real schedule, so anything
			// other than the full peak price means the guard leaked.
			cost := s.CalculateCost(resp, &LookupScopes{BilledAt: utc(t, "2026-08-17T05:00:00Z")})
			assert.InDelta(t, peakCost, cost, 1e-12)
		})
	}
}

// TestOffPeak_MidnightWrappingWindow covers a window whose end is before its
// start: 22:00-02:00 on Monday must also cover Tuesday 01:00.
func TestOffPeak_MidnightWrappingWindow(t *testing.T) {
	pricing := deepSeekPricing()
	pricing.PeakHours = &configstoreTables.PeakHoursSchedule{
		Timezone: "UTC",
		Windows: []configstoreTables.PeakHoursWindow{
			{Days: []int{1}, Start: "22:00", End: "02:00"}, // Monday 22:00 -> Tuesday 02:00
		},
	}
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): pricing,
	})

	const peakCost = 0.00176
	cases := []struct {
		name     string
		at       string
		expected float64
	}{
		{"monday late evening is peak", "2026-08-17T23:30:00Z", peakCost},
		{"tuesday small hours still peak", "2026-08-18T01:30:00Z", peakCost},
		{"tuesday after the wrap ends", "2026-08-18T02:30:00Z", peakCost / 2},
		{"monday before the window opens", "2026-08-17T21:00:00Z", peakCost / 2},
		{"tuesday evening is not covered", "2026-08-18T23:30:00Z", peakCost / 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", &schemas.BifrostLLMUsage{
				PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
			})
			cost := s.CalculateCost(resp, &LookupScopes{BilledAt: utc(t, tc.at)})
			assert.InDelta(t, tc.expected, cost, 1e-12)
		})
	}
}

// TestOffPeak_NonUTCSchedule verifies the schedule is evaluated in its own
// timezone, not the instant's UTC clock reading.
func TestOffPeak_NonUTCSchedule(t *testing.T) {
	pricing := deepSeekPricing()
	pricing.PeakHours = &configstoreTables.PeakHoursSchedule{
		Timezone: "Asia/Kolkata", // UTC+05:30
		Windows: []configstoreTables.PeakHoursWindow{
			{Days: []int{1}, Start: "09:00", End: "17:00"},
		},
	}
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): pricing,
	})

	const peakCost = 0.00176
	resp := func() *schemas.BifrostResponse {
		return makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", &schemas.BifrostLLMUsage{
			PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
		})
	}

	// 2026-08-17 05:00 UTC == 10:30 Monday in Kolkata -> peak.
	assert.InDelta(t, peakCost,
		s.CalculateCost(resp(), &LookupScopes{BilledAt: utc(t, "2026-08-17T05:00:00Z")}), 1e-12)
	// 2026-08-17 02:00 UTC == 07:30 Monday in Kolkata -> off-peak.
	assert.InDelta(t, peakCost/2,
		s.CalculateCost(resp(), &LookupScopes{BilledAt: utc(t, "2026-08-17T02:00:00Z")}), 1e-12)
}

// TestOffPeak_AppliesToNonTextModalities confirms the single application site
// in computeCostFromInput reaches modalities other than chat.
func TestOffPeak_AppliesToNonTextModalities(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("embed-model", "deepseek", "embedding"): {
			Model: "embed-model", Provider: "deepseek", Mode: "embedding",
			InputCostPerToken:     bifrost.Ptr(0.000001),
			OffPeakCostMultiplier: bifrost.Ptr(0.5),
			PeakHours:             deepSeekPeakHours(),
		},
	})

	usage := &schemas.BifrostLLMUsage{PromptTokens: 1000, TotalTokens: 1000}
	peak := s.CalculateCost(makeEmbeddingResponse(schemas.DeepSeek, "embed-model", usage),
		&LookupScopes{BilledAt: utc(t, "2026-08-17T02:00:00Z")})
	off := s.CalculateCost(makeEmbeddingResponse(schemas.DeepSeek, "embed-model", usage),
		&LookupScopes{BilledAt: utc(t, "2026-08-17T05:00:00Z")})

	assert.InDelta(t, 0.001, peak, 1e-12)
	assert.InDelta(t, 0.0005, off, 1e-12)
}

// TestOffPeak_ZeroBilledAtFallsBackToWallClock verifies a caller that never
// learned the request start time still prices coherently rather than treating
// the zero time (year 1) as a real instant.
func TestOffPeak_ZeroBilledAtFallsBackToWallClock(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): deepSeekPricing(),
	})
	resp := makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
	})

	const peakCost = 0.00176
	expected := peakCost / 2
	if peak, ok := isWithinPeakWindows(deepSeekPeakHours(), time.Now()); ok && peak {
		expected = peakCost
	}
	assert.InDelta(t, expected, s.CalculateCost(resp, &LookupScopes{}), 1e-12)
}

// TestOffPeak_NilScopes exercises the nil-scopes entry point, which builds an
// empty LookupScopes and therefore a zero BilledAt.
func TestOffPeak_NilScopes(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): deepSeekPricing(),
	})
	resp := makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
	})
	assert.NotPanics(t, func() { s.CalculateCost(resp, nil) })
}

// TestParseClockMinutes pins the "HH:MM" parser, including the 24:00
// end-of-day marker that lets a window span a full day.
func TestParseClockMinutes(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"00:00", 0, true},
		{"01:00", 60, true},
		{"09:59", 599, true},
		{"23:59", 1439, true},
		{"24:00", 1440, true},
		{"24:01", 0, false},
		{"25:00", 0, false},
		{"01:60", 0, false},
		{"-1:00", 0, false},
		{"0100", 0, false},
		{"", 0, false},
		{"aa:bb", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseClockMinutes(tc.in)
		assert.Equal(t, tc.ok, ok, "ok for %q", tc.in)
		if tc.ok {
			assert.Equal(t, tc.want, got, "value for %q", tc.in)
		}
	}
}

// TestEntryUnmarshal_TimeOfDayFields verifies the two fields survive Entry's
// custom UnmarshalJSON, which routes through an alias struct to special-case
// search_context_cost_per_query and could otherwise drop embedded fields.
func TestEntryUnmarshal_TimeOfDayFields(t *testing.T) {
	raw := []byte(`{
		"provider": "deepseek",
		"mode": "chat",
		"input_cost_per_token": 0.00000044,
		"output_cost_per_token": 0.00000132,
		"off_peak_cost_multiplier": 0.5,
		"peak_hours": {
			"timezone": "UTC",
			"windows": [
				{"days": [1,2,3,4,5], "start": "01:00", "end": "04:00"},
				{"days": [1,2,3,4,5], "start": "06:00", "end": "10:00"}
			]
		}
	}`)

	var entry Entry
	require.NoError(t, entry.UnmarshalJSON(raw))

	require.NotNil(t, entry.OffPeakCostMultiplier)
	assert.Equal(t, 0.5, *entry.OffPeakCostMultiplier)
	require.NotNil(t, entry.PeakHours)
	assert.Equal(t, "UTC", entry.PeakHours.Timezone)
	require.Len(t, entry.PeakHours.Windows, 2)
	assert.Equal(t, []int{1, 2, 3, 4, 5}, entry.PeakHours.Windows[0].Days)
	assert.Equal(t, "01:00", entry.PeakHours.Windows[0].Start)
	assert.Equal(t, "10:00", entry.PeakHours.Windows[1].End)

	// And that they survive the Entry -> Table -> Entry round trip the sync uses.
	table := convertEntryToTablePricing("deepseek-v4-flash", entry)
	require.NotNil(t, table.OffPeakCostMultiplier)
	assert.Equal(t, 0.5, *table.OffPeakCostMultiplier)
	require.NotNil(t, table.PeakHours)
	assert.Len(t, table.PeakHours.Windows, 2)

	back := convertTablePricingToEntry(&table)
	require.NotNil(t, back.OffPeakCostMultiplier)
	assert.Equal(t, 0.5, *back.OffPeakCostMultiplier)
	require.NotNil(t, back.PeakHours)
	assert.Equal(t, "UTC", back.PeakHours.Timezone)
	assert.Len(t, back.PeakHours.Windows, 2)
}

// TestOffPeak_EndToEndFromDatasheetFile drives the real sync entry point
// against a file:// datasheet, so the fields are exercised through URL fetch,
// JSON decode, Entry -> TableModelPricing conversion and the in-memory catalog
// — not just the hand-built pricing rows the other tests use.
func TestOffPeak_EndToEndFromDatasheetFile(t *testing.T) {
	datasheet := `{
		"deepseek-v4-flash": {
			"provider": "deepseek",
			"mode": "chat",
			"input_cost_per_token": 0.00000044,
			"output_cost_per_token": 0.00000132,
			"off_peak_cost_multiplier": 0.5,
			"peak_hours": {
				"timezone": "UTC",
				"windows": [
					{"days": [1,2,3,4,5], "start": "01:00", "end": "04:00"},
					{"days": [1,2,3,4,5], "start": "06:00", "end": "10:00"}
				]
			}
		}
	}`

	path := filepath.Join(t.TempDir(), "datasheet.json")
	require.NoError(t, os.WriteFile(path, []byte(datasheet), 0o600))

	s := newTestStore()
	s.url = "file://" + path
	require.NoError(t, s.LoadFromURLIntoMemory(context.Background()))

	pricing, ok := s.pricingData[makeKey("deepseek-v4-flash", "deepseek", "chat")]
	require.True(t, ok, "model missing from catalog after sync")
	require.NotNil(t, pricing.OffPeakCostMultiplier, "off_peak_cost_multiplier lost in sync")
	assert.Equal(t, 0.5, *pricing.OffPeakCostMultiplier)
	require.NotNil(t, pricing.PeakHours, "peak_hours lost in sync")
	require.Len(t, pricing.PeakHours.Windows, 2)

	resp := func() *schemas.BifrostResponse {
		return makeChatResponse(schemas.DeepSeek, "deepseek-v4-flash", &schemas.BifrostLLMUsage{
			PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
		})
	}
	const peakCost = 0.00176
	assert.InDelta(t, peakCost,
		s.CalculateCost(resp(), &LookupScopes{BilledAt: utc(t, "2026-08-17T02:00:00Z")}), 1e-12)
	assert.InDelta(t, peakCost/2,
		s.CalculateCost(resp(), &LookupScopes{BilledAt: utc(t, "2026-08-17T05:00:00Z")}), 1e-12)
}
