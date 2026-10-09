package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
)

// GatewayPushCallbackPathSuffix is the agent-scoped mount point of Bifrost's
// push ingress: the URL the upstream agent is told to push to instead of the
// downstream client's real callback.
const GatewayPushCallbackPathSuffix = "/push/callback"

// PushNotificationTokenHeader is the A2A push notification token header,
// matching the header the SDK's own push sender uses so any conformant
// upstream replays the gateway-minted token verbatim. The transport reads it
// at the ingress route; the relay sets it on re-originated deliveries.
const PushNotificationTokenHeader = "A2A-Notification-Token"

// startPushTrace owns only traces that have no transport owner. Relay attempts
// always use a fresh context, so retries never share an ingress trace. The
// returned function ends the trace and exports it only when export is true.
func (m *Manager) startPushTrace(ctx *schemas.BifrostContext, name string) func(error, bool) {
	if traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string); traceID != "" {
		return func(error, bool) { ctx.StampUpstreamLatency() }
	}
	tracer := m.tracer
	if m.tracerProvider != nil {
		tracer = m.tracerProvider()
	}
	if tracer == nil {
		return func(error, bool) {}
	}
	requestID := uuid.NewString()
	traceID := tracer.CreateTrace("", requestID)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, traceID)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	id, root := tracer.StartSpanID(ctx, name, schemas.SpanKindHTTPRequest)
	ctx.SetValue(schemas.BifrostContextKeySpanID, id)
	return func(err error, export bool) {
		ctx.StampUpstreamLatency()
		if err != nil {
			tracer.EndSpan(root, schemas.SpanStatusError, err.Error())
		} else {
			tracer.EndSpan(root, schemas.SpanStatusOk, "")
		}
		if export {
			tracer.CompleteAndFlushTrace(traceID)
		} else if trace := tracer.EndTrace(traceID); trace != nil {
			tracer.ReleaseTrace(trace)
		}
	}
}

const pushNotificationTransport = "Push notification"

func markPushNotificationTransport(ctx *schemas.BifrostContext) {
	ctx.SetValue(schemas.BifrostContextKeyA2ADownstreamTransport, pushNotificationTransport)
	ctx.SetValue(schemas.BifrostContextKeyA2AUpstreamTransport, pushNotificationTransport)
}

const (
	// maxPushPayloadSize bounds one accepted push event, mirroring the agent
	// card size cap: push payloads are protocol events, not bulk data.
	maxPushPayloadSize = 1 << 20
	// pushMaxAttempts bounds retries so an unreachable downstream callback
	// dead-letters instead of retrying forever.
	pushMaxAttempts = 6
	// pushDeliveryBatchSize bounds how many due rows one relay pass loads.
	pushDeliveryBatchSize = 16
	// pushPollInterval is the relay's steady-state poll cadence; ingress
	// accepts additionally wake it immediately.
	pushPollInterval = 3 * time.Second

	// Keep terminal rows for seven days after their final outcome for deduplication.
	pushDeliveryRetention = 7 * 24 * time.Hour
	pushCleanupInterval   = time.Hour
	// pushBaseBackoff and pushMaxBackoff shape the exponential retry schedule.
	pushBaseBackoff = time.Second
	pushMaxBackoff  = 5 * time.Minute
	// pushSendTimeout bounds one downstream delivery attempt, matching the
	// SDK push sender's default.
	pushSendTimeout = 30 * time.Second
	// pushDeliveryLease outlives the bounded HTTP attempt and surrounding
	// plugin and persistence work. Expiry recovers work from a stopped node.
	pushDeliveryLease = 2 * pushSendTimeout
)

// errPushConfigMissing marks a delivery whose push configuration was deleted
// after the event was queued; it is permanent, so the delivery dead-letters
// immediately instead of retrying.
var errPushConfigMissing = errors.New("push configuration no longer exists")

// PushStore is the durable persistence contract for the push relay, declared
// here (like Store) so core does not depend on the framework config store. The
// config-store getters report a missing row as (nil, nil) so this package needs
// no store error sentinel. It is satisfied by the framework config store; a
// Store that does not implement it simply leaves push notifications
// unsupported and unadvertised.
type PushStore interface {
	SaveAgentPushConfig(ctx context.Context, config *schemas.AgentPushConfig) error
	// BindAgentPushConfigTask attaches a task id to a pending embedded push
	// config identified by its ingress token hash and exact internal pending
	// task id. It must not touch rows that have already been bound.
	BindAgentPushConfigTask(ctx context.Context, agentName, ingressTokenHash, pendingTaskID, taskID string) error
	GetAgentPushConfig(ctx context.Context, agentName, taskID, configID string) (*schemas.AgentPushConfig, error)
	GetAgentPushConfigByIngressTokenHash(ctx context.Context, agentName, hash string) (*schemas.AgentPushConfig, error)
	ListAgentPushConfigs(ctx context.Context, agentName, taskID string) ([]schemas.AgentPushConfig, error)
	ListAgentPushConfigsPaginated(ctx context.Context, query schemas.AgentPushConfigQuery) ([]schemas.AgentPushConfig, int64, error)
	ListAgentPushConfigAgentNames(ctx context.Context) ([]string, error)
	DeleteAgentPushConfig(ctx context.Context, agentName, taskID, configID string) (bool, error)
	DeletePendingAgentPushConfig(ctx context.Context, agentName, ingressTokenHash, pendingTaskID string) (bool, error)
	CreateAgentPushDeliveryIfNotExists(ctx context.Context, delivery *schemas.AgentPushDelivery) (bool, error)
	ListDueAgentPushDeliveries(ctx context.Context, now time.Time, limit int) ([]schemas.AgentPushDelivery, error)
	ClaimAgentPushDelivery(ctx context.Context, id, runnerID string, leaseUntil time.Time) (*schemas.AgentPushDelivery, error)
	UpdateAgentPushDeliveryOutcome(ctx context.Context, delivery *schemas.AgentPushDelivery, runnerID string, leaseUntil time.Time) error
	PruneAgentPushDeliveries(ctx context.Context, before time.Time) error
}

