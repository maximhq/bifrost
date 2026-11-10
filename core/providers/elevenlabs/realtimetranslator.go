package elevenlabs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	scribeReplacementRune   = "�"
	scribeMaxPendingCommits = 16 // commits a client may have waiting for Scribe's transcript
	scribeRecentSegments    = 8  // completed segments kept to match the frames that describe them
)

// The kinds of frame that describe a completed segment, as bits of scribeDoneSegment.seen.
const (
	scribeSeenWords uint8 = 1 << iota
	scribeSeenEntities
	scribeSeenRedacted
	scribeSeenEdited
)

// scribeTranslator is the RealtimeTranslator for one Scribe transcription connection. The OpenAI Realtime
// client and Scribe differ in ways a per-event mapping cannot bridge:
//
//   - Scribe takes every setting in the connect URL, which the provider builds from the client's first
//     session.update. The translator holds the later ones to those settings.
//   - A Scribe partial is the whole text of the segment so far, and a later partial may revise it, while
//     an OpenAI delta only appends. The delta event therefore carries the appended text, when there is
//     any, and the whole partial text in a transcript field OpenAI does not send.
//   - A commit is one frame for Scribe and several events for OpenAI, and it may come from the client, from
//     Scribe's voice detection or from its own time limit. A client that keeps streaming commits again
//     before Scribe has answered, so each commit the client makes is queued with the audio it covered, and
//     Scribe answers the queue in order.
//   - Scribe reports no usage. It bills the audio it is sent, so the translator counts that audio and
//     reports it as the duration of the segment that ends.
//
// The relay calls it one step at a time, so it holds no locks.
type scribeTranslator struct {
	model     string
	seq       int
	sessionID string
	session   json.RawMessage // the settings the client has sent so far, which each session.update builds on
	settings  *scribeSettings // the settings the connection opened with, once a session.update names the model
	defaults  *scribeSettings // what the connection opens with when the client has not sent any

	item          string              // the segment Scribe is transcribing, until it is committed
	announced     string              // the segment an error interrupted, which the client was already told is committed
	lastItem      string              // the segment announced as committed last
	pending       []scribeSegment     // the commits the client made that Scribe has not answered, oldest first
	recent        []scribeDoneSegment // the segments completed last, oldest first
	partial       string              // the latest partial text of the segment
	committedText string              // the text of the segment committed last
	sent          string              // the text the segment's deltas have added up to
	audioBytes    int                 // the audio sent since the last commit
	failed        bool                // Scribe reported an error, so its close is not reported again
	extensions    bool                // the client asked for the Bifrost extension events
}

// scribeSegment is a segment the client committed, with the audio it covered.
type scribeSegment struct {
	item  string
	bytes int
}

// scribeDoneSegment is a completed segment and the text Scribe committed for it. The frames that describe
// it name it by that text, and seen holds a bit for each kind of them it has had.
type scribeDoneSegment struct {
	item string
	text string
	seen uint8
}

func newScribeTranslator(model string) *scribeTranslator {
	// The defaults name no setting that can fail.
	defaults, _ := newScribeSettings(nil)
	t := &scribeTranslator{model: model, defaults: defaults}
	t.sessionID = t.id("sess_")
	return t
}

func (t *scribeTranslator) id(prefix string) string {
	t.seq++
	return prefix + strconv.Itoa(t.seq)
}

func (t *scribeTranslator) newEvent(eventType string) *scribeEvent {
	return &scribeEvent{Type: eventType, EventID: t.id("event_")}
}

// current returns the settings the connection runs on.
func (t *scribeTranslator) current() *scribeSettings {
	if t.settings != nil {
		return t.settings
	}
	return t.defaults
}

func (t *scribeTranslator) sessionObject() *scribeSession {
	input := t.current().input
	input.Transcription.Model = t.model
	session := &scribeSession{
		ID:     t.sessionID,
		Object: "realtime.session",
		Type:   "transcription",
		Audio:  scribeSessionIO{Input: input},
	}
	if t.extensions {
		session.Bifrost = &scribeBifrostOptions{Extensions: true}
	}
	return session
}

