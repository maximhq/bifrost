package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// liveMaxQueuedBytes bounds the client frames held while the session starts. Live answers setup in
// about a second, so this is minutes of audio; reaching it means the session is not coming up.
const liveMaxQueuedBytes = 4 << 20

const (
	liveEventItemTruncate = "conversation.item.truncate"
	liveEventItemRetrieve = "conversation.item.retrieve"
)

type livePhase int

const (
	liveAwaitingSetup livePhase = iota // nothing has been sent to Live yet
	liveSettingUp                      // the setup frame is out and setupComplete is awaited
	liveReady                          // frames go straight through
)

// liveTranslator is the RealtimeTranslator for one Gemini Live connection. The OpenAI Realtime client
// and Live differ in ways a per-event mapping cannot bridge:
//
//   - Live takes one setup frame, first, and cannot change it. The translator builds it from the first
//     session.update (or from defaults when the client speaks first) and holds later client frames
//     until setupComplete.
//   - A Live frame can carry audio, a transcript, the end of a turn and its usage together, and a
//     turn's usage arrives only on the frame that ends it. One frame becomes several OpenAI events.
//   - Live answers function calls only when all of a turn's outputs arrive in one frame, and it needs
//     the function name that an OpenAI function_call_output does not carry.
//
// The relay calls it one step at a time, so it holds no locks.
type liveTranslator struct {
	model     string
	config    *liveConfig
	phase     livePhase
	sentSetup []byte
	// fromUpdate is set when the setup came from a session.update, which is answered with session.updated.
	fromUpdate  bool
	queued      []json.RawMessage
	queuedBytes int
	sessionID   string
	seq         int

	turns        []*Content          // client turns held until response.create
	outputs      []*FunctionResponse // function outputs held until response.create
	calls        map[string]string   // function call id to name, until the client answers it
	inputActive  bool                // with manual detection, activityStart is sent and activityEnd is not
	audioPending bool                // audio was sent and the model has not started answering it
	nextTextOnly *bool               // the next response's modality, when response.create overrides it

	userItem *liveUserItem
	response *liveResponseState
	usage    *liveUsage
}

// liveUserItem is the conversation item for one utterance the client spoke.
type liveUserItem struct {
	id         string
	transcript string
	stopped    bool
}

// liveResponseState is the response being built from Live's turn.
type liveResponseState struct {
	id          string
	textOnly    bool
	items       []liveItem
	message     *liveMessage
	nextIndex   int
	interrupted bool
}

// liveMessage is the assistant message item of a response while its content streams.
type liveMessage struct {
	id    string
	index int
	text  strings.Builder
	audio bool
}

func newLiveTranslator(model string) (*liveTranslator, error) {
	config, err := newLiveConfig(model, &liveSessionUpdate{})
	if err != nil {
		return nil, err
	}
	t := &liveTranslator{model: model, config: config, calls: map[string]string{}}
	t.sessionID = t.id("sess_")
	t.config.session.ID = t.sessionID
	return t, nil
}

func (t *liveTranslator) id(prefix string) string {
	t.seq++
	return prefix + strconv.Itoa(t.seq)
}

func (t *liveTranslator) newEvent(eventType string) *liveEvent {
	return &liveEvent{Type: eventType, EventID: t.id("event_")}
}

// Open announces the session before the client speaks. Nothing reaches Live yet: its setup waits for
// the client's first session.update or event.
func (t *liveTranslator) Open() schemas.RealtimeTranslation {
	out := &liveOutput{}
	event := t.newEvent("session.created")
	event.Session = &t.config.session
	out.client(event)
	// The event is plain data, so marshaling it cannot fail.
	return out.RealtimeTranslation
}

// Close turns the reason Live closed with into an error event for the client.
func (t *liveTranslator) Close(code int, reason string) schemas.RealtimeTranslation {
	failure := liveCloseError(code, reason)
	if failure == nil {
		return schemas.RealtimeTranslation{}
	}
	out := &liveOutput{}
	event := t.newEvent("error")
	event.Error = failure
	out.client(event)
	return out.RealtimeTranslation
}

