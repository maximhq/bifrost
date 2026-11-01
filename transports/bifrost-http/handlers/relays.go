package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// RelaysHandler serves the Relays management API, the usage preview and the Relay Logs API. Every
// write goes through the relay manager, which stores the row first (so the vault and encryption
// hooks see the credential) and then updates the runtime, the way the Agent Gateway handler
// delegates registration writes to its manager.
type RelaysHandler struct {
	manager      *relay.Manager
	relayManager RelayManager
	config       *lib.Config
	// vkReloader refreshes the governance in-memory copy of every virtual key a relay delete
	// took a grant from, as the Agent Gateway handler does for agent grants. Optional.
	vkReloader VirtualKeyReloader
}

// RelayManager owns relay mutations and the local runtime updates they require. Deployments may
// wrap these with additional coordination (a cluster broadcast) without exposing that concern to
// the HTTP handler, as AgentRegistrationManager does for agents.
type RelayManager interface {
	CreateRelay(ctx context.Context, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error)
	UpdateRelay(ctx context.Context, name string, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error)
	// DeleteRelay returns the ids of the virtual keys whose grants the delete took away.
	DeleteRelay(ctx context.Context, name string) ([]string, error)
}

type localRelayManager struct {
	manager *relay.Manager
}

func (m localRelayManager) CreateRelay(ctx context.Context, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	return m.manager.Create(ctx, cfg)
}

func (m localRelayManager) UpdateRelay(ctx context.Context, name string, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	return m.manager.Update(ctx, name, cfg)
}

func (m localRelayManager) DeleteRelay(ctx context.Context, name string) ([]string, error) {
	return m.manager.Delete(ctx, name)
}

// NewRelaysHandler builds the handler. A nil relayManager mutates the manager directly.
func NewRelaysHandler(manager *relay.Manager, relayManager RelayManager, config *lib.Config, vkReloader VirtualKeyReloader) *RelaysHandler {
	if manager == nil {
		return nil
	}
	if relayManager == nil {
		relayManager = localRelayManager{manager: manager}
	}
	return &RelaysHandler{manager: manager, relayManager: relayManager, config: config, vkReloader: vkReloader}
}

// ReloadRelay brings the live relay named in line with its stored row. It is what another node
// calls when told the relay was created or updated here.
func (h *RelaysHandler) ReloadRelay(ctx context.Context, name string) error {
	_, err := h.manager.ReloadRelay(ctx, name)
	return err
}

// RemoveRelay takes the relay named out of service after its row was deleted on another node.
func (h *RelaysHandler) RemoveRelay(name string) {
	h.manager.RemoveRuntime(name)
}

// ReloadFromStore recompiles every relay from the store. It runs after the global proxy changes,
// since a relay without its own proxy resolved the inherited one when built.
func (h *RelaysHandler) ReloadFromStore(ctx context.Context) error {
	return h.manager.Reload(ctx)
}

// RegisterRoutes mounts the management and logs routes behind the dashboard middleware.
func (h *RelaysHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/relays", lib.ChainMiddlewares(h.list, middlewares...))
	r.POST("/api/relays", lib.ChainMiddlewares(h.create, middlewares...))
	r.POST("/api/relays/usage-preview", lib.ChainMiddlewares(h.preview, middlewares...))
	r.POST("/api/relays/openapi-spec", lib.ChainMiddlewares(h.fetchOpenAPISpec, middlewares...))
	r.GET("/api/relays/{name}", lib.ChainMiddlewares(h.get, middlewares...))
	r.PUT("/api/relays/{name}", lib.ChainMiddlewares(h.update, middlewares...))
	r.DELETE("/api/relays/{name}", lib.ChainMiddlewares(h.delete, middlewares...))

	r.GET("/api/relay-logs", lib.ChainMiddlewares(h.searchLogs, middlewares...))
	r.DELETE("/api/relay-logs", lib.ChainMiddlewares(h.deleteLogs, middlewares...))
	r.GET("/api/relay-logs/stats", lib.ChainMiddlewares(h.logStats, middlewares...))
	r.GET("/api/relay-logs/filterdata", lib.ChainMiddlewares(h.logFilterData, middlewares...))
	r.GET("/api/relay-logs/histogram", lib.ChainMiddlewares(h.logHistogram, middlewares...))
	r.GET("/api/relay-logs/top-endpoints", lib.ChainMiddlewares(h.topEndpoints, middlewares...))
	r.GET("/api/relay-logs/{id}", lib.ChainMiddlewares(h.getLog, middlewares...))
}

