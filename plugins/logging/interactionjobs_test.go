package logging

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet/datasheettest"
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
	ds := datasheetForTest(t, nil, testLogger{}, datasheet.Config{URL: "file://" + path})
	require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()))
	return catalogWithDatasheetForTest(t, ds)
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

// catalogWithDatasheetForTest returns a test model catalog over ds that is
// cleaned up when the test ends, failing the test if it cannot be built.
func catalogWithDatasheetForTest(t testing.TB, ds *datasheet.Store) *modelcatalog.ModelCatalog {
	t.Helper()
	catalog, err := modelcatalog.NewTestCatalogWithDatasheet(ds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Cleanup() })
	return catalog
}

// datasheetForTest returns a datasheet over a new live store closed when the test ends.
var datasheetForTest = datasheettest.New

const testBackgroundModel = "gpt-4o"

// backgroundCost is the committed gpt-4o rate: 2.5e-06 per input token, 1e-05 per output token.
func backgroundCost(input, output int) float64 {
	return float64(input)*2.5e-06 + float64(output)*1e-05
}

// recordingUsageReporter stands in for governance: it keeps every usage report a settlement sends.
type recordingUsageReporter struct {
	mu      sync.Mutex
	reports []jobaccounting.UsageReport
}

func (r *recordingUsageReporter) ReportUsage(_ context.Context, report jobaccounting.UsageReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
	return nil
}

func (r *recordingUsageReporter) all() []jobaccounting.UsageReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]jobaccounting.UsageReport(nil), r.reports...)
}

func newBackgroundResponsePlugin(t *testing.T) (*LoggerPlugin, *fakeBatchStore, *recordingUsageReporter) {
	t.Helper()
	bs := newFakeBatchStore()
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, newTestStore(t), bs, newTestPricingManager(t), nil)
	require.NoError(t, err)
	reporter := &recordingUsageReporter{}
	plugin.SetBatchUsageReporter(reporter)
	return plugin, bs, reporter
}

// responseObject is a Responses object as the provider returns it. Zero tokens mean it carries no
// usage, which is what a queued or in-progress response looks like.
func responseObject(id, status string, input, output int) *schemas.BifrostResponsesResponse {
	resp := &schemas.BifrostResponsesResponse{ID: &id, Status: &status, Model: testBackgroundModel + "-2024-08-06", Background: new(true)}
	if input > 0 || output > 0 {
		resp.Usage = &schemas.ResponsesResponseUsage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
	}
	return resp
}

// sendResponsesCall drives one Responses request through the plugin the way the gateway does. A
// create names the model and carries the limits it was admitted under; a retrieve or cancel names
// only the response, as the caller identified by requestID.
func sendResponsesCall(t *testing.T, plugin *LoggerPlugin, requestID string, requestType schemas.RequestType, resp *schemas.BifrostResponsesResponse) {
	t.Helper()
	sendResponsesCallWith(t, plugin, requestID, requestType, resp, nil)
}

