package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

// agentAuthenticationResult is the authentication decision established by the
// Agent Gateway HTTP middleware and propagated through generic request context.
type agentAuthenticationResult struct {
	credentialSource   lib.VirtualKeyHeaderSource
	userID             string
	rawVirtualKey      string
	acceptedCredential *schemas.AcceptedBifrostCredential
}

type agentGatewayContextKey string

type agentBinding int

func (b agentBinding) String() string {
	switch b {
	case agentBindingJSONRPC:
		return string(a2a.TransportProtocolJSONRPC)
	case agentBindingREST:
		return string(a2a.TransportProtocolHTTPJSON)
	case agentBindingCard:
		return "http"
	default:
		return ""
	}
}

const (
	agentBindingJSONRPC agentBinding = iota
	agentBindingREST
	agentBindingCard

	agentGatewayAuthenticationContextKey agentGatewayContextKey = "bifrost-agent-gateway-authentication"
	agentGatewayBifrostContextKey        agentGatewayContextKey = "bifrost-agent-gateway-context"
)

// AgentGatewayIdentityResolver validates an Authorization credential and returns
// the local user identity it represents. When nil, no credential identity is resolved.
type AgentGatewayIdentityResolver func(context.Context, string) (string, bool)

// AgentGatewayVirtualKeyValidator is the narrow governance-cache contract used
// by Agent Gateway authentication middleware. Authentication remains DB-free.
type AgentGatewayVirtualKeyValidator interface {
	VirtualKeyCache
	GetVirtualKey(ctx context.Context, value string) (*tables.TableVirtualKey, bool)
}

// VirtualKeyReloader refreshes one virtual key in the governance plugin's
// in-memory store. The Agent Gateway needs it because the agent-side
// virtual_key_ids field writes the same grant rows the virtual-key-side
// agent_grants field writes, and the governance authorization decision reads
// those rows from the cached virtual key; the virtual-key APIs already reload
// after their own writes, so the agent APIs must do the same for the virtual keys
// they touch, exactly as the MCP client APIs do after changing VK assignments.
type VirtualKeyReloader interface {
	ReloadVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
}

// AgentRegistrationManager owns registration mutations and the local runtime
// updates they require. Deployments may wrap these callbacks with additional
// coordination without exposing that concern to the HTTP handler.
type AgentRegistrationManager interface {
	CreateAgentRegistration(ctx context.Context, req agent.CreateRequest) (schemas.AgentRegistrationView, error)
	UpdateAgentRegistration(ctx context.Context, name string, req agent.UpdateRequest) (schemas.AgentRegistrationView, error)
	DeleteAgentRegistration(ctx context.Context, name string) error
}

type localAgentRegistrationManager struct {
	manager *agent.Manager
}

func (m localAgentRegistrationManager) CreateAgentRegistration(ctx context.Context, req agent.CreateRequest) (schemas.AgentRegistrationView, error) {
	return m.manager.Create(ctx, req)
}

func (m localAgentRegistrationManager) UpdateAgentRegistration(ctx context.Context, name string, req agent.UpdateRequest) (schemas.AgentRegistrationView, error) {
	return m.manager.Update(ctx, name, req)
}

func (m localAgentRegistrationManager) DeleteAgentRegistration(ctx context.Context, name string) error {
	return m.manager.Delete(ctx, name)
}

// AgentGatewayHandler exposes the Agent Gateway over HTTP in two deliberately
// separate security domains: administrative registration APIs behind the existing
// dashboard middleware, and A2A protocol routes behind their own virtual-key
// authentication. A virtual key can therefore never reach the management APIs.
type AgentGatewayHandler struct {
	manager             *agent.Manager
	registrationManager AgentRegistrationManager
	config              *lib.Config
	// vkReloader refreshes the governance in-memory copy of every virtual key an
	// agent mutation granted or revoked. Optional: without it, agent-side grant
	// changes would only be observed after the next virtual-key reload.
	vkReloader VirtualKeyReloader
}

// NewAgentGatewayHandler returns nil when a required dependency is missing. The
// handler owns protocol dispatch and management only; request authentication is
// supplied independently when protocol routes are registered.
func NewAgentGatewayHandler(manager *agent.Manager, registrationManager AgentRegistrationManager, config *lib.Config, vkReloader VirtualKeyReloader) *AgentGatewayHandler {
	if manager == nil || config == nil {
		return nil
	}
	if registrationManager == nil {
		registrationManager = localAgentRegistrationManager{manager: manager}
	}
	return &AgentGatewayHandler{manager: manager, registrationManager: registrationManager, config: config, vkReloader: vkReloader}
}

// ResyncFromStore rebuilds every agent runtime from persisted registrations and
// refreshes the enabled-agent policy map.
func (h *AgentGatewayHandler) ResyncFromStore(ctx context.Context) error {
	if err := h.manager.Reload(ctx); err != nil {
		return err
	}
	return h.SyncEnabledAgents(ctx)
}

// GetRegistration returns one redacted persisted registration.
func (h *AgentGatewayHandler) GetRegistration(ctx context.Context, name string) (schemas.AgentRegistrationView, error) {
	return h.manager.Get(ctx, name)
}

// ReloadRegistration replaces one runtime and its enabled-agent policy from the
// persisted registration.
func (h *AgentGatewayHandler) ReloadRegistration(ctx context.Context, name string) error {
	view, err := h.manager.ReloadRegistration(ctx, name)
	if err != nil {
		return err
	}
	h.config.SetEnabledAgent(view.Name, view.Enabled, view.AllowByDefault)
	return nil
}

// RemoveRegistration unpublishes one runtime and removes its enabled-agent policy.
func (h *AgentGatewayHandler) RemoveRegistration(name string) {
	h.manager.RemoveRuntime(name)
	h.config.DeleteEnabledAgent(name)
}

// SyncEnabledAgents replaces the HTTP-owned enabled-agent policy map from
// persisted registrations at startup or an explicit reload boundary.
func (h *AgentGatewayHandler) SyncEnabledAgents(ctx context.Context) error {
	views, err := h.manager.List(ctx)
	if err != nil {
		return err
	}
	agents := make(map[string]bool, len(views))
	for _, view := range views {
		if view.Enabled {
			agents[view.Name] = view.AllowByDefault
		}
	}
	h.config.ReplaceEnabledAgents(agents)
	return nil
}

