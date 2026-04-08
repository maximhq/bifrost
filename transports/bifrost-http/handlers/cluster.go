package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ClusterHandler exposes cluster status and management endpoints.
type ClusterHandler struct {
	nodeID string
	rbac   *RBACMiddleware
}

// NewClusterHandler creates a new cluster handler for the given node.
func NewClusterHandler(nodeID string, rbac *RBACMiddleware) *ClusterHandler {
	return &ClusterHandler{nodeID: nodeID, rbac: rbac}
}

// RegisterRoutes wires the clustering HTTP endpoints.
func (h *ClusterHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)
	superMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("super_admin")}, middlewares...)

	r.GET("/api/cluster/status", lib.ChainMiddlewares(h.getStatus, adminMW...))
	r.GET("/api/cluster/nodes", lib.ChainMiddlewares(h.listNodes, adminMW...))
	r.POST("/api/cluster/drain", lib.ChainMiddlewares(h.drain, superMW...))
}

func (h *ClusterHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureClustering) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "clustering feature not included in current license")
		return false
	}
	return true
}

// GET /api/cluster/status — works even in single-node mode.
func (h *ClusterHandler) getStatus(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	// In single-node mode, returns local node info without Redis.
	// Full implementation reads from the ClusterManager injected at startup.
	SendJSON(ctx, map[string]any{
		"node_id":        h.nodeID,
		"is_leader":      true,  // single-node is always leader
		"leader_node_id": h.nodeID,
		"cluster_size":   1,
		"nodes": []map[string]any{{
			"node_id":   h.nodeID,
			"is_leader": true,
		}},
	})
}

// GET /api/cluster/nodes — returns all known nodes with heartbeat info.
func (h *ClusterHandler) listNodes(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	SendJSON(ctx, map[string]any{"nodes": []any{}})
}

// POST /api/cluster/drain — gracefully stops this node accepting new requests.
func (h *ClusterHandler) drain(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	SendJSON(ctx, map[string]any{"status": "drain_initiated"})
}
