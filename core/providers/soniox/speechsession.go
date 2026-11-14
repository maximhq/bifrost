package soniox

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ws "github.com/fasthttp/websocket"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var _ schemas.SpeechSessionProvider = (*SonioxProvider)(nil)

const (
	speechSessionEventBuffer  = 64
	speechSessionMaxChars     = 5000             // characters Soniox accepts in one text message
	speechSessionMaxStreams   = 5                // streams Soniox allows open on one connection
	speechSessionMaxHeldBytes = 8 << 20          // audio held behind an earlier segment
	speechSessionMaxDeferred  = 1 << 20          // text held while every stream slot is taken
	speechSessionKeepAlive    = 20 * time.Second // Soniox closes a connection idle for 40 seconds
	speechSessionStreamIdle   = 3 * time.Second  // Soniox fails a stream that gets no text for a few seconds
	speechSessionRawRate      = 24000            // the rate Soniox gives raw audio when none is requested
)

var (
	sonioxKeepAliveFrame = []byte(`{"keep_alive":true}`)

	errSpeechSessionClosed = errors.New("soniox speech session is closed")
	errSpeechSessionEnded  = errors.New("soniox speech session has ended its input")
)

// OpenSpeechSession opens a text-to-speech WebSocket. Soniox has no flush, so a stream is one segment: Flush ends
// the stream and the next text opens another. Streams open lazily because Soniox fails one that gets no text.
func (provider *SonioxProvider) OpenSpeechSession(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest, dial schemas.SpeechSessionDialer) (schemas.SpeechSession, *schemas.BifrostError) {
	speechRequest := ToSonioxSpeechRequest(request)
	if speechRequest == nil {
		return nil, providerUtils.NewBifrostBadRequestError("a speech request is required to open a speech session")
	}
	headers, bifrostErr := provider.RealtimeHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	conn, bifrostErr := dial(ctx, ttsWebSocketURL(provider.ttsBaseURL), headers)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	config := SonioxTTSStreamConfig{
		Model:             speechRequest.Model,
		Language:          speechRequest.Language,
		Voice:             speechRequest.Voice,
		AudioFormat:       speechRequest.AudioFormat,
		SampleRate:        speechRequest.SampleRate,
		Bitrate:           speechRequest.Bitrate,
		Speed:             speechRequest.Speed,
		ReduceSilence:     speechRequest.ReduceSilence,
		ClientReferenceID: speechRequest.ClientReferenceID,
		ExtraParams:       speechRequest.ExtraParams,
	}
	format := "mp3"
	if request.Params != nil {
		config.ReturnTimestamps = request.Params.WithTimestamps
		if request.Params.ResponseFormat != "" {
			format = request.Params.ResponseFormat
		}
	}
	// Raw audio has no header to carry its rate, so the session sends it and tells the client.
	if config.SampleRate == nil && strings.HasPrefix(config.AudioFormat, "pcm_") {
		config.SampleRate = new(speechSessionRawRate)
	}
	sampleRate := 0
	if config.SampleRate != nil {
		sampleRate = *config.SampleRate
	}

	session := &sonioxSpeechSession{
		conn:        conn,
		config:      config,
		passthrough: ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true,
		format:      format,
		sampleRate:  sampleRate,
		events:      make(chan *schemas.BifrostSpeechSessionEvent, speechSessionEventBuffer),
		done:        make(chan struct{}),
		lastWrite:   time.Now(),
	}
	session.wg.Add(2)
	go session.readLoop()
	go session.keepAliveLoop()
	return session, nil
}

// speechOutEvent is an event ready to send. stream is the stream its audio belongs to, nil for the others.
type speechOutEvent struct {
	event  *schemas.BifrostSpeechSessionEvent
	stream *speechStream
}

// speechStream is one Soniox stream, which is one segment of the session.
type speechStream struct {
	id         string
	segment    int
	ended      bool                                 // text_end was sent
	terminated bool                                 // Soniox answered with terminated
	held       []*schemas.BifrostSpeechSessionEvent // audio received while an earlier segment was still playing
	idle       *time.Timer
	lastText   time.Time
	cancelled  atomic.Bool // set under the session lock, read by a sender that holds only sendMu
}