// relayView is what the API returns for an endpoint: the config with every secret
// redacted.
type relayView struct {
	*schemas.RelayConfig
	URL string `json:"url"` // the path callers use, /relay/{name}
}

func endpointView(cfg *schemas.RelayConfig) relayView {
	return relayView{RelayConfig: cfg.Redacted(), URL: "/relay/" + cfg.Name}
}

func (h *RelaysHandler) list(ctx *fasthttp.RequestCtx) {
	workCtx, cancel := RequestWorkContext(ctx, RequestWorkTimeout)
	defer cancel()
	// search matches the name or description case-insensitively; limit and offset page the matches,
	// so a picker never fetches the whole list to filter it. Without limit every match is returned.
	args := ctx.QueryArgs()
	search := strings.ToLower(strings.TrimSpace(string(args.Peek("search"))))
	limit, offset := 0, 0
	if raw := string(args.Peek("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > relaysMaxPageSize {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid limit: must be between 1 and %d", relaysMaxPageSize))
			return
		}
		limit = n
	}
	if raw := string(args.Peek("offset")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "invalid offset: must be zero or more")
			return
		}
		offset = n
	}
	cfgs, err := h.manager.List(workCtx)
	if err != nil {
		h.storeError(ctx, err)
		return
	}
	matches := make([]relayView, 0, len(cfgs))
	for _, cfg := range cfgs {
		if search != "" && !strings.Contains(strings.ToLower(cfg.Name), search) && !strings.Contains(strings.ToLower(cfg.Description), search) {
			continue
		}
		matches = append(matches, endpointView(cfg))
	}
	page := matches[min(offset, len(matches)):]
	if limit > 0 && len(page) > limit {
		page = page[:limit]
	}
	SendJSON(ctx, map[string]any{"relays": page, "count": len(page), "total_count": len(matches)})
}

// relaysMaxPageSize bounds one page of the relay list.
const relaysMaxPageSize = 1000

func (h *RelaysHandler) get(ctx *fasthttp.RequestCtx) {
	workCtx, cancel := RequestWorkContext(ctx, RequestWorkTimeout)
	defer cancel()
	cfg, err := h.manager.Get(workCtx, pathName(ctx))
	if err != nil {
		h.storeError(ctx, err)
		return
	}
	SendJSON(ctx, endpointView(cfg))
}

func (h *RelaysHandler) create(ctx *fasthttp.RequestCtx) {
	workCtx, cancel := RequestWorkContext(ctx, RequestWorkTimeout)
	defer cancel()
	var cfg schemas.RelayConfig
	if err := json.Unmarshal(ctx.PostBody(), &cfg); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid relay: "+err.Error())
		return
	}
	cfg.Name = strings.TrimSpace(cfg.Name)
	if requireGenuineAuthForRelayChange(ctx, nil, &cfg) {
		return
	}
	stored, err := h.relayManager.CreateRelay(workCtx, &cfg)
	if err != nil {
		h.storeError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "relay.created", "relay", cfg.Name, cfg.Name, nil, endpointView(stored))
	SendJSONWithStatus(ctx, endpointView(stored), fasthttp.StatusCreated)
}

func (h *RelaysHandler) update(ctx *fasthttp.RequestCtx) {
	workCtx, cancel := RequestWorkContext(ctx, RequestWorkTimeout)
	defer cancel()
	name := pathName(ctx)
	previous, err := h.manager.Get(workCtx, name)
	if err != nil {
		h.storeError(ctx, err)
		return
	}
	var cfg schemas.RelayConfig
	if err := json.Unmarshal(ctx.PostBody(), &cfg); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid relay: "+err.Error())
		return
	}
	// Compared after the merge the manager applies, so a redacted secret sent back unchanged
	// stands for the stored value it keeps and is not mistaken for a new one.
	cfg.MergeRedacted(previous)
	if requireGenuineAuthForRelayChange(ctx, previous, &cfg) {
		return
	}
	updated, err := h.relayManager.UpdateRelay(workCtx, name, &cfg)
	if err != nil {
		h.storeError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "relay.updated", "relay", name, name, endpointView(previous), endpointView(updated))
	SendJSON(ctx, endpointView(updated))
}

