// Agent Gateway gRPC binding.
//
// The A2A gRPC service has no request path, so a shared listener cannot learn
// the target agent from the URL the way the HTTP bindings do. Instead each
// agent is advertised under its own hostname, <agent-name>.<base-domain>, all
// resolving to this server. Every conformant gRPC client sets the mandatory
// HTTP/2 :authority pseudo-header to the host it dialed, so the first DNS
// label of the request authority identifies the agent without any proprietary
// metadata. Agent names are already valid DNS labels by registration rule;
// names longer than 63 characters cannot form a label and are not served here.

package handlers

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	a2agrpc "github.com/a2aproject/a2a-go/v2/a2agrpc/v1"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AgentGatewayGRPCServer is the shared gRPC front door for every registered
// agent. It authenticates each request with the same virtual-key rules as the
// HTTP bindings, routes by the dialed authority, and delegates to the official
// SDK handler over the agent's shared RequestHandler, so governance, logging,
// leasing, and forwarding semantics are identical across all three bindings.
type AgentGatewayGRPCServer struct {
	a2apb.UnimplementedA2AServiceServer
	manager          *agent.Manager
	config           *lib.Config
	validator        AgentGatewayVirtualKeyValidator
	identityResolver AgentGatewayIdentityResolver
	tracing          *TracingMiddleware
	baseDomain       string
	server           *grpc.Server
}

// NewAgentGatewayGRPCServer wires the dispatcher and its authentication
// interceptors. Returns nil when a required dependency is missing, matching
// NewAgentGatewayHandler.
func NewAgentGatewayGRPCServer(manager *agent.Manager, config *lib.Config, validator AgentGatewayVirtualKeyValidator, baseDomain string, identityResolver AgentGatewayIdentityResolver, tracing *TracingMiddleware) *AgentGatewayGRPCServer {
	if manager == nil || config == nil || baseDomain == "" {
		return nil
	}
	s := &AgentGatewayGRPCServer{
		manager:          manager,
		config:           config,
		validator:        validator,
		identityResolver: identityResolver,
		tracing:          tracing,
		baseDomain:       strings.Trim(strings.ToLower(baseDomain), "."),
	}
	s.server = grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.unaryAuth),
		grpc.ChainStreamInterceptor(s.streamAuth),
	)
	a2apb.RegisterA2AServiceServer(s.server, s)
	return s
}

// Start begins serving on the configured port. Serving happens on a background
// goroutine; listener creation failures are returned so startup fails loudly.
func (s *AgentGatewayGRPCServer) Start(port int) error {
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logger.Error("agent gateway grpc server stopped: %v", err)
		}
	}()
	return nil
}

const agentGatewayGRPCGracefulStopTimeout = 5 * time.Second

// Stop drains in-flight RPCs up to a fixed deadline, then force-closes any
// streams that did not finish. Nil-safe so shutdown does not need to know
// whether gRPC was enabled.
func (s *AgentGatewayGRPCServer) Stop() {
	s.stop(agentGatewayGRPCGracefulStopTimeout)
}

func (s *AgentGatewayGRPCServer) stop(gracefulTimeout time.Duration) {
	if s == nil || s.server == nil {
		return
	}

	done := make(chan struct{})
	go func() {
		s.server.GracefulStop()
		close(done)
	}()

	timer := time.NewTimer(gracefulTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		s.server.Stop()
		<-done
	}
}

// agentGatewayGRPCDelegateKey carries the per-agent SDK handler resolved by the
// interceptor to the service method that will delegate to it.
type agentGatewayGRPCDelegateKey struct{}

// intercept performs the per-request work shared by both interceptors: resolve
// the target agent from the dialed authority, authenticate the caller, and
// derive the context the delegated SDK handler will run under. The returned
// cancel stops the Bifrost context's cancellation watcher when the RPC ends.
func (s *AgentGatewayGRPCServer) intercept(ctx context.Context) (context.Context, func(), error) {
	delegate, err := s.delegateFor(ctx)
	if err != nil {
		return nil, nil, err
	}
	authedCtx, cancel, err := s.authenticate(ctx)
	if err != nil {
		return nil, nil, err
	}
	return context.WithValue(authedCtx, agentGatewayGRPCDelegateKey{}, delegate), cancel, nil
}