type speechOpKind int

const (
	speechOpText speechOpKind = iota
	speechOpFlush
)

// speechOp is a call that arrived while every stream slot was taken, to be applied in order when one frees.
type speechOp struct {
	kind speechOpKind
	text string
}

// sonioxSpeechSession implements schemas.SpeechSession over one TTS WebSocket.
//
// mu guards the stream state and serializes writes to Soniox. sendMu orders the events: a sender takes it
// before it releases mu, so events leave in the order their state changes happened, and a cancel's
// acknowledgement can never be overtaken by audio of the streams it dropped.
type sonioxSpeechSession struct {
	conn        schemas.RealtimeUpstream
	config      SonioxTTSStreamConfig // template; the stream id is set per stream
	passthrough bool                  // merge the extra params into each stream config
	format      string
	sampleRate  int

	events    chan *schemas.BifrostSpeechSessionEvent
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	sendMu    sync.Mutex
	closed    atomic.Bool // set by Close without mu, which a blocked sender may hold

	mu            sync.Mutex
	streams       []*speechStream // live streams in segment order; the first one's audio is the one sent
	dropped       []*speechStream // cancelled streams whose frames are discarded until they terminate
	current       *speechStream   // the stream taking text; nil until the next text opens one
	deferred      []speechOp
	deferredBytes int
	heldBytes     int
	streamCount   int // streams opened, which names the next one
	segments      int // segments opened, the number of the latest
	ending        bool
	finished      bool
	err           *schemas.BifrostError
	lastWrite     time.Time

	frameOut []speechOutEvent // used by the reader alone, reused for every frame
}

func (s *sonioxSpeechSession) AudioFormat() (string, int) {
	return s.format, s.sampleRate
}

func (s *sonioxSpeechSession) Events() <-chan *schemas.BifrostSpeechSessionEvent {
	return s.events
}

func (s *sonioxSpeechSession) Err() *schemas.BifrostError {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the session and waits for its goroutines.
func (s *sonioxSpeechSession) Close() error {
	s.closed.Store(true)
	s.shutdown()
	s.wg.Wait()
	return nil
}

// shutdown closes the connection, which ends the reader, and releases every blocked sender.
func (s *sonioxSpeechSession) shutdown() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.conn.Close()
	})
}

// failLocked ends the session with err unless it has already ended or the client closed it.
func (s *sonioxSpeechSession) failLocked(err *schemas.BifrostError) {
	if s.err == nil && !s.finished && !s.closed.Load() {
		s.err = err
	}
	s.shutdown()
}

// openLocked reports why the session can take no call at all.
func (s *sonioxSpeechSession) openLocked() error {
	if s.closed.Load() || s.finished || s.err != nil {
		return errSpeechSessionClosed
	}
	return nil
}

// writableLocked reports why the session can take no more input.
func (s *sonioxSpeechSession) writableLocked() error {
	if err := s.openLocked(); err != nil {
		return err
	}
	if s.ending {
		return errSpeechSessionEnded
	}
	return nil
}

func (s *sonioxSpeechSession) SendText(text string) error {
	if text == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	return s.sendTextLocked(text, false)
}

func (s *sonioxSpeechSession) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	return s.flushLocked(false)
}

func (s *sonioxSpeechSession) End() error {
	s.mu.Lock()
	if err := s.writableLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.flushLocked(false); err != nil {
		s.mu.Unlock()
		return err
	}
	s.ending = true
	out := s.maybeFinishLocked(nil)
	finished := s.finished
	s.unlockAndEmit(out)
	if finished {
		s.shutdown()
	}
	return nil
}

// Cancel cancels every live stream and drops what they and the text waiting for a slot would have said.
func (s *sonioxSpeechSession) Cancel() error {
	s.mu.Lock()
	if err := s.openLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	for _, stream := range s.streams {
		stream.cancelled.Store(true)
		stream.idle.Stop()
		stream.held = nil
		if stream.terminated {
			continue
		}
		if err := s.writeJSONLocked(&SonioxTTSCancel{StreamID: stream.id, Cancel: true}); err != nil {
			s.mu.Unlock()
			return err
		}
		s.dropped = append(s.dropped, stream)
	}
	s.streams, s.current = nil, nil
	s.heldBytes, s.deferred, s.deferredBytes = 0, nil, 0
	out := []speechOutEvent{{event: &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventCancelled, Segment: s.segments}}}
	out = s.maybeFinishLocked(out)
	finished := s.finished
	s.unlockAndEmit(out)
	if finished {
		s.shutdown()
	}
	return nil
}

