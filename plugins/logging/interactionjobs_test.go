package logging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInteractionAgent = "deep-research-preview-04-2026"

// newInteractionPricingManager prices the Deep Research agent at a standard per-token rate, from an
// offline datasheet (the committed testdata has no Gemini rows).
func newInteractionPricingManager(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pricing.json")
	sheet := `{"` + testInteractionAgent + `":{"provider":"gemini","mode":"responses","input_cost_per_token":2e-06,"output_cost_per_token":1.2e-05}}`
	require.NoError(t, os.WriteFile(path, []byte(sheet), 0o600))
	ds := datasheet.New(nil, testLogger{}, datasheet.Config{URL: "file://" + path})
	require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()))
	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

// sendInteractionRead drives one passthrough request through the plugin the way the gateway does,
// with the response reporting the stored run it read.
func sendInteractionRead(t *testing.T, plugin *LoggerPlugin, requestID, method, path string, run *schemas.BifrostPassthroughJob) {
	t.Helper()
	sendInteraction(t, plugin, requestID, method, path, run, schemas.PassthroughRequest)
}

// sendInteraction is sendInteractionRead for either request type. A stream is delivered as its
// final chunk, the one the provider's Finalize hook put the run on.
func sendInteraction(t *testing.T, plugin *LoggerPlugin, requestID, method, path string, run *schemas.BifrostPassthroughJob, requestType schemas.RequestType) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	// Key selection always names the key that served the read; the job keeps the create's.
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-"+requestID)
	model := ""
	if run.Created {
		model = testInteractionAgent
	}
	_, _, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: requestType,
		PassthroughRequest: &schemas.BifrostPassthroughRequest{
			Provider: schemas.Gemini,
			Model:    model,
			Method:   method,
			Path:     path,
		},
	})
	require.NoError(t, err)
	if requestType == schemas.PassthroughStreamRequest {
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	}
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{
		PassthroughResponse: &schemas.BifrostPassthroughResponse{
			StatusCode:  200,
			Body:        []byte(`{"id":"` + run.ID + `"}`),
			ServedModel: testInteractionAgent,
			ProviderJob: run,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:     requestType,
				Provider:        schemas.Gemini,
				PassthroughPath: path,
			},
		},
	}, nil)
	require.NoError(t, err)
}