func (h *RelaysHandler) delete(ctx *fasthttp.RequestCtx) {
	workCtx, cancel := RequestWorkContext(ctx, RequestWorkTimeout)
	defer cancel()
	name := pathName(ctx)
	previous, err := h.manager.Get(workCtx, name)
	if err != nil {
		h.storeError(ctx, err)
		return
	}
	// The keys whose cascade-deleted grants just disappeared stop carrying them in memory.
	granted, err := h.relayManager.DeleteRelay(workCtx, name)
	if err != nil {
		h.storeError(ctx, err)
		return
	}
	h.reloadGrantedVirtualKeys(workCtx, granted)
	lib.RecordAuditChange(ctx, "relay.deleted", "relay", name, name, endpointView(previous), nil)
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

// reloadGrantedVirtualKeys refreshes the in-memory copy of each key whose grant a delete took
// away, as the Agent Gateway handler does after an agent grant change. A failed reload is
// logged; the key is refreshed on its next reload.
func (h *RelaysHandler) reloadGrantedVirtualKeys(ctx context.Context, ids []string) {
	if h.vkReloader == nil {
		return
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, err := h.vkReloader.ReloadVirtualKey(ctx, id); err != nil {
			logger.Error("failed to reload virtual key %s after relay delete: %v", id, err)
		}
	}
}

func (h *RelaysHandler) storeError(ctx *fasthttp.RequestCtx, err error) {
	switch {
	case errors.Is(err, configstore.ErrNotFound):
		SendError(ctx, fasthttp.StatusNotFound, "relay not found")
	case errors.Is(err, configstore.ErrRelayExists):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
	case errors.Is(err, relay.ErrInvalidConfig):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
	case errors.Is(err, relay.ErrStoreDisabled):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
	default:
		logger.Error("relay store error: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to save relay")
	}
}

// relayDialChanges returns the sorted JSON paths of the settings next adds or changes relative to
// old (nil on create) that decide where the relay's credential goes or which gateway secret it
// sends: the base URL, the TLS trust and proxy the upstream is reached through, and every env. or
// vault. reference among its secrets. A reference resolves from the gateway's own environment or
// vault and is sent upstream on every call, so naming a new one discloses it as surely as a new
// base URL does. Narrowing changes (clearing a value, turning insecure_skip_verify off) pass. Equality
// is SecretVar.Equals, so swapping one reference for another counts even when the values match.
func relayDialChanges(old, next *schemas.RelayConfig) []string {
	if old == nil {
		old = &schemas.RelayConfig{}
	}
	var changed []string
	if next.BaseURL != "" && next.BaseURL != old.BaseURL {
		changed = append(changed, "base_url")
	}
	var oldNetwork, nextNetwork schemas.RelayNetworkConfig
	if old.Network != nil {
		oldNetwork = *old.Network
	}
	if next.Network != nil {
		nextNetwork = *next.Network
	}
	if nextNetwork.InsecureSkipVerify && !oldNetwork.InsecureSkipVerify {
		changed = append(changed, "network.insecure_skip_verify")
	}
	if nextNetwork.CACertPEM != "" && nextNetwork.CACertPEM != oldNetwork.CACertPEM {
		changed = append(changed, "network.ca_cert_pem")
	}
	var oldProxy, nextProxy schemas.ProxyConfig
	if old.ProxyConfig != nil {
		oldProxy = *old.ProxyConfig
	}
	if next.ProxyConfig != nil {
		nextProxy = *next.ProxyConfig
	}
	widened := func(oldV, nextV *schemas.SecretVar) bool {
		return nextV.IsSet() && !nextV.Equals(oldV)
	}
	if widened(oldProxy.URL, nextProxy.URL) {
		changed = append(changed, "proxy_config.url")
	}
	if widened(oldProxy.CACertPEM, nextProxy.CACertPEM) {
		changed = append(changed, "proxy_config.ca_cert_pem")
	}
	reference := func(path string, oldV, nextV *schemas.SecretVar) {
		if nextV.IsFromSecret() && !nextV.Equals(oldV) {
			changed = append(changed, path)
		}
	}
	reference("proxy_config.username", oldProxy.Username, nextProxy.Username)
	reference("proxy_config.password", oldProxy.Password, nextProxy.Password)
	var oldCred, nextCred schemas.RelayCredential
	if old.Credential != nil {
		oldCred = *old.Credential
	}
	if next.Credential != nil {
		nextCred = *next.Credential
	}
	reference("credential.value", oldCred.Value, nextCred.Value)
	var oldAWS, nextAWS schemas.RelayAWSCredential
	if oldCred.AWS != nil {
		oldAWS = *oldCred.AWS
	}
	if nextCred.AWS != nil {
		nextAWS = *nextCred.AWS
	}
	reference("credential.aws.access_key", oldAWS.AccessKey, nextAWS.AccessKey)
	reference("credential.aws.secret_key", oldAWS.SecretKey, nextAWS.SecretKey)
	reference("credential.aws.session_token", oldAWS.SessionToken, nextAWS.SessionToken)
	reference("credential.aws.region", oldAWS.Region, nextAWS.Region)
	reference("credential.aws.role_arn", oldAWS.RoleARN, nextAWS.RoleARN)
	reference("credential.aws.external_id", oldAWS.ExternalID, nextAWS.ExternalID)
	reference("credential.aws.session_name", oldAWS.SessionName, nextAWS.SessionName)
	for name, value := range next.ExtraHeaders {
		var oldValue *schemas.SecretVar
		if stored, ok := old.ExtraHeaders[name]; ok {
			oldValue = &stored
		}
		reference("extra_headers."+name, oldValue, &value)
	}
	slices.Sort(changed)
	return changed
}

// requireGenuineAuthForRelayChange rejects a relay create/update under the fail-open bypass
// (dashboard auth disabled or unconfigured) that would let the caller choose where the relay's
// credential is sent or which gateway secret it sends (see relayDialChanges): repointing base_url
// while sending back the redacted credential would hand the stored secret to the new host. It is
// the relay counterpart of the provider, provider-key, plugin and webhook gates. old is the stored
// relay on update, merged with the incoming config's redacted secrets, and nil on create; an update
// that keeps every such setting passes, so bypassed callers can still edit routes and pricing. On
// rejection this sends the response and returns true; callers should return immediately.
func requireGenuineAuthForRelayChange(ctx *fasthttp.RequestCtx, old, next *schemas.RelayConfig) bool {
	if !isAuthBypassed(ctx) {
		return false
	}
	changed := relayDialChanges(old, next)
	if len(changed) == 0 {
		return false
	}
	SendError(ctx, fasthttp.StatusForbidden, fmt.Sprintf("Changing where a relay sends its credential or which secrets it references (%s) requires an authenticated admin session; dashboard auth is currently disabled or unconfigured. Enable dashboard authentication first.", strings.Join(changed, ", ")))
	return true
}

// usagePreviewRequest checks a mapping against a sample call. Config is an unsaved endpoint; Name
// previews a saved one instead.
type usagePreviewRequest struct {
	Name   string                  `json:"name,omitempty"`
	Config *schemas.RelayConfig    `json:"config,omitempty"`
	Sample relay.UsagePreviewInput `json:"sample"`
}

func (h *RelaysHandler) preview(ctx *fasthttp.RequestCtx) {
	workCtx, cancel := RequestWorkContext(ctx, RequestWorkTimeout)
	defer cancel()
	var req usagePreviewRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid preview request: "+err.Error())
		return
	}
	cfg := req.Config
	if cfg == nil {
		if req.Name == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "config or the name of a saved endpoint is required")
			return
		}
		stored, err := h.manager.Get(workCtx, req.Name)
		if err != nil {
			h.storeError(ctx, err)
			return
		}
		cfg = stored
	}
	out, err := relay.PreviewUsage(cfg, req.Sample)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	SendJSON(ctx, out)
}