// Open announces the session before the client speaks. Scribe's own session_started follows on its side.
func (t *scribeTranslator) Open() schemas.RealtimeTranslation {
	out := &scribeOutput{}
	event := t.newEvent("session.created")
	event.Session = t.sessionObject()
	out.client(event)
	return out.RealtimeTranslation
}

// Close reports a close that carries a reason as an error, unless Scribe has already sent one.
func (t *scribeTranslator) Close(code int, reason string) schemas.RealtimeTranslation {
	if t.failed || reason == "" || code == 1000 || code == 1001 || code == 1005 {
		return schemas.RealtimeTranslation{}
	}
	out := &scribeOutput{}
	event := t.newEvent("error")
	event.Error = &scribeError{Type: "server_error", Code: "elevenlabs_close_" + strconv.Itoa(code), Message: reason}
	out.client(event)
	return out.RealtimeTranslation
}

// ToProvider translates one client event. An error names what Scribe cannot do, and leaves the session
// as it was.
func (t *scribeTranslator) ToProvider(event *schemas.BifrostRealtimeEvent) (schemas.RealtimeTranslation, error) {
	out := &scribeOutput{}
	var err error
	switch event.Type {
	case schemas.RTEventSessionUpdate:
		err = t.sessionUpdate(out, event)
	case schemas.RTEventInputAudioAppend:
		err = t.audioAppend(out, event)
	case schemas.RTEventInputAudioCommit:
		err = t.audioCommit(out)
	case schemas.RTEventInputAudioClear:
		err = t.audioClear(out)
	default:
		err = fmt.Errorf("%s is not supported by a scribe transcription session", event.Type)
	}
	if err == nil {
		err = out.err
	}
	if err != nil {
		return schemas.RealtimeTranslation{}, err
	}
	return out.RealtimeTranslation, nil
}

// sessionUpdate holds an update to the settings the connection opened with. Scribe cannot change a
// session once connected. Repeating the settings is harmless, and common in clients that send them on
// every connect, so it is acknowledged.
func (t *scribeTranslator) sessionUpdate(out *scribeOutput, event *schemas.BifrostRealtimeEvent) error {
	merged, err := providerUtils.MergeJSONObjects(t.session, []byte(providerUtils.GetJSONField(event.RawData, "session").Raw))
	if err != nil {
		return fmt.Errorf("invalid session in session.update: %w", err)
	}
	settings, err := newScribeSettings(merged)
	if err != nil {
		return err
	}
	if t.settings != nil && settings.query != t.settings.query {
		return errors.New("scribe fixes a session's settings when it connects, so this session.update would change them; send the settings first or open a new session")
	}
	t.session = merged
	t.extensions = settings.extensions
	if t.settings == nil && scribeNamesModel(merged) {
		t.settings = settings
	}
	reply := t.newEvent("session.updated")
	reply.Session = t.sessionObject()
	out.client(reply)
	return nil
}

func (t *scribeTranslator) audioAppend(out *scribeOutput, event *schemas.BifrostRealtimeEvent) error {
	rate := t.current().rate
	if len(event.Audio) > 0 {
		// The audio was decoded when the event was parsed. Encoding it again yields only characters
		// that need no escaping, so the frame is written straight into one buffer.
		frame := make([]byte, 0, base64.StdEncoding.EncodedLen(len(event.Audio))+96)
		frame = append(frame, `{"message_type":"input_audio_chunk","audio_base_64":"`...)
		frame = base64.StdEncoding.AppendEncode(frame, event.Audio)
		frame = append(frame, `","commit":false,"sample_rate":`...)
		frame = strconv.AppendInt(frame, int64(rate), 10)
		out.Provider = append(out.Provider, append(frame, '}'))
		t.audioBytes += len(event.Audio)
		return nil
	}
	data := providerUtils.GetJSONField(event.RawData, "audio").String()
	if data == "" {
		return errors.New("audio must be set for input_audio_buffer.append")
	}
	if frame := out.marshal(&scribeAudioChunk{MessageType: scribeInputAudioChunk, AudioBase64: data, SampleRate: rate}); frame != nil {
		out.Provider = append(out.Provider, frame)
	}
	t.audioBytes += base64.StdEncoding.DecodedLen(len(data))
	return nil
}

