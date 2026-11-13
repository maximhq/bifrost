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

// TraceIngestAuthenticator resolves who an ingest request acts as. Enterprise sets one to accept
// scoped API keys. Without one, requests are checked against the dashboard's own credentials.
type TraceIngestAuthenticator interface {
	AuthenticateTraceIngest(ctx *fasthttp.RequestCtx) (*traceingest.Principal, error)
}

// TraceIngestCredentials checks the dashboard's credentials. The auth middleware implements it.
// SDKs authenticate with what the dashboard uses: when dashboard auth is off, no credential is needed.
type TraceIngestCredentials interface {
	IsDashboardAuthActive() bool
	VerifyAdminCredentials(username, password string) bool
	VerifySessionToken(token string) bool
}

// TraceIngestHandler serves the endpoints observability SDKs push traces to. Each vendor adapter
// (Braintrust, Langfuse, OTLP) is a set of methods on it. The routes sit outside the LLM pipeline:
// no plugin runs and nothing is written to the gateway's own logs.
type TraceIngestHandler struct {
	service       *traceingest.Service
	config        *lib.Config
	logger        schemas.Logger
	authenticator TraceIngestAuthenticator
	credentials   TraceIngestCredentials
	prompts       TracePromptStore
	datasets      *datasets.Service
}

// NewTraceIngestHandler builds the handler over the shared ingest service.
func NewTraceIngestHandler(service *traceingest.Service, config *lib.Config, logger schemas.Logger) *TraceIngestHandler {
	return &TraceIngestHandler{service: service, config: config, logger: logger}
}

// SetCredentials sets the dashboard credential check. Without one every request is accepted, as
// the dashboard accepts every request when its auth is off.
func (h *TraceIngestHandler) SetCredentials(credentials TraceIngestCredentials) {
	h.credentials = credentials
}

// SetAuthenticator replaces how ingest credentials are resolved.
func (h *TraceIngestHandler) SetAuthenticator(authenticator TraceIngestAuthenticator) {
	h.authenticator = authenticator
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

// ingestCredentialHeaders are the headers SDKs carry their API key in: Authorization for most,
// api_key and arize-api-key for Arize, x-api-key for others.
var ingestCredentialHeaders = []string{"Authorization", "api_key", "arize-api-key", "x-api-key", "x-bf-api-key"}

// authenticate resolves who the request acts as, writing the error response when it cannot.
func (h *TraceIngestHandler) authenticate(ctx *fasthttp.RequestCtx) (*traceingest.Principal, bool) {
	var principal *traceingest.Principal
	var err error
	if h.authenticator != nil {
		principal, err = h.authenticator.AuthenticateTraceIngest(ctx)
	} else {
		// A request an earlier middleware already authenticated with an API key needs nothing more.
		if isAPIKey, _ := ctx.UserValue(schemas.IsAPIKeyAuthContextKey).(bool); isAPIKey {
			return traceingest.AdminPrincipal(), true
		}
		candidates := make([]string, 0, 2)
		for _, name := range ingestCredentialHeaders {
			if value := strings.TrimSpace(string(ctx.Request.Header.Peek(name))); value != "" {
				candidates = append(candidates, value)
			}
		}
		principal, err = h.authorize(candidates)
	}
	if err != nil {
		h.fail(ctx, err)
		return nil, false
	}
	return principal, true
}

// authorize checks the credentials a request carried against the dashboard's. Any one that holds
// is enough. A credential is accepted in every form SDKs send an API key in:
//
//   - "Basic base64(username:password)", which Langfuse sends as public key and secret key
//   - "Bearer <session token>", a dashboard session
//   - "username:password", bare or after "Bearer"
//   - "base64(username:password)", bare or after "Bearer", as the dashboard API takes it
func (h *TraceIngestHandler) authorize(candidates []string) (*traceingest.Principal, error) {
	if h.credentials == nil || !h.credentials.IsDashboardAuthActive() {
		return traceingest.AdminPrincipal(), nil
	}
	for _, candidate := range candidates {
		if h.credentialHolds(candidate) {
			return traceingest.AdminPrincipal(), nil
		}
	}
	return nil, traceingest.ErrUnauthorized
}

func (h *TraceIngestHandler) credentialHolds(value string) bool {
	scheme, rest, found := strings.Cut(strings.TrimSpace(value), " ")
	if !found {
		rest, scheme = scheme, ""
	}
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(scheme) {
	case "basic":
		username, password, ok := decodeUserPassword(rest)
		return ok && h.credentials.VerifyAdminCredentials(username, password)
	case "bearer", "":
	default:
		return false
	}
	if h.credentials.VerifySessionToken(rest) {
		return true
	}
	if username, password, ok := strings.Cut(rest, ":"); ok {
		return h.credentials.VerifyAdminCredentials(username, password)
	}
	username, password, ok := decodeUserPassword(rest)
	return ok && h.credentials.VerifyAdminCredentials(username, password)
}

// decodeUserPassword reads "username:password" out of its base64 form, padded or not.
func decodeUserPassword(encoded string) (string, string, bool) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(encoded); err == nil {
			return strings.Cut(string(decoded), ":")
		}
	}
	return "", "", false
}

// fail writes the status an SDK should see for an ingest error. A store failure is a 503, so the
// SDK retries: a resent part is stored once.
func (h *TraceIngestHandler) fail(ctx *fasthttp.RequestCtx, err error) {
	switch {
	case errors.Is(err, traceingest.ErrUnauthorized):
		ctx.Response.Header.Set("WWW-Authenticate", `Basic realm="bifrost"`)
		SendError(ctx, fasthttp.StatusUnauthorized, "Send your Bifrost dashboard username and password as the SDK's API key.")
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
