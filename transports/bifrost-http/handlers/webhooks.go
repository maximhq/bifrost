// Package handlers - webhooks.go implements the admin API for webhook
// endpoints: registration, secret lifecycle, test deliveries, delivery
// history, and redelivery. Every mutation writes the database first and then
// refreshes the in-memory endpoint store, which serves the submit path and the
// delivery worker.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/webhooks"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// WebhookEndpointManager refreshes the in-memory webhook endpoint store from
// the database after a single-endpoint mutation. The base server implementation
// updates local memory; a clustered deployment overrides it to also notify
// peers so their in-memory copies (read by the submit path and the delivery
// worker) stay current.
type WebhookEndpointManager interface {
	ReloadWebhookEndpoint(ctx context.Context, id string) error
	RemoveWebhookEndpoint(ctx context.Context, id string) error
}

// WebhookHandler manages webhook endpoint configuration and delivery history.
type WebhookHandler struct {
	manager    WebhookEndpointManager
	store      *lib.Config
	dispatcher *webhooks.Dispatcher
}

// NewWebhookHandler creates a new webhook admin handler. The manager refreshes
// (and, in a cluster, propagates) the in-memory endpoint store after each
// mutation. The dispatcher may be nil when delivery is not configured; test
// deliveries are then refused.
func NewWebhookHandler(manager WebhookEndpointManager, store *lib.Config, dispatcher *webhooks.Dispatcher) *WebhookHandler {
	return &WebhookHandler{
		manager:    manager,
		store:      store,
		dispatcher: dispatcher,
	}
}

// RegisterRoutes registers the webhook admin routes.
func (h *WebhookHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/webhooks", lib.ChainMiddlewares(h.listWebhookEndpoints, middlewares...))
	r.POST("/api/webhooks", lib.ChainMiddlewares(h.createWebhookEndpoint, middlewares...))
	r.GET("/api/webhooks/{id}", lib.ChainMiddlewares(h.getWebhookEndpoint, middlewares...))
	r.PUT("/api/webhooks/{id}", lib.ChainMiddlewares(h.updateWebhookEndpoint, middlewares...))
	r.DELETE("/api/webhooks/{id}", lib.ChainMiddlewares(h.deleteWebhookEndpoint, middlewares...))
	r.POST("/api/webhooks/{id}/rotate-secret", lib.ChainMiddlewares(h.rotateWebhookEndpointSecret, middlewares...))
	r.POST("/api/webhooks/{id}/test", lib.ChainMiddlewares(h.testWebhookEndpoint, middlewares...))
	r.GET("/api/webhooks/{id}/deliveries", lib.ChainMiddlewares(h.listWebhookDeliveries, middlewares...))
	r.POST("/api/webhooks/deliveries/{id}/redeliver", lib.ChainMiddlewares(h.redeliverWebhook, middlewares...))
}

// webhookEndpointRequest is the caller-editable shape for create and update.
// Signing secrets are always server-generated and rotated through the
// dedicated endpoint, so no secret field is accepted here. The tuning knobs
// are optional; zero means "use the delivery worker's default".
type webhookEndpointRequest struct {
	Name                string                           `json:"name"`
	URL                 string                           `json:"url"`
	Events              []configstoreTables.WebhookEvent `json:"events"`
	Headers             map[string]schemas.SecretVar     `json:"headers"`
	IncludeResponse     bool                             `json:"include_response"`
	AllowPrivateNetwork bool                             `json:"allow_private_network"`
	Disabled            bool                             `json:"disabled"`

	MaxRetries                 int `json:"max_retries"`
	RetryBackoffInitialSeconds int `json:"retry_backoff_initial_seconds"`
	RetryBackoffMaxSeconds     int `json:"retry_backoff_max_seconds"`
	AttemptTimeoutSeconds      int `json:"attempt_timeout_seconds"`
	MaxResponsePayloadKBs      int `json:"max_response_payload_kbs"`
	MaxConcurrentDeliveries    int `json:"max_concurrent_deliveries"`
}