// audioCommit asks Scribe to commit the segment and tells the client it is committed. Scribe answers with
// the transcript, which ends the segment. The audio sent from here on belongs to the next segment, so a
// commit with none since the last is refused: Scribe ends a session that commits under 0.3 seconds.
func (t *scribeTranslator) audioCommit(out *scribeOutput) error {
	if t.audioBytes == 0 {
		return errors.New("the input audio buffer is empty")
	}
	if len(t.pending) >= scribeMaxPendingCommits {
		return fmt.Errorf("scribe has not answered the last %d commits yet", scribeMaxPendingCommits)
	}
	frame := out.marshal(&scribeAudioChunk{MessageType: scribeInputAudioChunk, Commit: true, SampleRate: t.current().rate})
	if frame == nil {
		return out.err
	}
	out.Provider = append(out.Provider, frame)
	// The partials are for the oldest commit Scribe has not answered, so a commit behind others is a new item.
	var id string
	if len(t.pending) == 0 {
		id = t.ensureItem()
	} else {
		id = t.id("item_")
	}
	t.pending = append(t.pending, scribeSegment{item: id, bytes: t.audioBytes})
	t.audioBytes = 0
	t.committed(out, id)
	return nil
}

// audioClear accepts a clear that has nothing to discard, which clients send on every interruption. Audio
// already sent cannot be taken back from Scribe.
func (t *scribeTranslator) audioClear(out *scribeOutput) error {
	if t.audioBytes > 0 {
		return errors.New("scribe cannot discard audio it has already been sent; commit it, or open a new session")
	}
	out.client(t.newEvent("input_audio_buffer.cleared"))
	return nil
}

// ensureItem names the segment Scribe is transcribing: the oldest commit it has not answered, or else the
// one still streaming.
func (t *scribeTranslator) ensureItem() string {
	if t.item == "" {
		if len(t.pending) > 0 {
			t.item = t.pending[0].item
		} else {
			t.item = t.id("item_")
		}
	}
	return t.item
}

// committed announces a segment as committed, unless an error left it waiting after the client was told.
func (t *scribeTranslator) committed(out *scribeOutput, id string) {
	if id == t.announced {
		return
	}
	event := t.newEvent("input_audio_buffer.committed")
	event.ItemID, event.PreviousItemID = id, t.lastItem
	t.lastItem = id
	out.client(event)
}

// ToClient translates one Scribe frame. A frame that is not JSON ends the session.
func (t *scribeTranslator) ToClient(frame json.RawMessage) (schemas.RealtimeTranslation, error) {
	var parsed scribeFrame
	if err := schemas.Unmarshal(frame, &parsed); err != nil {
		return schemas.RealtimeTranslation{}, fmt.Errorf("invalid scribe frame: %w", err)
	}
	out := &scribeOutput{}
	switch errorType, isError := scribeErrorTypes[parsed.MessageType]; {
	case parsed.MessageType == scribePartialTranscript:
		t.partialTranscript(out, &parsed)
	case parsed.MessageType == scribeCommittedTranscript:
		t.committedTranscript(out, parsed.Text)
	case isError:
		t.failed = true
		// Scribe will not answer the commits it refused, so their audio waits for the next segment that completes.
		for _, segment := range t.pending {
			t.audioBytes += segment.bytes
		}
		if len(t.pending) > 0 {
			t.item = t.pending[0].item
			t.announced = t.item
		}
		t.pending = t.pending[:0]
		event := t.newEvent("error")
		event.Error = &scribeError{Type: errorType, Code: parsed.MessageType, Message: parsed.Error}
		out.client(event)
	default:
		t.extension(out, &parsed, frame)
	}
	if out.err != nil {
		return schemas.RealtimeTranslation{}, out.err
	}
	return out.RealtimeTranslation, nil
}

