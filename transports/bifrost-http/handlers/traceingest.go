package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	// tracingPrefix is the root of every SDK-facing tracing route. It is "tracing", not "traces":
	// several SDKs treat "traces" as a path segment of their own and rewrite URLs that contain it.
	tracingPrefix = "/tracing"
	// traceUploadWindow is how long an upload URL handed to an SDK stays usable.
	traceUploadWindow = 15 * time.Minute
	// traceUploadMaxBytes caps one uploaded file.
	traceUploadMaxBytes = 25 << 20
)

// TraceIngestAuthenticator resolves the credential on an ingest request. The default checks
// tracing keys. Enterprise replaces it to accept scoped API keys too.
type TraceIngestAuthenticator interface {
	AuthenticateTraceIngest(ctx *fasthttp.RequestCtx, credential string) (*traceingest.Principal, error)
}

// TraceIngestHandler serves the endpoints observability SDKs push traces to. Each vendor adapter
// (Braintrust, Langfuse, OTLP) is a set of methods on it. The routes sit outside the LLM pipeline:
// no plugin runs and nothing is written to the gateway's own logs.
type TraceIngestHandler struct {
	service       *traceingest.Service
	config        *lib.Config
	logger        schemas.Logger
	authenticator TraceIngestAuthenticator
	forwarder     *traceingest.Forwarder
	prompts       TracePromptStore
	datasets      *datasets.Service
}

// NewTraceIngestHandler builds the handler over the shared ingest service.
func NewTraceIngestHandler(service *traceingest.Service, config *lib.Config, logger schemas.Logger) *TraceIngestHandler {
	return &TraceIngestHandler{service: service, config: config, logger: logger}
}

// SetAuthenticator replaces how ingest credentials are resolved.
func (h *TraceIngestHandler) SetAuthenticator(authenticator TraceIngestAuthenticator) {
	h.authenticator = authenticator
}

// SetForwarder sets the worker that replays received requests to a project's upstream vendor.
func (h *TraceIngestHandler) SetForwarder(forwarder *traceingest.Forwarder) {
	h.forwarder = forwarder
}

// forward queues the request for replay to each given project's upstream, when it has one. It is
// called after the request was stored, and cannot change the answer the SDK gets. path is the path
// below the adapter's base.
func (h *TraceIngestHandler) forward(ctx *fasthttp.RequestCtx, source, path string, projects ...*tables.TableTraceProject) {
	if h.forwarder == nil {
		return
	}
	var body []byte
	for _, project := range projects {
		if project == nil || project.Forward == nil || !project.Forward.Enabled {
			continue
		}
		if body == nil {
			// One copy for every target: fasthttp reuses the request buffer after the handler returns.
			body = append([]byte{}, ctx.PostBody()...)
		}
		request := traceingest.ForwardRequest{Source: source, Path: path, ContentType: string(ctx.Request.Header.ContentType()), Body: body}
		if parent := string(ctx.Request.Header.Peek("x-bt-parent")); parent != "" {
			request.Headers = map[string]string{"x-bt-parent": parent}
		}
		h.forwarder.Enqueue(project, request)
	}
}

// RegisterRoutes mounts every tracing ingest route.
func (h *TraceIngestHandler) RegisterRoutes(rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	upload := lib.ChainMiddlewares(h.receiveUpload, middlewares...)
	rt.PUT(tracingPrefix+"/uploads/{id}", upload)
	rt.POST(tracingPrefix+"/uploads/{id}", upload)
	for _, register := range traceIngestAdapters {
		register(h, rt, middlewares...)
	}
}

// traceIngestAdapters are the vendor adapters, each registered by its own file.
var traceIngestAdapters []func(h *TraceIngestHandler, rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware)

// traceIngestTimeout bounds the store work of one ingest request.
const traceIngestTimeout = 30 * time.Second

// ingestContext is the context store calls of an ingest request run under. It is not the request
// context: a commit must finish once it has started, even when the SDK hangs up, so the retry the
// SDK then sends finds its parts already stored.
func ingestContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), traceIngestTimeout)
}

// authenticate resolves the request's credential, writing the error response when it fails.
func (h *TraceIngestHandler) authenticate(ctx *fasthttp.RequestCtx, credential string) (*traceingest.Principal, bool) {
	var principal *traceingest.Principal
	var err error
	if h.authenticator != nil {
		principal, err = h.authenticator.AuthenticateTraceIngest(ctx, credential)
	} else {
		storeCtx, cancel := ingestContext()
		defer cancel()
		principal, err = h.service.Authenticate(storeCtx, credential)
	}
	if err != nil {
		h.fail(ctx, err)
		return nil, false
	}
	return principal, true
}