// ToProvider translates one client event. An error names what Live cannot do, and leaves the
// session as it was.
func (t *liveTranslator) ToProvider(event *schemas.BifrostRealtimeEvent) (schemas.RealtimeTranslation, error) {
	out := &liveOutput{}
	var err error
	switch event.Type {
	case schemas.RTEventSessionUpdate:
		err = t.sessionUpdate(out, event)
	case schemas.RTEventConversationItemCreate:
		err = t.itemCreate(out, event)
	case schemas.RTEventResponseCreate:
		err = t.responseCreate(out, event)
	case schemas.RTEventInputAudioAppend:
		err = t.audioAppend(out, event)
	case schemas.RTEventInputAudioCommit:
		err = t.audioCommit(out)
	case schemas.RTEventResponseCancel, schemas.RTEventInputAudioClear, liveEventItemTruncate:
		// Accepted and ignored: Live interrupts the model and drops unplayed audio when new input arrives.
	case schemas.RTEventConversationItemDelete, liveEventItemRetrieve:
		err = fmt.Errorf("%s is not supported by gemini live", event.Type)
	default:
		err = fmt.Errorf("unsupported event type %q", event.Type)
	}
	if err == nil {
		err = out.err
	}
	if err != nil {
		return schemas.RealtimeTranslation{}, err
	}
	return out.RealtimeTranslation, nil
}

// send routes a frame for Live. The setup frame goes out the moment the client first speaks, and
// frames behind it wait for setupComplete.
func (t *liveTranslator) send(out *liveOutput, message *GeminiLiveClientMessage) error {
	t.ensureSetup(out)
	frame := out.marshal(message)
	if frame == nil {
		return nil
	}
	if t.phase == liveReady {
		out.Provider = append(out.Provider, frame)
		return nil
	}
	if t.queuedBytes += len(frame); t.queuedBytes > liveMaxQueuedBytes {
		return errors.New("the gemini live session did not start in time")
	}
	t.queued = append(t.queued, frame)
	return nil
}

// ensureSetup starts the session from the current settings if the client has not configured one, so the
// setup handshake runs while the client is still building its first turn.
func (t *liveTranslator) ensureSetup(out *liveOutput) {
	if t.phase == liveAwaitingSetup {
		t.startSetup(out, false)
	}
}

func (t *liveTranslator) startSetup(out *liveOutput, fromUpdate bool) {
	frame := out.marshal(&GeminiLiveClientMessage{Setup: t.config.setup})
	if frame == nil {
		return
	}
	t.sentSetup = frame
	t.fromUpdate = fromUpdate
	t.phase = liveSettingUp
	out.Provider = append(out.Provider, frame)
}

func (t *liveTranslator) sessionUpdate(out *liveOutput, event *schemas.BifrostRealtimeEvent) error {
	update, err := parseLiveSessionUpdate(event)
	if err != nil {
		return err
	}
	config, err := newLiveConfig(t.model, update)
	if err != nil {
		return err
	}
	config.session.ID = t.sessionID
	if t.phase == liveAwaitingSetup {
		t.config = config
		t.startSetup(out, true)
		return nil
	}
	// Live cannot change a session once set up. Repeating its configuration is harmless, and common
	// in clients that send their settings on every connect, so it is acknowledged.
	if resent := out.marshal(&GeminiLiveClientMessage{Setup: config.setup}); out.err != nil || !bytes.Equal(resent, t.sentSetup) {
		if out.err != nil {
			return out.err
		}
		return errors.New("gemini live fixes a session's configuration with the first message, so this session.update would change it; send the settings first or open a new session")
	}
	// What the setup does not hold can still change: the text/audio choice and the input rate.
	t.config = config
	reply := t.newEvent("session.updated")
	reply.Session = &t.config.session
	out.client(reply)
	return nil
}