// partialTranscript reports the segment so far. A partial the segment has already shown is a keepalive,
// and so is a repeat of the segment committed last before a new one has begun.
//
// The transcript field carries the whole partial. The delta carries the words of it that are finished,
// the ones before its last space, that add to what the deltas have sent: the last word is the one Scribe
// revises most ("How-", then "How are"), and a delta cannot be taken back. A partial that revises words
// already sent, or one cut mid-character at its end, adds nothing until the text agrees with what was sent
// again. The completed event carries the final text, and the deltas are brought up to it first.
func (t *scribeTranslator) partialTranscript(out *scribeOutput, frame *scribeFrame) {
	if frame.Text == t.partial || (t.item == "" && frame.Text == t.committedText) {
		return
	}
	t.partial = frame.Text
	delta := t.nextDelta(finishedScribeWords(frame.Text))
	event := t.newEvent(string(schemas.RTEventInputAudioTransDelta))
	event.ItemID, event.ContentIndex = t.ensureItem(), new(0)
	event.Delta, event.Transcript, event.TurnProbability = &delta, &frame.Text, frame.TurnProbability
	out.client(event)
}

// finishedScribeWords returns the text up to and including its last space.
func finishedScribeWords(text string) string {
	end := strings.LastIndexFunc(text, unicode.IsSpace)
	if end < 0 {
		return ""
	}
	_, size := utf8.DecodeRuneInString(text[end:])
	return text[:end+size]
}

// nextDelta returns what text adds to the deltas sent so far, or nothing when it does not extend them.
func (t *scribeTranslator) nextDelta(text string) string {
	if !strings.HasPrefix(text, t.sent) || strings.Contains(text, scribeReplacementRune) {
		return ""
	}
	delta := text[len(t.sent):]
	t.sent = text
	return delta
}

// committedTranscript ends a segment: the oldest commit the client made, or else the one Scribe committed
// itself, which is announced as committed here. It is completed with its transcript and the audio it took.
func (t *scribeTranslator) committedTranscript(out *scribeOutput, text string) {
	var id string
	var audio int
	if len(t.pending) > 0 {
		id, audio = t.pending[0].item, t.pending[0].bytes
		t.pending = slices.Delete(t.pending, 0, 1)
	} else {
		id, audio = t.ensureItem(), t.audioBytes
		t.audioBytes = 0
		t.committed(out, id)
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, scribeReplacementRune)
	}

	if delta := t.nextDelta(text); delta != "" {
		flush := t.newEvent(string(schemas.RTEventInputAudioTransDelta))
		flush.ItemID, flush.ContentIndex, flush.Delta, flush.Transcript = id, new(0), &delta, &text
		out.client(flush)
	}

	added := t.newEvent(string(schemas.RTEventConversationItemAdded))
	added.Item = &scribeItem{ID: id, Object: "realtime.item", Type: "message", Status: "completed", Role: "user", Content: []scribePart{{Type: "input_audio"}}}
	out.client(added)

	completed := t.newEvent(string(schemas.RTEventInputAudioTransCompleted))
	completed.ItemID, completed.ContentIndex, completed.Transcript = id, new(0), &text
	completed.Usage = &scribeUsage{Type: "duration", Seconds: float64(audio) / float64(t.current().bytesPerSecond)}
	out.client(completed)

	done := t.newEvent(string(schemas.RTEventConversationItemDone))
	done.Item = &scribeItem{ID: id, Object: "realtime.item", Type: "message", Status: "completed", Role: "user", Content: []scribePart{{Type: "input_audio", Transcript: &text}}}
	out.client(done)

	if len(t.recent) == scribeRecentSegments {
		t.recent = slices.Delete(t.recent, 0, 1)
	}
	t.recent = append(t.recent, scribeDoneSegment{item: id, text: text})
	t.item, t.announced, t.partial, t.sent, t.committedText = "", "", "", "", text
}

