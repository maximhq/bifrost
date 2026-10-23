package jobaccounting

import (
	"context"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/generic"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGenericPoller struct {
	resp  *GenericPollResponse
	err   error
	calls int
	last  generic.JobPollRequest
}

func (f *fakeGenericPoller) PollGenericJob(ctx context.Context, job *cstables.TableProviderJob, req generic.JobPollRequest) (*GenericPollResponse, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// textractSnapshot is what a Textract StartDocumentAnalysis route with a job block captures:
// poll GetDocumentAnalysis with the job id, settle on SUCCEEDED at pages x rate.
func textractSnapshot(t *testing.T) string {
	t.Helper()
	snap := generic.JobSnapshot{
		Job: schemas.GenericJobConfig{
			IDPath:     "JobId",
			Poll:       schemas.GenericJobPoll{Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"}, Body: `{"JobId":"{{job.id}}","MaxResults":1}`, ContentType: "application/x-amz-json-1.1"},
			StatusPath: "JobStatus",
			Succeeded:  []string{"SUCCEEDED", "PARTIAL_SUCCESS"},
			Failed:     []string{"FAILED"},
		},
		Usage: &schemas.GenericUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: schemas.Ptr(0.0015), CostPerRequest: schemas.Ptr(0.01)},
	}
	data, err := sonic.Marshal(snap)
	require.NoError(t, err)
	return string(data)
}

func genericJob(t *testing.T, params string) *cstables.TableProviderJob {
	t.Helper()
	return &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindGenericPassthrough, "textract", "job-1"),
		Kind:             cstables.ProviderJobKindGenericPassthrough,
		Provider:         "textract",
		JobID:            "job-1",
		Params:           &params,
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		CreatedAt:        time.Now().UTC(),
	}
}

func jsonPoll(status int, body string) *GenericPollResponse {
	return &GenericPollResponse{StatusCode: status, ContentType: "application/x-amz-json-1.1", Body: []byte(body)}
}

func TestGenericJobPoll(t *testing.T) {
	job := genericJob(t, textractSnapshot(t))

	t.Run("the poll request is built from the snapshot with the job id substituted", func(t *testing.T) {
		poller := &fakeGenericPoller{resp: jsonPoll(200, `{"JobStatus":"IN_PROGRESS"}`)}
		result, err := NewGenericJobSettler(poller).Poll(context.Background(), job)
		require.NoError(t, err)
		assert.Equal(t, "POST", poller.last.Method)
		assert.Equal(t, "/", poller.last.Path)
		assert.Equal(t, "Textract.GetDocumentAnalysis", poller.last.Headers["x-amz-target"])
		assert.Equal(t, "application/x-amz-json-1.1", poller.last.Headers["content-type"])
		assert.JSONEq(t, `{"JobId":"job-1","MaxResults":1}`, string(poller.last.Body))
		assert.False(t, result.Terminal, "still running")
		require.NotNil(t, result.Job)
		assert.Equal(t, "IN_PROGRESS", result.Job.ProviderStatus, "the status advanced on the row")
	})
	t.Run("a succeeded status is terminal and settleable with the response as payload", func(t *testing.T) {
		poller := &fakeGenericPoller{resp: jsonPoll(200, `{"JobStatus":"SUCCEEDED","DocumentMetadata":{"Pages":7}}`)}
		result, err := NewGenericJobSettler(poller).Poll(context.Background(), job)
		require.NoError(t, err)
		assert.True(t, result.Terminal)
		assert.True(t, result.Settleable)
		load := result.Payload.(GenericJobPayload)
		assert.Equal(t, "SUCCEEDED", load.Status)
		assert.False(t, load.Failed)
	})
	t.Run("a failed status is terminal and settles at zero", func(t *testing.T) {
		poller := &fakeGenericPoller{resp: jsonPoll(200, `{"JobStatus":"FAILED"}`)}
		result, err := NewGenericJobSettler(poller).Poll(context.Background(), job)
		require.NoError(t, err)
		assert.True(t, result.Terminal)
		assert.True(t, result.Settleable)
		assert.True(t, result.Payload.(GenericJobPayload).Failed)
	})
	t.Run("status words compare case-insensitively", func(t *testing.T) {
		poller := &fakeGenericPoller{resp: jsonPoll(200, `{"JobStatus":"succeeded"}`)}
		result, err := NewGenericJobSettler(poller).Poll(context.Background(), job)
		require.NoError(t, err)
		assert.True(t, result.Settleable)
	})
	t.Run("a 404 from the upstream parks the job as gone", func(t *testing.T) {
		poller := &fakeGenericPoller{resp: jsonPoll(404, `{"__type":"InvalidJobIdException"}`)}
		result, err := NewGenericJobSettler(poller).Poll(context.Background(), job)
		require.NoError(t, err)
		assert.True(t, result.Terminal)
		assert.False(t, result.Settleable)
		assert.Equal(t, UnpriceableReasonGenericJobGone, result.UnpriceableReason)
	})
	t.Run("a 403 parks the job as access denied, a 5xx retries", func(t *testing.T) {
		result, err := NewGenericJobSettler(&fakeGenericPoller{resp: jsonPoll(403, `{}`)}).Poll(context.Background(), job)
		require.NoError(t, err)
		assert.Equal(t, UnpriceableReasonGenericJobAccessDenied, result.UnpriceableReason)
		_, err = NewGenericJobSettler(&fakeGenericPoller{resp: jsonPoll(503, `{}`)}).Poll(context.Background(), job)
		require.Error(t, err, "a 5xx is retried by the sweeper")
	})
	t.Run("a job past its timeout is parked without polling", func(t *testing.T) {
		old := genericJob(t, textractSnapshot(t))
		old.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
		poller := &fakeGenericPoller{resp: jsonPoll(200, `{"JobStatus":"IN_PROGRESS"}`)}
		result, err := NewGenericJobSettler(poller).Poll(context.Background(), old)
		require.NoError(t, err)
		assert.Equal(t, UnpriceableReasonGenericJobTimeout, result.UnpriceableReason)
		assert.Zero(t, poller.calls)
	})
	t.Run("a row whose snapshot cannot be read is parked", func(t *testing.T) {
		result, err := NewGenericJobSettler(&fakeGenericPoller{}).Poll(context.Background(), genericJob(t, "not json"))
		require.NoError(t, err)
		assert.Equal(t, UnpriceableReasonGenericJobBadParams, result.UnpriceableReason)
	})
}

