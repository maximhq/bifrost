package logstore

// Round-5 defect: the real billing entry point, SearchLogsForBilling, runs
// stripNonBillingPayloadBytes on every row it returns (rdb.go), releasing the
// generated-image bytes pricing never reads. The CAS billing hydration then
// compared that deliberately-cleared in-memory object against the untouched
// database columns — verifyRootRowRevision byte-compared image_generation_output
// and hydrateLogForBilling's modality-integrity check read the same cleared
// column — so every healthy image_generation row whose output stayed in the
// row (below the CAS offload threshold) failed with
// ErrCasConcurrentModification on the verify and/or "hydrated payload has no
// image_generation_output", and was misreported Unpriceable. No concurrency
// is needed to trigger it: HasObject=true (other fields offloaded), a small
// row-resident image output, done.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newTestCasBilling is newTestCas with a configurable offload threshold, so a
// test can keep one payload field row-resident while another goes to CAS.
func newTestCasBilling(t *testing.T, minFieldBytes int) (*CasLogStore, *RDBLogStore) {
	t.Helper()
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "cas-bill.db")}, hybridTestLogger{})
	require.NoError(t, err)
	cfg := &ContentAddressedConfig{Enabled: true, MinFieldBytes: minFieldBytes, MinChunkBytes: 32}
	cas, err := newCasLogStore(ctx, inner, cfg, hybridTestLogger{})
	require.NoError(t, err)
	return cas, inner
}

// billingSearchRow runs the REAL billing entry point — the same projection,
// strip and all — and returns the row for id.
func billingSearchRow(t *testing.T, inner *RDBLogStore, id string) *Log {
	t.Helper()
	ctx := context.Background()
	result, err := inner.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	for i := range result.Logs {
		if result.Logs[i].ID == id {
			return &result.Logs[i]
		}
	}
	t.Fatalf("log %s not returned by SearchLogsForBilling", id)
	return nil
}

// Defect: a row-resident (small) image output is stripped in memory by
// SearchLogsForBilling while the database column keeps the full bytes. The
// billing hydration must not treat that deliberate divergence as a concurrent
// modification or a missing pricing input: the parsed structure the strip
// preserves is exactly what pricing reads, so the row must be Hydrated.
func TestCas_BillingSearchStrippedRowResidentImageHydrates(t *testing.T) {
	cas, inner := newTestCasBilling(t, 4096)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const id = "imgstrip-1"
	entry := bigChatEntry(id, strings.Repeat("history that goes to CAS ", 200))
	entry.Object = "image_generation"
	entry.ImageGenerationOutputParsed = &schemas.BifrostImageGenerationResponse{
		Data: []schemas.ImageData{{B64JSON: "aGVsbG8=", RevisedPrompt: "a shiny red apple"}},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	// Scene-setting: input_history offloaded to CAS, the image output small
	// enough to stay in the row. HasObject=true with a row-resident modality
	// column is the exact shape the defect lives in.
	assert.Positive(t, casPointerCount(t, cas, id, "input_history"))
	assert.Zero(t, casPointerCount(t, cas, id, "image_generation_output"))
	row, err := inner.FindByID(ctx, id)
	require.NoError(t, err)
	require.True(t, row.HasObject)
	require.NotEmpty(t, row.ImageGenerationOutput, "small image output stays in the row")

	// The real billing path: the search strips the in-memory copy.
	billed := billingSearchRow(t, inner, id)
	require.True(t, billed.HasObject)
	require.Empty(t, billed.ImageGenerationOutput,
		"SearchLogsForBilling must have stripped the image bytes")
	require.NotNil(t, billed.ImageGenerationOutputParsed,
		"the parsed structure (image count) must survive the strip")
	require.Len(t, billed.ImageGenerationOutputParsed.Data, 1)
	require.Empty(t, billed.ImageGenerationOutputParsed.Data[0].B64JSON)

	result, err := cas.HydrateBillingChunk(ctx, []*Log{billed})
	require.NoError(t, err)
	assert.Contains(t, result.Hydrated, id,
		"a healthy row-resident image must be billable, not Unpriceable")
	assert.NotContains(t, result.Unpriceable, id)
	assert.True(t, billed.billingPayloadsHydrated)
	assert.NotNil(t, billed.ImageGenerationOutputParsed,
		"pricing reads the image count from the parsed structure")
	assert.Len(t, billed.ImageGenerationOutputParsed.Data, 1)
}

// Regression: an image output large enough to be CAS-offloaded must keep
// hydrating through the billing-search path (the round-4 scenario, now driven
// by SearchLogsForBilling + strip instead of a full-row FindByID).
func TestCas_BillingSearchCasOffloadedImageStillHydrates(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const id = "imgstrip-2"
	entry := bigChatEntry(id, "tiny")
	entry.Object = "image_generation"
	entry.ImageGenerationOutputParsed = &schemas.BifrostImageGenerationResponse{
		Data: []schemas.ImageData{{B64JSON: strings.Repeat("aGVsbG8=", 32), RevisedPrompt: "a shiny red apple"}},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, id, "image_generation_output"),
		"large image payload must be offloaded to CAS")

	billed := billingSearchRow(t, inner, id)
	require.True(t, billed.HasObject)
	require.Empty(t, billed.ImageGenerationOutput, "offloaded column is empty in the row")

	result, err := cas.HydrateBillingChunk(ctx, []*Log{billed})
	require.NoError(t, err)
	assert.Contains(t, result.Hydrated, id)
	assert.NotContains(t, result.Unpriceable, id)
	assert.True(t, billed.billingPayloadsHydrated)
	require.NotNil(t, billed.ImageGenerationOutputParsed)
	assert.Len(t, billed.ImageGenerationOutputParsed.Data, 1)
	assert.Empty(t, billed.ImageGenerationOutputParsed.Data[0].B64JSON,
		"strip still releases the base64 bytes pricing never reads")
}

