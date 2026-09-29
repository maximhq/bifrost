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
	// GetAvailableTeams, GetAvailableCustomers and GetAvailableBusinessUnits
	// list the id/name pairs seen in logged traffic - the same distinct lookups
	// the Logs filter bar uses. describe_filter_space reads these rather than ranking
	// each dimension: a ranking on the enterprise hierarchy path fans every row
	// out through JSON-array columns, which took tens of seconds on a large
	// table, all to learn which names exist.
	ScopeDiscoveryReader
	RoutingDiscoveryReader
}

// RoutingDiscoveryReader lists the routing values that occur in the logs: the
// same lookups behind the Logs page's filter dropdowns. They exist so a filter
// is named from a list rather than guessed - a guessed id returns an empty
// result that reads exactly like a real finding of zero.
type RoutingDiscoveryReader interface {
	GetAvailableRoutingRules(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableSelectedKeys(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableAliases(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableRoutingEngines(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableToolCallNames(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableMetadataKeys(ctx context.Context, limit int, query string) (map[string][]string, error)
}

// ScopeDiscoveryReader is the enterprise hierarchy half of the read surface.
//
// Named separately because it is optional in a way the rest is not: teams,
// customers and business units only exist where the enterprise user path
// records them, so an OSS deployment has nothing to answer with. Keeping it its
// own interface says that in the type rather than in a comment, and lets a
// reader that cannot serve these be described exactly.
//
// It is embedded in LogReader because the concrete manager does implement all
// of it, and splitting the dependency Warp actually holds would buy nothing but
// a second field to thread through.
type ScopeDiscoveryReader interface {
	GetAvailableTeams(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableCustomers(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableBusinessUnits(ctx context.Context, limit int, query string) ([]KeyPair, error)
}

// SemanticHydrator reads whole log rows for a set of ids.
//
// Kept out of LogReader deliberately. LogReader is exported and accepted by
// exported APIs - WithLogReader, NewAgent - so adding a method to it breaks
// every reader outside this repo at compile time, including ones that never
// touch semantic search. Semantic search asks for this separately and is
// enabled only when the supplied reader satisfies it, so an older reader keeps
// working with the rest of Warp's tools.
type SemanticHydrator interface {
	GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error)
}

// KeyPair is an id paired with the name it is known by.
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