// TestPostLLMHookBillsAnInteractionRunOnce pins the Deep Research flow end to end through the
// logging plugin. The create records the run; a poll of the finished run settles it as one cost row
// under the create; a second poll of the same finished run writes no second row. No request row is
// billed for the run itself, because each read repeats its full usage.
func TestPostLLMHookBillsAnInteractionRunOnce(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, bs, newInteractionPricingManager(t), nil)
	require.NoError(t, err)

	usage := &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{PromptTokens: 361700, CompletionTokens: 33400, TotalTokens: 395100}}
	sendInteractionRead(t, plugin, "req-int-create", "POST", "/interactions", &schemas.BifrostPassthroughJob{
		ID: "int_e2e", Status: "in_progress", Created: true, PollPath: "/interactions/int_e2e", APIVersion: "v1beta",
	})
	jobID := cstables.ProviderJobID(cstables.ProviderJobKindInteraction, string(schemas.Gemini), "int_e2e")
	job, err := bs.GetProviderJob(context.Background(), jobID)
	require.NoError(t, err)
	require.NotNil(t, job, "the create records the run")
	assert.Equal(t, testInteractionAgent, job.Model)
	assert.Equal(t, "/interactions/int_e2e", job.Endpoint)
	assert.Equal(t, "v1beta", jobaccounting.InteractionParamsFromJob(job).APIVersion)
	require.NotNil(t, job.SourceLogID)
	assert.Equal(t, "req-int-create", *job.SourceLogID)
	require.NotNil(t, job.NextCheckAt, "a running run is left due for the sweeper")

	finished := &schemas.BifrostPassthroughJob{ID: "int_e2e", Status: "completed", Terminal: true, PollPath: "/interactions/int_e2e", Usage: usage}
	sendInteractionRead(t, plugin, "req-int-poll-1", "GET", "/interactions/int_e2e", finished)
	sendInteractionRead(t, plugin, "req-int-poll-2", "GET", "/interactions/int_e2e", finished)
	require.NoError(t, plugin.Cleanup())

	settled, err := bs.GetProviderJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, settled.AccountingStatus)

	settlementID := jobaccounting.AccountingLogID(jobaccounting.ProviderJobKindInteraction, schemas.Gemini, "int_e2e")
	row, err := store.FindByID(context.Background(), settlementID)
	require.NoError(t, err, "the run's cost row is written")
	require.NotNil(t, row.Cost)
	assert.InDelta(t, 361700*2e-06+33400*1.2e-05, *row.Cost, 1e-9)
	assert.Equal(t, testInteractionAgent, row.Model)
	require.NotNil(t, row.ParentRequestID)
	assert.Equal(t, "req-int-create", *row.ParentRequestID, "the bill sits under the create")
	assert.Equal(t, "key-req-int-create", row.SelectedKeyID, "the run is billed to the key that created it, not the one that polled it")

	// The row keeps its usage on token_usage and is a Responses row, so cost recalculation reprices
	// it on the generic path to the same number; no kind-specific repricing is needed.
	repriced, err := jobaccounting.RepriceLog(row, plugin.pricingManager)
	require.NoError(t, err)
	assert.Nil(t, repriced, "no job kind claims an interaction row")
	breakdown, err := plugin.calculateCostBreakdownForLog(row)
	require.NoError(t, err)
	require.NotNil(t, breakdown)
	assert.InDelta(t, *row.Cost, breakdown.TotalCost, 1e-9)

	for _, id := range []string{"req-int-create", "req-int-poll-1", "req-int-poll-2"} {
		entry, err := store.FindByID(context.Background(), id)
		require.NoError(t, err, id)
		assert.Equal(t, testInteractionAgent, entry.Model, "%s names the agent", id)
		assert.True(t, entry.Cost == nil || *entry.Cost == 0, "%s is not billed for the run: cost=%v", id, entry.Cost)
	}
}

// A poll of a run Bifrost never saw created starts no tracking and bills no one.
func TestPostLLMHookIgnoresAnInteractionItNeverSawCreated(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, bs, newInteractionPricingManager(t), nil)
	require.NoError(t, err)

	sendInteractionRead(t, plugin, "req-int-foreign", "GET", "/interactions/int_foreign", &schemas.BifrostPassthroughJob{
		ID: "int_foreign", Status: "completed", Terminal: true, PollPath: "/interactions/int_foreign",
		Usage: &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})
	require.NoError(t, plugin.Cleanup())

	job, _ := bs.GetProviderJob(context.Background(), cstables.ProviderJobID(cstables.ProviderJobKindInteraction, string(schemas.Gemini), "int_foreign"))
	assert.Nil(t, job, "a read must not start tracking a run")
	_, err = store.FindByID(context.Background(), jobaccounting.AccountingLogID(jobaccounting.ProviderJobKindInteraction, schemas.Gemini, "int_foreign"))
	assert.Error(t, err, "no cost row for a run nobody here created")
}