// Close shuts the gateway down during server teardown, draining upstream clients.
// It is nil-safe so shutdown does not need to know whether the gateway was enabled.
func (h *AgentGatewayHandler) Close() {
	if h != nil && h.manager != nil {
		h.manager.Close()
	}
}

// AgentGatewayAuthPolicy adapts live server configuration into the policy the core
// manager uses when generating gateway cards. Enforcement is a closure rather than
// a snapshot so a runtime change to enforce_auth_on_inference is reflected in the
// next card without rebuilding the manager, and the read takes the config lock.
func AgentGatewayAuthPolicy(config *lib.Config) schemas.AgentGatewayAuthPolicy {
	return schemas.AgentGatewayAuthPolicy{
		EnforceAuthentication: func() bool {
			if config == nil {
				return false
			}
			config.Mu.RLock()
			defer config.Mu.RUnlock()
			return config.ClientConfig != nil && config.ClientConfig.EnforceAuthOnInference
		},
	}
}

// RegisterManagementRoutes mounts registration CRUD under the administrative API
// surface, wrapped in the caller-supplied dashboard middleware so these routes
// require administrative authentication rather than a virtual key.
func (h *AgentGatewayHandler) RegisterManagementRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/agents/push-configs", lib.ChainMiddlewares(h.listPushConfigs, middlewares...))
	r.DELETE("/api/agents/push-configs/{agent}/{task}/{config}", lib.ChainMiddlewares(h.deletePushConfig, middlewares...))
	r.POST("/api/agents", lib.ChainMiddlewares(h.create, middlewares...))
	r.POST("/api/agents/inspect", lib.ChainMiddlewares(h.inspect, middlewares...))
	r.GET("/api/agents", lib.ChainMiddlewares(h.list, middlewares...))
	r.GET("/api/agents/{name}", lib.ChainMiddlewares(h.get, middlewares...))
	r.PUT("/api/agents/{name}", lib.ChainMiddlewares(h.update, middlewares...))
	r.DELETE("/api/agents/{name}", lib.ChainMiddlewares(h.delete, middlewares...))
	r.GET("/api/agents/history", lib.ChainMiddlewares(h.listHistory, middlewares...))
	r.DELETE("/api/agents/history", lib.ChainMiddlewares(h.deleteHistory, middlewares...))
	// The static "stats" and "histogram" segments are registered alongside the
	// pre-existing "{id}" wildcard. The fasthttp radix router prefers the static
	// sibling, so both resolve; TestAgentGatewayHistoryRoutesResolve pins that
	// behaviour so the aggregates cannot silently start hitting the entry
	// lookup.
	r.GET("/api/agents/history/filterdata", lib.ChainMiddlewares(h.historyFilterData, middlewares...))
	r.GET("/api/agents/history/stats", lib.ChainMiddlewares(h.historyStats, middlewares...))
	r.GET("/api/agents/history/histogram", lib.ChainMiddlewares(h.historyHistogram, middlewares...))
	r.GET("/api/agents/history/{id}", lib.ChainMiddlewares(h.getHistoryEntry, middlewares...))
}

// RegisterProtocolRoutes mounts all A2A endpoints through the supplied transport
// middleware and the Agent Gateway authentication boundary.
func (h *AgentGatewayHandler) RegisterProtocolRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/agents/a2a/{name}/.well-known/agent-card.json", lib.ChainMiddlewares(h.card, middlewares...))
	r.POST("/agents/a2a/{name}"+agent.GatewayJSONRPCPathSuffix, lib.ChainMiddlewares(h.protocol, middlewares...))
	for _, method := range []string{fasthttp.MethodGet, fasthttp.MethodPost, fasthttp.MethodDelete} {
		r.Handle(method, "/agents/a2a/{name}"+agent.GatewayRESTPathSuffix+"/{restpath:*}", lib.ChainMiddlewares(h.rest, middlewares...))
	}
}

// RegisterPushIngressRoute mounts the push callback ingress. It is registered
// outside the Agent Gateway authentication boundary on purpose: the upstream
// agent authenticates with the per-config token Bifrost minted at push-config
// registration, never with a downstream virtual key.
func (h *AgentGatewayHandler) RegisterPushIngressRoute(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/agents/a2a/{name}"+agent.GatewayPushCallbackPathSuffix, lib.ChainMiddlewares(h.pushCallback, middlewares...))
}

// pushCallback accepts one upstream push event. Authentication, task
// verification, and durable enqueueing are the manager's responsibility; the
// transport only carries the token header and body across.
func (h *AgentGatewayHandler) pushCallback(ctx *fasthttp.RequestCtx) {
	token := string(ctx.Request.Header.Peek(agent.PushNotificationTokenHeader))
	status, err := h.manager.AcceptPushCallback(ctx, pathName(ctx), token, ctx.PostBody())
	if err != nil {
		if status >= fasthttp.StatusInternalServerError {
			SendError(ctx, status, "failed to accept push notification")
			return
		}
		SendError(ctx, status, err.Error())
		return
	}
	ctx.SetStatusCode(status)
}

func pushConfigAgentNames(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	names := strings.Split(string(raw), ",")
	result := names[:0]
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			result = append(result, name)
		}
	}
	return result
}

func (h *AgentGatewayHandler) listPushConfigs(ctx *fasthttp.RequestCtx) {
	limit, _ := strconv.Atoi(string(ctx.QueryArgs().Peek("limit")))
	offset, _ := strconv.Atoi(string(ctx.QueryArgs().Peek("offset")))
	limit, offset = ClampPaginationParams(limit, offset)
	configs, total, err := h.manager.ListStoredPushConfigs(ctx, schemas.AgentPushConfigQuery{
		AgentNames: pushConfigAgentNames(ctx.QueryArgs().Peek("agent_names")),
		TaskID:     string(ctx.QueryArgs().Peek("task_id")),
		ConfigID:   string(ctx.QueryArgs().Peek("config_id")),
		URL:        string(ctx.QueryArgs().Peek("url")),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list agent push configurations")
		return
	}
	names, err := h.manager.ListStoredPushConfigAgentNames(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list agent push configuration filters")
		return
	}
	SendJSON(ctx, map[string]any{
		"push_configs": configs,
		"agent_names":  names,
		"count":        len(configs),
		"total_count":  total,
		"limit":        limit,
		"offset":       offset,
	})
}

func (h *AgentGatewayHandler) deletePushConfig(ctx *fasthttp.RequestCtx) {
	agentName, _ := ctx.UserValue("agent").(string)
	taskID, _ := ctx.UserValue("task").(string)
	configID, _ := ctx.UserValue("config").(string)
	if agentName == "" || taskID == "" || configID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "agent, task, and config are required")
		return
	}
	deleted, err := h.manager.DeleteStoredPushConfig(ctx, agentName, taskID, configID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to delete agent push configuration")
		return
	}
	if !deleted {
		SendError(ctx, fasthttp.StatusNotFound, "agent push configuration not found")
		return
	}
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

