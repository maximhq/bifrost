package soniox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const sonioxMaxPendingCommits = 16 // commits a client may have waiting for Soniox's answer

// sonioxTranslator is the RealtimeTranslator for one Soniox transcription connection. The OpenAI Realtime
// client and Soniox differ in ways a per-event mapping cannot bridge:
//
//   - Soniox takes every setting in a start frame it fixes for the connection. The translator sends it with
//     the client's first session.update that names the model, or before the first audio, and holds the later
//     ones to those settings.
//   - Audio is a binary frame and control is a text frame, where OpenAI sends both as JSON.
//   - Soniox streams tokens, not events. The final ones are sent once; the non-final ones of each frame
//     replace the last frame's. The partial text is the two together, and it may be revised, while an OpenAI
//     delta only appends. The delta event therefore carries the appended text, when there is any, and the
//     whole partial text in a transcript field OpenAI does not send.
//   - A segment ends with an <end> token when Soniox detects the end of an utterance, and with a <fin> token
//     when it answers a commit, which is a finalize message. A client that keeps streaming commits again
//     before Soniox has answered, so each commit is queued and Soniox answers the queue in order.
//   - Soniox reports no usage per segment. It reports how much audio it has processed, so the translator
//     reports the audio processed since the last segment ended.
//
// The relay calls it one step at a time, so it holds no locks.
type sonioxTranslator struct {
	model     string
	seq       int
	sessionID string
	session   []byte          // the settings the client has sent so far, which each session.update builds on
	draft     *sonioxSettings // what those settings amount to, until the connection starts
	settings  *sonioxSettings // the settings the connection started with
	defaults  *sonioxSettings // what the connection starts with when the client has sent none

	item       string         // the segment Soniox is transcribing, until it ends
	lastItem   string         // the segment announced as committed last
	windowTail string         // the last item announced in the oldest commit's window, which a later utterance of it follows
	pending    []sonioxCommit // the commits the client made that Soniox has not answered, oldest first
	final      strings.Builder
	nonFinal   string
	partial    string        // the latest partial text of the segment
	sent       string        // the text the segment's deltas have added up to
	segment    []SonioxToken // the final tokens of the segment, kept for the words event
	sentBytes  int           // the audio sent to Soniox
	usedBytes  int           // the part of it a commit or an endpoint has used, so it is not in the buffer
	usageMs    int           // the audio processed that completed events have reported
	failed     bool          // Soniox reported an error, so its close is not reported again
	extensions bool          // the client asked for the Bifrost extension events
}

// sonioxCommit is a commit the client made. done marks one an endpoint already ended, which still waits for
// the <fin> token that answers its finalize message.
type sonioxCommit struct {
	item string
	done bool
}

func newSonioxTranslator(model string) *sonioxTranslator {
	// The defaults name no setting that can fail.
	defaults, _ := newSonioxSettings(model, nil)
	t := &sonioxTranslator{model: model, defaults: defaults}
	t.sessionID = "sess_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	return t
}

func (t *sonioxTranslator) id(prefix string) string {
	t.seq++
	return prefix + strconv.Itoa(t.seq)
}

func (t *sonioxTranslator) newEvent(eventType string) *sonioxEvent {
	return &sonioxEvent{Type: eventType, EventID: t.id("event_")}
}

// current returns the settings the connection runs on, or will start with.
func (t *sonioxTranslator) current() *sonioxSettings {
	switch {
	case t.settings != nil:
		return t.settings
	case t.draft != nil:
		return t.draft
	}
	return t.defaults
}

func (t *sonioxTranslator) sessionObject() *sonioxSession {
	input := t.current().input
	input.Transcription.Model = t.model
	session := &sonioxSession{
		ID:     t.sessionID,
		Object: "realtime.session",
		Type:   "transcription",
		Audio:  sonioxSessionIO{Input: input},
	}
	if t.extensions {
		session.Bifrost = &sonioxBifrostOptions{Extensions: true}
	}
	return session
}