func (t *liveTranslator) audioAppend(out *liveOutput, event *schemas.BifrostRealtimeEvent) error {
	data := providerUtils.GetJSONField(event.RawData, "audio").String()
	if data == "" && len(event.Audio) > 0 {
		data = base64.StdEncoding.EncodeToString(event.Audio)
	}
	if data == "" {
		return errors.New("audio must be set for input_audio_buffer.append")
	}
	if t.config.manualVAD && !t.inputActive {
		t.inputActive = true
		if err := t.send(out, &GeminiLiveClientMessage{RealtimeInput: &GeminiLiveRealtimeInput{ActivityStart: &GeminiLiveActivity{}}}); err != nil {
			return err
		}
	}
	t.audioPending = true
	return t.send(out, &GeminiLiveClientMessage{RealtimeInput: &GeminiLiveRealtimeInput{Audio: &GeminiLiveBlob{Data: data, MimeType: t.config.audioMime}}})
}

func (t *liveTranslator) audioCommit(out *liveOutput) error {
	if !t.audioPending {
		return errors.New("the input audio buffer is empty")
	}
	if !t.config.manualVAD {
		return t.send(out, &GeminiLiveClientMessage{RealtimeInput: &GeminiLiveRealtimeInput{AudioStreamEnd: true}})
	}
	return t.endActivity(out)
}

// endActivity closes the utterance the client marked with activityStart.
func (t *liveTranslator) endActivity(out *liveOutput) error {
	if !t.inputActive {
		return nil
	}
	t.inputActive = false
	return t.send(out, &GeminiLiveClientMessage{RealtimeInput: &GeminiLiveRealtimeInput{ActivityEnd: &GeminiLiveActivity{}}})
}

// liveClientItem is a conversation item the client created, as a message or a function output.
type liveClientItem struct {
	ID      string      `json:"id"`
	Type    string      `json:"type"`
	Role    string      `json:"role"`
	Content liveContent `json:"content"`
	CallID  string      `json:"call_id"`
	Output  string      `json:"output"`
}

type liveContentPart struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	Transcript string `json:"transcript"`
}

// liveContent is an item's content, an array of parts or a plain string.
type liveContent []liveContentPart

func (c *liveContent) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil {
		*c = liveContent{{Type: "input_text", Text: text}}
		return nil
	}
	return json.Unmarshal(data, (*[]liveContentPart)(c))
}

func (t *liveTranslator) itemCreate(out *liveOutput, event *schemas.BifrostRealtimeEvent) error {
	var item liveClientItem
	if err := schemas.Unmarshal([]byte(providerUtils.GetJSONField(event.RawData, "item").Raw), &item); err != nil {
		return fmt.Errorf("invalid item in conversation.item.create: %w", err)
	}
	echo, err := t.bufferItem(&item)
	if err != nil {
		return err
	}
	t.ensureSetup(out)
	added := t.newEvent("conversation.item.added")
	added.Item = echo
	out.client(added)
	done := t.newEvent("conversation.item.done")
	done.Item = echo
	out.client(done)
	return nil
}

// bufferItem holds a client item until response.create, because Live wants a turn's items together,
// and returns the item as the client should see it created.
func (t *liveTranslator) bufferItem(item *liveClientItem) (*liveItem, error) {
	echo := &liveItem{ID: item.ID, Object: "realtime.item", Type: item.Type, Status: "completed", Role: item.Role}
	if echo.ID == "" {
		echo.ID = t.id("item_")
	}
	switch item.Type {
	case "message":
		var role, partType string
		switch item.Role {
		case "user":
			role, partType = "user", "input_text"
		case "assistant":
			role, partType = "model", "output_text"
		default:
			return nil, fmt.Errorf("messages with role %q cannot be added to a gemini live session, set instructions with session.update", item.Role)
		}
		turn := &Content{Role: role}
		for _, part := range item.Content {
			switch part.Type {
			case "input_text", "output_text", "text":
				turn.Parts = append(turn.Parts, &Part{Text: part.Text})
				echo.Content = append(echo.Content, livePart{Type: partType, Text: new(part.Text)})
			case "input_audio", "output_audio":
				if part.Transcript == "" {
					return nil, errors.New("audio in a conversation item is not supported by gemini live, stream it with input_audio_buffer.append")
				}
				turn.Parts = append(turn.Parts, &Part{Text: part.Transcript})
				echo.Content = append(echo.Content, livePart{Type: partType, Text: new(part.Transcript)})
			default:
				return nil, fmt.Errorf("content of type %q is not supported by gemini live", part.Type)
			}
		}
		if len(turn.Parts) == 0 {
			return nil, errors.New("the message has no content gemini live can use")
		}
		t.turns = append(t.turns, turn)
	case "function_call_output":
		if item.CallID == "" {
			return nil, errors.New("a function_call_output needs a call_id")
		}
		response, err := liveToolOutput(item.Output)
		if err != nil {
			return nil, err
		}
		t.outputs = append(t.outputs, &FunctionResponse{ID: item.CallID, Name: t.calls[item.CallID], Response: response})
		delete(t.calls, item.CallID)
		echo.CallID, echo.Output = item.CallID, item.Output
	default:
		return nil, fmt.Errorf("items of type %q are not supported by gemini live", item.Type)
	}
	return echo, nil
}