// relayOpenAPISpecRequest names the OpenAPI document to fetch for a route import.
type relayOpenAPISpecRequest struct {
	SpecURL string `json:"spec_url"`
}

// relayOpenAPISpecResponse is the fetched document and the URL it was read from after redirects.
type relayOpenAPISpecResponse struct {
	Spec    string `json:"spec"`
	SpecURL string `json:"spec_url"`
}

// fetchOpenAPISpec handles POST /api/relays/openapi-spec. The route import reads the document in
// the browser, which cannot fetch most spec URLs itself (CORS), so the gateway fetches it with the
// same timeout, size cap and private-network guard as the MCP OpenAPI preview and returns the text
// unparsed. Nothing is stored.
func (h *RelaysHandler) fetchOpenAPISpec(ctx *fasthttp.RequestCtx) {
	var req relayOpenAPISpecRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	specURL := strings.TrimSpace(req.SpecURL)
	if parsed, err := url.Parse(specURL); specURL == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "spec_url must be an absolute http(s) URL")
		return
	}
	data, finalURL, status, err := fetchOpenAPISpecForCaller(ctx, specURL)
	if err != nil {
		SendError(ctx, status, err.Error())
		return
	}
	SendJSON(ctx, relayOpenAPISpecResponse{Spec: string(data), SpecURL: finalURL})
}