// Open announces the session before the client speaks. Soniox sends nothing until it has its start frame.
func (t *sonioxTranslator) Open() schemas.RealtimeTranslation {
	out := &sonioxOutput{}
	event := t.newEvent("session.created")
	event.Session = t.sessionObject()
	out.client(event)
	return out.RealtimeTranslation
}

// Close reports a close that carries a reason as an error, unless Soniox has already sent one. Soniox reports
// its errors in a frame and then closes normally, so this is for a connection that dropped.
func (t *sonioxTranslator) Close(code int, reason string) schemas.RealtimeTranslation {
	if t.failed || reason == "" || code == 1000 || code == 1001 || code == 1005 {
		return schemas.RealtimeTranslation{}
	}
	out := &sonioxOutput{}
	event := t.newEvent("error")
	event.Error = &sonioxError{Type: "server_error", Code: "soniox_close_" + strconv.Itoa(code), Message: reason}
	out.client(event)
	return out.RealtimeTranslation
}

// ToProvider translates one client event. An error names what Soniox cannot do, and leaves the session as it
// was.
func (t *sonioxTranslator) ToProvider(event *schemas.BifrostRealtimeEvent) (schemas.RealtimeTranslation, error) {
	out := &sonioxOutput{}
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
		err = fmt.Errorf("%s is not supported by a soniox transcription session", event.Type)
	}
	if err == nil {
		err = out.err
	}
	if err != nil {
		return schemas.RealtimeTranslation{}, err
	}
	return out.RealtimeTranslation, nil
}

// sessionUpdate builds on the settings the client has sent. The first update that names the model starts the
// connection. Soniox cannot change a session once started, so a later update may only repeat the settings,
// which is common in clients that send them on every connect, and is acknowledged.
func (t *sonioxTranslator) sessionUpdate(out *sonioxOutput, event *schemas.BifrostRealtimeEvent) error {
	merged, err := providerUtils.MergeJSONObjects(t.session, []byte(providerUtils.GetJSONField(event.RawData, "session").Raw))
	if err != nil {
		return fmt.Errorf("invalid session in session.update: %w", err)
	}
	settings, err := newSonioxSettings(t.model, merged)
	if err != nil {
		return err
	}
	if t.settings != nil && !bytes.Equal(settings.frame, t.settings.frame) {
		return errors.New("soniox fixes a session's settings when it starts, so this session.update would change them; send the settings first or open a new session")
	}
	t.session, t.draft, t.extensions = merged, settings, settings.extensions
	if t.settings == nil && sonioxNamesModel(merged) {
		t.start(out)
	}
	reply := t.newEvent("session.updated")
	reply.Session = t.sessionObject()
	out.client(reply)
	return nil
}

// start sends the start frame with the settings the client has named so far.
func (t *sonioxTranslator) start(out *sonioxOutput) {
	t.settings = t.current()
	out.Provider = append(out.Provider, t.settings.frame)
}

// audioAppend sends the audio as a binary frame. A client that sends audio before it names the model starts
// the connection on the default settings.
func (t *sonioxTranslator) audioAppend(out *sonioxOutput, event *schemas.BifrostRealtimeEvent) error {
	audio := event.Audio
	if len(audio) == 0 {
		data := providerUtils.GetJSONField(event.RawData, "audio").String()
		if data == "" {
			return errors.New("audio must be set for input_audio_buffer.append")
		}
		var err error
		if audio, err = base64.StdEncoding.DecodeString(data); err != nil {
			return fmt.Errorf("audio of input_audio_buffer.append is not base64: %w", err)
		}
	}
	if t.settings == nil {
		t.start(out)
	}
	out.AddProviderBinary(audio)
	t.sentBytes += len(audio)
	return nil
}