func (r *webhookEndpointRequest) toTable(id string) *configstoreTables.TableWebhookEndpoint {
	return &configstoreTables.TableWebhookEndpoint{
		ID:                         id,
		Name:                       r.Name,
		URL:                        r.URL,
		Events:                     r.Events,
		Headers:                    r.Headers,
		IncludeResponse:            r.IncludeResponse,
		AllowPrivateNetwork:        r.AllowPrivateNetwork,
		Disabled:                   r.Disabled,
		MaxRetries:                 r.MaxRetries,
		RetryBackoffInitialSeconds: r.RetryBackoffInitialSeconds,
		RetryBackoffMaxSeconds:     r.RetryBackoffMaxSeconds,
		AttemptTimeoutSeconds:      r.AttemptTimeoutSeconds,
		MaxResponsePayloadKBs:      r.MaxResponsePayloadKBs,
		MaxConcurrentDeliveries:    r.MaxConcurrentDeliveries,
	}
}

// redactedWebhookEndpoint returns a copy safe for API responses: custom
// header values are replaced with placeholders, preserving env references
// (the signing secret is already excluded from JSON).
func redactedWebhookEndpoint(endpoint *configstoreTables.TableWebhookEndpoint) *configstoreTables.TableWebhookEndpoint {
	if endpoint == nil || len(endpoint.Headers) == 0 {
		return endpoint
	}
	copied := *endpoint
	copied.Headers = make(map[string]schemas.SecretVar, len(endpoint.Headers))
	for name, value := range endpoint.Headers {
		copied.Headers[name] = *value.FullyRedacted()
	}
	return &copied
}

// storeAvailable guards every route against a disabled config store.
func (h *WebhookHandler) storeAvailable(ctx *fasthttp.RequestCtx) bool {
	if h.store == nil || h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Config store is not available")
		return false
	}
	return true
}

// listWebhookEndpoints returns one page of endpoints matching the query
// filters. Reads the store, not memory: list views include the operational
// failure counters, which are only tracked in the database.
func (h *WebhookHandler) listWebhookEndpoints(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	args := ctx.QueryArgs()
	params := configstore.WebhookEndpointsQueryParams{
		Search: strings.TrimSpace(string(args.Peek("search"))),
		Events: parseCommaSeparated(string(args.Peek("event"))),
	}
	for _, event := range params.Events {
		if !configstoreTables.WebhookEvent(event).IsValid() {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Unknown webhook event %q", event))
			return
		}
	}
	if disabledStr := string(args.Peek("disabled")); disabledStr != "" {
		disabled, err := strconv.ParseBool(disabledStr)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid disabled parameter: must be true or false")
			return
		}
		params.Disabled = &disabled
	}
	if limitStr := string(args.Peek("limit")); limitStr != "" {
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid limit parameter: must be a non-negative number")
			return
		}
		params.Limit = limit
	}
	if offsetStr := string(args.Peek("offset")); offsetStr != "" {
		offset, err := strconv.Atoi(offsetStr)
		if err != nil || offset < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid offset parameter: must be a non-negative number")
			return
		}
		params.Offset = offset
	}
	params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)

	endpoints, totalCount, err := h.store.ConfigStore.GetWebhookEndpointsPaginated(ctx, params)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to list webhook endpoints: %v", err))
		return
	}
	redacted := make([]*configstoreTables.TableWebhookEndpoint, 0, len(endpoints))
	for i := range endpoints {
		redacted = append(redacted, redactedWebhookEndpoint(&endpoints[i]))
	}
	SendJSON(ctx, map[string]any{
		"endpoints":   redacted,
		"count":       len(redacted),
		"total_count": totalCount,
		"limit":       params.Limit,
		"offset":      params.Offset,
	})
}

// getWebhookEndpoint returns one endpoint; the signing secret is never
// included in responses.
func (h *WebhookHandler) getWebhookEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	endpoint, err := h.store.ConfigStore.GetWebhookEndpointByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Webhook endpoint not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get webhook endpoint: %v", err))
		return
	}
	SendJSON(ctx, redactedWebhookEndpoint(endpoint))
}

