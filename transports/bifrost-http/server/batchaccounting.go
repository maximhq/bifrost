package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

type bifrostBatchResultFetcher struct {
	client *bifrost.Bifrost
}

func (f *bifrostBatchResultFetcher) RetrieveBatch(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostBatchRetrieveResponse, error) {
	if f == nil || f.client == nil {
		return nil, fmt.Errorf("bifrost client is nil")
	}
	if job == nil {
		return nil, fmt.Errorf("batch job is nil")
	}
	req := &schemas.BifrostBatchRetrieveRequest{
		Provider: schemas.ModelProvider(job.Provider),
		Model:    modelPtr(job.Model),
		BatchID:  job.JobID,
	}
	resp, bifrostErr := f.client.BatchRetrieveRequest(internalJobContext(ctx, job.SelectedKeyID), req)
	if bifrostErr != nil {
		return nil, jobaccounting.NewProviderCallError(bifrostErr)
	}
	return resp, nil
}

func (f *bifrostBatchResultFetcher) FetchBatchResults(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostBatchResultsResponse, error) {
	if f == nil || f.client == nil {
		return nil, fmt.Errorf("bifrost client is nil")
	}
	if job == nil {
		return nil, fmt.Errorf("batch job is nil")
	}
	req := &schemas.BifrostBatchResultsRequest{
		Provider: schemas.ModelProvider(job.Provider),
		Model:    modelPtr(job.Model),
		BatchID:  job.JobID,
	}
	resp, bifrostErr := f.client.BatchResultsRequest(internalJobContext(ctx, job.SelectedKeyID), req)
	if bifrostErr != nil {
		return nil, jobaccounting.NewProviderCallError(bifrostErr)
	}
	return resp, nil
}

// internalJobContext builds the context the sweeper polls a provider job with,
// pinned to the key that created the job when the row recorded one.
//
// The sweeper skips the plugin pipeline, so nothing upstream resolves a key for it
// and core is otherwise free to pick any key registered for the provider. On OpenAI
// a video id is scoped to its creating key, so an unpinned poll asks a key that has
// never heard of the job and is told it does not exist.
//
// There is deliberately no unpinned retry when a pinned poll fails, for either kind.
// It would fire on every transient error — a provider outage, a rate limit — and so
// double the load on an upstream already in trouble, to rescue only the rare case of
// a key deleted mid-flight. That case already has two better answers: the job parks
// as unpriceable, which is visible and re-drivable, and a user hitting
// /v1/batches/{id}/results settles it inline with their own key.
//
// BifrostContextKeyAPIKeyID pins by id through the registered pool rather than
// injecting a raw secret, and it is only write-blocked while the plugin pipeline
// holds restricted writes — which is exactly the phase this context never enters.
func internalJobContext(parent context.Context, selectedKeyID string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.NewString())
	ctx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)
	ctx.SetValue(schemas.BifrostContextKeySkipBudgetAndRateLimits, true)
	if selectedKeyID = strings.TrimSpace(selectedKeyID); selectedKeyID != "" {
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, selectedKeyID)
	}
	return ctx
}

func modelPtr(model string) *string {
	if model == "" {
		return nil
	}
	return &model
}

func (s *BifrostHTTPServer) WireBatchAccountingSweeper() {
	if s == nil || s.Client == nil || s.Config == nil {
		return
	}
	loggerPlugin, err := lib.FindPluginAs[*logging.LoggerPlugin](s.Config, logging.PluginName)
	if err != nil || loggerPlugin == nil {
		logger.Warn("batch accounting sweeper not wired: logging plugin not found (err=%v)", err)
		return
	}
	// The reporter must be cleared, not just left alone, when governance is gone:
	// this runs on reload, and a stale reporter keeps the logging plugin (and the
	// sweeper it is about to start, which snapshots it) reporting batch usage
	// through a torn-down plugin instance.
	var usageReporter jobaccounting.UsageReporter
	if governancePlugin, govErr := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, s.getGovernancePluginName()); govErr == nil && governancePlugin != nil {
		if reporter, ok := governancePlugin.(jobaccounting.UsageReporter); ok {
			usageReporter = reporter
		}
	}
	loggerPlugin.SetBatchUsageReporter(usageReporter)
	loggerPlugin.StartBatchAccountingSweeper(&bifrostBatchResultFetcher{client: s.Client}, time.Minute, s.Config.KVStore)
	// Video jobs finish in minutes, not hours, so their sweeper runs on a much
	// tighter tick than the batch one.
	loggerPlugin.StartVideoAccountingSweeper(&bifrostVideoRetriever{client: s.Client}, 30*time.Second, s.Config.KVStore)
}

type bifrostVideoRetriever struct {
	client *bifrost.Bifrost
}

func (f *bifrostVideoRetriever) RetrieveVideo(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostVideoGenerationResponse, error) {
	if f == nil || f.client == nil {
		return nil, fmt.Errorf("bifrost client is nil")
	}
	if job == nil {
		return nil, fmt.Errorf("video job is nil")
	}
	resp, bifrostErr := f.client.VideoRetrieveRequest(internalJobContext(ctx, job.SelectedKeyID), &schemas.BifrostVideoRetrieveRequest{
		Provider: schemas.ModelProvider(job.Provider),
		ID:       job.JobID,
	})
	if bifrostErr != nil {
		return nil, jobaccounting.NewProviderCallError(bifrostErr)
	}
	return resp, nil
}