// sendTextLocked writes text to the current stream, opening one if there is none. While every stream slot is
// taken, or earlier calls are already waiting for one, the text waits too so the order is kept.
func (s *sonioxSpeechSession) sendTextLocked(text string, replay bool) error {
	if !replay && (len(s.deferred) > 0 || (s.current == nil && s.slotsFullLocked())) {
		if s.deferredBytes+len(text) > speechSessionMaxDeferred {
			s.failLocked(providerUtils.NewBifrostOperationError("soniox speech session held too much text while its streams were busy", nil))
			return errSpeechSessionClosed
		}
		s.deferred = append(s.deferred, speechOp{kind: speechOpText, text: text})
		s.deferredBytes += len(text)
		return nil
	}
	stream := s.current
	if stream == nil {
		var err error
		if stream, err = s.openStreamLocked(); err != nil {
			return err
		}
	}
	for text != "" {
		chunk, rest := text, ""
		if len(text) > speechSessionMaxChars {
			// Soniox counts characters, so the split lands on a rune boundary.
			count := 0
			for i := range text {
				if count == speechSessionMaxChars {
					chunk, rest = text[:i], text[i:]
					break
				}
				count++
			}
		}
		if err := s.writeJSONLocked(&SonioxTTSText{StreamID: stream.id, Text: chunk}); err != nil {
			return err
		}
		text = rest
	}
	stream.lastText = time.Now()
	stream.idle.Reset(speechSessionStreamIdle)
	return nil
}

// slotsFullLocked reports whether Soniox holds as many streams as it allows, counting the cancelled ones it has
// not yet terminated.
func (s *sonioxSpeechSession) slotsFullLocked() bool {
	open := len(s.dropped)
	for _, stream := range s.streams {
		if !stream.terminated {
			open++
		}
	}
	return open >= speechSessionMaxStreams
}

// flushLocked ends the current stream, which makes Soniox speak what it holds.
func (s *sonioxSpeechSession) flushLocked(replay bool) error {
	if !replay && len(s.deferred) > 0 {
		// A flush right after a flush ends nothing more, so it is queued once; the queue is bounded by its text.
		if s.deferred[len(s.deferred)-1].kind != speechOpFlush {
			s.deferred = append(s.deferred, speechOp{kind: speechOpFlush})
		}
		return nil
	}
	stream := s.current
	if stream == nil {
		return nil
	}
	stream.idle.Stop()
	stream.ended = true
	s.current = nil
	return s.writeJSONLocked(&SonioxTTSText{StreamID: stream.id, TextEnd: true})
}

// endIdleStream ends a stream that has had no text for a while, before Soniox fails it.
func (s *sonioxSpeechSession) endIdleStream(stream *speechStream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The timer may have fired just before a text reset it.
	if s.current == stream && s.openLocked() == nil && time.Since(stream.lastText) >= speechSessionStreamIdle {
		_ = s.flushLocked(true)
	}
}

// openStreamLocked sends the config of the next stream, which is the next segment.
func (s *sonioxSpeechSession) openStreamLocked() (*speechStream, error) {
	s.streamCount++
	s.segments++
	config := s.config
	config.StreamID = "s" + strconv.Itoa(s.streamCount)
	frame, err := providerUtils.MarshalSorted(&config)
	if err == nil && s.passthrough && len(config.ExtraParams) > 0 {
		frame, err = providerUtils.MergeExtraParamsIntoJSON(frame, config.ExtraParams)
	}
	if err != nil {
		bifrostErr := providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
		s.failLocked(bifrostErr)
		return nil, err
	}
	if err := s.writeLocked(frame); err != nil {
		return nil, err
	}
	stream := &speechStream{id: config.StreamID, segment: s.segments}
	stream.idle = time.AfterFunc(speechSessionStreamIdle, func() { s.endIdleStream(stream) })
	s.streams = append(s.streams, stream)
	s.current = stream
	return stream, nil
}