// Billing re-authorizes the current root: out-of-scope ownership fails closed;
// hidden content remains billable and the complete current metadata is used.
func TestCas_BillingHalfOpenWindowOwnershipAndHiddenStillDetected(t *testing.T) {
	cas, inner := newTestCasBilling(t, 4096)
	defer cas.Close(context.Background())
	ctx := context.Background()

	makeRow := func(id string) {
		entry := bigChatEntry(id, strings.Repeat("history that goes to CAS ", 200))
		entry.Object = "image_generation"
		entry.UserID = strPtr("original-owner")
		entry.ImageGenerationOutputParsed = &schemas.BifrostImageGenerationResponse{
			Data: []schemas.ImageData{{B64JSON: "aGVsbG8=", RevisedPrompt: "a shiny red apple"}},
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, cas.CreateIfNotExists(ctx, entry))
		assert.Positive(t, casPointerCount(t, cas, id, "input_history"))
	}
	makeRow("billhalf-1")
	makeRow("billhalf-2")

	// Sanity: without interference both rows hydrate cleanly.
	clean := billingSearchRow(t, inner, "billhalf-1")
	result, err := cas.HydrateBillingChunk(ctx, []*Log{clean})
	require.NoError(t, err)
	require.Contains(t, result.Hydrated, "billhalf-1")

	// Half-open window: rows read by the billing search, then the writer
	// commits before hydration runs.
	staleOwner := billingSearchRow(t, inner, "billhalf-1")
	staleHidden := billingSearchRow(t, inner, "billhalf-2")
	require.NoError(t, cas.Update(ctx, "billhalf-1", map[string]interface{}{"user_id": "someone-else"}))
	require.NoError(t, cas.Update(ctx, "billhalf-2", map[string]interface{}{"content_hidden": true}))

	scopedCtx := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("user_id = ?", "original-owner") })
	result, err = cas.HydrateBillingChunk(scopedCtx, []*Log{staleOwner, staleHidden})
	require.NoError(t, err)
	assert.Contains(t, result.Unpriceable, "billhalf-1",
		"a concurrent ownership change must still be detected")
	assert.Contains(t, result.Hydrated, "billhalf-2", "billing explicitly permits hidden content on the fresh authorized snapshot")
	assert.True(t, staleHidden.ContentHidden)
	assert.NotContains(t, result.Hydrated, "billhalf-1")
	assert.NotContains(t, result.Unpriceable, "billhalf-2")
	assert.False(t, staleOwner.billingPayloadsHydrated,
		"a refused row must not carry the hydrated success marker")
	assert.True(t, staleHidden.billingPayloadsHydrated)

	// A fresh billing pass over the SAME rows (fresh search) recovers them:
	// the refusal was a retryable staleness report, not permanent damage.
	freshOwner := billingSearchRow(t, inner, "billhalf-1")
	freshHidden := billingSearchRow(t, inner, "billhalf-2")
	result, err = cas.HydrateBillingChunk(ctx, []*Log{freshOwner, freshHidden})
	require.NoError(t, err)
	assert.Contains(t, result.Hydrated, "billhalf-1")
	assert.Contains(t, result.Hydrated, "billhalf-2")
}

