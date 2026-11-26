package schemas

// SpeechSessionEventType names an event of an incremental speech session, in either direction.
type SpeechSessionEventType string

const (
	// Events the client sends.
	SpeechSessionEventConfig SpeechSessionEventType = "speech.config"
	SpeechSessionEventText   SpeechSessionEventType = "speech.text"
	SpeechSessionEventFlush  SpeechSessionEventType = "speech.flush"
	SpeechSessionEventEnd    SpeechSessionEventType = "speech.end"
	SpeechSessionEventCancel SpeechSessionEventType = "speech.cancel"

	// Events Bifrost sends.
	SpeechSessionEventCreated     SpeechSessionEventType = "speech.session.created"
	SpeechSessionEventAudioDelta  SpeechSessionEventType = SpeechSessionEventType(SpeechStreamResponseTypeDelta)
	SpeechSessionEventSegmentDone SpeechSessionEventType = "speech.segment.done"
	SpeechSessionEventCancelled   SpeechSessionEventType = "speech.cancelled"
	SpeechSessionEventAudioDone   SpeechSessionEventType = SpeechSessionEventType(SpeechStreamResponseTypeDone)
)

// SpeechSessionClientEvent is an event the client sends after speech.config.
type SpeechSessionClientEvent struct {
	Type SpeechSessionEventType `json:"type"`
	Text string                 `json:"text,omitempty"` // speech.text only
}

// BifrostSpeechSessionEvent is an event a speech session sends the client.
type BifrostSpeechSessionEvent struct {
	Type      SpeechSessionEventType `json:"type"`
	Segment   int                    `json:"segment,omitempty"`   // 1-based; rises at every flush and cancel
	Audio     string                 `json:"audio,omitempty"`     // base64, as the provider sent it
	Alignment *SpeechAlignment       `json:"alignment,omitempty"` // character timings of this audio
	Usage     *SpeechUsage           `json:"usage,omitempty"`     // speech.audio.done only
	Session   *SpeechSessionInfo     `json:"session,omitempty"`   // speech.session.created only
}

// SpeechSessionInfo describes an open session to its client.
type SpeechSessionInfo struct {
	ID             string `json:"id"`
	Model          string `json:"model"`
	ResponseFormat string `json:"response_format"`
	SampleRate     int    `json:"sample_rate,omitempty"` // raw formats only
}

// SpeechSessionProvider is an optional interface for providers whose speech synthesis takes text
// incrementally over one connection. Checked via type assertion: provider.(SpeechSessionProvider).
type SpeechSessionProvider interface {
	// OpenSpeechSession opens a session configured by req, whose Input is empty. dial opens each upstream
	// WebSocket the session needs, so the transport owns proxying; a session dials again to recover from
	// a cancel. A failure to connect or to authenticate is returned here, before the client is told the
	// session exists.
	OpenSpeechSession(ctx *BifrostContext, key Key, req *BifrostSpeechRequest, dial SpeechSessionDialer) (SpeechSession, *BifrostError)
}

// SpeechSessionDialer opens one upstream WebSocket connection for a speech session.
type SpeechSessionDialer func(ctx *BifrostContext, url string, headers map[string]string) (RealtimeUpstream, *BifrostError)

// SpeechSession is an open incremental synthesis connection. Its text methods are called from one
// goroutine at a time and Events is read by another.
//
// Segments number the audio between flushes, from 1: speech.flush ends a segment and so does the
// provider, and speech.cancel ends every segment still pending. Audio carries the segment it belongs
// to, speech.segment.done closes a segment that finished, and speech.cancelled names the last
// segment dropped, after which nothing of a cancelled segment is sent. Text sent after a cancel
// starts a new segment.
type SpeechSession interface {
	// AudioFormat is the audio the session produces: the format name and, for raw formats, the sample rate.
	AudioFormat() (format string, sampleRate int)
	// SendText appends text to the current segment. The provider may hold short text until a flush.
	SendText(text string) error
	// Flush asks the provider to speak what it holds and closes the current segment.
	Flush() error
	// End flushes and finishes the session; Events then ends with speech.audio.done.
	End() error
	// Cancel drops pending text and audio and answers with speech.cancelled. The session stays open.
	Cancel() error
	// Events carries the session's events and is closed when the session is over.
	Events() <-chan *BifrostSpeechSessionEvent
	// Err is why Events closed abnormally, and nil after speech.audio.done or a Close.
	Err() *BifrostError
	// Close ends the session and releases its connections. It is safe to call more than once.
	Close() error
}