// create registers an agent. Validation and upstream reachability are the
// manager's responsibility, so any failure here is reported as a client error with
// the manager's message.
func (h *AgentGatewayHandler) create(ctx *fasthttp.RequestCtx) {
	var req agent.CreateRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid agent registration")
		return
	}
	view, err := h.registrationManager.CreateAgentRegistration(ctx, req)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	h.config.SetEnabledAgent(view.Name, view.Enabled, view.AllowByDefault)
	h.reloadGrantedVirtualKeys(ctx, view.VirtualKeyIDs)
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, view)
}

func (h *AgentGatewayHandler) inspect(ctx *fasthttp.RequestCtx) {
	var req agent.InspectRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid agent inspection request")
		return
	}
	result, err := h.manager.Inspect(ctx, req)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	SendJSON(ctx, result)
}

// update replaces a registration. A missing agent is distinguished from a rejected
// body so callers get 404 rather than 400 when the target does not exist.
func (h *AgentGatewayHandler) update(ctx *fasthttp.RequestCtx) {
	var req agent.UpdateRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid agent replacement")
		return
	}
	name := pathName(ctx)
	// The grants being replaced are read before the write so the virtual keys that
	// lose access are refreshed too, not just the ones that gain it.
	previous, _ := h.manager.Get(ctx, name)
	view, err := h.registrationManager.UpdateAgentRegistration(ctx, name, req)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) || errors.Is(err, agent.ErrNotFound) {
			h.managementError(ctx, configstore.ErrNotFound)
			return
		}
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	h.config.SetEnabledAgent(view.Name, view.Enabled, view.AllowByDefault)
	h.reloadGrantedVirtualKeys(ctx, previous.VirtualKeyIDs, view.VirtualKeyIDs)
	SendJSON(ctx, view)
}

// list returns redacted registration summaries with a count, including disabled
// agents so administrators can see everything they have configured.
func (h *AgentGatewayHandler) list(ctx *fasthttp.RequestCtx) {
	views, err := h.manager.List(ctx)
	if err != nil {
		SendError(ctx, 500, "failed to list agents")
		return
	}
	SendJSON(ctx, map[string]any{"agents": views, "count": len(views)})
}

// get returns one redacted registration; secrets are never included.
func (h *AgentGatewayHandler) get(ctx *fasthttp.RequestCtx) {
	view, err := h.manager.Get(ctx, pathName(ctx))
	if err != nil {
		h.managementError(ctx, err)
		return
	}
	SendJSON(ctx, view)
}

// delete removes a registration and its grants, responding 204 because there is no
// remaining representation to return.
func (h *AgentGatewayHandler) delete(ctx *fasthttp.RequestCtx) {
	name := pathName(ctx)
	// Read the grants before deleting so the virtual keys whose cascade-deleted
	// rows just disappeared stop carrying a stale grant for this agent name.
	previous, _ := h.manager.Get(ctx, name)
	if err := h.registrationManager.DeleteAgentRegistration(ctx, name); err != nil {
		h.managementError(ctx, err)
		return
	}
	h.config.DeleteEnabledAgent(name)
	h.reloadGrantedVirtualKeys(ctx, previous.VirtualKeyIDs)
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

const agentHistoryDefaultLimit = 50

func (h *AgentGatewayHandler) listHistory(ctx *fasthttp.RequestCtx) {
	if h.config.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "logs store is not available")
		return
	}
	filter, pagination, err := parseAgentHistorySearch(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	includePayloads, includeEvents, err := parseAgentHistoryIncludes(ctx, filter)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	if includePayloads && includeEvents {
		rows, err := h.config.LogsStore.ListAgentLogSessions(logCtx, filter, pagination)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to list agent history")
			return
		}
		result := logstore.AgentLogSessionResult{Logs: make([]logstore.AgentLogSession, 0, len(rows.Requests)), Pagination: rows.Pagination}
		for _, request := range rows.Requests {
			session := logstore.AgentLogSession{AgentLogDetail: newSanitizedAgentLogDetail(request), Events: []logstore.AgentLogDetail{}}
			for _, event := range rows.Events[logstore.AgentSessionCorrelationKey(request)] {
				session.Events = append(session.Events, newSanitizedAgentLogDetail(event))
			}
			result.Logs = append(result.Logs, session)
		}
		SendJSON(ctx, result)
		return
	}
	result, err := h.config.LogsStore.ListAgentLogHistory(logCtx, filter, pagination)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list agent history")
		return
	}
	for i := range result.Logs {
		result.Logs[i].Input = sanitizeAgentHistoryPayload(result.Logs[i].Input)
	}
	SendJSON(ctx, result)
}

// deleteHistory serves DELETE /api/agents/history. The store also removes the
// correlated stream-event rows sharing the deleted entries' request IDs.
func (h *AgentGatewayHandler) deleteHistory(ctx *fasthttp.RequestCtx) {
	if h.config.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "logs store is not available")
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return
	}
	if len(req.IDs) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "No log IDs provided")
		return
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	if err := h.config.LogsStore.DeleteAgentLogs(logCtx, req.IDs); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete agent logs")
		return
	}
	SendJSON(ctx, map[string]any{"message": "Agent logs deleted successfully"})
}