// sendResponsesCallWith is sendResponsesCall with what the provider call leaves on the context
// between the two hooks.
func sendResponsesCallWith(t *testing.T, plugin *LoggerPlugin, requestID string, requestType schemas.RequestType, resp *schemas.BifrostResponsesResponse, providerCall func(ctx *schemas.BifrostContext)) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-"+requestID)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-"+requestID)

	req := &schemas.BifrostRequest{RequestType: requestType}
	extra := schemas.BifrostResponseExtraFields{RequestType: requestType, Provider: schemas.OpenAI}
	switch requestType {
	case schemas.ResponsesRequest:
		req.ResponsesRequest = &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    testBackgroundModel,
			Params:   &schemas.ResponsesParameters{Background: resp.Background},
		}
		extra.OriginalModelRequested = testBackgroundModel
		ctx.SetValue(schemas.BifrostContextKeyGovernanceBudgetIDs, []string{"budget-" + requestID})
		ctx.SetValue(schemas.BifrostContextKeyGovernanceRateLimitIDs, []string{"rl-" + requestID})
	case schemas.ResponsesRetrieveRequest, schemas.ResponsesRetrieveStreamRequest:
		req.ResponsesRetrieveRequest = &schemas.BifrostResponsesRetrieveRequest{Provider: schemas.OpenAI, ResponseID: *resp.ID}
	case schemas.ResponsesCancelRequest:
		req.ResponsesCancelRequest = &schemas.BifrostResponsesCancelRequest{Provider: schemas.OpenAI, ResponseID: *resp.ID}
	default:
		t.Fatalf("unsupported request type %s", requestType)
	}
	_, _, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	if providerCall != nil {
		providerCall(ctx)
	}

	result := &schemas.BifrostResponse{}
	if requestType == schemas.ResponsesRetrieveStreamRequest {
		// A streamed retrieve is delivered as its final chunk, the one that carries the response.
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		result.ResponsesStreamResponse = &schemas.BifrostResponsesStreamResponse{
			Type:        schemas.ResponsesStreamResponseTypeCompleted,
			Response:    resp,
			ExtraFields: extra,
		}
	} else {
		// The routing the core resolved for the call, when the test set one.
		extra.RoutingInfo = resp.ExtraFields.RoutingInfo
		resp.ExtraFields = extra
		result.ResponsesResponse = resp
	}
	_, _, err = plugin.PostLLMHook(ctx, result, nil)
	require.NoError(t, err)
}

func responseJobID(id string) string {
	return cstables.ProviderJobID(cstables.ProviderJobKindResponse, string(schemas.OpenAI), id)
}

func responseSettlementID(id string) string {
	return jobaccounting.AccountingLogID(jobaccounting.ProviderJobKind(cstables.ProviderJobKindResponse), schemas.OpenAI, id)
}