// --- Relay Logs ---

func (h *RelaysHandler) logsStore(ctx *fasthttp.RequestCtx) logstore.LogStore {
	if h.config == nil || h.config.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "logs store is not available")
		return nil
	}
	return h.config.LogsStore
}

const relayLogsMaxLimit = 1000

func parseRelayLogFilters(ctx *fasthttp.RequestCtx) (logstore.RelayLogFilters, error) {
	args := ctx.QueryArgs()
	value := func(name string) string { return strings.TrimSpace(string(args.Peek(name))) }
	values := func(name string) []string { return parseAgentHistoryMultiValue(args, name) }
	f := logstore.RelayLogFilters{
		EndpointNames: values("relay_names"), Methods: values("methods"), Statuses: values("statuses"), Kinds: values("kinds"),
		VirtualKeyIDs: values("virtual_key_ids"), UserIDs: values("user_ids"), TeamIDs: values("team_ids"),
		CustomerIDs: values("customer_ids"), BusinessUnitIDs: values("business_unit_ids"), ProjectIDs: values("project_ids"),
		Apps: values("apps"), RequestID: value("request_id"), JobID: value("job_id"), Search: value("search"),
	}
	if raw := value("billable"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return f, fmt.Errorf("invalid billable: must be true or false")
		}
		f.Billable = &b
	}
	for name, target := range map[string]**time.Time{"start_time": &f.StartTime, "end_time": &f.EndTime} {
		if raw := value(name); raw != "" {
			parsed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return f, fmt.Errorf("invalid %s: must be RFC3339", name)
			}
			*target = &parsed
		}
	}
	for name, target := range map[string]**float64{"min_cost": &f.MinCost, "max_cost": &f.MaxCost} {
		if raw := value(name); raw != "" {
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return f, fmt.Errorf("invalid %s", name)
			}
			*target = &parsed
		}
	}
	return f, nil
}

func (h *RelaysHandler) searchLogs(ctx *fasthttp.RequestCtx) {
	store := h.logsStore(ctx)
	if store == nil {
		return
	}
	filters, err := parseRelayLogFilters(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	pagination := logstore.PaginationOptions{Limit: 50, SortBy: string(ctx.QueryArgs().Peek("sort_by")), Order: string(ctx.QueryArgs().Peek("order"))}
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > relayLogsMaxLimit {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid limit: must be between 1 and %d", relayLogsMaxLimit))
			return
		}
		pagination.Limit = limit
	}
	if raw := string(ctx.QueryArgs().Peek("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "invalid offset: must be a non-negative number")
			return
		}
		pagination.Offset = offset
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	result, err := store.SearchRelayLogs(logCtx, filters, pagination)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to search relay logs")
		return
	}
	SendJSON(ctx, result)
}

func (h *RelaysHandler) getLog(ctx *fasthttp.RequestCtx) {
	store := h.logsStore(ctx)
	if store == nil {
		return
	}
	id, _ := ctx.UserValue("id").(string)
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	entry, err := store.FindRelayLog(logCtx, id)
	if err != nil {
		if errors.Is(err, logstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "relay log not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to read relay log")
		return
	}
	SendJSON(ctx, entry)
}