func (h *AgentGatewayHandler) historyFilterData(ctx *fasthttp.RequestCtx) {
	if h.config.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "logs store is not available")
		return
	}
	dimensions := strings.Split(strings.TrimSpace(string(ctx.QueryArgs().Peek("dimensions"))), ",")
	if len(dimensions) == 1 && dimensions[0] == "" {
		dimensions = nil
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	result, err := h.config.LogsStore.GetAgentFilterData(logCtx, dimensions, 100, strings.TrimSpace(string(ctx.QueryArgs().Peek("q"))))
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to get agent history filter data")
		return
	}
	SendJSON(ctx, result)
}

// historyStats serves GET /api/agents/history/stats. It reuses the list
// endpoint's filter parsing so every sidebar filter narrows the totals too.
func (h *AgentGatewayHandler) historyStats(ctx *fasthttp.RequestCtx) {
	if h.config.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "logs store is not available")
		return
	}
	filter, _, err := parseAgentHistorySearch(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	stats, err := h.config.LogsStore.GetAgentLogStats(logCtx, filter)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to get agent history stats")
		return
	}
	SendJSON(ctx, stats)
}

// historyHistogram serves GET /api/agents/history/histogram, the volume chart's
// source. Bucket size follows the same time-range ladder as the LLM histogram.
func (h *AgentGatewayHandler) historyHistogram(ctx *fasthttp.RequestCtx) {
	if h.config.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "logs store is not available")
		return
	}
	filter, _, err := parseAgentHistorySearch(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	result, err := h.config.LogsStore.GetAgentHistogram(logCtx, filter, calculateBucketSize(filter.StartTime, filter.EndTime))
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to get agent history histogram")
		return
	}
	SendJSON(ctx, result)
}

func (h *AgentGatewayHandler) getHistoryEntry(ctx *fasthttp.RequestCtx) {
	if h.config.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "logs store is not available")
		return
	}
	id, _ := ctx.UserValue("id").(string)
	if id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "history entry id is required")
		return
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	entry, err := h.config.LogsStore.FindAgentLog(logCtx, id)
	if err != nil {
		if errors.Is(err, logstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "agent history entry not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to get agent history entry")
		return
	}
	detail := newSanitizedAgentLogDetail(entry)
	SendJSON(ctx, detail)
}

func newSanitizedAgentLogDetail(entry *logstore.AgentLog) logstore.AgentLogDetail {
	detail := logstore.NewAgentLogDetail(entry)
	detail.RequestBody = sanitizeAgentHistoryPayload(detail.RequestBody)
	detail.ResponseBody = sanitizeAgentHistoryPayload(detail.ResponseBody)
	detail.EventBody = sanitizeAgentHistoryPayload(detail.EventBody)
	return detail
}

func agentGatewayLogContext(ctx *fasthttp.RequestCtx) *schemas.BifrostContext {
	logCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.VisitUserValuesAll(func(key, value any) {
		logCtx.SetValue(key, value)
	})
	return logCtx
}

func parseAgentHistoryIncludes(ctx *fasthttp.RequestCtx, filter logstore.AgentLogHistoryFilter) (bool, bool, error) {
	parse := func(name string) (bool, error) {
		value := strings.TrimSpace(string(ctx.QueryArgs().Peek(name)))
		if value == "" {
			return false, nil
		}
		if value != "true" {
			return false, fmt.Errorf("invalid %s parameter: must be true", name)
		}
		return true, nil
	}
	includePayloads, err := parse("include_payloads")
	if err != nil {
		return false, false, err
	}
	includeEvents, err := parse("include_events")
	if err != nil {
		return false, false, err
	}
	if includeEvents && !includePayloads {
		return false, false, fmt.Errorf("include_events=true requires include_payloads=true")
	}
	if includeEvents {
		if filter.ContextID == "" {
			return false, false, fmt.Errorf("include_events=true requires context_id")
		}
		if len(filter.RecordKind) != 1 || filter.RecordKind[0] != "request" {
			return false, false, fmt.Errorf("include_events=true requires record_kind=request")
		}
	}
	return includePayloads, includeEvents, nil
}

func parseAgentHistorySearch(ctx *fasthttp.RequestCtx) (logstore.AgentLogHistoryFilter, logstore.PaginationOptions, error) {
	args := ctx.QueryArgs()
	value := func(name string) string { return strings.TrimSpace(string(args.Peek(name))) }
	values := func(name string) []string { return parseAgentHistoryMultiValue(args, name) }
	filter := logstore.AgentLogHistoryFilter{
		AgentName: values("agent_name"), Operation: values("operation"), UserID: values("user_id"),
		VirtualKeyID: values("virtual_key_id"), TeamID: values("team_id"), CustomerID: values("customer_id"),
		BusinessUnitID: values("business_unit_id"), ProjectID: values("project_id"), RequestID: value("request_id"), TraceID: value("trace_id"),
		TaskID: value("task_id"), ContextID: value("context_id"), PushConfigID: value("push_config_id"),
		DeliveryID: value("delivery_id"), AttemptID: value("attempt_id"), EventType: values("event_type"), TaskState: values("task_state"), Status: values("status"), RecordKind: values("record_kind"),
		Search: value("search"),
	}
	for name, target := range map[string]**time.Time{"start_time": &filter.StartTime, "end_time": &filter.EndTime} {
		if raw := value(name); raw != "" {
			parsed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return filter, logstore.PaginationOptions{}, fmt.Errorf("invalid %s: must be RFC3339", name)
			}
			*target = &parsed
		}
	}
	limit, offset, err := parseAgentHistoryPagination(ctx)
	// Only timestamp and latency are sortable; anything else falls back to
	// newest-first so an unknown param can never produce an unbounded scan.
	sortBy := "timestamp"
	if value("sort_by") == "latency" {
		sortBy = "latency"
	}
	order := "desc"
	if value("order") == "asc" {
		order = "asc"
	}
	return filter, logstore.PaginationOptions{
		Limit: limit, Offset: offset, SortBy: sortBy, Order: order,
		SelectedID: value("selected_id"),
	}, err
}

