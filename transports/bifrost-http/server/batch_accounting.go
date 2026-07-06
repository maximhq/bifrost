package server

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/batchaccounting"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

type bifrostBatchResultFetcher struct {
	client *bifrost.Bifrost
}

func (f *bifrostBatchResultFetcher) RetrieveBatch(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchRetrieveResponse, error) {
	if f == nil || f.client == nil {
		return nil, fmt.Errorf("bifrost client is nil")
	}
	if job == nil {
		return nil, fmt.Errorf("batch job is nil")
	}
	resp, bifrostErr := f.client.BatchRetrieveRequest(internalBatchContext(ctx), &schemas.BifrostBatchRetrieveRequest{
		Provider: schemas.ModelProvider(job.Provider),
		Model:    modelPtr(job.Model),
		BatchID:  job.BatchID,
	})
	if bifrostErr != nil {
		return nil, fmt.Errorf("%s", bifrostErr.GetErrorString())
	}
	return resp, nil
}

func (f *bifrostBatchResultFetcher) FetchBatchResults(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchResultsResponse, error) {
	if f == nil || f.client == nil {
		return nil, fmt.Errorf("bifrost client is nil")
	}
	if job == nil {
		return nil, fmt.Errorf("batch job is nil")
	}
	resp, bifrostErr := f.client.BatchResultsRequest(internalBatchContext(ctx), &schemas.BifrostBatchResultsRequest{
		Provider: schemas.ModelProvider(job.Provider),
		Model:    modelPtr(job.Model),
		BatchID:  job.BatchID,
	})
	if bifrostErr != nil {
		return nil, fmt.Errorf("%s", bifrostErr.GetErrorString())
	}
	return resp, nil
}

func internalBatchContext(parent context.Context) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.NewString())
	ctx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)
	ctx.SetValue(schemas.BifrostContextKeySkipBudgetAndRateLimits, true)
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
	if governancePlugin, govErr := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, s.getGovernancePluginName()); govErr == nil && governancePlugin != nil {
		if usageReporter, ok := governancePlugin.(batchaccounting.UsageReporter); ok {
			loggerPlugin.SetBatchUsageReporter(usageReporter)
		}
	}
	loggerPlugin.StartBatchAccountingSweeper(&bifrostBatchResultFetcher{client: s.Client}, time.Minute, s.Config.KVStore)
}
