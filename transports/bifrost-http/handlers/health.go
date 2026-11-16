package handlers

import (
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// HealthHandler manages HTTP requests for health checks.
type HealthHandler struct {
	config *lib.Config
}

// NewHealthHandler creates a new health handler instance.
func NewHealthHandler(config *lib.Config) *HealthHandler {
	return &HealthHandler{
		config: config,
	}
}

// RegisterRoutes registers the health-related routes.
func (h *HealthHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/health", lib.ChainMiddlewares(h.getHealth, middlewares...))
	r.GET("/api/health/vectorstore", lib.ChainMiddlewares(h.getVectorStoreHealth, middlewares...))
}

// getHealth handles GET /api/health - Get the health status of the server.
func (h *HealthHandler) getHealth(ctx *fasthttp.RequestCtx) {
	// If DB pings are disabled, just return OK
	if h.config.ClientConfig.DisableDBPingsInHealth {
		SendJSON(ctx, map[string]any{"status": "ok", "components": map[string]any{"db_pings": "disabled"}})
		return
	}
	// Pinging config store
	reqCtx, cancel := RequestWorkContext(ctx, 10*time.Second)
	defer cancel()
	var errors []string
	var mu sync.Mutex
	var wg sync.WaitGroup

	if h.config.ConfigStore != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.config.ConfigStore.Ping(reqCtx); err != nil {
				mu.Lock()
				errors = append(errors, "config store not available")
				mu.Unlock()
			}
		}()
	}

	// Pinging log store
	if h.config.LogsStore != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.config.LogsStore.Ping(reqCtx); err != nil {
				mu.Lock()
				errors = append(errors, "log store not available")
				mu.Unlock()
			}
		}()
	}

	// Pinging vector store
	if h.config.VectorStore != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.config.VectorStore.Ping(reqCtx); err != nil {
				mu.Lock()
				errors = append(errors, "vector store not available")
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(errors) > 0 {
		SendError(ctx, fasthttp.StatusServiceUnavailable, errors[0])
		return
	}
	SendJSON(ctx, map[string]any{"status": "ok", "components": map[string]any{"db_pings": "ok"}})
}

// VectorStoreHealthStatus is the overall verdict for GET /api/health/vectorstore.
type VectorStoreHealthStatus string

const (
	VectorStoreHealthStatusHealthy   VectorStoreHealthStatus = "healthy"
	VectorStoreHealthStatusDegraded  VectorStoreHealthStatus = "degraded"
	VectorStoreHealthStatusUnhealthy VectorStoreHealthStatus = "unhealthy"
	VectorStoreHealthStatusDisabled  VectorStoreHealthStatus = "disabled"
)

// VectorStoreHealthResponse is the body returned by GET /api/health/vectorstore. It is meant
// to be genuinely actionable during an incident: status explains at a glance whether to page
// anyone, detail says why in plain English (with any credential redacted), and pool, when the
// backing store exposes one, shows whether the configured pool size is the actual bottleneck.
type VectorStoreHealthResponse struct {
	Status VectorStoreHealthStatus `json:"status"`
	// Type names the configured backend (e.g. "pgvector", "redis"), derived from the concrete
	// implementation so this stays correct for every VectorStore without hardcoding a list.
	Type string `json:"type,omitempty"`
	// LatencyMs is how long the health Ping took to return, successful or not.
	LatencyMs int64 `json:"latency_ms,omitempty"`
	// Detail explains a non-healthy status in plain English. Never includes a password: Ping
	// errors are network/driver errors (connection refused, timeout, auth failed) that pgx and
	// the standard library report without the connection string, and pgx itself redacts the
	// password from any DSN-parsing error.
	Detail string `json:"detail,omitempty"`
	// Pool is the backing connection pool's current usage, when the store exposes one
	// (see vectorstore.PoolStatsProvider). Nil for a store that is not pool-backed.
	Pool *vectorstore.PoolStats `json:"pool,omitempty"`
}

// vectorStorePoolExhausted reports a pool with no room left to hand out another connection,
// the condition that turns a technically-reachable store into a "degraded" one: callers will
// start queuing (or failing) on Acquire even though Ping just succeeded.
func vectorStorePoolExhausted(stats vectorstore.PoolStats) bool {
	return stats.MaxConns > 0 && stats.AcquiredConns >= stats.MaxConns
}

// vectorStoreTypeName derives a short, human-readable name for the configured VectorStore
// implementation from its concrete Go type (e.g. "*vectorstore.PgVectorStore" -> "pgvector"),
// so a new backend shows up here without this handler needing to know about it.
func vectorStoreTypeName(store vectorstore.VectorStore) string {
	name := reflect.TypeOf(store).String()
	name = strings.TrimPrefix(name, "*")
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSuffix(name, "Store")
	return strings.ToLower(name)
}

// getVectorStoreHealth handles GET /api/health/vectorstore - a detailed, operator-facing
// health check for the configured vector store (semantic cache / Warp's backing store).
// Unlike /health's coarse ok/unavailable, this reports a real status with enough detail to
// act on during an incident: whether the store is reachable right now, how long the check
// took, and - for a pool-backed store like pgvector - current pool usage, so "unhealthy" vs.
// "merely saturated" don't look the same.
func (h *HealthHandler) getVectorStoreHealth(ctx *fasthttp.RequestCtx) {
	if h.config.VectorStore == nil {
		SendJSON(ctx, VectorStoreHealthResponse{Status: VectorStoreHealthStatusDisabled, Detail: "no vector store is configured"})
		return
	}

	store := h.config.VectorStore
	response := VectorStoreHealthResponse{Type: vectorStoreTypeName(store)}

	if h.config.ClientConfig.DisableDBPingsInHealth {
		response.Status = VectorStoreHealthStatusHealthy
		response.Detail = "DB pings are disabled (client_config.disable_db_pings_in_health); reporting pool stats only"
		if stats, ok := vectorstore.VectorStorePoolStats(store); ok {
			response.Pool = &stats
			if vectorStorePoolExhausted(stats) {
				response.Status = VectorStoreHealthStatusDegraded
			}
		}
		SendJSON(ctx, response)
		return
	}

	reqCtx, cancel := RequestWorkContext(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	pingErr := store.Ping(reqCtx)
	response.LatencyMs = time.Since(start).Milliseconds()

	if pingErr != nil {
		response.Status = VectorStoreHealthStatusUnhealthy
		response.Detail = "unreachable: " + pingErr.Error()
		if stats, ok := vectorstore.VectorStorePoolStats(store); ok {
			response.Pool = &stats
		}
		SendJSONWithStatus(ctx, response, fasthttp.StatusServiceUnavailable)
		return
	}

	response.Status = VectorStoreHealthStatusHealthy
	if stats, ok := vectorstore.VectorStorePoolStats(store); ok {
		response.Pool = &stats
		if vectorStorePoolExhausted(stats) {
			response.Status = VectorStoreHealthStatusDegraded
			response.Detail = "connection pool exhausted: acquired_conns has reached max_conns, new requests will queue for a connection"
		}
	}
	SendJSON(ctx, response)
}