// writeJSONLocked marshals and writes one control message.
func (s *sonioxSpeechSession) writeJSONLocked(message any) error {
	frame, err := providerUtils.MarshalSorted(message)
	if err != nil {
		s.failLocked(providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err))
		return err
	}
	return s.writeLocked(frame)
}

// writeLocked writes one frame. A failed write ends the session, since Soniox has lost track of the streams.
func (s *sonioxSpeechSession) writeLocked(frame []byte) error {
	if err := s.conn.WriteMessage(ws.TextMessage, frame); err != nil {
		s.failLocked(providerUtils.NewBifrostUpstreamConnectionError("failed to write to the soniox text-to-speech websocket", err))
		return err
	}
	s.lastWrite = time.Now()
	return nil
}

// drainDeferredLocked applies the calls that waited for a stream slot, as far as the slots allow.
func (s *sonioxSpeechSession) drainDeferredLocked() {
	for len(s.deferred) > 0 && s.err == nil {
		op := s.deferred[0]
		if op.kind == speechOpText && s.current == nil && s.slotsFullLocked() {
			return
		}
		s.deferred[0] = speechOp{}
		s.deferred = s.deferred[1:]
		s.deferredBytes -= len(op.text)
		if op.kind == speechOpText {
			_ = s.sendTextLocked(op.text, true)
		} else {
			_ = s.flushLocked(true)
		}
	}
	if len(s.deferred) == 0 {
		s.deferred = nil
	}
}

// maybeFinishLocked appends speech.audio.done once the input has ended and every segment has been spoken.
func (s *sonioxSpeechSession) maybeFinishLocked(out []speechOutEvent) []speechOutEvent {
	if !s.ending || s.finished || len(s.streams) > 0 || len(s.deferred) > 0 {
		return out
	}
	s.finished = true
	return append(out, speechOutEvent{event: &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDone}})
}

// unlockAndEmit releases mu and sends the events in order, skipping audio of a stream cancelled meanwhile.
func (s *sonioxSpeechSession) unlockAndEmit(out []speechOutEvent) {
	if len(out) == 0 {
		s.mu.Unlock()
		return
	}
	s.sendMu.Lock()
	s.mu.Unlock()
	defer s.sendMu.Unlock()
	for _, item := range out {
		if item.stream != nil && item.stream.cancelled.Load() {
			continue
		}
		select {
		case s.events <- item.event:
		case <-s.done:
			return
		}
	}
}

// readLoop turns Soniox frames into events until the connection ends.
func (s *sonioxSpeechSession) readLoop() {
	defer s.wg.Done()
	defer func() {
		// A call that finished the session may still be sending its last events, which a shutdown now would drop.
		s.mu.Lock()
		sendingLast := s.finished && s.err == nil
		s.mu.Unlock()
		if !sendingLast {
			s.shutdown()
		}
		s.sendMu.Lock()
		close(s.events)
		s.sendMu.Unlock()
		s.shutdown()
	}()
	var frame SonioxTTSFrame
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			s.mu.Lock()
			if !s.closed.Load() && !s.finished {
				s.failLocked(providerUtils.NewBifrostUpstreamConnectionError("the soniox text-to-speech websocket closed", err))
			}
			s.mu.Unlock()
			return
		}
		frame = SonioxTTSFrame{}
		decodeErr := schemas.Unmarshal(data, &frame)

		s.mu.Lock()
		if decodeErr != nil {
			s.failLocked(providerUtils.NewBifrostOperationError("invalid soniox text-to-speech frame", decodeErr))
		}
		out := s.handleFrameLocked(&frame, s.frameOut[:0])
		stop := s.finished || s.err != nil
		s.unlockAndEmit(out)
		clear(out)
		s.frameOut = out[:0]
		if stop {
			return
		}
	}
}