// pushEnabled reports whether the full two-hop push path is operational: a
// durable store to hold configs and the outbox, and an external URL the
// upstream can be pointed at. Only then are push operations accepted and the
// capability advertised.
func (m *Manager) pushEnabled() bool {
	return m.pushStore != nil && m.externalBaseURL() != ""
}

// pushCallbackURL is the ingress endpoint the upstream agent is registered to
// push to for one agent.
func (m *Manager) pushCallbackURL(agentName string) string {
	return m.externalBaseURL() + "/agents/a2a/" + agentName + GatewayPushCallbackPathSuffix
}

// mintPushIngressToken creates the independent upstream-facing credential for
// one push configuration and the SHA-256 digest that is the only durable form
// of it: the raw token exists in the upstream agent's config store and nowhere
// in Bifrost.
func mintPushIngressToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("mint push ingress token: %w", err)
	}
	token = hex.EncodeToString(raw)
	return token, hashPushIngressToken(token), nil
}

func hashPushIngressToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// validatePushCallbackURL applies the same posture as agent card URLs: an
// absolute HTTP(S) URL without embedded credentials. Deliveries additionally
// never follow redirects, mirroring how card discovery contains SSRF.
func validatePushCallbackURL(rawURL string) error {
	u, err := url.ParseRequestURI(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: push config url must be an absolute HTTP(S) URL without user info", a2a.ErrInvalidParams)
	}
	return nil
}

// validatePushCallbackAuth accepts only callback authentication Bifrost can
// reproduce from the durable push configuration without upstream participation.
func validatePushCallbackAuth(auth *a2a.PushAuthInfo) error {
	if auth == nil {
		return nil
	}
	if auth.Credentials == "" {
		return fmt.Errorf("%w: push callback authentication credentials are required", a2a.ErrInvalidParams)
	}
	if !strings.EqualFold(auth.Scheme, "basic") && !strings.EqualFold(auth.Scheme, "bearer") {
		return fmt.Errorf("%w: push callback authentication scheme must be Basic or Bearer", a2a.ErrInvalidParams)
	}
	return nil
}

// redactedPushConfig projects a stored push configuration for protocol reads.
// Downstream secrets are write-only: the token is omitted entirely and the
// auth block keeps only its scheme.
func redactedPushConfig(stored *schemas.AgentPushConfig) *a2a.PushConfig {
	out := &a2a.PushConfig{Tenant: stored.Tenant, TaskID: a2a.TaskID(stored.TaskID), ID: stored.ConfigID, URL: stored.URL}
	if stored.AuthScheme != "" {
		out.Auth = &a2a.PushAuthInfo{Scheme: stored.AuthScheme}
	}
	return out
}

// redactedPushConfigRequest is the loggable projection of a submitted push
// configuration: same shape, secrets removed.
func redactedPushConfigRequest(config *a2a.PushConfig) *a2a.PushConfig {
	out := *config
	out.Token = ""
	if config.Auth != nil {
		out.Auth = &a2a.PushAuthInfo{Scheme: config.Auth.Scheme}
	}
	return &out
}

// pushEnvelope builds the plugin-facing request for one push operation,
// carrying task and push-config correlation.
func (h *proxyRequestHandler) pushEnvelope(requestType schemas.A2ARequestType, taskID a2a.TaskID, configID string, body any) *schemas.BifrostA2ARequest {
	envelope := h.envelope(requestType)
	envelope.BifrostA2ATaskRequest = &schemas.BifrostA2ATaskRequest{TaskID: string(taskID)}
	envelope.BifrostA2APushRequest = &schemas.BifrostA2APushRequest{PushConfigID: configID}
	return setA2ARequestBody(envelope, body)
}