// The real billing projection deliberately omits payload bytes. Rehydration
// must refresh every pricing scalar, including fields outside the old manual
// revision list, and select modality inputs using the CURRENT object type.
func TestCas_BillingSearchRefreshesCompletePricingRevision(t *testing.T) {
	for _, changeObject := range []bool{false, true} {
		t.Run(fmt.Sprintf("change-object-%t", changeObject), func(t *testing.T) {
			cas, inner := newTestCas(t)
			defer cas.Close(context.Background())
			ctx := context.Background()
			entry := bigChatEntry("bill-revision", strings.Repeat("old history ", 40))
			entry.Object = "image_generation"
			entry.Model = "old-model"
			entry.Provider = "old-provider"
			entry.UserID = strPtr("old-user")
			entry.ImageGenerationOutputParsed = &schemas.BifrostImageGenerationResponse{
				Data: []schemas.ImageData{{B64JSON: strings.Repeat("aGVsbG8=", 32)}},
			}
			require.NoError(t, entry.SerializeFields())
			require.NoError(t, cas.CreateIfNotExists(ctx, entry))
			billed := billingSearchRow(t, inner, entry.ID)
			require.True(t, billed.HasObject)
			require.True(t, billingRowNeedsHydration(billed, cas.excluded))
			updates := map[string]any{"model": "new-model", "provider": "new-provider", "user_id": "new-user"}
			if changeObject {
				updates["object_type"] = "chat.completion"
				updates["token_usage"] = `{"prompt_tokens":17,"completion_tokens":23,"total_tokens":40}`
			} else {
				updates["image_generation_output"] = `{"data":[{"b64_json":"new-a"},{"b64_json":"new-b"}]}`
			}
			require.NoError(t, cas.Update(ctx, entry.ID, updates))
			result, err := cas.HydrateBillingChunk(ctx, []*Log{billed})
			require.NoError(t, err)
			require.Equal(t, []string{entry.ID}, result.Hydrated)
			require.Empty(t, result.Unpriceable)
			require.Equal(t, "new-model", billed.Model)
			require.Equal(t, "new-provider", billed.Provider)
			require.Equal(t, "new-user", *billed.UserID)
			require.True(t, billed.billingPayloadsHydrated)
			if changeObject {
				require.Equal(t, "chat.completion", billed.Object)
				require.Contains(t, billed.TokenUsage, `"prompt_tokens":17`)
				require.NotNil(t, billed.TokenUsageParsed)
				require.Equal(t, 40, billed.TokenUsageParsed.TotalTokens)
			} else {
				require.Equal(t, "image_generation", billed.Object)
				require.NotNil(t, billed.ImageGenerationOutputParsed)
				require.Len(t, billed.ImageGenerationOutputParsed.Data, 2)
				require.Empty(t, billed.ImageGenerationOutput)
				require.Empty(t, billed.ImageGenerationOutputParsed.Data[0].B64JSON)
			}
		})
	}
}

func TestCas_BillingSearchChangedCallerScopeFailsClosed(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()
	entry := bigChatEntry("bill-scope", strings.Repeat("history ", 40))
	entry.UserID = strPtr("owner")
	entry.Object = "image_generation"
	entry.ImageGenerationOutputParsed = &schemas.BifrostImageGenerationResponse{
		Data: []schemas.ImageData{{B64JSON: strings.Repeat("aGVsbG8=", 32)}},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	billed := billingSearchRow(t, inner, entry.ID)
	require.True(t, billingRowNeedsHydration(billed, cas.excluded))
	denied := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("user_id = ?", "other-user") })
	result, err := cas.HydrateBillingChunk(denied, []*Log{billed})
	require.NoError(t, err)
	require.Equal(t, []string{entry.ID}, result.Unpriceable)
	require.Empty(t, result.Hydrated)
	require.False(t, billed.billingPayloadsHydrated)
	require.Empty(t, billed.ImageGenerationOutput)
	result, err = cas.HydrateBillingChunk(denied, []*Log{billed})
	require.NoError(t, err)
	require.Equal(t, []string{entry.ID}, result.Unpriceable)
}