func (s *AgentGatewayGRPCServer) unaryAuth(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	tracedCtx, finish := s.startTrace(ctx, info.FullMethod)
	authedCtx, cancel, err := s.intercept(tracedCtx)
	if err != nil {
		finish(err)
		return nil, err
	}
	defer cancel()
	// a2a.dispatch covers the SDK gRPC handler machinery, installed as the
	// active parent so manager and plugin phase spans subtract from its
	// self-time. Unary only: a stream handler is held open for the whole
	// stream, which would attribute upstream wait to dispatch.
	dispatchCtx, endDispatch := startGRPCDispatchSpan(authedCtx)
	resp, err := handler(dispatchCtx, req)
	endDispatch()
	finish(err)
	return resp, err
}

func (s *AgentGatewayGRPCServer) streamAuth(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	tracedCtx, finish := s.startTrace(ss.Context(), info.FullMethod)
	authedCtx, cancel, err := s.intercept(tracedCtx)
	if err != nil {
		finish(err)
		return err
	}
	defer cancel()
	err = handler(srv, &authenticatedServerStream{ServerStream: ss, ctx: authedCtx})
	finish(err)
	return err
}

// startTrace opens a per-RPC trace with a root span, mirroring what
// TracingMiddleware does for the HTTP bindings, and threads the tracer plus
// trace/span IDs onto the context so the A2A plugin gate's op span (and every
// plugin hook span) nests under it. The returned finish func ends the root span
// with the RPC outcome and flushes the trace to the observability connectors.
// The root span reuses SpanKindHTTPRequest so downstream root-span handling is
// uniform across bindings; the rpc.* attributes identify it as gRPC. A gRPC
// stream handler only returns once the stream has fully drained, so ending the
// root at return needs no deferred-completion machinery.
func (s *AgentGatewayGRPCServer) startTrace(ctx context.Context, fullMethod string) (context.Context, func(error)) {
	noop := func(error) {}
	if s.tracing == nil {
		return ctx, noop
	}
	tracer := s.tracing.GetTracer()
	if tracer == nil {
		return ctx, noop
	}
	requestID := uuid.New().String()
	traceID := tracer.CreateTrace("", requestID)
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyTraceID, traceID)
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyRequestID, requestID)
	spanCtx, rootSpan := tracer.StartSpan(ctx, fullMethod, schemas.SpanKindHTTPRequest)
	if rootSpan == nil {
		return ctx, func(error) { tracer.CompleteAndFlushTrace(traceID) }
	}
	tracer.SetAttribute(rootSpan, "rpc.system", "grpc")
	tracer.SetAttribute(rootSpan, "rpc.method", fullMethod)
	if spanCtx != nil {
		if spanID, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok && spanID != "" {
			ctx = context.WithValue(ctx, schemas.BifrostContextKeySpanID, spanID)
		}
	}
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyTracer, tracer)
	return ctx, func(err error) {
		if err != nil {
			tracer.EndSpan(rootSpan, schemas.SpanStatusError, err.Error())
		} else {
			tracer.EndSpan(rootSpan, schemas.SpanStatusOk, "")
		}
		tracer.CompleteAndFlushTrace(traceID)
	}
}

// startGRPCDispatchSpan opens the a2a.dispatch overhead phase span for a unary
// RPC and installs it as the active parent on the returned context, mirroring
// the HTTP binding's startDispatchSpan. The returned end func is always safe to
// call; both are no-ops when no tracer is on the context.
func startGRPCDispatchSpan(ctx context.Context) (context.Context, func()) {
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	if tracer == nil {
		return ctx, func() {}
	}
	id, h := tracer.StartSpanID(ctx, "a2a.dispatch", schemas.SpanKindInternal)
	if h == nil {
		return ctx, func() {}
	}
	return context.WithValue(ctx, schemas.BifrostContextKeySpanID, id), func() {
		tracer.EndSpan(h, schemas.SpanStatusOk, "")
	}
}

// authenticatedServerStream substitutes the authenticated context on a server
// stream; everything else forwards to the embedded stream.
type authenticatedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authenticatedServerStream) Context() context.Context { return s.ctx }

// delegateFor resolves the target agent from the request authority and wraps
// its shared RequestHandler in the official SDK gRPC handler.
func (s *AgentGatewayGRPCServer) delegateFor(ctx context.Context) (a2apb.A2AServiceServer, error) {
	name, err := s.agentNameFromAuthority(ctx)
	if err != nil {
		return nil, err
	}
	requestHandler, ok := s.manager.A2ARequestHandler(name)
	if !ok {
		return nil, status.Error(codes.NotFound, "agent not found")
	}
	return a2agrpc.NewHandler(requestHandler), nil
}

