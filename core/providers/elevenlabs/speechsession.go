package elevenlabs

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	ws "github.com/fasthttp/websocket"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	speechSessionEventBuffer       = 64
	speechSessionKeepAliveInterval = 10 * time.Second // the socket closes after 20 s without a message
	speechSessionPolicyCloseCode   = 1008             // the close code the socket sends with an error frame
)

// The control messages of the socket.
var (
	speechSessionFlushFrame     = []byte(`{"flush":true}`)
	speechSessionCloseFrame     = []byte(`{"close_socket":true}`)
	speechSessionKeepAliveFrame = []byte(`{"keep_alive":true}`)
)

var (
	errSpeechSessionEnded  = errors.New("speech session has ended")
	errSpeechSessionClosed = errors.New("speech session is closed")
)

var _ schemas.SpeechSessionProvider = (*ElevenlabsProvider)(nil)

// OpenSpeechSession opens the text-to-dialogue WebSocket, the only one that serves eleven_v4_turbo, and
// registers the voice. The socket takes one voice and has no cancel, so a session cancels by closing the
// socket and dials a fresh one with the next text.
func (provider *ElevenlabsProvider) OpenSpeechSession(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostSpeechRequest, dial schemas.SpeechSessionDialer) (schemas.SpeechSession, *schemas.BifrostError) {
	if bifrostErr := providerUtils.CheckOperationAllowed(schemas.Elevenlabs, provider.customProviderConfig, schemas.SpeechStreamRequest); bifrostErr != nil {
		return nil, bifrostErr
	}
	if schemas.IsElevenlabsSoundModelFamily(ctx, req.Model) {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
	}

	var voice, responseFormat string
	config := ElevenlabsSessionConfig{}
	query := url.Values{"model_id": {req.Model}}
	if params := req.Params; params != nil {
		responseFormat = params.ResponseFormat
		if params.VoiceConfig != nil {
			if params.VoiceConfig.Voice != nil {
				voice = *params.VoiceConfig.Voice
			} else if len(params.VoiceConfig.MultiVoiceConfig) > 0 {
				voice = params.VoiceConfig.MultiVoiceConfig[0].Voice
			}
		}
		config.PronunciationDictionaryLocators = toElevenlabsPronunciationDictionaryLocators(params.PronunciationDictionaryLocators)
		stability, hasStability := schemas.SafeExtractFloat64Pointer(params.ExtraParams["stability"])
		cloneStrength, hasCloneStrength := schemas.SafeExtractFloat64Pointer(params.ExtraParams["clone_strength"])
		if hasStability || hasCloneStrength {
			config.VoiceSettings = &ElevenlabsSessionVoiceSettings{Stability: stability, CloneStrength: cloneStrength}
		}
		if params.LanguageCode != nil {
			query.Set("language_code", *params.LanguageCode)
		}
		if params.WithTimestamps != nil && *params.WithTimestamps {
			query.Set("sync_alignment", "true")
		}
		if params.EnableLogging != nil {
			query.Set("enable_logging", strconv.FormatBool(*params.EnableLogging))
		}
	}
	if voice == "" {
		return nil, providerUtils.NewBifrostBadRequestError("voice parameter is required")
	}
	config.Voices = []string{voice}
	outputFormat := ConvertBifrostSpeechFormatToElevenlabs(responseFormat)
	query.Set("output_format", outputFormat)

	registration, err := schemas.MarshalSorted(config)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	headers, bifrostErr := provider.RealtimeHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	s := &speechSession{
		providerKey:  provider.GetProviderKey(),
		ctx:          ctx,
		dial:         dial,
		url:          provider.realtimeBaseURL() + "/v1/text-to-dialogue/stream-input?" + query.Encode(),
		headers:      headers,
		registration: registration,
		voice:        voice,
		events:       make(chan *schemas.BifrostSpeechSessionEvent, speechSessionEventBuffer),
		done:         make(chan struct{}),
		textSegment:  1,
		audioSegment: 1,
	}
	s.format, s.sampleRate = speechSessionAudioFormat(outputFormat)
	if _, bifrostErr := s.connect(); bifrostErr != nil {
		return nil, bifrostErr
	}
	return s, nil
}

// speechSessionAudioFormat reads the format and, for raw audio, the sample rate out of an output_format
// such as pcm_24000 or mp3_44100_128.
func speechSessionAudioFormat(outputFormat string) (string, int) {
	format, rest, _ := strings.Cut(outputFormat, "_")
	switch format {
	case "pcm", "ulaw", "alaw":
		rate, _, _ := strings.Cut(rest, "_")
		sampleRate, _ := strconv.Atoi(rate)
		return format, sampleRate
	}
	return format, 0
}

// speechConn is one provider socket of a session.
type speechConn struct {
	upstream  schemas.RealtimeUpstream
	writeMu   sync.Mutex
	lastWrite atomic.Int64 // unix nanoseconds
	done      chan struct{}
	closeOnce sync.Once
}

func (c *speechConn) write(frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.lastWrite.Store(time.Now().UnixNano())
	return c.upstream.WriteMessage(ws.TextMessage, frame)
}

