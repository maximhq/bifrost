package relay

import (
	"fmt"
	"net/http"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// relaySpan is the operation span of one relay call: relay.<name>, started once the pre-hooks
// have admitted the call and ended when the reply (or the stream) has ended, so the trace shows
// the upstream call between the hook spans as it does for MCP tools and Agent operations. Nil
// when no tracer is on the context.
type relaySpan struct {
	tracer schemas.Tracer
	handle schemas.SpanHandle
	spanID string
	// raw is whether a connector asked for raw bodies and content logging is on for this call, as
	// the Agent span decides it; request is the caller's body kept for that export.
	raw     bool
	request []byte
	// callStart is when the call entered the gateway (before the pre-hooks), so the call's
	// duration covers a call the pre-hooks refused, which never reaches the operation itself.
	callStart time.Time
}

// startRelaySpan opens the operation span under the gate-entry parent, so it is not a child of
// the last (already ended) pre-hook span, and threads its id onto ctx so the post-hook spans nest
// under it.
func startRelaySpan(ctx *schemas.BifrostContext, entrySpanID any, req *schemas.BifrostRelayRequest, callStart time.Time) *relaySpan {
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	if tracer == nil || req == nil {
		return nil
	}
	ctx.SetValue(schemas.BifrostContextKeySpanID, entrySpanID)
	spanCtx, handle := tracer.StartSpan(ctx, fmt.Sprintf("relay.%s", req.RelayName), schemas.SpanKindRelay)
	if handle == nil {
		return nil
	}
	if spanCtx != nil {
		if id, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok && id != "" {
			ctx.SetValue(schemas.BifrostContextKeySpanID, id)
		}
	}
	tracer.SetAttribute(handle, schemas.AttrBifrostRelayName, req.RelayName)
	tracer.SetAttribute(handle, schemas.AttrHTTPRequestMethod, req.Method)
	tracer.SetAttribute(handle, schemas.AttrBifrostRelayRoutePath, req.RoutePath)
	tracer.SetAttribute(handle, schemas.AttrBifrostRelayBillable, req.Billable)
	tracer.SetAttribute(handle, schemas.AttrBifrostRelayStreamed, req.Streams)
	s := &relaySpan{tracer: tracer, handle: handle, callStart: callStart}
	s.spanID, _ = ctx.Value(schemas.BifrostContextKeySpanID).(string)
	// Raw bodies ride the span's raw-payload slot, never its attributes, so a connector exports
	// them only through its own raw-payload opt-in and content controls, exactly as Agent bodies.
	if consumer, ok := tracer.(interface{ A2AContentDemand() (bool, bool) }); ok {
		_, s.raw = consumer.A2AContentDemand()
	}
	if disabled, _ := ctx.Value(schemas.BifrostContextKeyGovernanceDisableContentLogging).(bool); disabled {
		s.raw = false
	}
	// Bodies are attached whole: the relay keeps no size bound of its own on what it records.
	if s.raw && len(req.Body) > 0 {
		s.request = req.Body
	}
	return s
}

// end records the outcome and closes the span. resp may be nil on a transport failure.
func (s *relaySpan) end(ctx *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bifrostErr *schemas.BifrostError) {
	if s == nil {
		return
	}
	if resp != nil {
		s.tracer.SetAttribute(s.handle, schemas.AttrHTTPResponseStatusCode, resp.StatusCode)
		s.tracer.SetAttribute(s.handle, schemas.AttrBifrostRelayBytesOut, resp.BytesOut)
		s.tracer.SetAttribute(s.handle, schemas.AttrBifrostRelayStreamed, resp.Streamed)
		s.tracer.SetAttribute(s.handle, schemas.AttrBifrostRelayCounted, resp.Usage != nil)
		if resp.Usage != nil {
			s.tracer.SetAttribute(s.handle, schemas.AttrBifrostRelayCost, resp.Usage.Cost)
		}
	} else {
		// No reply was streamed: the start marked the span with the route's stream mode, so a call
		// that failed before any reply would otherwise read as a stream in the overhead breakdown.
		s.tracer.SetAttribute(s.handle, schemas.AttrBifrostRelayStreamed, false)
		if bifrostErr != nil && bifrostErr.StatusCode != nil {
			s.tracer.SetAttribute(s.handle, schemas.AttrHTTPResponseStatusCode, *bifrostErr.StatusCode)
		}
	}
	// Fractional milliseconds from the context, as the root span takes them, and on a failed
	// call too: time spent before a transport error is still time on the upstream.
	if upstream, ok := schemas.GetUpstreamLatency(ctx); ok && upstream > 0 {
		s.tracer.SetAttribute(s.handle, schemas.AttrBifrostRelayUpstreamDurationMs, float64(upstream)/float64(time.Millisecond))
	}
	if !s.callStart.IsZero() {
		s.tracer.SetAttribute(s.handle, schemas.AttrBifrostRelayDurationMs, float64(time.Since(s.callStart))/float64(time.Millisecond))
	}
	setRelayGovernanceSpanAttrs(s.tracer, s.handle, ctx)
	// Governance's decision for the key or project lands after the span started, so it is read
	// again here before any body is attached.
	if disabled, _ := ctx.Value(schemas.BifrostContextKeyGovernanceDisableContentLogging).(bool); disabled {
		s.raw = false
	}
	if s.raw {
		if span := s.tracer.SpanFromHandle(s.handle); span != nil {
			var response string
			if resp != nil && len(resp.Body) > 0 {
				response = string(resp.Body)
			}
			span.SetA2ARawPayloads(s.spanID, &schemas.A2ASpanData{RawRequest: string(s.request), RawResponse: response})
		}
	}
	switch {
	case bifrostErr != nil:
		msg := ""
		if bifrostErr.Error != nil {
			msg = bifrostErr.Error.Message
		}
		s.tracer.SetAttribute(s.handle, schemas.AttrErrorTypeSpec, relayErrorType(bifrostErr))
		s.tracer.EndSpan(s.handle, schemas.SpanStatusError, msg)
	case resp != nil && resp.StatusCode >= http.StatusBadRequest:
		// The upstream's status is classified as an LLM provider status is (rate limited, auth,
		// server error, ...), so error_type is a failure class and never a bare code.
		code := resp.StatusCode
		s.tracer.SetAttribute(s.handle, schemas.AttrErrorTypeSpec, relayErrorType(&schemas.BifrostError{StatusCode: &code}))
		s.tracer.EndSpan(s.handle, schemas.SpanStatusError, fmt.Sprintf("HTTP %d", resp.StatusCode))
	default:
		s.tracer.EndSpan(s.handle, schemas.SpanStatusOk, "")
	}
}

// relayErrorType classifies a failed call for the error.type attribute and metric label the way
// LLM calls are classified (ClassifyErrorType): a governance decision (budget exceeded, rate
// limited, access denied), a timeout or cancellation, or the upstream's failure class, rather than
// a bare status code.
func relayErrorType(bifrostErr *schemas.BifrostError) string {
	if t := schemas.ClassifyErrorType(bifrostErr, schemas.RelayRequest); t != "" {
		return string(t)
	}
	return "_OTHER"
}

// setRelayGovernanceSpanAttrs copies the governance identity onto the span, as the MCP and
// Agent operation spans carry it, so the metrics derived from the span can be cut by it. The
// identity is read from the Grant, the settled source, never from the deprecated context copies.
func setRelayGovernanceSpanAttrs(tracer schemas.Tracer, handle schemas.SpanHandle, ctx *schemas.BifrostContext) {
	if tracer == nil || ctx == nil {
		return
	}
	grant := ctx.Grant()
	if grant == nil {
		return
	}
	identity := grant.Identity()
	if identity == nil {
		return
	}
	setRef := func(ref *schemas.EntityRef, idKey, nameKey string) {
		if ref == nil {
			return
		}
		if ref.ID != "" {
			tracer.SetAttribute(handle, idKey, ref.ID)
		}
		if ref.Name != "" {
			tracer.SetAttribute(handle, nameKey, ref.Name)
		}
	}
	first := func(refs []schemas.EntityRef) *schemas.EntityRef {
		if len(refs) == 0 {
			return nil
		}
		return &refs[0]
	}
	setRef(identity.VirtualKey(), schemas.AttrBifrostVirtualKeyID, schemas.AttrBifrostVirtualKeyName)
	setRef(first(identity.Teams()), schemas.AttrBifrostTeamID, schemas.AttrBifrostTeamName)
	setRef(first(identity.Customers()), schemas.AttrBifrostCustomerID, schemas.AttrBifrostCustomerName)
	setRef(first(identity.BusinessUnits()), schemas.AttrBifrostBusinessUnitID, schemas.AttrBifrostBusinessUnitName)
	setRef(identity.Project(), schemas.AttrBifrostProjectID, schemas.AttrBifrostProjectName)
}