// parseAgentHistoryMultiValue reads a filter that may appear as repeated query
// params, as a single comma-separated value, or any mix of the two. Blanks are
// dropped and duplicates collapsed so the resulting IN clause stays minimal.
func parseAgentHistoryMultiValue(args *fasthttp.Args, name string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, raw := range args.PeekMulti(name) {
		for _, part := range strings.Split(string(raw), ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}

func parseAgentHistoryPagination(ctx *fasthttp.RequestCtx) (int, int, error) {
	limit, offset := agentHistoryDefaultLimit, 0
	if value := string(ctx.QueryArgs().Peek("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > logstore.AgentLogHistoryMaxLimit {
			return 0, 0, fmt.Errorf("invalid limit parameter: must be between 1 and %d", logstore.AgentLogHistoryMaxLimit)
		}
		limit = parsed
	}
	if value := string(ctx.QueryArgs().Peek("offset")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return 0, 0, fmt.Errorf("invalid offset parameter: must be a non-negative number")
		}
		offset = parsed
	}
	return limit, offset, nil
}

func sanitizeAgentHistoryPayload(payload *string) *string {
	if payload == nil || *payload == "" {
		return payload
	}
	var value any
	if err := json.Unmarshal([]byte(*payload), &value); err != nil {
		return payload
	}
	redactAgentHistorySecrets(value)
	data, err := json.Marshal(value)
	if err != nil {
		return payload
	}
	result := string(data)
	return &result
}

func redactAgentHistorySecrets(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if schemas.IsSensitiveHeader(normalized) || normalized == "password" || normalized == "client_secret" || normalized == "access_token" || normalized == "refresh_token" {
				typed[key] = schemas.RedactedAttrValue
				continue
			}
			redactAgentHistorySecrets(child)
		}
	case []any:
		for _, child := range typed {
			redactAgentHistorySecrets(child)
		}
	}
}

// reloadGrantedVirtualKeys refreshes the governance in-memory copy of every
// virtual key whose agent grants an agent mutation just changed, so the
// authorization decision observes the new rows on the very next protocol request
// without reading them from the database. Each key is reloaded at most once, and
// a failure is logged rather than failing the mutation, which is already durably
// committed. This mirrors what the MCP client APIs do after changing virtual-key
// assignments.
func (h *AgentGatewayHandler) reloadGrantedVirtualKeys(ctx context.Context, virtualKeyIDSets ...[]string) {
	if h.vkReloader == nil {
		return
	}
	reloaded := make(map[string]struct{})
	for _, virtualKeyIDs := range virtualKeyIDSets {
		for _, id := range virtualKeyIDs {
			if id == "" {
				continue
			}
			if _, done := reloaded[id]; done {
				continue
			}
			reloaded[id] = struct{}{}
			if _, err := h.vkReloader.ReloadVirtualKey(ctx, id); err != nil {
				logger.Error("failed to reload virtual key %s after agent grant change: %v", id, err)
			}
		}
	}
}

// managementError maps store errors to responses, reporting 404 only for a genuine
// missing record and otherwise a generic 500 so internal failure details are not
// leaked to API clients.
func (h *AgentGatewayHandler) managementError(ctx *fasthttp.RequestCtx, err error) {
	if errors.Is(err, configstore.ErrNotFound) || errors.Is(err, agent.ErrNotFound) {
		SendError(ctx, 404, "agent not found")
		return
	}
	SendError(ctx, 500, "agent operation failed")
}

// card serves the gateway agent card. It goes through the same authenticated path
// as protocol traffic so card content is governed rather than freely enumerable.
func (h *AgentGatewayHandler) card(ctx *fasthttp.RequestCtx) {
	h.serveProtocol(ctx, agentBindingCard)
}

// protocol serves A2A JSON-RPC calls for one agent.
func (h *AgentGatewayHandler) protocol(ctx *fasthttp.RequestCtx) {
	h.serveProtocol(ctx, agentBindingJSONRPC)
}

// rest serves A2A HTTP+JSON calls for one agent, through the identical security
// path as JSON-RPC.
func (h *AgentGatewayHandler) rest(ctx *fasthttp.RequestCtx) {
	h.serveProtocol(ctx, agentBindingREST)
}

// serveProtocol performs protocol dispatch after HTTP middleware has established
// authentication. Authorization remains in PreA2A after SDK decoding.
func (h *AgentGatewayHandler) serveProtocol(ctx *fasthttp.RequestCtx, binding agentBinding) {
	authentication, _ := ctx.UserValue(agentGatewayAuthenticationContextKey).(agentAuthenticationResult)
	name := pathName(ctx)
	var handler http.Handler
	var found bool
	switch binding {
	case agentBindingCard:
		handler, found = h.manager.CardHandler(name, lib.BuildBaseURL(ctx, h.config.GetA2AExternalClientURL()))
	case agentBindingREST:
		handler, found = h.manager.RESTHandler(name)
		if found {
			// The SDK's REST routes are declared relative to the binding's mount
			// point, so the explicit agent-scoped REST prefix is removed before dispatch.
			handler = http.StripPrefix("/agents/a2a/"+name+agent.GatewayRESTPathSuffix, handler)
		}
	default:
		handler, found = h.manager.ProtocolHandler(name)
		if found {
			// Keep the SDK JSON-RPC handler mounted at its transport root after
			// matching the explicit agent-scoped JSON-RPC suffix.
			handler = http.StripPrefix("/agents/a2a/"+name+agent.GatewayJSONRPCPathSuffix, handler)
		}
	}
	if !found {
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		return
	}
	if binding != agentBindingCard {
		// a2a.normalize is the protocol-version check plus the JSON-RPC params-key
		// normalization (an unmarshal/re-marshal of the request body), surfaced as
		// its own overhead bucket instead of hiding in the residual.
		t, h := startTransportSpan(ctx, "a2a.normalize")
		supported := supportedA2AVersion(string(ctx.Request.Header.Peek("A2A-Version")))
		if supported && binding == agentBindingJSONRPC {
			normalizeJSONRPCParamKeys(ctx)
		}
		if t != nil {
			t.EndSpan(h, schemas.SpanStatusOk, "")
		}
		if !supported {
			writeVersionNotSupported(ctx, binding)
			return
		}
	}
	downstreamTransport := binding.String()
	ctx.SetUserValue(schemas.BifrostContextKeyA2ADownstreamTransport, downstreamTransport)
	if bifrostCtx, ok := ctx.UserValue(agentGatewayBifrostContextKey).(*schemas.BifrostContext); ok {
		bifrostCtx.SetValue(schemas.BifrostContextKeyA2ADownstreamTransport, downstreamTransport)
	}
	isStreaming := streamingA2ARequest(ctx, binding)
	if isStreaming {
		ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
	}
	// Streaming handlers hold ServeHTTP open for the entire stream, so the
	// dispatch phase span (whose self-time would then be dominated by upstream
	// wait) is only opened for unary operations.
	serveNetHTTP(ctx, handler, authentication, !isStreaming, isStreaming)
	if binding == agentBindingCard {
		h.setCardCachingHeaders(ctx, name)
	}
}

