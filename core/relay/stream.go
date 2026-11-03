package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
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
	eofPending   bool // the last bytes arrived with EOF; the next Read reports it
	flushed      bool // the framer's trailing event has been read

	capture  []byte        // the whole body for the log row; nil when content logging is off
	activity *atomic.Int64 // the logging plugin's last-activity slot; nil without the plugin

	// A buffered reply past the large-payload response threshold (handOffLarge): the bytes read
	// while deciding are forwarded first, and the log row keeps a preview, as an LLM row does.
	ahead         []byte
	captureLimit  int
	largeResponse bool

	once   sync.Once
	finish func(resp *schemas.BifrostRelayResponse, err *schemas.BifrostError)
}

// newStream wraps an open upstream reply. mapping and requestUnits decide what is read; finish runs
// the post-hooks. The returned stream keeps the whole reply in capture for the log row, as the
// stream accumulator keeps an LLM stream, but only when the logging plugin asked for it, so a
// stream nobody logs the body of never allocates the buffer.
func newStream(ctx *schemas.BifrostContext, e *endpoint, resp *http.Response, start time.Time, mapping resolvedUsage, billable bool, requestUnits usageFields, finish func(*schemas.BifrostRelayResponse, *schemas.BifrostError)) *Stream {
	s := &Stream{
		StatusCode: resp.StatusCode,
		Headers:    e.streamResponseHeaders(resp),
		ctx:        ctx,
		body:       resp.Body,
		start:      start,
		counted:    billable && resp.StatusCode >= 200 && resp.StatusCode < 300,
		finish:     finish,
	}
	if relayContentCaptureEnabled(ctx) {
		s.capture = make([]byte, 0, 4096)
	}
	s.activity, _ = ctx.Value(schemas.BifrostContextKeyRelayActivity).(*atomic.Int64)
	if !s.counted {
		s.armIdleTimeout(ctx, e, resp)
		return s
	}
	s.defaultUsage = mapping.defaultUsage(requestUnits)
	observe, final := mapping.streamUsage(requestUnits)
	// Only a route that reads usage from the events frames them (eventPaths is every path an event
	// is probed for): a flat fee, request-side units or a whole-body measure never look inside an
	// event, so such a stream pays nothing to frame it.
	if len(mapping.eventPaths) > 0 && isEventStream(resp.Header.Get("Content-Type")) {
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
	s.armIdleTimeout(ctx, e, resp)
	return s
}

// armIdleTimeout starts the idle guard, a timer, on the stream's way out of newStream: only once the
// stream is built, so a panic while building it leaves no timer running.
func (s *Stream) armIdleTimeout(ctx *schemas.BifrostContext, e *endpoint, resp *http.Response) {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, e.streamIdle)
	s.reader, s.stop = providerUtils.NewIdleTimeoutReader(resp.Body, resp.Body, providerUtils.GetStreamIdleTimeout(ctx), ctx)
}

// relayContentCaptureEnabled reports whether the logging plugin asked for the streamed reply's
// bytes. The plugin owns the content policy (its static setting, governance's per-key or
// per-project decision, the per-request override, and retention in object storage) and resolves
// it in PreRelayHook exactly as PreLLMHook gates input capture: on while content is stored, and
// provisionally while governance has yet to decide, with the post-hook applying the final policy.
// core/relay keeps no copy of that precedence. Without the plugin the key is absent and nothing
// is captured, so a stream nobody logs never pays for the buffer.
func relayContentCaptureEnabled(ctx *schemas.BifrostContext) bool {
	capture, _ := ctx.Value(schemas.BifrostContextKeyRelayContentCapture).(bool)
	return capture
}

// largeResponsePreviewBytes is the most of a large reply the log row keeps, the preview an LLM row
// keeps of a large response (FinalizeResponseWithLargeDetection); a variable so tests can shorten it.
var largeResponsePreviewBytes = 1 << 20

// handOffLarge turns the stream into the forwarding of a buffered reply that passed the
// large-payload response threshold: ahead (what was read while deciding) goes out first, and the
// log row keeps only a preview, so the reply is never held whole.
func (s *Stream) handOffLarge(ahead []byte) {
	s.ahead = ahead
	s.largeResponse = true
	s.captureLimit = largeResponsePreviewBytes
}

