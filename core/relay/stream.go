package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// Stream is an upstream reply being relayed to the caller as it arrives. The caller (the HTTP
// transport) copies it with Read and must call Close when done. Bytes are forwarded unchanged;
// an SSE reply is framed on the side so usage can be read per event, and a reply priced by its
// size is counted as it passes. When the stream ends (EOF, a read error, or Close before EOF) the
// Relays post-hooks run exactly once with the final usage.
type Stream struct {
	StatusCode int
	Headers    map[string]string

	ctx     *schemas.BifrostContext
	body    io.ReadCloser
	reader  io.Reader // body behind the idle-timeout guard
	stop    func()    // stops the idle-timeout guard
	start   time.Time
	counted bool // billable route and a 2xx reply

	framer       *providerUtils.SSEFramer // nil when the reply is not observed per event
	runes        *providerUtils.RuneCounter
	hasUsage     func(event []byte) bool
	observe      func(event []byte) *schemas.RelayUsage
	final        func(bodyBytes, bodyRunes int64) *schemas.RelayUsage
	defaultUsage *schemas.RelayUsage
	usage        *schemas.RelayUsage
	bytesOut     int64

	capture []byte // bounded body prefix for the log row; nil when content logging is off

	once   sync.Once
	finish func(resp *schemas.BifrostRelayResponse, err *schemas.BifrostError)
}

// newStream wraps an open upstream reply. mapping and requestUnits decide what is read; finish runs
// the post-hooks. The returned stream buffers up to maxBufferedResponseBytes response bytes in
// capture for the log row, but only when the content-logging policy would keep the body, so a
// stream nobody logs the body of never allocates the buffer.
func newStream(ctx *schemas.BifrostContext, e *endpoint, resp *http.Response, start time.Time, mapping resolvedUsage, billable bool, requestUnits usageFields, finish func(*schemas.BifrostRelayResponse, *schemas.BifrostError)) *Stream {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, e.streamIdle)
	reader, stop := providerUtils.NewIdleTimeoutReader(resp.Body, resp.Body, providerUtils.GetStreamIdleTimeout(ctx), ctx)
	s := &Stream{
		StatusCode: resp.StatusCode,
		Headers:    e.responseHeaders(resp),
		ctx:        ctx,
		body:       resp.Body,
		reader:     reader,
		stop:       stop,
		start:      start,
		counted:    billable && resp.StatusCode >= 200 && resp.StatusCode < 300,
		finish:     finish,
	}
	if relayContentCaptureEnabled(ctx) {
		s.capture = make([]byte, 0, 4096)
	}
	if !s.counted {
		return s
	}
	s.defaultUsage = mapping.defaultUsage(requestUnits)
	observe, final := mapping.streamUsage(requestUnits)
	if isEventStream(resp.Header.Get("Content-Type")) {
		s.framer = &providerUtils.SSEFramer{}
		s.hasUsage = mapping.hasUsageEvent
		s.observe = observe
	}
	// A reply priced by its size is counted as it is forwarded, framed or not, so a streamed reply
	// bills what the buffered relay would bill for the same bytes.
	s.final = final
	if final != nil && mapping.unitsMeasure == schemas.RelayUnitsBodyChars {
		s.runes = &providerUtils.RuneCounter{}
	}
	return s
}