// A poll that started before the run was billed can finish after it, carrying the run as it was
// then (still in progress). That late read must not rewrite a settled job back to running: the row
// keeps the status it was billed at.
func TestPostLLMHookLateReadLeavesASettledInteractionAlone(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, bs, newInteractionPricingManager(t), nil)
	require.NoError(t, err)

	sendInteractionRead(t, plugin, "req-late-create", "POST", "/interactions", &schemas.BifrostPassthroughJob{
		ID: "int_late", Status: "in_progress", Created: true, PollPath: "/interactions/int_late",
	})
	sendInteractionRead(t, plugin, "req-late-final", "GET", "/interactions/int_late", &schemas.BifrostPassthroughJob{
		ID: "int_late", Status: "completed", Terminal: true, PollPath: "/interactions/int_late",
		Usage: &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}},
	})
	jobID := cstables.ProviderJobID(cstables.ProviderJobKindInteraction, string(schemas.Gemini), "int_late")
	settled, err := bs.GetProviderJob(context.Background(), jobID)
	require.NoError(t, err)
	require.Equal(t, cstables.ProviderJobAccountingStatusAccounted, settled.AccountingStatus)
	settledCheck := settled.NextCheckAt

	sendInteractionRead(t, plugin, "req-late-stale", "GET", "/interactions/int_late", &schemas.BifrostPassthroughJob{
		ID: "int_late", Status: "in_progress", PollPath: "/interactions/int_late",
	})
	require.NoError(t, plugin.Cleanup())

	after, err := bs.GetProviderJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.Equal(t, "completed", after.ProviderStatus, "a late in-progress read must not rewrite a settled run's status")
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, after.AccountingStatus)
	assert.Equal(t, settledCheck, after.NextCheckAt, "a late read must not reschedule a settled run")
}

// A streaming create that ends on interaction.completed reports the finished run on its final
// chunk. The stream branch of the post hook records the run and settles it right there, without
// waiting for a poll or the sweeper, and the stream's own row is not billed for it.
func TestPostLLMHookSettlesAStreamedInteractionWhenItEnds(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, bs, newInteractionPricingManager(t), nil)
	require.NoError(t, err)

	sendInteraction(t, plugin, "req-int-stream", "POST", "/interactions", &schemas.BifrostPassthroughJob{
		ID: "int_stream", Status: "completed", Terminal: true, Created: true, PollPath: "/interactions/int_stream", APIVersion: "v1beta",
		Usage: &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{PromptTokens: 1141649, CompletionTokens: 69527, TotalTokens: 1211176}},
	}, schemas.PassthroughStreamRequest)
	require.NoError(t, plugin.Cleanup())

	job, err := bs.GetProviderJob(context.Background(), cstables.ProviderJobID(cstables.ProviderJobKindInteraction, string(schemas.Gemini), "int_stream"))
	require.NoError(t, err)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus, "the run is settled when the stream ends")
	assert.Zero(t, job.PollAttempts, "no poll was needed")

	row, err := store.FindByID(context.Background(), jobaccounting.AccountingLogID(jobaccounting.ProviderJobKindInteraction, schemas.Gemini, "int_stream"))
	require.NoError(t, err)
	require.NotNil(t, row.Cost)
	assert.InDelta(t, 1141649*2e-06+69527*1.2e-05, *row.Cost, 1e-9)
	require.NotNil(t, row.ParentRequestID)
	assert.Equal(t, "req-int-stream", *row.ParentRequestID)

	streamRow, err := store.FindByID(context.Background(), "req-int-stream")
	require.NoError(t, err)
	assert.True(t, streamRow.Stream)
	assert.Equal(t, testInteractionAgent, streamRow.Model)
	assert.True(t, streamRow.Cost == nil || *streamRow.Cost == 0, "the stream's own row is not billed for the run: cost=%v", streamRow.Cost)
}

// sendTypedInteraction drives a typed Responses request through the plugin, the way the /genai
// Interactions route and /v1/responses reach a Gemini run: the create is a Responses request
// naming the agent, a read is a Responses retrieve naming none.
func sendTypedInteraction(t *testing.T, plugin *LoggerPlugin, requestID string, run *schemas.BifrostPassthroughJob, usage *schemas.ResponsesResponseUsage) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-"+requestID)
	request := &schemas.BifrostRequest{RequestType: schemas.ResponsesRetrieveRequest, ResponsesRetrieveRequest: &schemas.BifrostResponsesRetrieveRequest{Provider: schemas.Gemini, ResponseID: run.ID}}
	model := ""
	if run.Created {
		model = testInteractionAgent
		request = &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.Gemini, Model: model, Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)}}}
	}
	_, _, err := plugin.PreLLMHook(ctx, request)
	require.NoError(t, err)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ID:          schemas.Ptr(run.ID),
			Model:       testInteractionAgent,
			Status:      schemas.Ptr(run.Status),
			Usage:       usage,
			ProviderJob: run,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: request.RequestType,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.Gemini, Model: model},
			},
		},
	}, nil)
	require.NoError(t, err)
}