// audioCommit asks Soniox to finalize and tells the client the segment is committed. Soniox answers with a
// <fin> token, which ends the segment. The audio sent from here on belongs to the next segment, so a commit
// with none since the last is refused, as OpenAI refuses an empty buffer.
func (t *sonioxTranslator) audioCommit(out *sonioxOutput) error {
	if t.sentBytes == t.usedBytes {
		return errors.New("the input audio buffer is empty")
	}
	if len(t.pending) >= sonioxMaxPendingCommits {
		return fmt.Errorf("soniox has not answered the last %d commits yet", sonioxMaxPendingCommits)
	}
	out.Provider = append(out.Provider, sonioxFinalizeFrame)
	// With nothing pending the segment streaming now is this commit's; behind a commit it is a new item.
	var id string
	if len(t.pending) == 0 {
		id = t.ensureItem()
	} else {
		id = t.id("item_")
	}
	t.pending = append(t.pending, sonioxCommit{item: id})
	t.usedBytes = t.sentBytes
	t.committed(out, id, t.lastItem)
	t.lastItem = id
	return nil
}

// audioClear accepts a clear that has nothing to discard, which clients send on every interruption. Audio
// already sent cannot be taken back from Soniox.
func (t *sonioxTranslator) audioClear(out *sonioxOutput) error {
	if t.sentBytes > t.usedBytes {
		return errors.New("soniox cannot discard audio it has already been sent; commit it, or open a new session")
	}
	out.client(t.newEvent("input_audio_buffer.cleared"))
	return nil
}

// waiting returns the oldest commit when no endpoint has ended it yet, or an empty string.
func (t *sonioxTranslator) waiting() string {
	if len(t.pending) > 0 && !t.pending[0].done {
		return t.pending[0].item
	}
	return ""
}

// ensureItem names the segment Soniox is transcribing: the oldest commit it has not ended, or else the one
// still streaming.
func (t *sonioxTranslator) ensureItem() string {
	if t.item == "" {
		if t.item = t.waiting(); t.item == "" {
			t.item = t.id("item_")
		}
	}
	return t.item
}

// committed announces a segment as committed, to be inserted after the item previous.
func (t *sonioxTranslator) committed(out *sonioxOutput, id, previous string) {
	event := t.newEvent("input_audio_buffer.committed")
	event.ItemID, event.PreviousItemID = id, previous
	out.client(event)
}

// ToClient translates one Soniox frame. A frame that is not JSON ends the session.
func (t *sonioxTranslator) ToClient(frame json.RawMessage) (schemas.RealtimeTranslation, error) {
	var parsed sonioxFrame
	if err := schemas.Unmarshal(frame, &parsed); err != nil {
		return schemas.RealtimeTranslation{}, fmt.Errorf("invalid soniox frame: %w", err)
	}
	out := &sonioxOutput{}
	if parsed.ErrorCode != 0 {
		t.failure(out, &parsed)
	} else {
		t.tokens(out, &parsed)
	}
	if out.err != nil {
		return schemas.RealtimeTranslation{}, out.err
	}
	return out.RealtimeTranslation, nil
}

// failure reports an error frame. Soniox closes after it, so the commits it will not answer are dropped.
func (t *sonioxTranslator) failure(out *sonioxOutput, frame *sonioxFrame) {
	t.failed = true
	t.pending = t.pending[:0]
	event := t.newEvent("error")
	event.Error = &sonioxError{Type: sonioxErrorType(frame.ErrorCode), Code: frame.ErrorType, Message: frame.ErrorMessage}
	out.client(event)
}