// relayContentCaptureEnabled resolves whether a streamed reply's bytes should be buffered for the
// log row, mirroring the logging plugin's resolveContentPolicy precedence (plugin default,
// overridden by governance's per-key decision, overridden by the per-request header) without
// importing plugins/logging: core/relay/span.go already reads
// BifrostContextKeyGovernanceDisableContentLogging directly for the same reason. Called from
// newStream, which runs after RunRelayPreHooks has completed every pre-hook including governance's,
// so this sees the final decision rather than the early, plugin-only one PreRelayHook could offer.
func relayContentCaptureEnabled(ctx *schemas.BifrostContext) bool {
	enabled, pluginPresent := ctx.Value(schemas.BifrostContextKeyRelayContentLoggingEnabledByDefault).(bool)
	if !pluginPresent {
		// No logging plugin stamped a default at all — either it is not registered, or its
		// PreRelayHook did not run. Governance's or the per-request key being present on ctx for
		// unrelated reasons must not turn capture on by itself; without a plugin to ever read the
		// buffer, that would just retain up to maxBufferedResponseBytes per stream for nothing.
		return false
	}
	if disabled, ok := ctx.Value(schemas.BifrostContextKeyGovernanceDisableContentLogging).(bool); ok {
		enabled = !disabled
	}
	if allowed, _ := ctx.Value(schemas.BifrostContextKeyAllowPerRequestStorageOverride).(bool); allowed {
		if disabled, ok := ctx.Value(schemas.BifrostContextKeyDisableContentLogging).(bool); ok {
			enabled = !disabled
		}
	}
	if !enabled {
		// Content logging is off for this call, but the logging plugin may still retain it hidden
		// in object storage; the reply is then captured for that offload, as a buffered one is.
		retained, _ := ctx.Value(schemas.BifrostContextKeyRelayContentRetainedWhenDisabled).(bool)
		return retained
	}
	return enabled
}

// Read forwards the next bytes of the reply.
func (s *Stream) Read(p []byte) (int, error) {
	// The body DoHTTPRequest handed over counts each chunk wait as upstream time itself.
	n, err := s.reader.Read(p)
	if n > 0 {
		s.bytesOut += int64(n)
		if s.runes != nil {
			s.runes.Write(p[:n])
		}
		if s.framer != nil {
			for _, event := range s.framer.Write(p[:n]) {
				s.observeEvent(event)
			}
		}
		// The capture keeps the whole reply for the post-hooks, as a buffered reply's body does, and
		// stops at the same bound the buffered relay holds (maxBufferedResponseBytes).
		if s.capture != nil && int64(len(s.capture)) < maxBufferedResponseBytes {
			remaining := int(maxBufferedResponseBytes) - len(s.capture)
			chunk := p[:n]
			if len(chunk) > remaining {
				chunk = chunk[:remaining]
			}
			s.capture = append(s.capture, chunk...)
		}
	}
	switch {
	case err == io.EOF:
		if s.framer != nil {
			if event := s.framer.Flush(); event != nil {
				s.observeEvent(event)
			}
		}
		s.complete(nil)
	case err != nil:
		s.complete(err)
	}
	return n, err
}

func (s *Stream) observeEvent(event []byte) {
	if s.hasUsage != nil && !s.hasUsage(event) {
		return
	}
	if u := s.observe(event); u != nil {
		s.usage = u
	}
}

// Close releases the upstream connection. Closing before EOF ends the stream as cancelled; the
// usage observed so far is still reported, so a caller cannot avoid the charge by hanging up.
func (s *Stream) Close() error {
	s.complete(context.Canceled)
	return s.body.Close()
}

// complete runs the post-hooks once with the final usage.
func (s *Stream) complete(readErr error) {
	s.once.Do(func() {
		s.stop()
		resp := &schemas.BifrostRelayResponse{
			StatusCode: s.StatusCode,
			Headers:    s.Headers,
			Streamed:   true,
			BytesOut:   s.bytesOut,
			Latency:    time.Since(s.start).Milliseconds(),
		}
		if len(s.capture) > 0 {
			resp.Body = s.capture
		}
		if s.counted {
			usage := s.usage
			if s.final != nil {
				var runes int64
				if s.runes != nil {
					runes = s.runes.Total()
				}
				if u := s.final(s.bytesOut, runes); u != nil {
					usage = u
				}
			}
			if usage == nil {
				usage = s.defaultUsage
			}
			resp.Usage = usage
		}
		var bifrostErr *schemas.BifrostError
		switch {
		case readErr == nil:
		case errors.Is(readErr, context.Canceled):
			bifrostErr = &schemas.BifrostError{
				IsBifrostError: false,
				Error:          &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: readErr},
			}
		default:
			bifrostErr = transportError(readErr, time.Since(s.start))
		}
		s.finish(resp, bifrostErr)
	})
}