// agentNameFromAuthority extracts the agent name as the first DNS label of the
// dialed authority under the configured base domain. NotFound is deliberately
// uniform across a missing authority, a foreign host, and an unknown agent, so
// callers cannot probe the routing rule apart from agent existence.
func (s *AgentGatewayGRPCServer) agentNameFromAuthority(ctx context.Context) (string, error) {
	authority := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get(":authority"); len(values) > 0 {
			authority = values[0]
		}
	}
	host := authority
	if splitHost, _, err := net.SplitHostPort(authority); err == nil {
		host = splitHost
	}
	host = strings.Trim(strings.ToLower(host), ".")
	name, matched := strings.CutSuffix(host, "."+s.baseDomain)
	if !matched || name == "" || strings.Contains(name, ".") {
		return "", status.Error(codes.NotFound, "agent not found")
	}
	return name, nil
}

// authenticate applies the documented Agent Gateway identity rules to gRPC
// metadata: the first recognized Bifrost credential wins, an invalid selected
// credential is rejected without falling back, and anonymous access is allowed
// only while enforcement is disabled. The returned context carries the settled
// identity for governance and cleaned metadata for upstream forwarding.
func (s *AgentGatewayGRPCServer) authenticate(ctx context.Context) (context.Context, func(), error) {
	md, _ := metadata.FromIncomingContext(ctx)
	rawVirtualKey, credentialKey := resolveVirtualKeyFromGRPCMetadata(md)

	s.config.Mu.RLock()
	enforceAuth := s.config.ClientConfig != nil && s.config.ClientConfig.EnforceAuthOnInference
	s.config.Mu.RUnlock()

	errUnauthenticated := status.Error(codes.Unauthenticated, "authentication required")
	var userID string
	var acceptedCredential *schemas.AcceptedBifrostCredential
	if rawVirtualKey != "" {
		if s.validator == nil {
			return nil, nil, errUnauthenticated
		}
		vk, ok := s.validator.GetVirtualKey(ctx, rawVirtualKey)
		if !ok || vk == nil || !vk.IsActiveValue() {
			return nil, nil, errUnauthenticated
		}
		rawVirtualKey = vk.Value.GetValue()
		acceptedCredential = &schemas.AcceptedBifrostCredential{
			Header: credentialKey,
			Value:  firstGRPCMetadataValue(md, credentialKey),
		}
	} else if s.identityResolver != nil {
		authorization := firstGRPCMetadataValue(md, "authorization")
		if resolvedUserID, ok := s.identityResolver(ctx, authorization); ok {
			userID = resolvedUserID
			acceptedCredential = &schemas.AcceptedBifrostCredential{Header: "authorization", Value: authorization}
			credentialKey = "authorization"
		}
	}
	if rawVirtualKey == "" && userID == "" && enforceAuth {
		return nil, nil, errUnauthenticated
	}

	bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	bifrostCtx.SetValue(schemas.BifrostContextKeyA2ADownstreamTransport, string(a2a.TransportProtocolGRPC))
	requestHeaders := make(map[string]string, len(md))
	for key, values := range md {
		if len(values) > 0 {
			requestHeaders[strings.ToLower(key)] = values[0]
		}
	}
	bifrostCtx.SetValue(schemas.BifrostContextKeyRequestHeaders, requestHeaders)
	if acceptedCredential != nil {
		bifrostCtx.SetValue(schemas.BifrostContextKeyAcceptedCredential, *acceptedCredential)
	}
	if userID != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyUserID, userID)
	}
	if rawVirtualKey != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, rawVirtualKey)
	}
	lib.SettleIdentity(bifrostCtx)
	return metadata.NewIncomingContext(bifrostCtx, sanitizeGRPCMetadata(md, credentialKey)), bifrostCtx.Cancel, nil
}