// liveToolOutput shapes a function's output as the JSON object Live requires.
func liveToolOutput(output string) (json.RawMessage, error) {
	if gjson.Parse(output).IsObject() {
		return json.RawMessage(output), nil
	}
	wrapped, err := providerUtils.MarshalSorted(struct {
		Output string `json:"output"`
	}{output})
	return wrapped, err
}

// liveResponseCreate is the response object of a response.create event.
type liveResponseCreate struct {
	Instructions     string           `json:"instructions"`
	Modalities       []string         `json:"modalities"`
	OutputModalities []string         `json:"output_modalities"`
	Input            []liveClientItem `json:"input"`
}

func (t *liveTranslator) responseCreate(out *liveOutput, event *schemas.BifrostRealtimeEvent) error {
	var create liveResponseCreate
	if raw := event.ExtraParams["response"]; len(raw) > 0 {
		if err := schemas.Unmarshal(raw, &create); err != nil {
			return fmt.Errorf("invalid response in response.create: %w", err)
		}
	}
	turnsHeld, outputsHeld := len(t.turns), len(t.outputs)
	for i := range create.Input {
		if _, err := t.bufferItem(&create.Input[i]); err != nil {
			t.turns, t.outputs = t.turns[:turnsHeld], t.outputs[:outputsHeld]
			return err
		}
	}
	// Live cannot scope instructions to one response, so they ride along as a turn from the user.
	if strings.TrimSpace(create.Instructions) != "" {
		t.turns = append(t.turns, &Content{Role: "user", Parts: []*Part{{Text: create.Instructions}}})
	}
	if textOnly, ok := liveWantsTextOnly(create.OutputModalities, create.Modalities); ok {
		t.nextTextOnly = &textOnly
	}

	turns, outputs := t.turns, t.outputs
	t.turns, t.outputs = nil, nil
	switch {
	case len(outputs) > 0:
		if len(turns) > 0 {
			if err := t.send(out, &GeminiLiveClientMessage{ClientContent: &GeminiLiveClientContent{Turns: turns}}); err != nil {
				return err
			}
		}
		return t.send(out, &GeminiLiveClientMessage{ToolResponse: &GeminiLiveToolResponse{FunctionResponses: outputs}})
	case len(turns) > 0:
		return t.send(out, &GeminiLiveClientMessage{ClientContent: &GeminiLiveClientContent{Turns: turns, TurnComplete: true}})
	case t.inputActive:
		return t.endActivity(out)
	case t.audioPending:
		// The audio already asked for an answer, and asking again would interrupt it.
		return nil
	default:
		return t.send(out, &GeminiLiveClientMessage{ClientContent: &GeminiLiveClientContent{TurnComplete: true}})
	}
}

// ToClient translates one Live frame into the OpenAI events it carries.
func (t *liveTranslator) ToClient(frame json.RawMessage) (schemas.RealtimeTranslation, error) {
	var message GeminiLiveServerMessage
	if err := schemas.Unmarshal(frame, &message); err != nil {
		return schemas.RealtimeTranslation{}, fmt.Errorf("invalid gemini live frame: %w", err)
	}
	out := &liveOutput{}
	if message.SetupComplete != nil {
		t.setupComplete(out)
	}
	// Usage rides on the frame that ends the turn, so it is taken in before the turn is finished.
	if message.UsageMetadata != nil {
		if t.usage == nil {
			t.usage = &liveUsage{}
		}
		t.usage.add(message.UsageMetadata)
	}
	if message.VoiceActivity != nil {
		t.voiceActivity(out, message.VoiceActivity)
	}
	if message.ServerContent != nil {
		t.serverContent(out, message.ServerContent)
	}
	if message.ToolCall != nil {
		t.toolCall(out, message.ToolCall)
	}
	if message.ToolCallCancellation != nil {
		for _, id := range message.ToolCallCancellation.IDs {
			delete(t.calls, id)
		}
	}
	if out.err != nil {
		return schemas.RealtimeTranslation{}, out.err
	}
	return out.RealtimeTranslation, nil
}