// fail writes the status an SDK should see for an ingest error. A store failure is a 503, so the
// SDK retries: a resent part is stored once.
func (h *TraceIngestHandler) fail(ctx *fasthttp.RequestCtx, err error) {
	switch {
	case errors.Is(err, traceingest.ErrUnauthorized):
		SendError(ctx, fasthttp.StatusUnauthorized, "A valid tracing key is required. Create one under Observability > Tracing.")
	case errors.Is(err, traceingest.ErrForbidden):
		SendError(ctx, fasthttp.StatusForbidden, err.Error())
	case errors.Is(err, traceingest.ErrRateLimited):
		ctx.Response.Header.Set("Retry-After", "1")
		SendError(ctx, fasthttp.StatusTooManyRequests, err.Error())
	case errors.Is(err, logstore.ErrTraceUploadsUnavailable):
		SendError(ctx, fasthttp.StatusNotImplemented, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, traceingest.ErrClosed):
		SendError(ctx, fasthttp.StatusServiceUnavailable, "trace ingest is not available, retry")
	default:
		if h.logger != nil {
			h.logger.Error("trace ingest failed: %v", err)
		}
		SendError(ctx, fasthttp.StatusServiceUnavailable, "trace ingest failed, retry")
	}
}

// bearerCredential reads a key from an Authorization header in any of the forms SDKs send:
// "Bearer <key>", "Basic base64(public:secret)" (the secret is the key), or the bare key.
func bearerCredential(header string) string {
	header = strings.TrimSpace(header)
	scheme, rest, found := strings.Cut(header, " ")
	if !found {
		return header
	}
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(scheme) {
	case "bearer":
		return rest
	case "basic":
		decoded, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return ""
		}
		_, secret, _ := strings.Cut(string(decoded), ":")
		return secret
	}
	return header
}

// publicBaseURL is the URL SDKs reach this Bifrost on, with no trailing slash. It is the configured
// external URL when there is one, else what the request itself was sent to.
func (h *TraceIngestHandler) publicBaseURL(ctx *fasthttp.RequestCtx) string {
	if h.config != nil {
		if configured := strings.TrimRight(h.config.GetMCPExternalClientURL(), "/"); configured != "" {
			return configured
		}
	}
	scheme := "http"
	if ctx.IsTLS() {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(string(ctx.Request.Header.Peek("X-Forwarded-Proto")), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := string(ctx.Host())
	if forwarded := strings.TrimSpace(strings.Split(string(ctx.Request.Header.Peek("X-Forwarded-Host")), ",")[0]); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

// newUpload records an upload an SDK is about to send and returns the URL to send it to. The id is
// random and unguessable, and the URL works once, for a short window.
func (h *TraceIngestHandler) newUpload(ctx *fasthttp.RequestCtx, project *tables.TableTraceProject, source, id, contentType, filename string) (*logstore.TraceUpload, string, error) {
	store := h.service.Store()
	if !store.TraceUploadsAvailable() {
		return nil, "", logstore.ErrTraceUploadsUnavailable
	}
	if id == "" {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return nil, "", err
		}
		id = hex.EncodeToString(raw)
	}
	upload := &logstore.TraceUpload{ID: id, ProjectID: project.ID, Source: source, ContentType: contentType, Filename: filename, Status: logstore.TraceUploadStatusPending}
	if project.RetentionDays != nil && *project.RetentionDays > 0 {
		upload.ExpiresAt = new(time.Now().UTC().AddDate(0, 0, *project.RetentionDays))
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	if err := store.CreateTraceUpload(storeCtx, upload); err != nil {
		return nil, "", err
	}
	return upload, h.publicBaseURL(ctx) + tracingPrefix + "/uploads/" + id, nil
}

// receiveUpload stores the bytes of an upload an SDK was handed a URL for. The URL is the
// credential: SDKs send the file with no auth header, as they do to a presigned object-store URL.
func (h *TraceIngestHandler) receiveUpload(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	store := h.service.Store()
	storeCtx, cancel := ingestContext()
	defer cancel()
	upload, err := store.FindTraceUpload(storeCtx, id)
	if err != nil || upload.Status != logstore.TraceUploadStatusPending || time.Since(upload.CreatedAt) > traceUploadWindow {
		SendError(ctx, fasthttp.StatusNotFound, "upload not found or no longer open")
		return
	}
	body := ctx.PostBody()
	if len(body) > traceUploadMaxBytes {
		SendError(ctx, fasthttp.StatusRequestEntityTooLarge, "upload is larger than the 25 MB limit")
		return
	}
	// The stored copy must outlive the request buffer, which fasthttp reuses.
	if err := store.SaveTraceUploadBytes(storeCtx, id, append([]byte(nil), body...)); err != nil {
		h.fail(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"id": id, "status": logstore.TraceUploadStatusDone})
}
