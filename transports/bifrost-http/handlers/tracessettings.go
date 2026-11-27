package handlers

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	// traceForwardSecretMask stands in for a forward header value in every API response. Sent back
	// unchanged on an update, it means "keep the stored value".
	traceForwardSecretMask = "********"
	maxTraceMaskingRules   = 50
)

func init() {
	validateTraceProjectSettings = validateTraceMaskingAndForward
	tracesHandlerExtensions = append(tracesHandlerExtensions, func(h *TracesHandler, r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		r.DELETE("/api/traces/trace/{project_id}/{trace_id}", lib.ChainMiddlewares(h.deleteTrace, middlewares...))
	})
}

// validateTraceMaskingAndForward checks a project's masking rules and forward target before they
// are stored. A masking pattern that does not compile is refused here, so a rule the user believes
// is protecting them is never silently skipped at ingest.
func validateTraceMaskingAndForward(payload *traceProjectPayload) error {
	if len(payload.MaskingRules) > maxTraceMaskingRules {
		return fmt.Errorf("a project can have at most %d masking rules", maxTraceMaskingRules)
	}
	for i := range payload.MaskingRules {
		rule := &payload.MaskingRules[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.Pattern == "" {
			return fmt.Errorf("masking rule %d has no pattern", i+1)
		}
		compiled, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return fmt.Errorf("masking rule %d is not a valid regular expression: %v", i+1, err)
		}
		if compiled.MatchString("") {
			return fmt.Errorf("masking rule %d matches empty text, which would rewrite every value", i+1)
		}
	}
	forward := payload.Forward
	if forward == nil {
		return nil
	}
	switch forward.Vendor {
	case logstore.TraceSourceBraintrust, logstore.TraceSourceLangfuse, logstore.TraceSourceArize, logstore.TraceSourceOTel:
	default:
		return fmt.Errorf("forward.vendor must be braintrust, langfuse, arize or otel")
	}
	forward.BaseURL = strings.TrimSpace(forward.BaseURL)
	if forward.BaseURL == "" {
		return fmt.Errorf("forward.base_url is required")
	}
	if err := configstore.ValidatePublicBaseURL(forward.BaseURL); err != nil {
		return fmt.Errorf("forward.base_url %v", err)
	}
	for name := range forward.Headers {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \r\n:") {
			return fmt.Errorf("forward header name %q is not valid", name)
		}
	}
	return nil
}

// redactTraceForward hides the forward target's header values, which hold the user's vendor key.
func redactTraceForward(project *tables.TableTraceProject) {
	if project == nil || project.Forward == nil || len(project.Forward.Headers) == 0 {
		return
	}
	forward := *project.Forward
	forward.Headers = make(map[string]string, len(project.Forward.Headers))
	for name := range project.Forward.Headers {
		forward.Headers[name] = traceForwardSecretMask
	}
	project.Forward = &forward
}

// keepStoredForwardSecrets replaces each masked header value of an update with the stored value.
// The dashboard never holds the real value, so the mask is what it sends for a header left alone.
func keepStoredForwardSecrets(incoming, stored *tables.TraceForwardTarget) {
	if incoming == nil {
		return
	}
	for name, value := range incoming.Headers {
		if value != traceForwardSecretMask {
			continue
		}
		if stored != nil && stored.Headers[name] != "" {
			incoming.Headers[name] = stored.Headers[name]
		} else {
			delete(incoming.Headers, name)
		}
	}
}

// deleteTrace removes one trace: its parts, spans, links, scores, comments and rollup.
func (h *TracesHandler) deleteTrace(ctx *fasthttp.RequestCtx) {
	projectID, _ := ctx.UserValue("project_id").(string)
	traceID, _ := ctx.UserValue("trace_id").(string)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	spans, err := h.store.ListTraceSpans(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "delete trace", err)
		return
	}
	if len(spans) == 0 {
		SendError(ctx, fasthttp.StatusNotFound, "trace not found")
		return
	}
	if err := h.store.DeleteTraces(storeCtx, []logstore.TraceKey{{ProjectID: projectID, TraceID: traceID}}); err != nil {
		h.fail(ctx, "delete trace", err)
		return
	}
	SendJSON(ctx, map[string]any{"deleted": true})
}