func (t *liveTranslator) setupComplete(out *liveOutput) {
	if t.phase != liveSettingUp {
		return
	}
	t.phase = liveReady
	out.Provider = append(out.Provider, t.queued...)
	t.queued, t.queuedBytes = nil, 0
	if t.fromUpdate {
		event := t.newEvent("session.updated")
		event.Session = &t.config.session
		out.client(event)
	}
}

func (t *liveTranslator) serverContent(out *liveOutput, content *GeminiLiveServerContent) {
	if content.Interrupted && t.response != nil {
		t.response.interrupted = true
		t.closeMessage(out, "incomplete")
	}
	if content.InputTranscription != nil {
		t.inputTranscript(out, content.InputTranscription.Text)
	}
	if content.ModelTurn != nil {
		for _, part := range content.ModelTurn.Parts {
			switch {
			case part.Thought:
			case part.InlineData != nil && strings.HasPrefix(part.InlineData.MimeType, "audio/"):
				t.appendAudio(out, part.InlineData.Data)
			case part.Text != "":
				t.appendText(out, part.Text)
			}
		}
	}
	if content.OutputTranscription != nil && content.OutputTranscription.Text != "" {
		t.appendText(out, content.OutputTranscription.Text)
	}
	if content.GenerationComplete {
		t.closeMessage(out, "completed")
	}
	// An extended-thinking model ends a turn with IN_PROGRESS when it will keep talking, and with IDLE
	// when it is done. The response stays open across the first.
	if content.TurnComplete && content.InteractionStatus != "IN_PROGRESS" {
		t.finishResponse(out)
	}
}

// ensureResponse opens the response the next output belongs to.
func (t *liveTranslator) ensureResponse(out *liveOutput) *liveResponseState {
	if t.response != nil {
		return t.response
	}
	textOnly := t.config.textOnly
	if t.nextTextOnly != nil {
		textOnly, t.nextTextOnly = *t.nextTextOnly, nil
	}
	t.response = &liveResponseState{id: t.id("resp_"), textOnly: textOnly, items: []liveItem{}}
	t.audioPending = false
	event := t.newEvent("response.created")
	event.Response = &liveResponse{ID: t.response.id, Object: "realtime.response", Status: "in_progress", Output: []liveItem{}}
	out.client(event)
	return t.response
}

// openMessage returns the assistant message the response is currently writing, starting one if needed.
func (t *liveTranslator) openMessage(out *liveOutput) (*liveResponseState, *liveMessage) {
	r := t.ensureResponse(out)
	if r.message != nil {
		return r, r.message
	}
	r.message = &liveMessage{id: t.id("item_"), index: r.nextIndex}
	r.nextIndex++
	item := &liveItem{ID: r.message.id, Object: "realtime.item", Type: "message", Status: "in_progress", Role: "assistant"}
	added := t.newEvent("response.output_item.added")
	added.ResponseID, added.OutputIndex, added.Item = r.id, new(r.message.index), item
	out.client(added)
	conversation := t.newEvent("conversation.item.added")
	conversation.Item = item
	out.client(conversation)

	part := &livePart{Type: "output_audio", Transcript: new("")}
	if r.textOnly {
		part = &livePart{Type: "output_text", Text: new("")}
	}
	partAdded := t.newEvent("response.content_part.added")
	partAdded.ResponseID, partAdded.ItemID, partAdded.OutputIndex, partAdded.ContentIndex, partAdded.Part = r.id, r.message.id, new(r.message.index), new(0), part
	out.client(partAdded)
	return r, r.message
}