// createWebhookEndpoint registers a new endpoint. The generated signing
// secret is returned exactly once in this response and never again.
func (h *WebhookHandler) createWebhookEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	var req webhookEndpointRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	endpoint := req.toTable(uuid.NewString())
	if err := endpoint.Validate(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.ConfigStore.CreateWebhookEndpoint(ctx, endpoint); err != nil {
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, "A webhook endpoint with this name already exists")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create webhook endpoint: %v", err))
		return
	}
	// Refresh memory so the delivery worker can serve this endpoint.
	if err := h.manager.ReloadWebhookEndpoint(ctx, endpoint.ID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reload webhook endpoint in memory: %v, please restart bifrost to sync with the database", err))
		return
	}
	// The store left the plaintext secret on the struct after commit, so this
	// response can show it exactly once.
	SendJSONWithStatus(ctx, map[string]any{
		"endpoint": redactedWebhookEndpoint(endpoint),
		"secret":   endpoint.Secret.GetValue(),
	}, fasthttp.StatusCreated)
}

// updateWebhookEndpoint replaces an endpoint's caller-editable fields. The
// full desired state must be sent — omitted fields are cleared, and an empty
// name or URL is rejected by validation.
func (h *WebhookHandler) updateWebhookEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	var req webhookEndpointRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	// Masked header values round-tripped from a read are placeholders, not
	// real values — restore the stored value so an edit that touches other
	// fields cannot corrupt the headers.
	if len(req.Headers) > 0 {
		if existing, ok := h.store.WebhookEndpointByID(id); ok {
			for name, value := range req.Headers {
				if value.IsMaskedPlaceholder() {
					if stored, found := existing.Headers[name]; found {
						req.Headers[name] = stored
					}
				}
			}
		}
	}
	endpoint := req.toTable(id)
	if err := endpoint.Validate(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.ConfigStore.UpdateWebhookEndpoint(ctx, endpoint); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Webhook endpoint not found")
			return
		}
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, "A webhook endpoint with this name already exists")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update webhook endpoint: %v", err))
		return
	}
	// Reconcile memory from the canonical row in a single post-commit read, then
	// serve the response from that in-memory row — so a successful update never
	// leaves workers on stale config behind a second read that could fail.
	if err := h.manager.ReloadWebhookEndpoint(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reload webhook endpoint in memory: %v, please restart bifrost to sync with the database", err))
		return
	}
	updated, ok := h.store.WebhookEndpointByID(id)
	if !ok {
		SendError(ctx, fasthttp.StatusInternalServerError, "Webhook endpoint missing from memory after reload, please restart bifrost to sync with the database")
		return
	}
	SendJSON(ctx, redactedWebhookEndpoint(updated))
}

// deleteWebhookEndpoint removes an endpoint. In-flight deliveries referencing
// it are retired by the delivery worker on their next attempt.
func (h *WebhookHandler) deleteWebhookEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.ConfigStore.DeleteWebhookEndpoint(ctx, id); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Webhook endpoint not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to delete webhook endpoint: %v", err))
		return
	}
	// Remove from in-memory store via manager callback (non-fatal: DB already updated).
	if err := h.manager.RemoveWebhookEndpoint(ctx, id); err != nil {
		logger.Error("failed to remove webhook endpoint from memory: %v", err)
	}
	SendJSON(ctx, map[string]any{"status": "success"})
}

// rotateWebhookEndpointSecret swaps in a freshly generated signing secret,
// effective immediately, and returns it exactly once.
func (h *WebhookHandler) rotateWebhookEndpointSecret(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	rotated, err := h.store.ConfigStore.RotateWebhookEndpointSecret(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Webhook endpoint not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to rotate webhook secret: %v", err))
		return
	}
	// Memory must sign with the new secret from this moment on.
	if err := h.manager.ReloadWebhookEndpoint(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reload webhook endpoint in memory: %v, please restart bifrost to sync with the database", err))
		return
	}
	SendJSON(ctx, map[string]any{
		"endpoint": redactedWebhookEndpoint(rotated),
		"secret":   rotated.Secret.GetValue(),
	})
}