// runPushLocal is forwardUpstream for push operations answered from Bifrost's
// own durable store: same plugin gate, no upstream lease. Reads must be local
// because the upstream's stored copy of the config points at Bifrost's ingress
// and carries the gateway-minted token, which must never be served back.
func runPushLocal[T any](
	ctx context.Context,
	h *proxyRequestHandler,
	envelope *schemas.BifrostA2ARequest,
	op func(context.Context) (T, error),
	describe func(T) *schemas.BifrostA2AResponse,
) (T, error) {
	var zero T
	var result T
	var opErr error
	start := time.Now()
	gateCtx := gateContext(ctx)
	defer gateCtx.StampUpstreamLatency()
	_, gateErr := h.manager.RunWithPluginPipeline(gateCtx, envelope, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		phase := startPhaseSpan(gateCtx, "a2a.push.db.local")
		value, callErr := op(gateCtx)
		endPhaseSpan(phase)
		if callErr != nil {
			opErr = callErr
			return nil, callErr
		}
		result = value
		resp := describe(value)
		if resp != nil {
			resp.ResponseBody = marshalA2APayload(value)
			resp.ExtraFields.Latency = time.Since(start).Milliseconds()
		}
		return resp, nil
	})
	if err := gateOutcomeError(gateCtx, gateErr, opErr); err != nil {
		return zero, err
	}
	return result, nil
}

// describePushResult projects the acted-on configuration onto the plugin-facing
// response.
func describePushResult(configID string) *schemas.BifrostA2AResponse {
	return &schemas.BifrostA2AResponse{BifrostA2APushResponse: &schemas.BifrostA2APushResponse{PushConfigID: configID}}
}