func firstGRPCMetadataValue(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// resolveVirtualKeyFromGRPCMetadata mirrors lib.ResolveVirtualKeyFromHeaders
// over gRPC metadata, returning the credential and the metadata key it was
// selected from so exactly that key can be stripped before upstream forwarding.
func resolveVirtualKeyFromGRPCMetadata(md metadata.MD) (string, string) {
	get := func(key string) string { return firstGRPCMetadataValue(md, key) }
	if value := get(string(schemas.BifrostContextKeyVirtualKey)); value != "" {
		return value, string(schemas.BifrostContextKeyVirtualKey)
	}
	auth := get("authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token := strings.TrimSpace(auth[7:])
		if strings.HasPrefix(strings.ToLower(token), governance.VirtualKeyPrefix) {
			return token, "authorization"
		}
	}
	for _, key := range []string{"x-api-key", "x-goog-api-key", "api-key"} {
		if value := get(key); strings.HasPrefix(strings.ToLower(value), governance.VirtualKeyPrefix) {
			return value, key
		}
	}
	return "", ""
}

// sanitizeGRPCMetadata builds the metadata the SDK handler captures as service
// params and later forwards upstream as HTTP headers. HTTP/2 pseudo-headers and
// grpc-* transport metadata are not valid upstream headers, the selected
// credential must not cross the trust boundary, and x-bf-vk is always dropped
// to match the core's own upstream sanitization. a2a-version is dropped because
// the gateway's upstream client stamps its own protocol version.
func sanitizeGRPCMetadata(md metadata.MD, consumedKey string) metadata.MD {
	cleaned := metadata.MD{}
	for key, values := range md {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, ":") || strings.HasPrefix(lower, "grpc-") ||
			lower == consumedKey || lower == string(schemas.BifrostContextKeyVirtualKey) ||
			lower == "a2a-version" {
			continue
		}
		cleaned[lower] = values
	}
	return cleaned
}

// grpcDelegate returns the per-agent SDK handler stashed by the interceptor.
func grpcDelegate(ctx context.Context) (a2apb.A2AServiceServer, error) {
	delegate, ok := ctx.Value(agentGatewayGRPCDelegateKey{}).(a2apb.A2AServiceServer)
	if !ok {
		return nil, status.Error(codes.Internal, "agent delegate missing")
	}
	return delegate, nil
}

// The service methods below are mechanical delegation: the interceptor already
// resolved the agent and authenticated the caller, so each method forwards to
// the agent's official SDK handler under the derived context.

func (s *AgentGatewayGRPCServer) SendMessage(ctx context.Context, req *a2apb.SendMessageRequest) (*a2apb.SendMessageResponse, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.SendMessage(ctx, req)
}

func (s *AgentGatewayGRPCServer) SendStreamingMessage(req *a2apb.SendMessageRequest, stream grpc.ServerStreamingServer[a2apb.StreamResponse]) error {
	delegate, err := grpcDelegate(stream.Context())
	if err != nil {
		return err
	}
	return delegate.SendStreamingMessage(req, stream)
}

func (s *AgentGatewayGRPCServer) GetTask(ctx context.Context, req *a2apb.GetTaskRequest) (*a2apb.Task, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.GetTask(ctx, req)
}

func (s *AgentGatewayGRPCServer) ListTasks(ctx context.Context, req *a2apb.ListTasksRequest) (*a2apb.ListTasksResponse, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.ListTasks(ctx, req)
}

func (s *AgentGatewayGRPCServer) CancelTask(ctx context.Context, req *a2apb.CancelTaskRequest) (*a2apb.Task, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.CancelTask(ctx, req)
}

func (s *AgentGatewayGRPCServer) SubscribeToTask(req *a2apb.SubscribeToTaskRequest, stream grpc.ServerStreamingServer[a2apb.StreamResponse]) error {
	delegate, err := grpcDelegate(stream.Context())
	if err != nil {
		return err
	}
	return delegate.SubscribeToTask(req, stream)
}

func (s *AgentGatewayGRPCServer) CreateTaskPushNotificationConfig(ctx context.Context, req *a2apb.TaskPushNotificationConfig) (*a2apb.TaskPushNotificationConfig, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.CreateTaskPushNotificationConfig(ctx, req)
}

func (s *AgentGatewayGRPCServer) GetTaskPushNotificationConfig(ctx context.Context, req *a2apb.GetTaskPushNotificationConfigRequest) (*a2apb.TaskPushNotificationConfig, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.GetTaskPushNotificationConfig(ctx, req)
}

func (s *AgentGatewayGRPCServer) ListTaskPushNotificationConfigs(ctx context.Context, req *a2apb.ListTaskPushNotificationConfigsRequest) (*a2apb.ListTaskPushNotificationConfigsResponse, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.ListTaskPushNotificationConfigs(ctx, req)
}

func (s *AgentGatewayGRPCServer) DeleteTaskPushNotificationConfig(ctx context.Context, req *a2apb.DeleteTaskPushNotificationConfigRequest) (*emptypb.Empty, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.DeleteTaskPushNotificationConfig(ctx, req)
}

func (s *AgentGatewayGRPCServer) GetExtendedAgentCard(ctx context.Context, req *a2apb.GetExtendedAgentCardRequest) (*a2apb.AgentCard, error) {
	delegate, err := grpcDelegate(ctx)
	if err != nil {
		return nil, err
	}
	return delegate.GetExtendedAgentCard(ctx, req)
}