// TestPostLLMHookChargesABackgroundResponseOnceWhenItFinishes pins the background flow end to end
// through the logging plugin. The create answers queued and charges nothing, but records the
// response. A poll that finds it still running charges nothing. The first poll that finds it
// finished charges the limits the create was admitted under with the final usage, whoever polled.
// Every later poll charges nothing more.
func TestPostLLMHookChargesABackgroundResponseOnceWhenItFinishes(t *testing.T) {
	plugin, bs, reporter := newBackgroundResponsePlugin(t)

	sendResponsesCall(t, plugin, "req-bg-create", schemas.ResponsesRequest, responseObject("resp_bg", "queued", 0, 0))
	job, err := bs.GetProviderJob(context.Background(), responseJobID("resp_bg"))
	require.NoError(t, err, "the background create records the response")
	assert.Equal(t, cstables.ProviderJobKindResponse, job.Kind)
	assert.Equal(t, string(schemas.OpenAI), job.Provider)
	assert.Equal(t, "resp_bg", job.JobID)
	assert.Equal(t, testBackgroundModel, job.Model)
	assert.Equal(t, "queued", job.ProviderStatus)
	assert.Equal(t, cstables.ProviderJobAccountingStatusPending, job.AccountingStatus)
	assert.Equal(t, "key-req-bg-create", job.SelectedKeyID)
	require.NotNil(t, job.VirtualKeyID)
	assert.Equal(t, "vk-req-bg-create", *job.VirtualKeyID)
	require.NotNil(t, job.BudgetIDs)
	assert.JSONEq(t, `["budget-req-bg-create"]`, *job.BudgetIDs)
	require.NotNil(t, job.RateLimitIDs)
	assert.JSONEq(t, `["rl-req-bg-create"]`, *job.RateLimitIDs)
	require.NotNil(t, job.SourceLogID)
	assert.Equal(t, "req-bg-create", *job.SourceLogID)
	require.NotNil(t, job.NextCheckAt, "an unfinished response is left due for the sweeper")
	assert.Empty(t, reporter.all(), "the create charges nothing: the model has not run yet")

	sendResponsesCall(t, plugin, "req-bg-poll-running", schemas.ResponsesRetrieveRequest, responseObject("resp_bg", "in_progress", 0, 0))
	assert.Empty(t, reporter.all(), "a poll of a running response charges nothing")
	running, err := bs.GetProviderJob(context.Background(), responseJobID("resp_bg"))
	require.NoError(t, err)
	assert.Equal(t, cstables.ProviderJobAccountingStatusPending, running.AccountingStatus)

	sendResponsesCall(t, plugin, "req-bg-poll-1", schemas.ResponsesRetrieveRequest, responseObject("resp_bg", "completed", 42000, 9000))
	reports := reporter.all()
	require.Len(t, reports, 1, "the first poll that finds the response finished charges it")
	assert.Equal(t, responseSettlementID("resp_bg"), reports[0].RequestID)
	assert.Equal(t, int64(51000), reports[0].TokensUsed)
	assert.InDelta(t, backgroundCost(42000, 9000), reports[0].Cost, 1e-9)
	assert.Equal(t, []string{"budget-req-bg-create"}, reports[0].BudgetIDs, "the create's budgets are charged, not the poller's")
	assert.Equal(t, []string{"rl-req-bg-create"}, reports[0].RateLimitIDs)
	assert.Equal(t, "vk-req-bg-create", reports[0].VirtualKeyID)
	assert.True(t, reports[0].SkipRequestCount, "the create already counted as the request")

	sendResponsesCall(t, plugin, "req-bg-poll-2", schemas.ResponsesRetrieveRequest, responseObject("resp_bg", "completed", 42000, 9000))
	sendResponsesCall(t, plugin, "req-bg-poll-3", schemas.ResponsesRetrieveRequest, responseObject("resp_bg", "completed", 42000, 9000))
	assert.Len(t, reporter.all(), 1, "later polls charge nothing more")
	require.NoError(t, plugin.Cleanup())

	settled, err := bs.GetProviderJob(context.Background(), responseJobID("resp_bg"))
	require.NoError(t, err)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, settled.AccountingStatus)
	assert.Equal(t, "completed", settled.ProviderStatus)

	row, err := plugin.store.FindByID(context.Background(), responseSettlementID("resp_bg"))
	require.NoError(t, err, "the response's cost row is written")
	require.NotNil(t, row.Cost)
	assert.InDelta(t, backgroundCost(42000, 9000), *row.Cost, 1e-9)
	assert.Equal(t, testBackgroundModel, row.Model)
	assert.Equal(t, 42000, row.PromptTokens)
	assert.Equal(t, 9000, row.CompletionTokens)
	require.NotNil(t, row.ParentRequestID)
	assert.Equal(t, "req-bg-create", *row.ParentRequestID, "the cost sits under the create")
	assert.Equal(t, "key-req-bg-create", row.SelectedKeyID)

	// The row keeps its usage on token_usage and is a Responses row, so cost recalculation reprices
	// it on the generic path to the same number.
	repriced, err := jobaccounting.RepriceLog(row, plugin.pricingManager)
	require.NoError(t, err)
	assert.Nil(t, repriced)
	breakdown, err := plugin.calculateCostBreakdownForLog(row)
	require.NoError(t, err)
	require.NotNil(t, breakdown)
	assert.InDelta(t, *row.Cost, breakdown.TotalCost, 1e-9)

	for _, id := range []string{"req-bg-create", "req-bg-poll-running", "req-bg-poll-1", "req-bg-poll-2", "req-bg-poll-3"} {
		entry, err := plugin.store.FindByID(context.Background(), id)
		require.NoError(t, err, id)
		assert.True(t, entry.Cost == nil || *entry.Cost == 0, "%s carries no cost of its own: cost=%v", id, entry.Cost)
	}
}

// A create that returns finished was charged from its own response, as before. It starts no
// tracking, so a later retrieve of it charges nothing.
func TestPostLLMHookChargesAnOrdinaryResponseFromItsCreateOnly(t *testing.T) {
	plugin, bs, reporter := newBackgroundResponsePlugin(t)

	finished := responseObject("resp_sync", "completed", 1000, 200)
	finished.Background = nil
	sendResponsesCall(t, plugin, "req-sync-create", schemas.ResponsesRequest, finished)
	sendResponsesCall(t, plugin, "req-sync-read", schemas.ResponsesRetrieveRequest, responseObject("resp_sync", "completed", 1000, 200))
	require.NoError(t, plugin.Cleanup())

	_, err := bs.GetProviderJob(context.Background(), responseJobID("resp_sync"))
	assert.Error(t, err, "a finished create writes no job row")
	assert.Empty(t, bs.jobs)
	assert.Empty(t, reporter.all(), "nothing is settled for a response charged from its create")
	_, err = plugin.store.FindByID(context.Background(), responseSettlementID("resp_sync"))
	assert.Error(t, err, "no settlement row")

	create, err := plugin.store.FindByID(context.Background(), "req-sync-create")
	require.NoError(t, err)
	require.NotNil(t, create.Cost, "the create row carries the cost, once")
	assert.InDelta(t, backgroundCost(1000, 200), *create.Cost, 1e-9)
	read, err := plugin.store.FindByID(context.Background(), "req-sync-read")
	require.NoError(t, err)
	assert.True(t, read.Cost == nil || *read.Cost == 0, "the retrieve row carries no cost: %v", read.Cost)
}