// setCardCachingHeaders makes Agent Cards privately cacheable but requires clients
// to revalidate before reuse. Revalidation is necessary because authentication policy
// can change independently of the card body; the authentication middleware must see
// every request before a cached anonymous response can be reused.
func (h *AgentGatewayHandler) setCardCachingHeaders(ctx *fasthttp.RequestCtx, name string) {
	if ctx.Response.StatusCode() != fasthttp.StatusOK || ctx.Response.IsBodyStream() {
		return
	}
	ctx.Response.Header.Set(fasthttp.HeaderCacheControl, "private, no-cache")
	ctx.Response.Header.Set(fasthttp.HeaderVary, "Authorization, x-bf-vk, api-key, x-api-key, x-goog-api-key")
	sum := sha256.Sum256(ctx.Response.Body())
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	ctx.Response.Header.Set(fasthttp.HeaderETag, etag)
	if view, err := h.manager.Get(ctx, name); err == nil && !view.UpdatedAt.IsZero() {
		ctx.Response.Header.Set(fasthttp.HeaderLastModified, view.UpdatedAt.UTC().Format(http.TimeFormat))
	}
	if etagMatches(string(ctx.Request.Header.Peek(fasthttp.HeaderIfNoneMatch)), etag) {
		ctx.Response.ResetBody()
		ctx.Response.SetStatusCode(fasthttp.StatusNotModified)
	}
}

func etagMatches(ifNoneMatch, etag string) bool {
	for value := range strings.SplitSeq(ifNoneMatch, ",") {
		value = strings.TrimSpace(value)
		if value == "*" || value == etag || strings.TrimPrefix(value, "W/") == etag {
			return true
		}
	}
	return false
}

// normalizeJSONRPCParamKeys rewrites snake_case top-level params keys to their
// lowerCamelCase JSON names when the camelCase key is absent. A2A v1 mandates
// ProtoJSON serialization, whose parsers must accept both the proto field name
// and the JSON name; the a2a-go JSON-RPC server only decodes the camelCase
// form, so without this compensation spec-valid requests silently lose fields.
// Only top-level params keys are proto fields; nested user-controlled maps
// (such as metadata) are left untouched.
func normalizeJSONRPCParamKeys(ctx *fasthttp.RequestCtx) {
	var request map[string]json.RawMessage
	if json.Unmarshal(ctx.PostBody(), &request) != nil {
		return
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(request["params"], &params) != nil {
		return
	}
	changed := false
	for key, value := range params {
		if !strings.Contains(key, "_") {
			continue
		}
		camel := snakeToCamel(key)
		if _, exists := params[camel]; exists {
			continue
		}
		delete(params, key)
		params[camel] = value
		changed = true
	}
	if !changed {
		return
	}
	rewritten, err := json.Marshal(params)
	if err != nil {
		return
	}
	request["params"] = rewritten
	body, err := json.Marshal(request)
	if err != nil {
		return
	}
	ctx.Request.SetBody(body)
}

// snakeToCamel converts a proto field name to its ProtoJSON lowerCamelCase name.
func snakeToCamel(name string) string {
	parts := strings.Split(name, "_")
	var b strings.Builder
	for i, part := range parts {
		if i == 0 || part == "" {
			b.WriteString(part)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

// supportedA2AVersion accepts an absent or empty version (the SDK applies its
// own default) and any 1.x version, matching the gateway's strict-v1 contract.
func supportedA2AVersion(version string) bool {
	if version == "" {
		return true
	}
	major, _, _ := strings.Cut(version, ".")
	return major == "1"
}

// writeVersionNotSupported rejects a request whose declared A2A-Version the
// gateway does not speak, using the SDK's wire mapping for
// a2a.ErrVersionNotSupported: JSON-RPC code -32009, REST 400 FAILED_PRECONDITION.
func writeVersionNotSupported(ctx *fasthttp.RequestCtx, binding agentBinding) {
	const message = "this version is not supported"
	ctx.Response.Header.SetContentType("application/json")
	if binding == agentBindingJSONRPC {
		ctx.SetStatusCode(fasthttp.StatusOK)
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": jsonRPCRequestID(ctx.PostBody()), "error": map[string]any{"code": -32009, "message": message}})
		ctx.SetBody(body)
		return
	}
	ctx.SetStatusCode(fasthttp.StatusBadRequest)
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": fasthttp.StatusBadRequest, "status": "FAILED_PRECONDITION", "message": message}})
	ctx.SetBody(body)
}

// AgentGatewayAuthenticationMiddleware authenticates A2A protocol requests at
// the HTTP boundary. Enterprise middleware may first stamp a user onto generic
// fasthttp context; the injected generic resolver maps that principal to an
// effective governance scope without exposing enterprise types to OSS.
func AgentGatewayAuthenticationMiddleware(config *lib.Config, validator AgentGatewayVirtualKeyValidator, identityResolver AgentGatewayIdentityResolver) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// a2a.auth measures only the gateway's own auth work (VK lookup and
			// validation, identity resolution, grant settling); it ends before the
			// next handler runs so downstream time is excluded.
			spanTracer, spanHandle := startTransportSpan(ctx, "a2a.auth")
			authSpanEnded := false
			endAuthSpan := func() {
				if spanTracer != nil && !authSpanEnded {
					authSpanEnded = true
					spanTracer.EndSpan(spanHandle, schemas.SpanStatusOk, "")
				}
			}
			defer endAuthSpan()
			binding := agentBindingCard
			path := string(ctx.Path())
			if strings.Contains(path, agent.GatewayJSONRPCPathSuffix) {
				binding = agentBindingJSONRPC
			} else if strings.Contains(path, agent.GatewayRESTPathSuffix) {
				binding = agentBindingREST
			}

			config.Mu.RLock()
			enforceAuth := config.ClientConfig != nil && config.ClientConfig.EnforceAuthOnInference
			config.Mu.RUnlock()

			rawVirtualKey, source := lib.ResolveVirtualKeyFromHeaders(ctx)
			userID, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)

			// An explicitly selected credential always wins. It must be validated
			// before considering a pre-authenticated principal, and an invalid value
			// never falls back to that principal.
			if rawVirtualKey != "" {
				if validator == nil {
					writeProtocolDenial(ctx, binding)
					return
				}
				vk, ok := validator.GetVirtualKey(ctx, rawVirtualKey)
				if !ok || vk == nil || !vk.IsActiveValue() {
					writeProtocolDenial(ctx, binding)
					return
				}
				authentication := agentAuthenticationResult{credentialSource: source, rawVirtualKey: vk.Value.GetValue()}
				if header := source.HeaderName(); header != "" {
					authentication.acceptedCredential = &schemas.AcceptedBifrostCredential{
						Header: header,
						Value:  strings.TrimSpace(string(ctx.Request.Header.Peek(header))),
					}
				}
				removeBifrostIdentityHeader(ctx, authentication)
				settleAgentGatewayGrant(ctx, authentication)
				ctx.SetUserValue(agentGatewayAuthenticationContextKey, authentication)
				endAuthSpan()
				next(ctx)
				return
			}

			if userID != "" {
				authentication := agentAuthenticationResult{userID: userID}
				if identityResolver != nil {
					authorization := strings.TrimSpace(string(ctx.Request.Header.Peek(fasthttp.HeaderAuthorization)))
					if resolvedUserID, ok := identityResolver(ctx, authorization); ok && resolvedUserID == userID {
						authentication.acceptedCredential = &schemas.AcceptedBifrostCredential{Header: fasthttp.HeaderAuthorization, Value: authorization}
					}
				}
				removeBifrostIdentityHeader(ctx, authentication)
				settleAgentGatewayGrant(ctx, authentication)
				ctx.SetUserValue(agentGatewayAuthenticationContextKey, authentication)
				endAuthSpan()
				next(ctx)
				return
			}

			if enforceAuth {
				writeProtocolDenial(ctx, binding)
				return
			}
			authentication := agentAuthenticationResult{}
			settleAgentGatewayGrant(ctx, authentication)
			ctx.SetUserValue(agentGatewayAuthenticationContextKey, authentication)
			endAuthSpan()
			next(ctx)
		}
	}
}