func TestGenericJobSettle(t *testing.T) {
	job := genericJob(t, textractSnapshot(t))
	settle := func(t *testing.T, load GenericJobPayload) *Settlement {
		t.Helper()
		settlement, err := NewGenericJobSettler(nil).Settle(context.Background(), fakePricing{}, JobRequest{
			Provider: "textract", ProviderJobID: "job-1", FallbackModel: "", Job: job, Payload: load,
		})
		require.NoError(t, err)
		return settlement
	}

	t.Run("a succeeded job is priced with the mapping captured at start", func(t *testing.T) {
		settlement := settle(t, GenericJobPayload{Status: "SUCCEEDED", ContentType: "application/x-amz-json-1.1", Body: []byte(`{"JobStatus":"SUCCEEDED","DocumentMetadata":{"Pages":7}}`)})
		assert.True(t, settlement.Priced)
		assert.True(t, settlement.Complete)
		assert.InDelta(t, 7*0.0015+0.01, settlement.Cost, 1e-9)
		assert.Equal(t, schemas.PassthroughRequest, settlement.Object)
		entry := &logstore.Log{}
		settlement.ApplyDebug(entry)
		params := entry.ParamsParsed.(*schemas.PassthroughLogParams)
		require.NotNil(t, params.JobAccounting)
		assert.Equal(t, "job-1", params.JobAccounting.JobID)
		assert.Equal(t, "SUCCEEDED", params.JobAccounting.Status)
		assert.InDelta(t, 7, *params.JobAccounting.Units, 0)
		assert.InDelta(t, 0.0015, params.JobAccounting.CostPerUnit, 0)
	})
	t.Run("a failed job is priced at zero, not parked", func(t *testing.T) {
		settlement := settle(t, GenericJobPayload{Status: "FAILED", Failed: true})
		assert.True(t, settlement.Priced)
		assert.Zero(t, settlement.Cost)
		entry := &logstore.Log{}
		settlement.ApplyDebug(entry)
		assert.True(t, entry.ParamsParsed.(*schemas.PassthroughLogParams).JobAccounting.Failed)
	})
	t.Run("a terminal response with no readable quantity bills the flat fee alone", func(t *testing.T) {
		settlement := settle(t, GenericJobPayload{Status: "SUCCEEDED", ContentType: "application/x-amz-json-1.1", Body: []byte(`{"JobStatus":"SUCCEEDED"}`)})
		assert.InDelta(t, 0.01, settlement.Cost, 1e-9)
	})
}

func TestGenericJobRepriceAndHydrate(t *testing.T) {
	settler := NewGenericJobSettler(nil)
	cost := 0.0205
	logID := AccountingLogID(ProviderJobKindGenericPassthrough, "textract", "job-1")

	t.Run("a settlement row keeps the cost it carries", func(t *testing.T) {
		entry := &logstore.Log{ID: logID, Provider: "textract", Cost: &cost, ParamsParsed: &schemas.PassthroughLogParams{JobAccounting: &schemas.PassthroughJobAccounting{JobID: "job-1", Status: "SUCCEEDED"}}}
		repriced, err := settler.RepriceFromLog(entry, fakePricing{})
		require.NoError(t, err)
		require.NotNil(t, repriced)
		assert.InDelta(t, cost, repriced.Cost, 0)
		var out Outcome
		settler.HydrateFromLog(entry, &out)
		assert.Equal(t, "SUCCEEDED", out.Status)
	})
	t.Run("a rehydrated row is read from its params column", func(t *testing.T) {
		entry := &logstore.Log{ID: logID, Provider: "textract", Cost: &cost, Params: `{"method":"POST","path":"/","job_accounting":{"job_id":"job-1","status":"SUCCEEDED"}}`}
		repriced, err := settler.RepriceFromLog(entry, fakePricing{})
		require.NoError(t, err)
		require.NotNil(t, repriced)
		assert.InDelta(t, cost, repriced.Cost, 0)
	})
	t.Run("an ordinary passthrough row is not this kind's", func(t *testing.T) {
		entry := &logstore.Log{ID: "req-1", Provider: "textract", ParamsParsed: &schemas.PassthroughLogParams{Method: "POST", Path: "/"}}
		repriced, err := settler.RepriceFromLog(entry, fakePricing{})
		require.NoError(t, err)
		assert.Nil(t, repriced)
		repriced, err = RepriceLog(entry, fakePricing{})
		require.NoError(t, err)
		assert.Nil(t, repriced, "no kind claims it, so the generic pricing path runs")
	})
}
