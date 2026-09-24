package mcptools

import (
	"context"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// LogReader is the slice of the deployment's telemetry these tools are allowed
// to read.
//
// It exists for two reasons, and the second is the one that forced it.
//
// The plain reason: this is the whole read surface. All queries, no writes and
// nothing that returns key material. Anything a tool can reach is on this list,
// so reviewing what the server can see means reading one interface rather than
// auditing every executor.
//
// The structural reason: plugins/logging depends on framework, so framework
// cannot depend back on it without a module cycle. Declaring the methods here
// and letting logging.LogManager satisfy them structurally is what lets the
// tools live in framework at all.
type LogReader interface {
	Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error)
	GetLog(ctx context.Context, id string) (*logstore.Log, error)
	// GetLogsByIDs hydrates vector-search candidates through the ordinary
	// scoped log reader. Implementations must preserve the input order and omit
	// rows that are missing or outside the caller's query scope.
	GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error)
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
	// GetAvailableVirtualKeys returns id/name pairs. The type is this package's
	// own rather than the log manager's: the two are field-identical but carry
	// different struct tags, and aliasing them would change the JSON an existing
	// endpoint already serves.
	GetAvailableVirtualKeys(ctx context.Context, limit int, query string) ([]KeyPair, error)
	// GetAvailableTeams, GetAvailableCustomers and GetAvailableBusinessUnits
	// list the id/name pairs seen in logged traffic - the same distinct lookups
	// the Logs filter bar uses. describe_filter_space reads these rather than
	// ranking each dimension: a ranking on the enterprise hierarchy path fans
	// every row out through JSON-array columns, which took tens of seconds on a
	// large table, all to learn which names exist.
	GetAvailableTeams(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableCustomers(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableBusinessUnits(ctx context.Context, limit int, query string) ([]KeyPair, error)
	// The routing values that occur in the logs: the same lookups behind the
	// Logs page's filter dropdowns. They exist so a filter is named from a list
	// rather than guessed - a guessed id returns an empty result that reads
	// exactly like a real finding of zero.
	GetAvailableRoutingRules(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableSelectedKeys(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableAliases(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableRoutingEngines(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableToolCallNames(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableMetadataKeys(ctx context.Context, limit int, query string) (map[string][]string, error)
}

// KeyPair is an id paired with the name it is known by.
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GovernanceReader is the slice of the config store describe_virtual_key is
// allowed to read.
//
// Like LogReader, this is deliberately one method: reviewing what the server
// can see about how the deployment is governed means reading this interface,
// not auditing configstore.ConfigStore's whole surface. GetVirtualKey in
// particular is scope-aware - a caller's ctx narrows which rows it can return,
// the same row-level enforcement every logstore query gets - so this tool
// inherits that for free rather than needing its own access check.
type GovernanceReader interface {
	GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
}

// MaxSemanticQueryChars bounds the natural-language query, in characters. The
// query becomes an embedding request, so an unbounded tool argument is
// provider capacity and usage budget spent on one call. The tool schema
// advertises the same figure as maxLength, so the model can stay inside it
// instead of learning the bound from a refusal. Declared here rather than in
// framework/warp so the schema can read it without an import cycle.
const MaxSemanticQueryChars = 2000

// SemanticSearcher is semantic_search_logs' dependency: a meaning search over
// indexed conversations that hydrates its candidates back through a scoped
// LogReader. framework/warp's SemanticSearcher satisfies it; declaring the
// shape here keeps this package independent of who runs the index.
type SemanticSearcher interface {
	Search(ctx context.Context, query string, filters *logstore.SearchFilters, requestedLimit int) (SemanticSearchResult, error)
}

// SemanticSearchRow is one hit: the projected log row and its vector score.
type SemanticSearchRow struct {
	Score float64 `json:"score"`
	LogRow
}

// SemanticSearchResult is what a SemanticSearcher returns, in score order.
type SemanticSearchResult struct {
	Rows      []SemanticSearchRow `json:"rows"`
	Returned  int                 `json:"returned"`
	Threshold float64             `json:"threshold"`
}