func settleAgentGatewayGrant(ctx *fasthttp.RequestCtx, authentication agentAuthenticationResult) {
	bifrostCtx := bifrostIdentityContext(ctx, context.Background(), authentication)
	ctx.SetUserValue(agentGatewayBifrostContextKey, bifrostCtx)
}

// removeBifrostIdentityHeader prevents the gateway's own credential from crossing
// into the upstream trust domain, where it would be meaningless and a leak. It
// removes only the credential source the shared resolver actually selected, so
// all other headers remain available to the upstream agent.
//
// This is deliberately narrower than the core's sanitizeUpstreamHeaders, which
// always drops x-bf-vk but forwards Authorization/x-api-key/x-goog-api-key
// because those may legitimately be the caller's upstream credentials. Only the
// gateway knows which one of them was consumed as Bifrost identity.
func removeBifrostIdentityHeader(ctx *fasthttp.RequestCtx, authentication agentAuthenticationResult) {
	if header := authentication.credentialSource.HeaderName(); header != "" {
		ctx.Request.Header.Del(header)
		return
	}
	if authentication.acceptedCredential != nil {
		ctx.Request.Header.Del(authentication.acceptedCredential.Header)
	}
}

// writeProtocolDenial rejects an unauthenticated request with a 401, preserving
// normal HTTP behavior for the public card while making pre-SDK protocol
// denials native to the selected A2A binding.
func writeProtocolDenial(ctx *fasthttp.RequestCtx, binding agentBinding) {
	const message = "authentication required"
	ctx.Response.Header.Set("WWW-Authenticate", `ApiKey name="x-bf-vk", in="header"`)
	if binding == agentBindingCard {
		ctx.SetStatusCode(fasthttp.StatusUnauthorized)
		return
	}
	metadata := map[string]string{}
	if requestID := safeRequestID(ctx); requestID != "" {
		metadata["requestId"] = requestID
	}
	detail := map[string]any{
		"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
		"reason": "UNAUTHENTICATED",
		"domain": a2a.ProtocolDomain,
	}
	if len(metadata) > 0 {
		detail["metadata"] = metadata
	}
	ctx.Response.Header.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusUnauthorized)
	if binding == agentBindingJSONRPC {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": jsonRPCRequestID(ctx.PostBody()), "error": map[string]any{"code": -31401, "message": message, "data": []any{detail}}})
		ctx.SetBody(body)
		return
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": fasthttp.StatusUnauthorized, "status": "UNAUTHENTICATED", "message": message, "details": []any{detail}}})
	ctx.SetBody(body)
}

// streamingA2ARequest reports whether the inbound protocol request selects a
// streaming operation: the JSON-RPC streaming methods, or the REST custom verbs
// message:stream and tasks/{id}:subscribe.
func streamingA2ARequest(ctx *fasthttp.RequestCtx, binding agentBinding) bool {
	switch binding {
	case agentBindingJSONRPC:
		var request struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(ctx.PostBody(), &request) != nil {
			return false
		}
		return request.Method == "SendStreamingMessage" || request.Method == "SubscribeToTask"
	case agentBindingREST:
		path := string(ctx.Path())
		return strings.HasSuffix(path, ":stream") || strings.HasSuffix(path, ":subscribe")
	default:
		return false
	}
}

// dispatchSpan carries the a2a.dispatch span handle plus the span ID that was
// active when it opened, so endDispatchSpan can restore the prior parent. The
// zero value is a valid no-op (no tracer on the context).
type dispatchSpan struct {
	tracer schemas.Tracer
	h      schemas.SpanHandle
	ctx    *schemas.BifrostContext
	prev   any
}