// testWebhookEndpoint sends one signed sample event through the production
// delivery path and reports the receiver's response. Rate-limited per
// endpoint so a misclicked button cannot hammer a receiver.
func (h *WebhookHandler) testWebhookEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	if h.dispatcher == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Webhook delivery is not available")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	endpoint, ok := h.store.WebhookEndpointByID(id)
	if !ok {
		SendError(ctx, fasthttp.StatusNotFound, "Webhook endpoint not found")
		return
	}
	if endpoint.Disabled {
		SendError(ctx, fasthttp.StatusBadRequest, "Webhook endpoint is disabled")
		return
	}
	// The event to sample is optional; it defaults to the endpoint's first
	// subscription and must be one the endpoint would actually receive.
	event := endpoint.Events[0]
	if body := ctx.PostBody(); len(body) > 0 {
		var req struct {
			Event configstoreTables.WebhookEvent `json:"event"`
		}
		if err := sonic.Unmarshal(body, &req); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
			return
		}
		if req.Event != "" {
			event = req.Event
		}
	}
	if !slices.Contains(endpoint.Events, event) {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Endpoint is not subscribed to event %q", event))
		return
	}
	statusCode, err := h.dispatcher.DeliverTest(ctx, endpoint, event)
	if err != nil {
		SendJSON(ctx, map[string]any{
			"delivered": false,
			"error":     err.Error(),
		})
		return
	}
	SendJSON(ctx, map[string]any{
		"delivered":            statusCode >= 200 && statusCode < 300,
		"receiver_status_code": statusCode,
	})
}

// listWebhookDeliveries returns one page of delivery history for an
// endpoint, newest first. History outlives its endpoint, so no existence
// check is made against the endpoint itself.
func (h *WebhookHandler) listWebhookDeliveries(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	if h.store.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Logs store is not available")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	limit, offset := 0, 0
	if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
		if limit, err = strconv.Atoi(limitStr); err != nil || limit < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid limit parameter: must be a non-negative number")
			return
		}
	}
	if offsetStr := string(ctx.QueryArgs().Peek("offset")); offsetStr != "" {
		if offset, err = strconv.Atoi(offsetStr); err != nil || offset < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid offset parameter: must be a non-negative number")
			return
		}
	}
	limit, offset = ClampPaginationParams(limit, offset)
	result, err := h.store.LogsStore.SearchWebhookDeliveries(ctx, id, logstore.PaginationOptions{Limit: limit, Offset: offset})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to list webhook deliveries: %v", err))
		return
	}
	SendJSON(ctx, result)
}

// redeliverWebhook re-queues the delivery a history record belongs to, under
// its original webhook id so receivers can deduplicate the replay.
func (h *WebhookHandler) redeliverWebhook(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	if h.store.LogsStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Logs store is not available")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	delivery, err := h.store.LogsStore.FindWebhookDeliveryByID(ctx, id)
	if err != nil {
		if errors.Is(err, logstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Webhook delivery not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load webhook delivery: %v", err))
		return
	}
	endpoint, ok := h.store.WebhookEndpointByID(delivery.EndpointID)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "The endpoint this delivery belongs to no longer exists")
		return
	}
	if endpoint.Disabled {
		SendError(ctx, fasthttp.StatusBadRequest, "The endpoint this delivery belongs to is disabled")
		return
	}
	// Same id as the original delivery: the webhook-id header stays stable,
	// so receiver-side deduplication of the replay remains intentional.
	job := &configstoreTables.TableWebhookJob{
		ID:         delivery.WebhookID,
		EndpointID: delivery.EndpointID,
		AsyncJobID: delivery.AsyncJobID,
		Event:      delivery.Event,
	}
	if err := h.store.ConfigStore.CreateWebhookJob(ctx, job); err != nil {
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, "This delivery is already in flight")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to queue redelivery: %v", err))
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Wake()
	}
	SendJSONWithStatus(ctx, map[string]any{
		"status":     "queued",
		"webhook_id": delivery.WebhookID,
	}, fasthttp.StatusAccepted)
}