// describedSegment returns the segment a frame of the given kind describes. Scribe names none, so it is
// found by the text the frame repeats: the oldest segment with that text that has not had a frame of this
// kind, since Scribe sends each kind in commit order. A text that matches only segments that have had one
// belongs to the last of them, and one that matches none to the segment completed last.
func (t *scribeTranslator) describedSegment(kind uint8, text string) string {
	var latest string
	for i := range t.recent {
		segment := &t.recent[i]
		if segment.text != text {
			continue
		}
		if segment.seen&kind == 0 {
			segment.seen |= kind
			return segment.item
		}
		latest = segment.item
	}
	if latest == "" && len(t.recent) > 0 {
		latest = t.recent[len(t.recent)-1].item
	}
	return latest
}

// extension reports what Scribe sends that OpenAI Realtime has no event for, as Bifrost extension events,
// to a session that asked for them. Without the option the frames are consumed, so a client that did not
// ask never sees an event type OpenAI does not define. The frames that describe a committed segment arrive
// after its completed event, so they name the segment by item_id instead of waiting to be merged into it,
// found by the text they repeat because they can arrive after later segments completed. A frame with no
// neutral event, such as a warning or one Scribe adds later, goes out whole in a bifrost.provider_event,
// so nothing is lost.
func (t *scribeTranslator) extension(out *scribeOutput, frame *scribeFrame, raw json.RawMessage) {
	if !t.extensions {
		return
	}
	header := scribeExtensionHeader{EventID: t.id("event_")}
	switch frame.MessageType {
	case scribeWordsFrame:
		header.Type, header.ItemID = bifrostEventWords, t.describedSegment(scribeSeenWords, frame.Text)
		event := &scribeWordsEvent{scribeExtensionHeader: header, Words: make([]scribeWord, 0, len(frame.Words))}
		if frame.LanguageCode != nil {
			event.Language = *frame.LanguageCode
		}
		for _, wire := range frame.Words {
			if wire.Type != "" && wire.Type != "word" {
				continue
			}
			word := scribeWord{Text: wire.Text, Start: wire.Start, End: wire.End}
			if wire.LogProb != nil {
				word.Confidence = new(math.Exp(*wire.LogProb))
			}
			if wire.SpeakerID != nil {
				word.Speaker = *wire.SpeakerID
			}
			event.Words = append(event.Words, word)
		}
		out.client(event)
	case scribeEntitiesFrame:
		header.Type, header.ItemID = bifrostEventEntities, t.describedSegment(scribeSeenEntities, frame.Text)
		event := &scribeEntitiesEvent{scribeExtensionHeader: header, Entities: make([]scribeEntity, 0, len(frame.Entities))}
		for _, wire := range frame.Entities {
			event.Entities = append(event.Entities, scribeEntity{Text: wire.Text, Type: wire.EntityType, StartChar: wire.StartChar, EndChar: wire.EndChar})
		}
		out.client(event)
	case scribeRedactedFrame:
		header.Type, header.ItemID = bifrostEventRedacted, t.describedSegment(scribeSeenRedacted, frame.Text)
		out.client(&scribeRedactedEvent{scribeExtensionHeader: header, RedactedText: frame.RedactedText})
	case scribeEditedFrame:
		header.Type, header.ItemID = bifrostEventEdited, t.describedSegment(scribeSeenEdited, frame.Text)
		out.client(&scribeEditedEvent{scribeExtensionHeader: header, EditedText: frame.EditedText})
	default:
		header.Type, header.ItemID = bifrostEventProviderEvent, ""
		out.client(&scribeProviderEvent{scribeExtensionHeader: header, Provider: "elevenlabs", Event: raw})
	}
}