func (h *RelaysHandler) deleteLogs(ctx *fasthttp.RequestCtx) {
	store := h.logsStore(ctx)
	if store == nil {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil || len(req.IDs) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "ids are required")
		return
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	if err := store.DeleteRelayLogs(logCtx, req.IDs); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to delete relay logs")
		return
	}
	SendJSON(ctx, map[string]any{"deleted": len(req.IDs)})
}

func (h *RelaysHandler) logStats(ctx *fasthttp.RequestCtx) {
	store := h.logsStore(ctx)
	if store == nil {
		return
	}
	filters, err := parseRelayLogFilters(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	stats, err := store.GetRelayLogStats(logCtx, filters)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to get relay log stats")
		return
	}
	response := &relayLogStatsResponse{RelayLogStats: *stats}
	// compare_to_previous adds the same stats for the preceding window of equal length, as
	// /api/logs/stats does, so the metric strip can show the change against it. A window with
	// no start and end has nothing to compare against, and a failed comparison degrades to none.
	if compare := string(ctx.QueryArgs().Peek("compare_to_previous")); compare != "" {
		if enabled, parseErr := strconv.ParseBool(compare); parseErr == nil && enabled && filters.StartTime != nil && filters.EndTime != nil && filters.EndTime.After(*filters.StartTime) {
			previous, prevErr := store.GetRelayLogStats(logCtx, previousRelayPeriodFilters(filters))
			switch {
			case prevErr != nil:
				logger.Warn("failed to get previous period relay log stats: %v", prevErr)
			case previous != nil:
				response.Previous = previous
				response.HasPreviousPeriod = true
			}
		}
	}
	SendJSON(ctx, response)
}

// relayLogStatsResponse is the GET /api/relay-logs/stats payload: the current period's stats at
// the top level, with the preceding period's beside them when compare_to_previous was asked for.
type relayLogStatsResponse struct {
	logstore.RelayLogStats
	Previous          *logstore.RelayLogStats `json:"previous,omitempty"`
	HasPreviousPeriod bool                    `json:"has_previous_period"`
}

// previousRelayPeriodFilters is the same filter over the window of equal length that ends where
// this one starts.
func previousRelayPeriodFilters(filters logstore.RelayLogFilters) logstore.RelayLogFilters {
	duration := filters.EndTime.Sub(*filters.StartTime)
	prevStart := filters.StartTime.Add(-duration)
	prevEnd := filters.StartTime.Add(-time.Nanosecond)
	filters.StartTime, filters.EndTime = &prevStart, &prevEnd
	return filters
}

func (h *RelaysHandler) logHistogram(ctx *fasthttp.RequestCtx) {
	store := h.logsStore(ctx)
	if store == nil {
		return
	}
	filters, err := parseRelayLogFilters(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	bucket := int64(3600)
	if raw := string(ctx.QueryArgs().Peek("bucket_size_seconds")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "invalid bucket_size_seconds")
			return
		}
		bucket = parsed
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	result, err := store.GetRelayHistogram(logCtx, filters, bucket)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to get relay log histogram")
		return
	}
	SendJSON(ctx, result)
}

func (h *RelaysHandler) topEndpoints(ctx *fasthttp.RequestCtx) {
	store := h.logsStore(ctx)
	if store == nil {
		return
	}
	filters, err := parseRelayLogFilters(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	limit := 10
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > relayLogsMaxLimit {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid limit: must be between 1 and %d", relayLogsMaxLimit))
			return
		}
		limit = parsed
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	result, err := store.GetRelayTopEndpoints(logCtx, filters, limit)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to rank relays")
		return
	}
	SendJSON(ctx, result)
}

func (h *RelaysHandler) logFilterData(ctx *fasthttp.RequestCtx) {
	store := h.logsStore(ctx)
	if store == nil {
		return
	}
	dimensions := strings.Split(strings.TrimSpace(string(ctx.QueryArgs().Peek("dimensions"))), ",")
	if len(dimensions) == 1 && dimensions[0] == "" {
		dimensions = nil
	}
	logCtx := agentGatewayLogContext(ctx)
	defer logCtx.Cancel()
	result, err := store.GetRelayFilterData(logCtx, dimensions, 100, strings.TrimSpace(string(ctx.QueryArgs().Peek("q"))))
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to load relay log filters")
		return
	}
	SendJSON(ctx, result)
}