// Read forwards the next bytes of the reply.
func (s *Stream) Read(p []byte) (int, error) {
	if s.eofPending {
		if err := s.finishEOF(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
	var n int
	var err error
	if len(s.ahead) > 0 {
		n = copy(p, s.ahead)
		s.ahead = s.ahead[n:]
	} else {
		// The body DoHTTPRequest handed over counts each chunk wait as upstream time itself.
		n, err = s.reader.Read(p)
	}
	if n > 0 {
		s.bytesOut += int64(n)
		if s.activity != nil {
			// One atomic store per chunk, so the logging plugin's stale sweep sees a live stream
			// the way PostLLMHook's per-chunk LastActivity keeps an LLM stream's pending row.
			s.activity.Store(time.Now().UnixNano())
		}
		if s.runes != nil {
			s.runes.Write(p[:n])
		}
		// The capture keeps the whole reply for the post-hooks, as a buffered reply's body does and
		// as the stream accumulator keeps an LLM stream; there is no bound of the relay's own. It is
		// taken before the events are read, so a chunk that fails the stream, which the caller still
		// receives, is in the logged body too, as an LLM row logs every chunk its caller was sent.
		if s.capture != nil {
			chunk := p[:n]
			if s.captureLimit > 0 {
				chunk = chunk[:min(len(chunk), max(s.captureLimit-len(s.capture), 0))]
			}
			s.capture = append(s.capture, chunk...)
		}
		if s.framer != nil {
			// Reading usage from the events is Bifrost's own work on the stream, as decoding an
			// LLM stream's events is (the breakdown's response-parse bucket).
			parseStart := time.Now()
			parseErr := s.observeChunk(p[:n])
			schemas.AddStreamParse(s.ctx, time.Since(parseStart))
			if parseErr != nil {
				// The bytes read are still handed over; the stream ends with the error (a panic in
				// the usage mapping).
				s.complete(parseErr)
				return n, parseErr
			}
		}
	}
	switch {
	case err == io.EOF && n > 0:
		// The last bytes arrived with EOF. They are returned on their own and EOF is reported on the
		// next Read, so a reader that stops at the first error still gets them, and the post-hooks
		// run only once the reader has taken the last bytes. The gateway reads ahead of its caller
		// (as a passthrough stream does), so there the post-hooks can run before the caller has
		// received them, as on an LLM stream.
		s.eofPending = true
		return n, nil
	case err == io.EOF:
		if flushErr := s.finishEOF(); flushErr != nil {
			return n, flushErr
		}
	case err != nil:
		s.complete(err)
	}
	return n, err
}

// finishEOF ends a stream the upstream completed. The last event, read only now when nothing
// followed it, is observed under the same recovery as observeChunk, so a panic there ends the
// stream as an internal error (returned) rather than escaping Read.
func (s *Stream) finishEOF() error {
	err := s.flushFramer()
	s.complete(err)
	return err
}

// flushFramer observes the event the framer still holds at EOF, once; see observeChunk.
func (s *Stream) flushFramer() (err error) {
	if s.framer == nil || s.flushed {
		return nil
	}
	s.flushed = true
	defer func() {
		if rec := recover(); rec != nil {
			err = streamPanic{value: rec}
		}
	}()
	if event := s.framer.Flush(); event != nil {
		s.observeEvent(event)
	}
	return nil
}

// observeChunk feeds a chunk's complete events to the usage mapping. A panic in it is Bifrost's own
// fault, not the caller's or the upstream's, so it comes back as a streamPanic. No event is too
// large: the framer holds one until its delimiter arrives, as relays enforce no size cap.
func (s *Stream) observeChunk(chunk []byte) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = streamPanic{value: rec}
		}
	}()
	for _, event := range s.framer.Write(chunk) {
		s.observeEvent(event)
	}
	return nil
}

// streamPanic is a panic recovered while reading a stream's usage.
type streamPanic struct{ value any }

func (p streamPanic) Error() string {
	return fmt.Sprintf("panic reading relay stream usage: %v", p.value)
}

func (s *Stream) observeEvent(event []byte) {
	if s.hasUsage != nil && !s.hasUsage(event) {
		return
	}
	if u := s.observe(event); u != nil {
		s.usage = u
	}
}

// finalUsage is the usage the stream is charged: what its events reported, a size-priced figure, or
// the route's default. A panic computing it is returned as a streamPanic and charges the default.
func (s *Stream) finalUsage() (usage *schemas.RelayUsage, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			usage, err = s.defaultUsage, streamPanic{value: rec}
		}
	}()
	usage = s.usage
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
	return usage, nil
}

// Close releases the upstream connection. Closing before EOF ends the stream as cancelled; the
// usage observed so far is still reported, so a caller cannot avoid the charge by hanging up. When
// the upstream had already ended (its last bytes came with EOF and the reader left before asking
// for more), the trailing event it left undelimited is complete and is read too, as finishEOF reads
// it; before that, what the framer holds is a partial event and is never read.
func (s *Stream) Close() error {
	var err error = context.Canceled
	if s.eofPending {
		if flushErr := s.flushFramer(); flushErr != nil {
			err = flushErr
		}
	}
	s.complete(err)
	return s.body.Close()
}

// complete runs the post-hooks once with the final usage.
func (s *Stream) complete(readErr error) {
	s.once.Do(func() {
		s.stop()
		resp := &schemas.BifrostRelayResponse{
			StatusCode:    s.StatusCode,
			Headers:       s.Headers,
			Streamed:      true,
			LargeResponse: s.largeResponse,
			BytesOut:      s.bytesOut,
			Latency:       time.Since(s.start).Milliseconds(),
		}
		if len(s.capture) > 0 {
			resp.Body = s.capture
		}
		if s.counted {
			usage, usageErr := s.finalUsage()
			if usageErr != nil && readErr == nil {
				readErr = usageErr
			}
			resp.Usage = usage
		}
		var bifrostErr *schemas.BifrostError
		var panicked streamPanic
		switch {
		case readErr == nil:
		case errors.As(readErr, &panicked):
			// Bifrost's own fault: classified bifrost_internal, never a caller cancellation.
			bifrostErr = &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "failed to read the relay stream's usage", Error: readErr}}
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