// tokens applies one frame of tokens. The final ones add to the segment, in order, and the special tokens end
// it. The non-final ones replace the partial text of the last frame; a frame with no tokens, such as the
// answer to a keepalive, leaves it as it was.
func (t *sonioxTranslator) tokens(out *sonioxOutput, frame *sonioxFrame) {
	if len(frame.Tokens) == 0 {
		return
	}
	var nonFinal strings.Builder
	for i := range frame.Tokens {
		token := &frame.Tokens[i]
		switch {
		case !token.IsFinal:
			nonFinal.WriteString(token.Text)
		case token.Text == sonioxEndToken:
			// Audio from the first token after the end on already belongs to the next utterance, so the endpoint does not use it up.
			consumedMs := frame.TotalAudioProcMs
			for _, next := range frame.Tokens[i+1:] {
				if next.Text != sonioxEndToken && next.Text != sonioxFinalizeToken && next.StartMs > 0 {
					consumedMs = min(consumedMs, next.StartMs)
					break
				}
			}
			t.endpoint(out, frame.TotalAudioProcMs, consumedMs)
		case token.Text == sonioxFinalizeToken:
			t.finalized(out, frame.TotalAudioProcMs)
		default:
			t.final.WriteString(token.Text)
			if t.extensions {
				t.segment = append(t.segment, SonioxToken{Text: token.Text, StartMs: token.StartMs, EndMs: token.EndMs, Speaker: token.Speaker, Language: token.Language})
			}
		}
	}
	t.nonFinal = nonFinal.String()
	t.partialText(out)
}

// partialText reports the segment so far. A partial the segment has already shown adds nothing.
//
// The transcript field carries the whole partial. The delta carries the words of it that are finished, the
// ones before its last space, that add to what the deltas have sent: the last word is the one Soniox revises
// most, and a delta cannot be taken back. A partial that revises words already sent adds nothing until the
// text agrees with what was sent again. The completed event carries the final text, and the deltas are
// brought up to it first.
func (t *sonioxTranslator) partialText(out *sonioxOutput) {
	text := strings.TrimLeftFunc(t.final.String()+t.nonFinal, unicode.IsSpace)
	if text == "" || text == t.partial {
		return
	}
	t.partial = text
	delta := t.nextDelta(finishedWords(text))
	event := t.newEvent(string(schemas.RTEventInputAudioTransDelta))
	event.ItemID, event.ContentIndex = t.ensureItem(), new(0)
	event.Delta, event.Transcript = &delta, &text
	out.client(event)
}

// finishedWords returns the text up to and including its last space.
func finishedWords(text string) string {
	end := strings.LastIndexFunc(text, unicode.IsSpace)
	if end < 0 {
		return ""
	}
	_, size := utf8.DecodeRuneInString(text[end:])
	return text[:end+size]
}

// nextDelta returns what text adds to the deltas sent so far, or nothing when it does not extend them.
func (t *sonioxTranslator) nextDelta(text string) string {
	if !strings.HasPrefix(text, t.sent) {
		return ""
	}
	delta := text[len(t.sent):]
	t.sent = text
	return delta
}

// endpoint ends the segment Soniox found the end of: the oldest commit, if no endpoint has ended it yet, or
// else a segment of its own. Soniox answers the commits in order, so only the oldest owns the audio now.
func (t *sonioxTranslator) endpoint(out *sonioxOutput, totalMs, consumedMs int) {
	// The audio of the ended utterance is used up; what was sent after it is still in flight, and stays buffered.
	t.usedBytes = max(t.usedBytes, min(t.sentBytes, consumedMs*t.current().bytesPerSecond/1000))
	if len(t.pending) > 0 && !t.pending[0].done {
		t.pending[0].done = true
		t.windowTail = t.pending[0].item
		t.complete(out, t.pending[0].item, totalMs)
		return
	}
	t.standalone(out, totalMs)
}