func (t *liveTranslator) delta(r *liveResponseState, m *liveMessage, eventType, delta string) *liveEvent {
	event := t.newEvent(eventType)
	event.ResponseID, event.ItemID, event.OutputIndex, event.ContentIndex, event.Delta = r.id, m.id, new(m.index), new(0), &delta
	return event
}

func (t *liveTranslator) appendAudio(out *liveOutput, data string) {
	r, m := t.openMessage(out)
	if r.textOnly || data == "" {
		return
	}
	m.audio = true
	out.client(t.delta(r, m, "response.output_audio.delta", data))
}

// appendText adds to the message text, which for Live is the transcript of the audio it speaks.
func (t *liveTranslator) appendText(out *liveOutput, text string) {
	r, m := t.openMessage(out)
	m.text.WriteString(text)
	eventType := "response.output_audio_transcript.delta"
	if r.textOnly {
		eventType = "response.output_text.delta"
	}
	out.client(t.delta(r, m, eventType, text))
}

// closeMessage finishes the open assistant message, if any, with the given item status.
func (t *liveTranslator) closeMessage(out *liveOutput, status string) {
	r := t.response
	if r == nil || r.message == nil {
		return
	}
	m := r.message
	r.message = nil
	text := m.text.String()
	base := func(eventType string) *liveEvent {
		event := t.newEvent(eventType)
		event.ResponseID, event.ItemID, event.OutputIndex, event.ContentIndex = r.id, m.id, new(m.index), new(0)
		return event
	}

	part := livePart{Type: "output_audio", Transcript: &text}
	if r.textOnly {
		part = livePart{Type: "output_text", Text: &text}
		done := base("response.output_text.done")
		done.Text = &text
		out.client(done)
	} else {
		if m.audio {
			out.client(base("response.output_audio.done"))
		}
		done := base("response.output_audio_transcript.done")
		done.Transcript = &text
		out.client(done)
	}
	partDone := base("response.content_part.done")
	partDone.Part = &part
	out.client(partDone)

	item := liveItem{ID: m.id, Object: "realtime.item", Type: "message", Status: status, Role: "assistant", Content: []livePart{part}}
	r.items = append(r.items, item)
	itemDone := t.newEvent("response.output_item.done")
	itemDone.ResponseID, itemDone.OutputIndex, itemDone.Item = r.id, new(m.index), &item
	out.client(itemDone)
	conversationDone := t.newEvent("conversation.item.done")
	conversationDone.Item = &item
	out.client(conversationDone)
}

// finishResponse ends the response with the usage Live reported for it. A turn that produced no
// output still gets a response, so its usage reaches the turn log.
func (t *liveTranslator) finishResponse(out *liveOutput) {
	r := t.ensureResponse(out)
	status, itemStatus, details := "completed", "completed", (*liveStatusDetails)(nil)
	if r.interrupted {
		status, itemStatus, details = "cancelled", "incomplete", &liveStatusDetails{Type: "cancelled", Reason: "turn_detected"}
	}
	t.closeMessage(out, itemStatus)
	done := t.newEvent("response.done")
	done.Response = &liveResponse{ID: r.id, Object: "realtime.response", Status: status, StatusDetails: details, Output: r.items, Usage: t.usage}
	out.client(done)
	t.response, t.usage = nil, nil
}

// toolCall turns Live's function calls into function_call items, which the client answers.
func (t *liveTranslator) toolCall(out *liveOutput, call *GeminiLiveToolCall) {
	t.closeMessage(out, "completed")
	r := t.ensureResponse(out)
	for _, function := range call.FunctionCalls {
		arguments := liveCompactJSON(function.Args)
		index := r.nextIndex
		r.nextIndex++
		t.calls[function.ID] = function.Name

		pending := &liveItem{ID: t.id("item_"), Object: "realtime.item", Type: "function_call", Status: "in_progress", Name: function.Name, CallID: function.ID}
		added := t.newEvent("response.output_item.added")
		added.ResponseID, added.OutputIndex, added.Item = r.id, new(index), pending
		out.client(added)
		conversationAdded := t.newEvent("conversation.item.added")
		conversationAdded.Item = pending
		out.client(conversationAdded)

		for _, eventType := range []string{"response.function_call_arguments.delta", "response.function_call_arguments.done"} {
			event := t.newEvent(eventType)
			event.ResponseID, event.ItemID, event.OutputIndex, event.CallID = r.id, pending.ID, new(index), function.ID
			if strings.HasSuffix(eventType, ".delta") {
				event.Delta = &arguments
			} else {
				event.Arguments, event.Name = &arguments, function.Name
			}
			out.client(event)
		}

		item := *pending
		item.Status, item.Arguments = "completed", arguments
		r.items = append(r.items, item)
		done := t.newEvent("response.output_item.done")
		done.ResponseID, done.OutputIndex, done.Item = r.id, new(index), &item
		out.client(done)
		conversationDone := t.newEvent("conversation.item.done")
		conversationDone.Item = &item
		out.client(conversationDone)
	}
}

