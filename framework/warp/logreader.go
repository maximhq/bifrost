package warp

import (
	"context"

	"github.com/maximhq/bifrost/framework/logstore"
)

// LogReader is the slice of the deployment's telemetry Warp is allowed to read.
//
// It exists for two reasons, and the second is the one that forced it.
//
// The plain reason: this is the whole read surface. Eighteen methods, all
// queries, no writes and nothing that returns key material. Anything a tool can
// reach is on this list, so reviewing what Warp can see means reading one
// interface rather than auditing every executor.
//
// The structural reason: plugins/logging depends on framework, so framework
// cannot depend back on it without a module cycle. Declaring the methods here
// and letting logging.LogManager satisfy them structurally is what lets the
// agent live in framework at all. It is the better shape regardless - a
// consumer naming what it needs, rather than importing a manager and inheriting
// everything else that hangs off it.
type LogReader interface {
	Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error)
	GetLog(ctx context.Context, id string) (*logstore.Log, error)
	GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error)

	GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error)
	GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error)
	GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error)
	GetLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.LatencyHistogramResult, error)
	GetThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ThroughputHistogramResult, error)

	GetModelRankings(ctx context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error)
	GetDimensionRankings(ctx context.Context, filters *logstore.SearchFilters, dimension logstore.RankingDimension) (*logstore.DimensionRankingResult, error)

	GetProviderCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error)
	GetProviderLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderLatencyHistogramResult, error)
	GetProviderThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderThroughputHistogramResult, error)
	GetProviderTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderTokenHistogramResult, error)

	GetAvailableModels(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableApps(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableStopReasons(ctx context.Context, limit int, query string) ([]string, error)
	// GetAvailableVirtualKeys returns id/name pairs. The type is Warp's own
	// rather than the log manager's: the two are field-identical but carry
	// different struct tags, and aliasing them would change the JSON an existing
	// endpoint already serves. The caller adapts this one method; the other
	// seventeen match exactly.
	GetAvailableVirtualKeys(ctx context.Context, limit int, query string) ([]KeyPair, error)
}

// KeyPair is an id paired with the name it is known by.
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