// standalone ends a segment no commit asked for, which is announced as committed here. A segment with no text,
// such as one after silence, ends nothing.
func (t *sonioxTranslator) standalone(out *sonioxOutput, totalMs int) {
	if strings.TrimSpace(t.final.String()) == "" {
		t.resetSegment()
		return
	}
	id := t.ensureItem()
	if len(t.pending) == 0 {
		t.committed(out, id, t.lastItem)
		t.lastItem = id
	} else {
		// Behind queued commits, a later utterance of the oldest one's window follows that window, not the newest commit.
		t.committed(out, id, t.windowTail)
		t.windowTail = id
		if len(t.pending) == 1 {
			t.lastItem = id // the oldest window is the newest too, so a commit made now follows its last utterance
		}
	}
	t.complete(out, id, totalMs)
}

// finalized answers the oldest commit, which closes its window of audio. An endpoint may have ended the commit
// already, in which case the text after that endpoint is a segment of its own.
func (t *sonioxTranslator) finalized(out *sonioxOutput, totalMs int) {
	if len(t.pending) == 0 {
		return
	}
	if commit := t.pending[0]; commit.done {
		t.standalone(out, totalMs)
	} else {
		t.complete(out, commit.item, totalMs)
	}
	t.pending = slices.Delete(t.pending, 0, 1)
	t.windowTail = ""
}

// complete ends a segment with the text its final tokens add up to and the audio processed since the last
// one, after the deltas are brought up to that text.
func (t *sonioxTranslator) complete(out *sonioxOutput, id string, totalMs int) {
	text := strings.TrimSpace(t.final.String())
	if delta := t.nextDelta(text); delta != "" {
		flush := t.newEvent(string(schemas.RTEventInputAudioTransDelta))
		flush.ItemID, flush.ContentIndex, flush.Delta, flush.Transcript = id, new(0), &delta, &text
		out.client(flush)
	}

	added := t.newEvent(string(schemas.RTEventConversationItemAdded))
	added.Item = &sonioxItem{ID: id, Object: "realtime.item", Type: "message", Status: "completed", Role: "user", Content: []sonioxPart{{Type: "input_audio"}}}
	out.client(added)

	var seconds float64
	if totalMs > t.usageMs {
		seconds = float64(totalMs-t.usageMs) / 1000
		t.usageMs = totalMs
	}
	completed := t.newEvent(string(schemas.RTEventInputAudioTransCompleted))
	completed.ItemID, completed.ContentIndex, completed.Transcript = id, new(0), &text
	completed.Usage = &sonioxUsage{Type: "duration", Seconds: seconds}
	out.client(completed)

	done := t.newEvent(string(schemas.RTEventConversationItemDone))
	done.Item = &sonioxItem{ID: id, Object: "realtime.item", Type: "message", Status: "completed", Role: "user", Content: []sonioxPart{{Type: "input_audio", Transcript: &text}}}
	out.client(done)

	if t.extensions {
		t.words(out, id)
	}
	t.resetSegment()
}

// words reports the words of a completed segment to a session that asked for the Bifrost extension events.
// Soniox sends them with the text, so they follow the completed event at once, naming the segment by item_id.
func (t *sonioxTranslator) words(out *sonioxOutput, id string) {
	grouped := sonioxTokensToWords(t.segment)
	if len(grouped) == 0 {
		return
	}
	event := &sonioxWordsEvent{
		sonioxExtensionHeader: sonioxExtensionHeader{Type: bifrostEventWords, EventID: t.id("event_"), ItemID: id},
		Words:                 make([]sonioxWord, 0, len(grouped)),
	}
	for _, token := range t.segment {
		if token.Language != nil && *token.Language != "" {
			event.Language = *token.Language
			break
		}
	}
	for _, word := range grouped {
		converted := sonioxWord{Text: word.Word, Start: word.Start, End: word.End}
		if word.Speaker != nil {
			converted.Speaker = *word.Speaker
		}
		event.Words = append(event.Words, converted)
	}
	out.client(event)
}

// resetSegment readies the translator for the next segment.
func (t *sonioxTranslator) resetSegment() {
	t.item, t.partial, t.sent, t.nonFinal = "", "", "", ""
	t.final.Reset()
	t.segment = t.segment[:0]
}