// voiceActivity reports the speech Live heard as the events a server-side detector would send.
func (t *liveTranslator) voiceActivity(out *liveOutput, activity *GeminiLiveVoiceActivity) {
	offset := liveOffsetMs(activity.AudioOffset)
	switch activity.Type {
	case "ACTIVITY_START":
		t.userItem = &liveUserItem{id: t.id("item_")}
		event := t.newEvent("input_audio_buffer.speech_started")
		event.ItemID, event.AudioStartMs = t.userItem.id, &offset
		out.client(event)
	case "ACTIVITY_END":
		if t.userItem == nil {
			t.userItem = &liveUserItem{id: t.id("item_")}
		}
		stopped := t.newEvent("input_audio_buffer.speech_stopped")
		stopped.ItemID, stopped.AudioEndMs = t.userItem.id, &offset
		out.client(stopped)
		t.commitUserItem(out)
	}
}

// commitUserItem announces the utterance as committed and as a conversation item, then finishes it if
// there is no transcript to wait for.
func (t *liveTranslator) commitUserItem(out *liveOutput) {
	u := t.userItem
	u.stopped = true
	committed := t.newEvent("input_audio_buffer.committed")
	committed.ItemID = u.id
	out.client(committed)
	added := t.newEvent("conversation.item.added")
	added.Item = &liveItem{ID: u.id, Object: "realtime.item", Type: "message", Status: "completed", Role: "user", Content: []livePart{{Type: "input_audio"}}}
	out.client(added)

	if u.transcript != "" || t.config.setup.InputAudioTranscription == nil {
		t.finishUserItem(out)
	}
}

func (t *liveTranslator) inputTranscript(out *liveOutput, text string) {
	if text == "" {
		return
	}
	if t.userItem == nil {
		t.userItem = &liveUserItem{id: t.id("item_")}
		t.commitUserItem(out)
	}
	t.userItem.transcript += text
	if t.userItem.stopped {
		t.finishUserItem(out)
	}
}

// finishUserItem completes the utterance's conversation item, with its transcript when there is one.
func (t *liveTranslator) finishUserItem(out *liveOutput) {
	u := t.userItem
	t.userItem = nil
	content := livePart{Type: "input_audio"}
	if u.transcript != "" {
		completed := t.newEvent("conversation.item.input_audio_transcription.completed")
		completed.ItemID, completed.ContentIndex, completed.Transcript = u.id, new(0), &u.transcript
		out.client(completed)
		content.Transcript = &u.transcript
	}
	done := t.newEvent("conversation.item.done")
	done.Item = &liveItem{ID: u.id, Object: "realtime.item", Type: "message", Status: "completed", Role: "user", Content: []livePart{content}}
	out.client(done)
}

// liveOffsetMs converts a protobuf duration such as "1.366125s" to milliseconds.
func liveOffsetMs(duration string) int {
	seconds, err := strconv.ParseFloat(strings.TrimSuffix(duration, "s"), 64)
	if err != nil {
		return 0
	}
	return int(seconds * 1000)
}

// liveCompactJSON returns function arguments as compact JSON, the way OpenAI sends them. Live
// pretty-prints them.
func liveCompactJSON(arguments json.RawMessage) string {
	if len(arguments) == 0 {
		return "{}"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, arguments); err != nil {
		return string(arguments)
	}
	return compact.String()
}