// A retrieve never starts tracking a response: one with no row was created somewhere else, or
// before this build, and reading it is not grounds to charge the reader.
func TestPostLLMHookIgnoresAResponseItNeverSawCreatedInBackground(t *testing.T) {
	plugin, bs, reporter := newBackgroundResponsePlugin(t)

	sendResponsesCall(t, plugin, "req-foreign-running", schemas.ResponsesRetrieveRequest, responseObject("resp_foreign", "in_progress", 0, 0))
	sendResponsesCall(t, plugin, "req-foreign-done", schemas.ResponsesRetrieveRequest, responseObject("resp_foreign", "completed", 10, 5))
	require.NoError(t, plugin.Cleanup())

	assert.Empty(t, bs.jobs, "a read must not start tracking a response")
	assert.Empty(t, reporter.all())
}

// A background response that is cancelled or fails settles whatever its final object reports,
// which may be nothing, and is then finished: no later read or sweep charges or retries it.
func TestPostLLMHookSettlesACancelledOrFailedBackgroundResponseAndStops(t *testing.T) {
	cases := []struct {
		name, status  string
		endedBy       schemas.RequestType
		input, output int
	}{
		{"cancelled before it ran", "cancelled", schemas.ResponsesCancelRequest, 0, 0},
		{"failed with no usage", "failed", schemas.ResponsesRetrieveRequest, 0, 0},
		{"failed after using tokens", "failed", schemas.ResponsesRetrieveRequest, 3000, 100},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plugin, bs, reporter := newBackgroundResponsePlugin(t)
			id := "resp_end_" + string(rune('a'+i))

			sendResponsesCall(t, plugin, "req-end-create", schemas.ResponsesRequest, responseObject(id, "queued", 0, 0))
			sendResponsesCall(t, plugin, "req-end-final", tc.endedBy, responseObject(id, tc.status, tc.input, tc.output))
			sendResponsesCall(t, plugin, "req-end-again", schemas.ResponsesRetrieveRequest, responseObject(id, tc.status, tc.input, tc.output))
			require.NoError(t, plugin.Cleanup())

			job, err := bs.GetProviderJob(context.Background(), responseJobID(id))
			require.NoError(t, err)
			assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus, "the response is settled, not retried")
			assert.Equal(t, tc.status, job.ProviderStatus)

			reports := reporter.all()
			require.Len(t, reports, 1)
			assert.Equal(t, int64(tc.input+tc.output), reports[0].TokensUsed)
			assert.InDelta(t, backgroundCost(tc.input, tc.output), reports[0].Cost, 1e-9)
			assert.Equal(t, []string{"budget-req-end-create"}, reports[0].BudgetIDs)
		})
	}
}

// A streamed retrieve ends on the event that carries the finished response, and settles it the same
// way a plain retrieve does.
func TestPostLLMHookSettlesABackgroundResponseFromAStreamedRetrieve(t *testing.T) {
	plugin, bs, reporter := newBackgroundResponsePlugin(t)

	sendResponsesCall(t, plugin, "req-sr-create", schemas.ResponsesRequest, responseObject("resp_sr", "in_progress", 0, 0))
	sendResponsesCall(t, plugin, "req-sr-stream", schemas.ResponsesRetrieveStreamRequest, responseObject("resp_sr", "completed", 2000, 300))
	sendResponsesCall(t, plugin, "req-sr-again", schemas.ResponsesRetrieveRequest, responseObject("resp_sr", "completed", 2000, 300))
	require.NoError(t, plugin.Cleanup())

	job, err := bs.GetProviderJob(context.Background(), responseJobID("resp_sr"))
	require.NoError(t, err)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
	reports := reporter.all()
	require.Len(t, reports, 1)
	assert.Equal(t, int64(2300), reports[0].TokensUsed)
	assert.InDelta(t, backgroundCost(2000, 300), reports[0].Cost, 1e-9)
	assert.Equal(t, []string{"budget-req-sr-create"}, reports[0].BudgetIDs)
}