// handleFrameLocked applies one frame and appends the events it releases to out. Frames of a stream the session
// no longer follows are late frames of a cancelled stream and are dropped.
func (s *sonioxSpeechSession) handleFrameLocked(frame *SonioxTTSFrame, out []speechOutEvent) []speechOutEvent {
	if s.finished || s.err != nil || s.closed.Load() {
		return out
	}
	stream := s.liveStream(frame.StreamID)
	if frame.ErrorCode != 0 {
		// An error names the stream it is about; one for a stream already dropped is its own cancellation.
		if frame.StreamID != "" && stream == nil {
			return out
		}
		s.failLocked(parseSonioxFrameError(frame))
		return out
	}
	if stream == nil {
		if frame.Terminated {
			before := len(s.dropped)
			s.dropped = slices.DeleteFunc(s.dropped, func(dropped *speechStream) bool { return dropped.id == frame.StreamID })
			if len(s.dropped) < before {
				return s.settleLocked(out)
			}
		}
		return out
	}
	if frame.Terminated {
		stream.terminated = true
		stream.idle.Stop()
		if s.current == stream {
			s.current = nil
		}
		return s.settleLocked(out)
	}
	if frame.Audio == "" && len(frame.Timestamps.Characters) == 0 {
		return out
	}
	event := &schemas.BifrostSpeechSessionEvent{
		Type:      schemas.SpeechSessionEventAudioDelta,
		Segment:   stream.segment,
		Audio:     frame.Audio,
		Alignment: toBifrostSpeechAlignment(&frame.Timestamps),
	}
	if s.streams[0] == stream {
		return append(out, speechOutEvent{event: event, stream: stream})
	}
	if s.heldBytes+len(event.Audio) > speechSessionMaxHeldBytes {
		s.failLocked(providerUtils.NewBifrostOperationError("soniox speech session held too much audio behind an earlier segment", nil))
		return out
	}
	stream.held = append(stream.held, event)
	s.heldBytes += len(event.Audio)
	return out
}

// liveStream finds a live stream by id, or nil.
func (s *sonioxSpeechSession) liveStream(id string) *speechStream {
	for _, stream := range s.streams {
		if stream.id == id {
			return stream
		}
	}
	return nil
}

// settleLocked sends what a freed stream releases: the segments that are done in order, the audio held behind
// them, then the calls that waited for a slot, and finally the end of the session if that was its last stream.
func (s *sonioxSpeechSession) settleLocked(out []speechOutEvent) []speechOutEvent {
	for len(s.streams) > 0 && s.streams[0].terminated {
		head := s.streams[0]
		out = append(out, speechOutEvent{event: &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventSegmentDone, Segment: head.segment}, stream: head})
		s.streams[0] = nil
		s.streams = s.streams[1:]
		if len(s.streams) == 0 {
			continue
		}
		next := s.streams[0]
		for _, event := range next.held {
			out = append(out, speechOutEvent{event: event, stream: next})
			s.heldBytes -= len(event.Audio)
		}
		next.held = nil
	}
	s.drainDeferredLocked()
	return s.maybeFinishLocked(out)
}

// toBifrostSpeechAlignment converts the timings of a frame, in seconds, to Bifrost's character alignment, in
// milliseconds. A frame without timestamps has none.
func toBifrostSpeechAlignment(timestamps *SonioxTTSTimestamps) *schemas.SpeechAlignment {
	if len(timestamps.Characters) == 0 {
		return nil
	}
	return &schemas.SpeechAlignment{
		Characters:                 timestamps.Characters,
		CharacterStartTimesSeconds: timestamps.CharacterStartTimesSeconds,
		CharacterEndTimesSeconds:   timestamps.CharacterEndTimesSeconds,
	}
}

// keepAliveLoop keeps the connection open while it is quiet, since Soniox closes one that is idle for 40 seconds.
func (s *sonioxSpeechSession) keepAliveLoop() {
	defer s.wg.Done()
	timer := time.NewTimer(speechSessionKeepAlive)
	defer timer.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-timer.C:
		}
		s.mu.Lock()
		wait := speechSessionKeepAlive - time.Since(s.lastWrite)
		if wait <= 0 && s.openLocked() == nil {
			_ = s.writeLocked(sonioxKeepAliveFrame)
			wait = speechSessionKeepAlive
		}
		s.mu.Unlock()
		timer.Reset(max(wait, time.Millisecond))
	}
}