// TestPostLLMHookBillsATypedInteractionRunOnce pins a background run started through a typed
// Responses request: the create records the run, a read of the finished run settles it once under
// the create, and neither the create nor the reads are billed for it themselves.
func TestPostLLMHookBillsATypedInteractionRunOnce(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, bs, newInteractionPricingManager(t), nil)
	require.NoError(t, err)

	sendTypedInteraction(t, plugin, "req-typed-create", &schemas.BifrostPassthroughJob{
		ID: "int_typed", Status: "in_progress", Created: true, PollPath: "/interactions/int_typed",
	}, nil)
	jobID := cstables.ProviderJobID(cstables.ProviderJobKindInteraction, string(schemas.Gemini), "int_typed")
	job, err := bs.GetProviderJob(context.Background(), jobID)
	require.NoError(t, err)
	require.NotNil(t, job, "the typed create records the run")
	assert.Equal(t, "/interactions/int_typed", job.Endpoint)

	runUsage := &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100}}
	finished := &schemas.BifrostPassthroughJob{ID: "int_typed", Status: "completed", Terminal: true, PollPath: "/interactions/int_typed", Usage: runUsage}
	readUsage := &schemas.ResponsesResponseUsage{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100}
	sendTypedInteraction(t, plugin, "req-typed-read-1", finished, readUsage)
	sendTypedInteraction(t, plugin, "req-typed-read-2", finished, readUsage)
	require.NoError(t, plugin.Cleanup())

	settled, err := bs.GetProviderJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, settled.AccountingStatus)
	row, err := store.FindByID(context.Background(), jobaccounting.AccountingLogID(jobaccounting.ProviderJobKindInteraction, schemas.Gemini, "int_typed"))
	require.NoError(t, err, "the run's cost row is written")
	require.NotNil(t, row.Cost)
	assert.InDelta(t, 1000*2e-06+100*1.2e-05, *row.Cost, 1e-9)

	for _, id := range []string{"req-typed-create", "req-typed-read-1", "req-typed-read-2"} {
		entry, err := store.FindByID(context.Background(), id)
		require.NoError(t, err, id)
		assert.True(t, entry.Cost == nil || *entry.Cost == 0, "%s is not billed for the run: cost=%v", id, entry.Cost)
	}
}

// TestInteractionPricingScopesAreTheCreators pins whose rates a run is charged at: a read that settles it,
// from another user on another key, prices it under the create's key, virtual key, user and time.
func TestInteractionPricingScopesAreTheCreators(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-reader")
	reader := grant.New()
	reader.SetIdentity(grant.NewIdentity(schemas.Credential{}, &schemas.UserRef{ID: "user-reader"}, &schemas.EntityRef{ID: "vk-reader"}, nil, nil, nil, nil))
	ctx.SetGrant(reader)
	entry := &logstore.Log{Provider: string(schemas.Gemini)}
	created := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)

	scopes := interactionPricingScopes(ctx, entry, &cstables.TableProviderJob{
		Provider: string(schemas.Gemini), SelectedKeyID: "key-creator", VirtualKeyID: schemas.Ptr("vk-creator"), UserID: schemas.Ptr("user-creator"), CreatedAt: created,
	})
	assert.Equal(t, &modelcatalog.PricingLookupScopes{Provider: string(schemas.Gemini), SelectedKeyID: "key-creator", VirtualKeyID: "vk-creator", UserID: "user-creator", BilledAt: created}, scopes)

	scopes = interactionPricingScopes(ctx, entry, nil)
	assert.Equal(t, "key-reader", scopes.SelectedKeyID, "a create that finished is its own caller's")
	assert.Equal(t, "user-reader", scopes.UserID)
}