// A background create saves the tool settings pricing will need on its job row. The read that
// later finds the response finished carries no request, so this is the only place they survive.
// A create with no such settings saves nothing.
func TestPostLLMHookSavesToolPricingSettingsOfABackgroundResponse(t *testing.T) {
	plugin, bs, _ := newBackgroundResponsePlugin(t)
	const settings = `{"tools":[{"type":"image_generation","model":"gpt-image-1-mini"}]}`

	sendResponsesCallWith(t, plugin, "req-bg-tools", schemas.ResponsesRequest, responseObject("resp_bg_tools", "queued", 0, 0), func(ctx *schemas.BifrostContext) {
		ctx.SetValue(schemas.BifrostContextKeyResponsesToolPricingContext, settings)
	})
	job, err := bs.GetProviderJob(context.Background(), responseJobID("resp_bg_tools"))
	require.NoError(t, err)
	require.NotNil(t, job.Params, "the tool settings are saved with the job")
	assert.JSONEq(t, settings, *job.Params)

	sendResponsesCall(t, plugin, "req-bg-plain", schemas.ResponsesRequest, responseObject("resp_bg_plain", "queued", 0, 0))
	plain, err := bs.GetProviderJob(context.Background(), responseJobID("resp_bg_plain"))
	require.NoError(t, err)
	assert.Nil(t, plain.Params)
	require.NoError(t, plugin.Cleanup())
}

// A create served through a key alias saves the alias on the job row, with the canonical model
// name the alias is priced at, because the read that later settles the response resolves no
// alias of its own. The tool settings share the same column and are kept alongside it.
func TestPostLLMHookSavesTheResolvedAliasOfABackgroundResponse(t *testing.T) {
	plugin, bs, _ := newBackgroundResponsePlugin(t)
	aliased := func(id string) *schemas.BifrostResponsesResponse {
		resp := responseObject(id, "queued", 0, 0)
		resp.ExtraFields.RoutingInfo = schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "team-default",
			ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: "deployment-7f3a", ModelName: new("gpt-4o-mini")}}
		return resp
	}

	sendResponsesCall(t, plugin, "req-bg-alias", schemas.ResponsesRequest, aliased("resp_bg_alias"))
	job, err := bs.GetProviderJob(context.Background(), responseJobID("resp_bg_alias"))
	require.NoError(t, err)
	require.NotNil(t, job.Params, "the alias is saved with the job")
	assert.JSONEq(t, `{"routing":{"model":"team-default","resolved_key_alias":{"model_id":"deployment-7f3a","model_name":"gpt-4o-mini"}}}`, *job.Params)

	const settings = `{"tools":[{"type":"image_generation","model":"gpt-image-1-mini"}]}`
	sendResponsesCallWith(t, plugin, "req-bg-alias-tools", schemas.ResponsesRequest, aliased("resp_bg_alias_tools"), func(ctx *schemas.BifrostContext) {
		ctx.SetValue(schemas.BifrostContextKeyResponsesToolPricingContext, settings)
	})
	both, err := bs.GetProviderJob(context.Background(), responseJobID("resp_bg_alias_tools"))
	require.NoError(t, err)
	require.NotNil(t, both.Params)
	assert.JSONEq(t, `{"tools":[{"type":"image_generation","model":"gpt-image-1-mini"}],"routing":{"model":"team-default","resolved_key_alias":{"model_id":"deployment-7f3a","model_name":"gpt-4o-mini"}}}`, *both.Params)
	require.NoError(t, plugin.Cleanup())
}
