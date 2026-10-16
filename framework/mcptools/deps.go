package mcptools

import (
	"context"

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
	GetAvailableVirtualKeys(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableTeams(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableCustomers(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableBusinessUnits(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableRoutingRules(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableSelectedKeys(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableAliases(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableRoutingEngines(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableToolCallNames(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableMetadataKeys(ctx context.Context, limit int, query string) (map[string][]string, error)

	GetSessionLogs(ctx context.Context, sessionID string, pagination *logstore.PaginationOptions) (*logstore.SessionDetailResult, error)
	GetSessionSummary(ctx context.Context, sessionID string) (*logstore.SessionSummaryResult, error)
	GetDroppedRequests(ctx context.Context) int64

	GetMCPToolLog(ctx context.Context, id string) (*logstore.MCPToolLog, error)
	SearchMCPToolLogs(ctx context.Context, filters *logstore.MCPToolLogSearchFilters, pagination *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error)
	GetMCPToolLogStats(ctx context.Context, filters *logstore.MCPToolLogSearchFilters) (*logstore.MCPToolLogStats, error)
	GetMCPHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPHistogramResult, error)
	GetMCPCostHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPCostHistogramResult, error)
	GetMCPTopTools(ctx context.Context, filters logstore.MCPToolLogSearchFilters, limit int) (*logstore.MCPTopToolsResult, error)
}

// KeyPair is an id paired with the name it is known by.
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MaxSemanticQueryChars bounds the natural-language query, in characters. The
// query becomes an embedding request, so an unbounded tool argument is
// provider capacity and usage budget spent on one call. The tool schema
// advertises the same figure as maxLength, so the model can stay inside it
// instead of learning the bound from a refusal. Declared here rather than in
// framework/warp so the schema can read it without an import cycle.
const MaxSemanticQueryChars = 2000

// MaxSemanticQueries bounds how many phrasings one search embeds. Each is an
// embedding request and a vector search per prefilter, so the bound is spend.
const MaxSemanticQueries = 5

// Pinger is a store that can answer whether it is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// SemanticSearcher is semantic_search_logs' dependency: a meaning search over
// indexed conversations that hydrates its candidates back through a scoped
// LogReader. framework/warp's SemanticSearcher satisfies it; declaring the
// shape here keeps this package independent of who runs the index.
//
// visible is the caller's row ownership when the caller view carries one (see
// CallerView.Visible). It only sharpens which candidates the index is asked
// for; every candidate is still reloaded through the caller's context. Nil
// searches unfiltered.
type SemanticSearcher interface {
	SearchVisible(ctx context.Context, queries []string, filters *logstore.SearchFilters, visible *LogVisibility, requestedLimit int) (SemanticSearchResult, error)
}

// LogVisibility is the row ownership row-level access control grants a
// restricted caller: a log is theirs to read when it matches any one of these
// sets. It mirrors the store's own predicate, and it only has to be no
// narrower than it - a set that is too wide costs candidates, one that is too
// narrow hides rows the caller may see.
type LogVisibility struct {
	UserIDs         []string
	VirtualKeyIDs   []string
	TeamIDs         []string
	BusinessUnitIDs []string
	CustomerIDs     []string
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

// AdminDispatcher is how a write tool changes Bifrost: it sends the request the
// dashboard would send, to the same admin handler, in this process.
//
// Write tools do not reach the config store's write methods or the in-memory
// reloaders themselves. The admin handlers already own validation, the
// transaction and the reload that makes a stored row live, and a second copy
// of those rules here drifts from them one missed check at a time. A write tool
// is an adapter: it decides whether this caller may make the change, builds the
// body the route takes, and hands it to Do.
//
// The handlers do not filter writes by tenant, so everything the caller is
// allowed to set is decided before Do is called. The body is built from an
// explicit set of fields, never passed through from the tool's arguments: an
// argument the tool does not name must not reach the handler.
//
// transports/bifrost-http implements it, because framework cannot import the
// handlers, and limits it to the routes write tools are meant to call. Nil
// means this deployment has nothing to dispatch to.
type AdminDispatcher interface {
	// Do runs one admin request and returns the route's status and body. err
	// is for a request that could not be dispatched; a refusal from the route
	// (4xx, 5xx) is a status with its error body, not an err.
	Do(ctx context.Context, method, path string, body []byte) (status int, response []byte, err error)
}