// startDispatchSpan opens the a2a.dispatch overhead phase span and installs it
// as the active parent on the Bifrost context, mirroring the manager's phase
// spans so child spans subtract from its self-time in the overhead breakdown.
func startDispatchSpan(ctx *schemas.BifrostContext) dispatchSpan {
	if ctx == nil {
		return dispatchSpan{}
	}
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	if tracer == nil {
		return dispatchSpan{}
	}
	prev := ctx.Value(schemas.BifrostContextKeySpanID)
	id, h := tracer.StartSpanID(ctx, "a2a.dispatch", schemas.SpanKindInternal)
	if h == nil {
		return dispatchSpan{}
	}
	ctx.SetValue(schemas.BifrostContextKeySpanID, id)
	return dispatchSpan{tracer: tracer, h: h, ctx: ctx, prev: prev}
}

// endDispatchSpan restores the parent that was active before dispatch and
// closes the span. Zero-value safe.
func endDispatchSpan(ds dispatchSpan) {
	if ds.h == nil {
		return
	}
	ds.ctx.SetValue(schemas.BifrostContextKeySpanID, ds.prev)
	ds.tracer.EndSpan(ds.h, schemas.SpanStatusOk, "")
}

// jsonRPCRequestID extracts the request id from a JSON-RPC request body for
// echoing in error responses, defaulting to null when absent or invalid.
func jsonRPCRequestID(body []byte) json.RawMessage {
	var request struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(body, &request) == nil && validJSONRPCID(request.ID) {
		return request.ID
	}
	return json.RawMessage("null")
}

func validJSONRPCID(id json.RawMessage) bool {
	if len(id) == 0 || string(id) == "null" {
		return false
	}
	var value any
	if json.Unmarshal(id, &value) != nil {
		return false
	}
	switch value.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func safeRequestID(ctx *fasthttp.RequestCtx) string {
	if requestID, ok := ctx.UserValue(schemas.BifrostContextKeyRequestID).(string); ok {
		return requestID
	}
	return ""
}

// pathName extracts the agent name from the route.
func pathName(ctx *fasthttp.RequestCtx) string { v, _ := ctx.UserValue("name").(string); return v }

// serveNetHTTP bridges fasthttp to the SDK's net/http handlers and re-attaches the
// authenticated identity as a Bifrost context, so downstream governance and logging
// see the same principal even though the credential header was removed from the
// proxied request. Cancelling the context when the handler returns is what stops
// its cancellation watcher, so the goroutine does not outlive the request.
//
// The response writer is wrapped so a failed write also cancels the context. On
// this bridge the request context is the fasthttp RequestCtx, which — unlike a
// net/http server's request context — is not ended when the client disconnects,
// so a vanished SSE consumer would otherwise leave the upstream stream running
// forever. The failing write is the disconnect signal.
func serveNetHTTP(ctx *fasthttp.RequestCtx, handler http.Handler, authentication agentAuthenticationResult, spanDispatch, completeTrace bool) {
	traceCompleter, _ := ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func([]schemas.PluginLogEntry))
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		bifrostCtx, _ := ctx.UserValue(agentGatewayBifrostContextKey).(*schemas.BifrostContext)
		if bifrostCtx == nil {
			bifrostCtx = bifrostIdentityContext(ctx, req.Context(), authentication)
		}
		defer bifrostCtx.Cancel()
		if completeTrace && traceCompleter != nil {
			defer traceCompleter(nil)
		}
		if spanDispatch {
			// a2a.dispatch covers the SDK handler machinery (JSON-RPC/REST
			// decode, routing, response marshal and write). It is installed as
			// the active parent so the manager and plugin phase spans nest under
			// it and subtract from its self-time, leaving only the SDK slice.
			defer endDispatchSpan(startDispatchSpan(bifrostCtx))
		}
		handler.ServeHTTP(&disconnectAwareWriter{ResponseWriter: w, onWriteError: bifrostCtx.Cancel}, req.WithContext(bifrostCtx))
	})
	fasthttpadaptor.NewFastHTTPHandler(wrapped)(ctx)
}

// disconnectAwareWriter reports a downstream disconnect by invoking onWriteError
// the first time a body write fails. Flush is forwarded explicitly because the
// fasthttp bridge only switches from buffering the whole response to streaming it
// when the handler flushes; losing that method would turn every SSE stream into a
// single flush at the end.
type disconnectAwareWriter struct {
	http.ResponseWriter
	onWriteError func()
}

func (w *disconnectAwareWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		w.onWriteError()
	}
	return n, err
}

// WriteHeader flushes SSE headers through the fasthttp bridge immediately. The
// SDK's SSE writer sets headers without flushing, and the bridge only starts
// streaming on the first flush; a subscription whose first upstream event is not
// immediate would otherwise leave the downstream client waiting for response
// headers until it times out.
func (w *disconnectAwareWriter) WriteHeader(statusCode int) {
	w.ResponseWriter.WriteHeader(statusCode)
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.Flush()
	}
}

func (w *disconnectAwareWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the underlying writer to net/http's ResponseController, so any
// capability this wrapper does not forward explicitly remains reachable.
func (w *disconnectAwareWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// bifrostIdentityContext publishes the authenticated principal onto a Bifrost
// context in exactly the form plugins expect. It is shared by the admission check
// and the proxied request so the governance plugin decides against the same
// identity it later bills, and only the raw virtual key is published: the resolved
// VK row ID is owned by the governance plugin
// (BifrostContextKeyGovernanceVirtualKeyID is explicitly documented as never being
// set manually).
func bifrostIdentityContext(fastCtx *fasthttp.RequestCtx, parent context.Context, authentication agentAuthenticationResult) *schemas.BifrostContext {
	bifrostCtx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	fastCtx.VisitUserValuesAll(func(key, value any) {
		bifrostCtx.SetValue(key, value)
	})
	requestHeaders := make(map[string]string)
	fastCtx.Request.Header.All()(func(key, value []byte) bool {
		requestHeaders[strings.ToLower(string(key))] = string(value)
		return true
	})
	bifrostCtx.SetValue(schemas.BifrostContextKeyRequestHeaders, requestHeaders)
	if authentication.acceptedCredential != nil {
		bifrostCtx.SetValue(schemas.BifrostContextKeyAcceptedCredential, *authentication.acceptedCredential)
	}
	if authentication.rawVirtualKey != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, authentication.rawVirtualKey)
	}
	if authentication.userID != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyUserID, authentication.userID)
	}
	lib.SettleIdentity(bifrostCtx)
	return bifrostCtx
}