func (c *speechConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.upstream.Close()
	})
}

// speechSession is the SpeechSession of one ElevenLabs text-to-dialogue connection, redialed after a cancel.
//
// The text methods run on one goroutine, and a reader goroutine per socket produces the events. mu guards
// the state and orders the events: every event is queued with it held, so nothing of a cancelled socket
// follows speech.cancelled. Writes never take mu, since a blocked write must not stop the reader that
// drains the socket.
type speechSession struct {
	providerKey  schemas.ModelProvider
	ctx          *schemas.BifrostContext
	dial         schemas.SpeechSessionDialer
	url          string
	headers      map[string]string
	registration []byte
	voice        string
	format       string
	sampleRate   int

	events   chan *schemas.BifrostSpeechSessionEvent
	done     chan struct{} // closed when the session is over, to release a blocked event send
	stopOnce sync.Once

	mu           sync.Mutex
	conn         *speechConn // nil until the first text after a cancel
	textSegment  int         // the segment text is being sent into
	audioSegment int         // the segment the socket is sending audio of
	finalSegment int         // the segment End closes with the session, 0 for none
	pending      bool        // text was sent into textSegment
	afterFlush   bool        // the next text starts a turn
	ending       bool
	finished     bool
	err          *schemas.BifrostError
}

func (s *speechSession) AudioFormat() (string, int) { return s.format, s.sampleRate }

func (s *speechSession) Events() <-chan *schemas.BifrostSpeechSessionEvent { return s.events }

func (s *speechSession) Err() *schemas.BifrostError {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// connect dials a socket, registers the voice and starts reading it.
func (s *speechSession) connect() (*speechConn, *schemas.BifrostError) {
	upstream, bifrostErr := s.dial(s.ctx, s.url, s.headers)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	conn := &speechConn{upstream: upstream, done: make(chan struct{})}
	if err := conn.write(s.registration); err != nil {
		conn.close()
		return nil, providerUtils.NewBifrostUpstreamConnectionError("failed to register the voice with the provider", err)
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		conn.close()
		return nil, providerUtils.NewBifrostOperationError(errSpeechSessionClosed.Error(), errSpeechSessionClosed)
	}
	s.conn = conn
	s.mu.Unlock()
	go s.read(conn)
	go s.keepAlive(conn)
	return conn, nil
}

// activeConn returns the socket text goes to, dialing one after a cancel.
func (s *speechSession) activeConn() (*speechConn, error) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return nil, errSpeechSessionClosed
	}
	if s.ending {
		s.mu.Unlock()
		return nil, errSpeechSessionEnded
	}
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	conn, bifrostErr := s.connect()
	if bifrostErr != nil {
		s.mu.Lock()
		s.finishLocked(bifrostErr)
		s.mu.Unlock()
		return nil, errors.New(bifrostErr.GetErrorString())
	}
	return conn, nil
}

func (s *speechSession) SendText(text string) error {
	if text == "" {
		return nil
	}
	conn, err := s.activeConn()
	if err != nil {
		return err
	}
	s.mu.Lock()
	newTurn := s.afterFlush
	s.afterFlush = false
	s.pending = true
	s.mu.Unlock()
	frame, err := schemas.MarshalSorted(ElevenlabsSessionText{Inputs: []ElevenlabsSessionInput{{Text: text, VoiceID: s.voice, NewTurn: newTurn}}})
	if err != nil {
		return err
	}
	return conn.write(frame)
}

func (s *speechSession) Flush() error {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return errSpeechSessionClosed
	}
	if s.ending {
		s.mu.Unlock()
		return errSpeechSessionEnded
	}
	conn := s.conn
	if conn == nil || !s.pending {
		s.mu.Unlock()
		return nil
	}
	s.pending = false
	s.afterFlush = true
	s.textSegment++
	s.mu.Unlock()
	return conn.write(speechSessionFlushFrame)
}

func (s *speechSession) End() error {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return errSpeechSessionClosed
	}
	if s.ending {
		s.mu.Unlock()
		return nil
	}
	conn := s.conn
	if conn == nil {
		s.emitLocked(&schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDone})
		s.finishLocked(nil)
		s.mu.Unlock()
		return nil
	}
	s.ending = true
	if s.pending {
		s.finalSegment = s.textSegment
	}
	s.mu.Unlock()
	return conn.write(speechSessionCloseFrame)
}

func (s *speechSession) Cancel() error {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return errSpeechSessionClosed
	}
	last := s.textSegment
	if !s.pending {
		last--
	}
	conn := s.conn
	s.conn = nil
	s.textSegment, s.audioSegment = last+1, last+1
	s.pending, s.afterFlush, s.finalSegment = false, false, 0
	s.emitLocked(&schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventCancelled, Segment: last})
	if s.ending {
		// The socket that would have finished the session is gone, so the session finishes here.
		s.emitLocked(&schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDone})
		s.finishLocked(nil)
	}
	s.mu.Unlock()
	if conn != nil {
		conn.close()
	}
	return nil
}