// TestInteractionReadSettlesUnderTheCreate pins the wiring: the row a read gets back is the create's.
func TestInteractionReadSettlesUnderTheCreate(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, bs, newInteractionPricingManager(t), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })
	sendInteractionRead(t, plugin, "create", "POST", "/interactions", &schemas.BifrostPassthroughJob{ID: "int_own", Status: "in_progress", Created: true, PollPath: "/interactions/int_own"})
	stored, ok := plugin.recordInteractionJob(&logstore.Log{Provider: string(schemas.Gemini), SelectedKeyID: "key-read"}, &schemas.BifrostPassthroughJob{ID: "int_own", Status: "completed", Terminal: true})
	require.True(t, ok)
	require.NotNil(t, stored)
	assert.Equal(t, "key-create", stored.SelectedKeyID)
}

// TestPostLLMHookRecordsAStreamedTypedInteractionRun pins a background run started through a
// streamed typed Responses request (/v1/responses with stream and background). The stream hands
// the run off on its final chunk while it is still going; that chunk records the run, so a later
// read of the finished run settles it once, even if the client never watched the stream end.
func TestPostLLMHookRecordsAStreamedTypedInteractionRun(t *testing.T) {
	store := newTestStore(t)
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, bs, newInteractionPricingManager(t), nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-typed-stream")
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-req-typed-stream")
	_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ResponsesStreamRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Gemini, Model: testInteractionAgent,
			Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)},
		},
	})
	require.NoError(t, err)
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeInProgress,
			Response: &schemas.BifrostResponsesResponse{
				ID: schemas.Ptr("int_typed_stream"), Model: testInteractionAgent, Status: schemas.Ptr("in_progress"),
				ProviderJob: &schemas.BifrostPassthroughJob{ID: "int_typed_stream", Status: "in_progress", Created: true, PollPath: "/interactions/int_typed_stream"},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ResponsesStreamRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.Gemini, Model: testInteractionAgent},
			},
		},
	}, nil)
	require.NoError(t, err)

	jobID := cstables.ProviderJobID(cstables.ProviderJobKindInteraction, string(schemas.Gemini), "int_typed_stream")
	job, err := bs.GetProviderJob(context.Background(), jobID)
	require.NoError(t, err)
	require.NotNil(t, job, "the streamed create records the run")
	assert.Equal(t, "/interactions/int_typed_stream", job.Endpoint)

	runUsage := &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100}}
	sendTypedInteraction(t, plugin, "req-typed-stream-read", &schemas.BifrostPassthroughJob{ID: "int_typed_stream", Status: "completed", Terminal: true, PollPath: "/interactions/int_typed_stream", Usage: runUsage},
		&schemas.ResponsesResponseUsage{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100})
	require.NoError(t, plugin.Cleanup())

	settled, err := bs.GetProviderJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, settled.AccountingStatus)
	row, err := store.FindByID(context.Background(), jobaccounting.AccountingLogID(jobaccounting.ProviderJobKindInteraction, schemas.Gemini, "int_typed_stream"))
	require.NoError(t, err, "the run's cost row is written")
	require.NotNil(t, row.Cost)
	assert.InDelta(t, 1000*2e-06+100*1.2e-05, *row.Cost, 1e-9)
	require.NotNil(t, row.ParentRequestID)
	assert.Equal(t, "req-typed-stream", *row.ParentRequestID)
}

// BenchmarkInteractionJobOf measures finding the stored run a result reports on, which the typed
// Responses post-hook runs for a result that carries one; an ordinary result stops at nil checks.
func BenchmarkInteractionJobOf(b *testing.B) {
	run := &schemas.BifrostPassthroughJob{ID: "v1_run"}
	for name, result := range map[string]*schemas.BifrostResponse{
		"no_run":    {ResponsesResponse: &schemas.BifrostResponsesResponse{Model: testInteractionAgent}},
		"unary_run": {ResponsesResponse: &schemas.BifrostResponsesResponse{Model: testInteractionAgent, ProviderJob: run}},
		"stream_run": {ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Response: &schemas.BifrostResponsesResponse{Model: testInteractionAgent, ProviderJob: run}}},
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				interactionJobOf(result)
			}
		})
	}
}