// CreateTaskPushConfig registers a downstream push callback through the
// gateway. Bifrost rewrites the upstream-facing configuration to point at its
// own push ingress with a freshly minted per-config credential, and durably
// stores the client's real callback (URL, token, auth) separately so every
// push crosses two independently credentialed hops. The upstream agent owns
// the configuration identity: whatever ID it returns keys the stored callback.
func (h *proxyRequestHandler) CreateTaskPushConfig(ctx context.Context, config *a2a.PushConfig) (*a2a.PushConfig, error) {
	m := h.manager
	if !m.pushEnabled() {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	if config == nil || config.TaskID == "" {
		return nil, fmt.Errorf("%w: push config with a task id is required", a2a.ErrInvalidParams)
	}
	if err := h.validateExtensions(ctx, config); err != nil {
		return nil, err
	}
	if err := validatePushCallbackURL(config.URL); err != nil {
		return nil, err
	}
	if err := validatePushCallbackAuth(config.Auth); err != nil {
		return nil, err
	}
	envelope := h.pushEnvelope(schemas.A2ARequestTypeCreateTaskPushConfig, config.TaskID, config.ID, redactedPushConfigRequest(config))
	return forwardUpstream(ctx, h, envelope, func(ctx context.Context, client sdkClient) (*a2a.PushConfig, error) {
		return m.createPushConfig(ctx, client, h.config.name, config)
	}, func(result *a2a.PushConfig) *schemas.BifrostA2AResponse {
		configID := ""
		if result != nil {
			configID = result.ID
		}
		return describePushResult(configID)
	})
}

// storedPushConfig builds the locally persisted copy of a push configuration for
// both the explicit-create and embedded-rewrite paths.
func storedPushConfig(agentName, taskID, configID, tokenHash string, cfg *a2a.PushConfig) *schemas.AgentPushConfig {
	now := time.Now().UTC()
	stored := &schemas.AgentPushConfig{
		AgentName:        agentName,
		TaskID:           taskID,
		ConfigID:         configID,
		Tenant:           cfg.Tenant,
		URL:              cfg.URL,
		IngressTokenHash: tokenHash,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if cfg.Token != "" {
		stored.Token = &schemas.SecretVar{Val: cfg.Token, SecretType: schemas.SecretTypePlainText}
	}
	if cfg.Auth != nil {
		stored.AuthScheme = cfg.Auth.Scheme
		if cfg.Auth.Credentials != "" {
			stored.AuthCredentials = &schemas.SecretVar{Val: cfg.Auth.Credentials, SecretType: schemas.SecretTypePlainText}
		}
	}
	return stored
}

// createPushConfig performs the two-hop registration: upstream first (it owns
// the config identity), then the durable local record of the client's real
// callback. If persistence fails the upstream registration is best-effort
// rolled back; if that also fails, the orphaned upstream config is harmless
// because its ingress token hash was never stored, so its pushes are refused.
func (m *Manager) createPushConfig(ctx context.Context, client sdkClient, agentName string, config *a2a.PushConfig) (*a2a.PushConfig, error) {
	token, hash, err := mintPushIngressToken()
	if err != nil {
		return nil, err
	}
	callStart := time.Now()
	created, err := client.CreateTaskPushConfig(ctx, &a2a.PushConfig{
		Tenant: config.Tenant,
		TaskID: config.TaskID,
		ID:     config.ID,
		URL:    m.pushCallbackURL(agentName),
		Token:  token,
	})
	schemas.AddUpstreamLatency(ctx, time.Since(callStart))
	if err != nil {
		return nil, err
	}
	configID := config.ID
	if created != nil && created.ID != "" {
		configID = created.ID
	}
	stored := storedPushConfig(agentName, string(config.TaskID), configID, hash, config)
	phase := startPhaseSpan(schemas.NewBifrostContext(ctx, schemas.NoDeadline), "a2a.push.db.save")
	saveErr := m.pushStore.SaveAgentPushConfig(ctx, stored)
	endPhaseSpan(phase)
	if err := saveErr; err != nil {
		rollbackStart := time.Now()
		_ = client.DeleteTaskPushConfig(ctx, &a2a.DeleteTaskPushConfigRequest{TaskID: config.TaskID, ID: configID})
		schemas.AddUpstreamLatency(ctx, time.Since(rollbackStart))
		return nil, fmt.Errorf("persist push configuration: %w", err)
	}
	return redactedPushConfig(stored), nil
}

// isPendingAgentPushConfig reports whether a stored task ID is an internal
// per-send identity that has not yet been replaced by an upstream task ID.
func isPendingAgentPushConfig(taskID string) bool {
	return strings.HasPrefix(taskID, pendingAgentPushConfigTaskIDPrefix)
}

const pendingAgentPushConfigTaskIDPrefix = "__bifrost_pending__:"

// rewriteEmbeddedPushConfig intercepts a push configuration embedded in a
// message send (SendMessageConfig.PushConfig). The same two-hop rule as
// CreateTaskPushConfig applies: the upstream must only ever see Bifrost's push
// ingress with a freshly minted credential, never the client's real callback.
//
// The upstream may push events for the newly born task while the send is still
// in flight, before any task id is visible on this side. The client's callback
// is therefore persisted up front under a unique internal pending task id so
// concurrent sends using the same client config id keep independent rows and
// credentials. The ingress binds the row to the first pushed task id. The
// returned finish function must be invoked once the send settles: with the born
// task id to bind the row, or with an empty id to discard a still-pending row
// when the send produced no task, so an unused minted credential does not
// outlive its send.
//
// Returns (nil, nil) when the request carries no embedded push config.
func (h *proxyRequestHandler) rewriteEmbeddedPushConfig(ctx context.Context, req *a2a.SendMessageRequest) (func(ctx context.Context, taskID a2a.TaskID), error) {
	if req == nil || req.Config == nil || req.Config.PushConfig == nil {
		return nil, nil
	}
	m := h.manager
	if !m.pushEnabled() {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	original := *req.Config.PushConfig
	if err := validatePushCallbackURL(original.URL); err != nil {
		return nil, err
	}
	if err := validatePushCallbackAuth(original.Auth); err != nil {
		return nil, err
	}
	token, hash, err := mintPushIngressToken()
	if err != nil {
		return nil, err
	}
	agentName := h.config.name
	configID := original.ID
	if configID == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("mint push config id: %w", err)
		}
		configID = hex.EncodeToString(raw)
	}
	pendingTaskID := pendingAgentPushConfigTaskIDPrefix + uuid.NewString()
	stored := storedPushConfig(agentName, pendingTaskID, configID, hash, &original)
	phase := startPhaseSpan(schemas.NewBifrostContext(ctx, schemas.NoDeadline), "a2a.push.db.save")
	saveErr := m.pushStore.SaveAgentPushConfig(ctx, stored)
	endPhaseSpan(phase)
	if err := saveErr; err != nil {
		return nil, fmt.Errorf("persist push configuration: %w", err)
	}
	req.Config.PushConfig = &a2a.PushConfig{
		Tenant: original.Tenant,
		ID:     configID,
		URL:    m.pushCallbackURL(agentName),
		Token:  token,
	}
	settled := false
	return func(ctx context.Context, taskID a2a.TaskID) {
		if settled {
			return
		}
		settled = true
		if taskID == "" {
			// No task was born: drop the row only if it is still pending; a
			// concurrent ingress push may already have bound it.
			phase := startPhaseSpan(schemas.NewBifrostContext(ctx, schemas.NoDeadline), "a2a.push.db.delete")
			_, err := m.pushStore.DeletePendingAgentPushConfig(ctx, agentName, hash, pendingTaskID)
			endPhaseSpan(phase)
			if err != nil && m.logger != nil {
				m.logger.Error("agent gateway: failed to discard pending embedded push configuration (agent=%s): %v", agentName, err)
			}
			return
		}
		// Binding is conditional on the row still being pending, so it cannot
		// fight an ingress push that bound the same task id first.
		phase := startPhaseSpan(schemas.NewBifrostContext(ctx, schemas.NoDeadline), "a2a.push.db.bind")
		err := m.pushStore.BindAgentPushConfigTask(ctx, agentName, hash, pendingTaskID, string(taskID))
		endPhaseSpan(phase)
		if err != nil && m.logger != nil {
			m.logger.Error("agent gateway: failed to bind embedded push configuration (agent=%s, task=%s): %v", agentName, taskID, err)
		}
	}, nil
}

// eventTaskID extracts the task identity carried by a stream event, if any.
func eventTaskID(event a2a.Event) a2a.TaskID {
	switch value := event.(type) {
	case *a2a.Task:
		if value != nil {
			return value.ID
		}
	case *a2a.TaskStatusUpdateEvent:
		if value != nil {
			return value.TaskID
		}
	case *a2a.TaskArtifactUpdateEvent:
		if value != nil {
			return value.TaskID
		}
	case *a2a.Message:
		if value != nil {
			return value.TaskID
		}
	}
	return ""
}

// GetTaskPushConfig answers from the gateway's own store: the upstream's copy
// describes Bifrost's ingress and its minted credential, never the client's
// callback, so forwarding the read would leak the wrong hop.
func (h *proxyRequestHandler) GetTaskPushConfig(ctx context.Context, req *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	m := h.manager
	if !m.pushEnabled() {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	if err := h.validateExtensions(ctx, req); err != nil {
		return nil, err
	}
	envelope := h.pushEnvelope(schemas.A2ARequestTypeGetTaskPushConfig, req.TaskID, req.ID, req)
	return runPushLocal(ctx, h, envelope, func(ctx context.Context) (*a2a.PushConfig, error) {
		stored, err := m.pushStore.GetAgentPushConfig(ctx, h.config.name, string(req.TaskID), req.ID)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			return nil, push.ErrPushConfigNotFound
		}
		return redactedPushConfig(stored), nil
	}, func(result *a2a.PushConfig) *schemas.BifrostA2AResponse {
		return describePushResult(result.ID)
	})
}

// ListTaskPushConfigs lists the gateway's stored callbacks for one task, for
// the same reason GetTaskPushConfig is local.
func (h *proxyRequestHandler) ListTaskPushConfigs(ctx context.Context, req *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	m := h.manager
	if !m.pushEnabled() {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	if err := h.validateExtensions(ctx, req); err != nil {
		return nil, err
	}
	envelope := h.envelope(schemas.A2ARequestTypeListTaskPushConfigs)
	envelope.BifrostA2ATaskRequest = &schemas.BifrostA2ATaskRequest{TaskID: string(req.TaskID)}
	setA2ARequestBody(envelope, req)
	return runPushLocal(ctx, h, envelope, func(ctx context.Context) (*a2a.ListTaskPushConfigResponse, error) {
		stored, err := m.pushStore.ListAgentPushConfigs(ctx, h.config.name, string(req.TaskID))
		if err != nil {
			return nil, err
		}
		configs := make([]*a2a.PushConfig, 0, len(stored))
		for i := range stored {
			configs = append(configs, redactedPushConfig(&stored[i]))
		}
		return &a2a.ListTaskPushConfigResponse{Configs: configs}, nil
	}, func(*a2a.ListTaskPushConfigResponse) *schemas.BifrostA2AResponse {
		return &schemas.BifrostA2AResponse{BifrostA2APushResponse: &schemas.BifrostA2APushResponse{}}
	})
}

// DeleteTaskPushConfig removes both hops: the upstream registration first (it
// is the authority and may hold a config Bifrost never stored), then the local
// callback record, idempotently.
func (h *proxyRequestHandler) DeleteTaskPushConfig(ctx context.Context, req *a2a.DeleteTaskPushConfigRequest) error {
	m := h.manager
	if !m.pushEnabled() {
		return a2a.ErrPushNotificationNotSupported
	}
	if err := h.validateExtensions(ctx, req); err != nil {
		return err
	}
	envelope := h.pushEnvelope(schemas.A2ARequestTypeDeleteTaskPushConfig, req.TaskID, req.ID, req)
	_, err := forwardUpstream(ctx, h, envelope, func(ctx context.Context, client sdkClient) (struct{}, error) {
		callStart := time.Now()
		callErr := client.DeleteTaskPushConfig(ctx, req)
		schemas.AddUpstreamLatency(ctx, time.Since(callStart))
		if err := callErr; err != nil {
			return struct{}{}, err
		}
		phase := startPhaseSpan(schemas.NewBifrostContext(ctx, schemas.NoDeadline), "a2a.push.db.delete")
		_, err := m.pushStore.DeleteAgentPushConfig(ctx, h.config.name, string(req.TaskID), req.ID)
		endPhaseSpan(phase)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	}, func(struct{}) *schemas.BifrostA2AResponse {
		return describePushResult(req.ID)
	})
	return err
}

// AcceptPushCallback is the push ingress: it authenticates one upstream push
// with the per-config gateway-minted token (never the general downstream auth
// middleware), verifies the pushed event addresses the config's task, and
// durably enqueues it for re-originated delivery. It returns the HTTP status
// the transport should write. Acceptance means queued, not delivered, so a
// success is 202.
func (m *Manager) AcceptPushCallback(ctx context.Context, agentName, token string, body []byte) (status int, resultErr error) {
	gateCtx := gateContext(ctx)
	finish := m.startPushTrace(gateCtx, "a2a.push.callback")
	defer func() { finish(resultErr, true) }()
	ctx = gateCtx
	if !m.pushEnabled() {
		return http.StatusNotFound, errors.New("push notifications are not supported")
	}
	if _, ok := m.runtimes.load(agentName); !ok {
		return http.StatusNotFound, ErrNotFound
	}
	if len(body) > maxPushPayloadSize {
		return http.StatusRequestEntityTooLarge, errors.New("push payload exceeds size limit")
	}
	if token == "" {
		return http.StatusUnauthorized, errors.New("push ingress token is required")
	}
	auth := startPhaseSpan(gateCtx, "a2a.push.db.authenticate")
	config, err := m.pushStore.GetAgentPushConfigByIngressTokenHash(ctx, agentName, hashPushIngressToken(token))
	endPhaseSpan(auth)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if config == nil {
		return http.StatusUnauthorized, errors.New("push ingress token is not recognized")
	}
	taskID, err := pushEventTaskID(body)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if isPendingAgentPushConfig(config.TaskID) {
		// Pending embedded push config: the upstream pushed before the send
		// settled on this side, so this first authenticated push binds the row
		// to the task it addresses. The token is minted per send and handed to
		// exactly one upstream, so the pushed task id is trustworthy here.
		if taskID == "" {
			return http.StatusBadRequest, errors.New("pushed event does not carry a task id")
		}
		bind := startPhaseSpan(gateCtx, "a2a.push.db.bind")
		err := m.pushStore.BindAgentPushConfigTask(ctx, agentName, hashPushIngressToken(token), config.TaskID, taskID)
		endPhaseSpan(bind)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		config.TaskID = taskID
	} else if taskID != config.TaskID {
		return http.StatusBadRequest, errors.New("pushed event does not address the configured task")
	}

	payload := string(body)
	// The delivery ID is a content digest scoped to the Agent and config, so an
	// upstream retransmission of the same event deduplicates without colliding
	// with an identical config ID and payload from another Agent.
	sum := sha256.Sum256([]byte(agentName + "\x00" + config.ConfigID + "\x00" + payload))
	deliveryID := hex.EncodeToString(sum[:])

	envelope := &schemas.BifrostA2ARequest{
		RequestType:           schemas.A2ARequestTypePushNotification,
		AgentName:             agentName,
		RequestBody:           &payload,
		BifrostA2ATaskRequest: &schemas.BifrostA2ATaskRequest{TaskID: config.TaskID},
		BifrostA2APushRequest: &schemas.BifrostA2APushRequest{PushConfigID: config.ConfigID, DeliveryID: deliveryID},
	}
	start := time.Now()
	markPushNotificationTransport(gateCtx)
	var opErr error
	_, gateErr := m.RunWithPluginPipeline(gateCtx, envelope, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		now := time.Now().UTC()
		enqueue := startPhaseSpan(gateCtx, "a2a.push.db.enqueue")
		_, err := m.pushStore.CreateAgentPushDeliveryIfNotExists(gateCtx, &schemas.AgentPushDelivery{
			ID:            deliveryID,
			AgentName:     agentName,
			TaskID:        config.TaskID,
			ConfigID:      config.ConfigID,
			Payload:       payload,
			Status:        schemas.AgentPushDeliveryStatusPending,
			NextAttemptAt: now,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		endPhaseSpan(enqueue)
		if err != nil {
			opErr = err
			return nil, err
		}
		return &schemas.BifrostA2AResponse{
			BifrostA2APushResponse: &schemas.BifrostA2APushResponse{PushConfigID: config.ConfigID, DeliveryID: deliveryID},
			ExtraFields:            schemas.BifrostA2AResponseExtraFields{Latency: time.Since(start).Milliseconds()},
		}, nil
	})
	if err := gateOutcomeError(gateCtx, gateErr, opErr); err != nil {
		status := http.StatusInternalServerError
		if opErr == nil && gateErr != nil && gateErr.StatusCode != nil {
			status = *gateErr.StatusCode
		}
		return status, err
	}
	if m.pushRelay != nil {
		m.pushRelay.notify()
	}
	return http.StatusAccepted, nil
}

// pushEventTaskID extracts the addressed task from one pushed strict-v1 event.
func pushEventTaskID(body []byte) (string, error) {
	var sr a2a.StreamResponse
	if err := json.Unmarshal(body, &sr); err != nil || sr.Event == nil {
		return "", errors.New("push payload is not a valid A2A event")
	}
	switch event := sr.Event.(type) {
	case *a2a.Message:
		return string(event.TaskID), nil
	case *a2a.Task:
		return string(event.ID), nil
	case *a2a.TaskStatusUpdateEvent:
		return string(event.TaskID), nil
	case *a2a.TaskArtifactUpdateEvent:
		return string(event.TaskID), nil
	default:
		return "", errors.New("push payload event type is not supported")
	}
}

// pushRelay is the durable delivery worker: a single background loop that
// drains the outbox and re-originates each queued push to the downstream
// callback. One sequential worker is the concurrency and rate bound; each
// attempt is further bounded by pushSendTimeout. Its goroutine is registered
// with the manager's drain group and observes the manager context, so shutdown
// stops it cleanly and a restart resumes pending rows from the store.
func (m *Manager) ListStoredPushConfigs(ctx context.Context, query schemas.AgentPushConfigQuery) ([]schemas.AgentPushConfigView, int64, error) {
	if m.pushStore == nil {
		return []schemas.AgentPushConfigView{}, 0, nil
	}
	configs, total, err := m.pushStore.ListAgentPushConfigsPaginated(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	views := make([]schemas.AgentPushConfigView, 0, len(configs))
	for _, config := range configs {
		views = append(views, config.Redacted())
	}
	return views, total, nil
}

func (m *Manager) ListStoredPushConfigAgentNames(ctx context.Context) ([]string, error) {
	if m.pushStore == nil {
		return []string{}, nil
	}
	return m.pushStore.ListAgentPushConfigAgentNames(ctx)
}

func (m *Manager) DeleteStoredPushConfig(ctx context.Context, agentName, taskID, configID string) (bool, error) {
	if m.pushStore == nil {
		return false, nil
	}
	return m.pushStore.DeleteAgentPushConfig(ctx, agentName, taskID, configID)
}

type pushRelay struct {
	manager *Manager
	wake    chan struct{}
}

func newPushRelay(m *Manager) *pushRelay {
	return &pushRelay{manager: m, wake: make(chan struct{}, 1)}
}

func (r *pushRelay) start() {
	r.manager.draining.Go(r.run)
}

// notify wakes the relay immediately after an ingress accept, without blocking
// the accept path; a full wake channel means a pass is already scheduled.
func (r *pushRelay) notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *pushRelay) run() {
	ticker := time.NewTicker(pushPollInterval)
	defer ticker.Stop()
	cleanupTicker := time.NewTicker(pushCleanupInterval)
	defer cleanupTicker.Stop()
	r.prune()
	for {
		select {
		case <-r.manager.ctx.Done():
			return
		case <-r.wake:
		case <-ticker.C:
		case <-cleanupTicker.C:
			r.prune()
			continue
		}
		r.processDue()
	}
}

func (r *pushRelay) prune() {
	m := r.manager
	ctx := gateContext(m.ctx)
	finish := m.startPushTrace(ctx, "a2a.push.maintenance.prune")
	phase := startPhaseSpan(ctx, "a2a.push.db.prune")
	err := m.pushStore.PruneAgentPushDeliveries(m.ctx, time.Now().UTC().Add(-pushDeliveryRetention))
	endPhaseSpan(phase)
	finish(err, true)
	if err != nil && m.logger != nil && m.ctx.Err() == nil {
		m.logger.Error("push relay failed to prune terminal deliveries: %v", err)
	}
}

// processDue drains everything currently due in bounded batches. Rows that
// fail are rescheduled into the future by deliver, so the loop terminates.
func (r *pushRelay) processDue() {
	m := r.manager
	for {
		// Shared batch lookup has its own trace, not a delivery's plugin log row.
		ctx := gateContext(m.ctx)
		finish := m.startPushTrace(ctx, "a2a.push.maintenance.list-due")
		phase := startPhaseSpan(ctx, "a2a.push.db.list-due")
		due, err := m.pushStore.ListDueAgentPushDeliveries(m.ctx, time.Now().UTC(), pushDeliveryBatchSize)
		endPhaseSpan(phase)
		finish(err, err != nil || len(due) > 0)
		if err != nil {
			if m.logger != nil && m.ctx.Err() == nil {
				m.logger.Error("push relay failed to list due deliveries: %v", err)
			}
			return
		}
		madeProgress := false
		for i := range due {
			if m.ctx.Err() != nil {
				return
			}
			leaseUntil := time.Now().UTC().Add(pushDeliveryLease)
			claimed, err := m.pushStore.ClaimAgentPushDelivery(m.ctx, due[i].ID, m.pushRelayID, leaseUntil)
			if err != nil {
				if m.logger != nil && m.ctx.Err() == nil {
					m.logger.Error("push relay failed to claim delivery %s: %v", due[i].ID, err)
				}
				continue
			}
			if claimed == nil {
				continue
			}
			madeProgress = true
			r.deliver(claimed, leaseUntil)
		}
		if len(due) < pushDeliveryBatchSize || !madeProgress {
			return
		}
	}
}

// deliver performs one attempt for one outbox row inside the plugin gate, then
// records the outcome. Delivery is at-least-once: an ambiguous failure (for
// example a timeout after the request was sent) is retried, but retries are
// bounded by pushMaxAttempts with per-attempt accounting, so an outage
// dead-letters instead of duplicating without bound.
func (r *pushRelay) deliver(delivery *schemas.AgentPushDelivery, leaseUntil time.Time) {
	m := r.manager
	attemptID := fmt.Sprintf("%s-%d", delivery.ID, delivery.Attempts+1)
	envelope := &schemas.BifrostA2ARequest{
		RequestType:           schemas.A2ARequestTypePushDelivery,
		AgentName:             delivery.AgentName,
		RequestBody:           &delivery.Payload,
		BifrostA2ATaskRequest: &schemas.BifrostA2ATaskRequest{TaskID: delivery.TaskID},
		BifrostA2APushRequest: &schemas.BifrostA2APushRequest{PushConfigID: delivery.ConfigID, DeliveryID: delivery.ID, AttemptID: attemptID},
	}
	start := time.Now()
	gateCtx := gateContext(m.ctx)
	finish := m.startPushTrace(gateCtx, "a2a.push.delivery")
	var traceErr error
	defer func() { finish(traceErr, true) }()
	markPushNotificationTransport(gateCtx)
	var opErr error
	_, gateErr := m.RunWithPluginPipeline(gateCtx, envelope, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		lookup := startPhaseSpan(gateCtx, "a2a.push.db.config")
		config, err := m.pushStore.GetAgentPushConfig(gateCtx, delivery.AgentName, delivery.TaskID, delivery.ConfigID)
		endPhaseSpan(lookup)
		if err == nil && config == nil {
			err = errPushConfigMissing
		}
		if err == nil {
			err = m.sendPushDownstream(gateCtx, config, delivery.Payload)
		}
		if err != nil {
			opErr = err
			return nil, err
		}
		return &schemas.BifrostA2AResponse{
			BifrostA2APushResponse: &schemas.BifrostA2APushResponse{PushConfigID: delivery.ConfigID, DeliveryID: delivery.ID, AttemptID: attemptID},
			ExtraFields:            schemas.BifrostA2AResponseExtraFields{Latency: time.Since(start).Milliseconds()},
		}, nil
	})
	err := gateOutcomeError(gateCtx, gateErr, opErr)
	traceErr = err

	now := time.Now().UTC()
	delivery.Attempts++
	delivery.UpdatedAt = now
	switch {
	case err == nil:
		delivery.Status = schemas.AgentPushDeliveryStatusDelivered
		delivery.LastError = ""
	case errors.Is(err, errPushConfigMissing) || delivery.Attempts >= pushMaxAttempts:
		delivery.Status = schemas.AgentPushDeliveryStatusDead
		delivery.LastError = bounded(err.Error())
		if m.logger != nil {
			m.logger.Error("push delivery %s (agent=%s) dead-lettered after %d attempts: %s", delivery.ID, delivery.AgentName, delivery.Attempts, delivery.LastError)
		}
	default:
		delivery.LastError = bounded(err.Error())
		delivery.NextAttemptAt = now.Add(pushBackoff(delivery.Attempts))
	}
	outcome := startPhaseSpan(gateCtx, "a2a.push.db.outcome")
	updateErr := m.pushStore.UpdateAgentPushDeliveryOutcome(gateCtx, delivery, m.pushRelayID, leaseUntil)
	endPhaseSpan(outcome)
	if updateErr != nil {
		traceErr = updateErr
	}
	if updateErr != nil && m.logger != nil && m.ctx.Err() == nil {
		m.logger.Error("push relay failed to record delivery outcome for %s: %v", delivery.ID, updateErr)
	}
}

// pushBackoff returns the exponential delay before the next attempt.
func pushBackoff(attempts int) time.Duration {
	backoff := pushBaseBackoff << (attempts - 1)
	if backoff > pushMaxBackoff || backoff <= 0 {
		return pushMaxBackoff
	}
	return backoff
}

// newPushDeliveryClient creates the dedicated client for untrusted callback
// URLs. Address validation happens on every dial so DNS changes cannot bypass
// the private-network boundary between configuration and delivery. When
// allowPrivate is true the address check is skipped so loopback and private
// callbacks work; this is only for controlled test environments.
func newPushDeliveryClient(allowPrivate bool) *http.Client {
	dial := network.SSRFSafeDialContext(pushSendTimeout)
	if allowPrivate {
		dial = (&net.Dialer{Timeout: pushSendTimeout}).DialContext
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dial,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// sendPushDownstream re-originates one queued push to the client's real
// callback with the client's own credentials, following the SDK push sender's
// header conventions. The dedicated transport validates the resolved address
// on every dial, redirects are refused, and the attempt is time-bounded.
func (m *Manager) sendPushDownstream(ctx context.Context, config *schemas.AgentPushConfig, payload string) error {
	ctx, cancel := context.WithTimeout(ctx, pushSendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.URL, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create push delivery request: %w", err)
	}
	req.Header.Set("Content-Type", "application/a2a+json")
	if token := config.Token.GetValue(); token != "" {
		req.Header.Set(PushNotificationTokenHeader, token)
	}
	if credentials := config.AuthCredentials.GetValue(); credentials != "" {
		switch strings.ToLower(config.AuthScheme) {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+credentials)
		case "basic":
			req.Header.Set("Authorization", "Basic "+credentials)
		}
	}
	callStart := time.Now()
	defer func() { schemas.AddUpstreamLatency(ctx, time.Since(callStart)) }()
	resp, err := m.pushDeliveryClient.Do(req)
	if err != nil {
		return fmt.Errorf("send push delivery: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Status only: the body is downstream-controlled and must not reach logs.
		return fmt.Errorf("push delivery endpoint returned HTTP status %s", resp.Status)
	}
	return nil
}