func (s *speechSession) Close() error {
	s.stop()
	s.mu.Lock()
	s.finishLocked(nil)
	s.mu.Unlock()
	return nil
}

func (s *speechSession) stop() {
	s.stopOnce.Do(func() { close(s.done) })
}

// emitLocked queues an event for the client, and reports false when the session ends first.
func (s *speechSession) emitLocked(event *schemas.BifrostSpeechSessionEvent) bool {
	select {
	case s.events <- event:
		return true
	case <-s.done:
		return false
	}
}

// finishLocked ends the session once: it records why, closes Events and releases the socket.
func (s *speechSession) finishLocked(bifrostErr *schemas.BifrostError) {
	if s.finished {
		return
	}
	s.finished = true
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = s.providerKey
		bifrostErr.ExtraFields.RequestType = schemas.SpeechStreamRequest
	}
	s.err = bifrostErr
	s.stop()
	close(s.events)
	if s.conn != nil {
		s.conn.close()
	}
}

// read turns the frames of one socket into events until it ends.
func (s *speechSession) read(conn *speechConn) {
	defer conn.close()
	var frame ElevenlabsSessionFrame
	for {
		messageType, data, err := conn.upstream.ReadMessage()
		if err != nil {
			s.failConn(conn, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderStreamTruncated, err))
			return
		}
		if messageType != ws.TextMessage {
			continue
		}
		frame = ElevenlabsSessionFrame{}
		if err := sonic.Unmarshal(data, &frame); err != nil {
			s.failConn(conn, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err))
			return
		}
		if !s.handleFrame(conn, &frame) {
			return
		}
	}
}

// failConn ends the session with an error, unless the socket is already abandoned: closed by the
// client or replaced by a cancel.
func (s *speechSession) failConn(conn *speechConn, bifrostErr *schemas.BifrostError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.conn != conn {
		return
	}
	s.finishLocked(bifrostErr)
}

// handleFrame emits what a frame stands for, and reports whether the socket is still to be read.
func (s *speechSession) handleFrame(conn *speechConn, frame *ElevenlabsSessionFrame) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.conn != conn {
		return false
	}
	switch {
	case frame.Error != "" || frame.Code != 0:
		s.finishLocked(speechSessionFrameError(frame))
		return false
	case frame.IsFinal:
		if s.finalSegment != 0 && s.audioSegment == s.finalSegment {
			s.emitLocked(&schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventSegmentDone, Segment: s.finalSegment})
		}
		s.emitLocked(&schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDone})
		s.finishLocked(nil)
		return false
	}
	alignment := toSpeechSessionAlignment(frame.Alignment)
	if frame.Audio != "" || alignment != nil {
		if !s.emitLocked(&schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDelta, Segment: s.audioSegment, Audio: frame.Audio, Alignment: alignment}) {
			return false
		}
	}
	if frame.IsFinalAudioForTurn {
		if !s.emitLocked(&schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventSegmentDone, Segment: s.audioSegment}) {
			return false
		}
		s.audioSegment++
	}
	return true
}

// keepAlive keeps the socket open while the client is quiet, until the socket closes or the session ends.
func (s *speechSession) keepAlive(conn *speechConn) {
	ticker := time.NewTicker(speechSessionKeepAliveInterval / 2)
	defer ticker.Stop()
	for {
		select {
		case <-conn.done:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, conn.lastWrite.Load())) < speechSessionKeepAliveInterval {
				continue
			}
			s.mu.Lock()
			ending := s.ending
			s.mu.Unlock()
			if ending || conn.write(speechSessionKeepAliveFrame) != nil {
				return
			}
		}
	}
}

// toSpeechSessionAlignment converts a frame's timings, in milliseconds with durations, to seconds with ends.
func toSpeechSessionAlignment(alignment *ElevenlabsSessionAlignment) *schemas.SpeechAlignment {
	if alignment == nil || len(alignment.Chars) == 0 {
		return nil
	}
	starts := make([]float64, len(alignment.CharStartTimesMs))
	ends := make([]float64, len(alignment.CharStartTimesMs))
	for i, startMs := range alignment.CharStartTimesMs {
		endMs := startMs
		if i < len(alignment.CharDurationsMs) {
			endMs += alignment.CharDurationsMs[i]
		}
		starts[i] = startMs / 1000
		ends[i] = endMs / 1000
	}
	return &schemas.SpeechAlignment{
		Characters:                 alignment.Chars,
		CharacterStartTimesSeconds: starts,
		CharacterEndTimesSeconds:   ends,
	}
}

// speechSessionFrameError is the error an error frame reports. The socket sends close code 1008 with a
// frame for a request it refuses, such as an unregistered voice.
func speechSessionFrameError(frame *ElevenlabsSessionFrame) *schemas.BifrostError {
	message := frame.Message
	if message == "" {
		message = frame.Error
	}
	status := 502
	if frame.Code == speechSessionPolicyCloseCode {
		status = 400
	}
	bifrostErr := providerUtils.NewProviderAPIError(message, nil, status, &frame.Error, nil)
	if frame.Param != "" {
		bifrostErr.Error.Param = frame.Param
	}
	return bifrostErr
}
