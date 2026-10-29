package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RawResponseFormat names the provider-native wire shape held in a streamed chunk's RawResponse.
type RawResponseFormat string

const (
	// RawResponseFormatOpenAIChatCompletionChunk is one or more OpenAI chat.completion.chunk frames joined by blank lines.
	RawResponseFormatOpenAIChatCompletionChunk RawResponseFormat = "openai.chat.completion.chunk"
	// RawResponseFormatOpenAITextCompletionChunk is one OpenAI legacy completions stream frame.
	RawResponseFormatOpenAITextCompletionChunk RawResponseFormat = "openai.text_completion.chunk"
	// RawResponseFormatOpenAIResponsesStreamEvent is one OpenAI Responses API stream event.
	RawResponseFormatOpenAIResponsesStreamEvent RawResponseFormat = "openai.responses.stream_event"
	// RawResponseFormatAnthropicMessagesStreamEvent is one Anthropic Messages API stream event, backing a Chat or a
	// Responses chunk.
	RawResponseFormatAnthropicMessagesStreamEvent RawResponseFormat = "anthropic.messages.stream_event"
	// RawResponseFormatBedrockConverseStreamEvent is the JSON payload of one Bedrock ConverseStream event.
	RawResponseFormatBedrockConverseStreamEvent RawResponseFormat = "bedrock.converse.stream_event"
	// RawResponseFormatCohereChatStreamEvent is one Cohere v2 chat stream event.
	RawResponseFormatCohereChatStreamEvent RawResponseFormat = "cohere.chat.stream_event"
	// RawResponseFormatGeminiStreamChunk is one Gemini generateContent stream chunk.
	RawResponseFormatGeminiStreamChunk RawResponseFormat = "gemini.generate_content.chunk"
	// RawResponseFormatReplicateStreamEvent is one Replicate prediction stream event encoded as {"Data","Event","ID"}.
	RawResponseFormatReplicateStreamEvent RawResponseFormat = "replicate.stream_event"
)

// ErrRawResponseTextUnsupported reports a raw payload whose declared format, Go type, or field shape is
// not handled by SyncRawResponseText; callers keep their previous handling for that chunk.
var ErrRawResponseTextUnsupported = errors.New("raw response text synchronization is not supported for this payload")

// TextOnlyInDeltaEvents reports whether a format keeps streamed text only in its delta events. Its closing
// events (stop reason, usage) have no text field, so text released onto a closing chunk cannot be mirrored
// into that chunk's raw payload and has to be placed on an earlier text chunk instead.
func (format RawResponseFormat) TextOnlyInDeltaEvents() bool {
	return rawTextFormats[format].deltaOnly
}

// rawPayloadKind is a normalized stream chunk type a raw format can back.
type rawPayloadKind uint8

const (
	rawPayloadChat rawPayloadKind = iota + 1
	rawPayloadText
	rawPayloadResponses
)

// rawTextLayout describes where one declared format keeps the text of one normalized payload type. The zero value
// is a format whose frames use the normalized JSON paths themselves.
type rawTextLayout struct {
	// carries reports whether this format's events can hold the normalized field at path at all. Edits to
	// other fields, such as accumulated Responses text on a provider whose events only stream deltas, have no
	// raw counterpart and are left out. nil means every field.
	carries func(path []string) bool
	// slots returns, in text order, the raw paths in one frame that hold the normalized field at path; none
	// means the frame has no place for it, and such a field is never created. nil means the normalized path
	// itself, or the text blocks of a content array found there.
	slots func(frame []byte, path []string) []string
	// objects returns the raw paths in one frame that hold tool-call arguments as JSON objects rather than text;
	// an arguments edit replaces the object equal to its original value. nil means arguments are text.
	objects func(frame []byte) []string
}

// rawTextFormat describes one declared format: a layout for each normalized payload type its raw payload can back,
// and whether its closing events carry text (see TextOnlyInDeltaEvents).
type rawTextFormat struct {
	layouts   map[rawPayloadKind]rawTextLayout
	deltaOnly bool
}

// rawTextFormats lists every format SyncRawResponseText understands.
var rawTextFormats = map[RawResponseFormat]rawTextFormat{
	// Chat frames also back Text chunks (text served through chat) and Responses events (Responses served through
	// chat), whose streamed text sits in the frame's delta.
	RawResponseFormatOpenAIChatCompletionChunk: {layouts: map[rawPayloadKind]rawTextLayout{
		rawPayloadChat:      {},
		rawPayloadText:      {carries: isStreamedTextPath, slots: chatFrameTextSlots},
		rawPayloadResponses: {carries: isStreamedTextPath, slots: chatFrameTextSlots},
	}},
	RawResponseFormatOpenAITextCompletionChunk: {layouts: map[rawPayloadKind]rawTextLayout{rawPayloadText: {}}},
	// Responses events also back Chat chunks (chat served through Responses).
	RawResponseFormatOpenAIResponsesStreamEvent: {layouts: map[rawPayloadKind]rawTextLayout{
		rawPayloadResponses: {},
		rawPayloadChat:      {carries: isStreamedTextPath, slots: responsesEventTextSlots},
	}},
	RawResponseFormatAnthropicMessagesStreamEvent: eventFormat(rawTextLayout{carries: isStreamedTextPath, slots: anthropicTextSlots}, true),
	RawResponseFormatBedrockConverseStreamEvent:   eventFormat(rawTextLayout{carries: isStreamedTextPath, slots: bedrockTextSlots}, true),
	RawResponseFormatCohereChatStreamEvent:        eventFormat(rawTextLayout{carries: isStreamedTextPath, slots: cohereTextSlots}, true),
	RawResponseFormatGeminiStreamChunk:            eventFormat(rawTextLayout{carries: isStreamedTextPath, slots: geminiTextSlots, objects: geminiArgumentObjects}, false),
	RawResponseFormatReplicateStreamEvent:         eventFormat(rawTextLayout{carries: isStreamedTextPath, slots: replicateTextSlots}, true),
}

// eventFormat is a provider event format whose one layout serves every payload type, since the same provider events
// back Chat, Text, and Responses chunks.
func eventFormat(layout rawTextLayout, deltaOnly bool) rawTextFormat {
	return rawTextFormat{
		layouts:   map[rawPayloadKind]rawTextLayout{rawPayloadChat: layout, rawPayloadText: layout, rawPayloadResponses: layout},
		deltaOnly: deltaOnly,
	}
}

// RawTextEdit is one string field of a streamed chunk's normalized payload whose value a plugin changed from Before
// to After. Path holds the field's escaped GJSON/SJSON segments in the normalized payload's JSON: object keys are
// escaped and array positions are bare indexes, as in {"choices", "0", "delta", "content"}.
type RawTextEdit struct {
	Path   []string
	Before string
	After  string
}

// rawJSONFrames keeps the JSON values of a raw stream payload together with the exact bytes between them,
// so a rewrite changes only edited string fields and reassembles the payload byte-for-byte otherwise.
type rawJSONFrames struct {
	frames     [][]byte
	separators []string
	tail       string
}

// SyncRawResponseText applies the string changes a plugin made to a streamed chunk's normalized payload
// (before → after) to the chunk's provider-native raw payload, so both copies carry the same text.
//
// before and after must be a normalized type the format can back: *BifrostChatResponse,
// *BifrostTextCompletionResponse, or *BifrostResponsesStreamResponse. A payload of the type the format natively
// describes uses the same JSON field paths as the raw payload. Otherwise, and for provider event formats, streamed
// text (Chat content and tool-argument deltas, text-completion choices, Responses event deltas) maps to the field
// the raw event keeps it in, accumulated text the events never carry is left out, and tool arguments a format keeps
// as JSON objects replace the object equal to their original value. Each changed field is rewritten after verifying
// its original value in the raw payload. Text spread over several raw fields moves to the last of them; text added
// where the normalized payload had none goes into an existing empty field, or, for a natively described payload,
// into the last frame whose parent object exists. Every other raw byte is preserved.
//
// A payload whose normalized text did not change returns raw unchanged with no error, whatever its format. Otherwise
// it returns an error wrapping ErrRawResponseTextUnsupported when the format, payload type, or raw field shape is not
// handled, and a plain error when the raw text does not match the normalized original.
func SyncRawResponseText(raw any, format RawResponseFormat, before, after any) (any, error) {
	kind := rawPayloadKindOf(before)
	if kind == 0 || kind != rawPayloadKindOf(after) {
		return raw, fmt.Errorf("%w: before and after are not the same normalized payload type", ErrRawResponseTextUnsupported)
	}
	edits, err := normalizedStringEdits(before, after)
	if err != nil || len(edits) == 0 {
		return raw, err
	}
	return applyRawTextEdits(raw, format, kind, edits)
}

// SyncRawResponseTextEdits is SyncRawResponseText for a caller that already knows which normalized fields it
// changed: it applies edits to raw directly instead of diffing two copies of the payload. payload is the chunk's
// normalized payload (either copy) and only selects how the format lays out its text. Edits whose Before equals
// their After change nothing and are skipped, so raw is returned unchanged when no edit changes text. Field lookup,
// verification, and errors are those of SyncRawResponseText.
func SyncRawResponseTextEdits(raw any, format RawResponseFormat, payload any, edits []RawTextEdit) (any, error) {
	kind := rawPayloadKindOf(payload)
	if kind == 0 {
		return raw, fmt.Errorf("%w: payload is %T, not a normalized stream chunk", ErrRawResponseTextUnsupported, payload)
	}
	if !slices.ContainsFunc(edits, func(edit RawTextEdit) bool { return edit.Before != edit.After }) {
		return raw, nil
	}
	changed := edits
	if slices.ContainsFunc(edits, func(edit RawTextEdit) bool { return edit.Before == edit.After }) {
		changed = slices.DeleteFunc(slices.Clone(edits), func(edit RawTextEdit) bool { return edit.Before == edit.After })
	}
	return applyRawTextEdits(raw, format, kind, changed)
}

// applyRawTextEdits writes edits to one normalized payload kind into raw using the format's layout for that kind.
// raw is returned unchanged on any error.
func applyRawTextEdits(raw any, format RawResponseFormat, kind rawPayloadKind, edits []RawTextEdit) (any, error) {
	layout, ok := rawTextFormats[format].layouts[kind]
	if !ok {
		return raw, fmt.Errorf("%w: format %q does not describe this payload", ErrRawResponseTextUnsupported, format)
	}
	rawText, ok := raw.(string)
	if !ok {
		return raw, fmt.Errorf("%w: raw payload is %T, not a string", ErrRawResponseTextUnsupported, raw)
	}
	frames, err := splitRawJSONFrames(rawText)
	if err != nil {
		return raw, err
	}
	for _, edit := range edits {
		if err := frames.apply(edit, layout); err != nil {
			return raw, err
		}
	}
	return frames.String(), nil
}

// rawPayloadKindOf classifies a normalized stream chunk; nil pointers and other types are not payloads.
func rawPayloadKindOf(payload any) rawPayloadKind {
	switch typed := payload.(type) {
	case *BifrostChatResponse:
		if typed != nil {
			return rawPayloadChat
		}
	case *BifrostTextCompletionResponse:
		if typed != nil {
			return rawPayloadText
		}
	case *BifrostResponsesStreamResponse:
		if typed != nil {
			return rawPayloadResponses
		}
	}
	return 0
}

// normalizedStringEdits returns every string field whose value differs between the two normalized payloads,
// sorted by path. extra_fields objects are ignored because they never reach the provider-native payload.
func normalizedStringEdits(before, after any) ([]RawTextEdit, error) {
	beforeJSON, err := Marshal(withoutExtraFields(before))
	if err != nil {
		return nil, fmt.Errorf("marshal normalized payload before change: %w", err)
	}
	afterJSON, err := Marshal(withoutExtraFields(after))
	if err != nil {
		return nil, fmt.Errorf("marshal normalized payload after change: %w", err)
	}
	if string(beforeJSON) == string(afterJSON) {
		return nil, nil
	}
	var beforeValue, afterValue any
	if err := Unmarshal(beforeJSON, &beforeValue); err != nil {
		return nil, fmt.Errorf("decode normalized payload before change: %w", err)
	}
	if err := Unmarshal(afterJSON, &afterValue); err != nil {
		return nil, fmt.Errorf("decode normalized payload after change: %w", err)
	}
	var edits []RawTextEdit
	if err := collectStringEdits(nil, beforeValue, afterValue, &edits); err != nil {
		return nil, err
	}
	sort.Slice(edits, func(i, j int) bool {
		return strings.Join(edits[i].Path, "\x00") < strings.Join(edits[j].Path, "\x00")
	})
	return edits, nil
}

// withoutExtraFields returns a shallow copy of a normalized payload with its top-level extra fields cleared. The diff
// ignores extra_fields, and clearing them first keeps the raw payload they carry out of the marshal and decode.
func withoutExtraFields(payload any) any {
	switch typed := payload.(type) {
	case *BifrostChatResponse:
		clone := *typed
		clone.ExtraFields = BifrostResponseExtraFields{}
		return &clone
	case *BifrostTextCompletionResponse:
		clone := *typed
		clone.ExtraFields = BifrostResponseExtraFields{}
		return &clone
	case *BifrostResponsesStreamResponse:
		clone := *typed
		clone.ExtraFields = BifrostResponseExtraFields{}
		return &clone
	}
	return payload
}

// collectStringEdits walks after against before and records changed or added string leaves.
// A string that disappears or changes type is not a text edit the raw payload can mirror.
func collectStringEdits(path []string, before, after any, edits *[]RawTextEdit) error {
	switch afterValue := after.(type) {
	case string:
		beforeText, beforeIsString := before.(string)
		if beforeIsString && beforeText == afterValue {
			return nil
		}
		if before != nil && !beforeIsString {
			return fmt.Errorf("%w: normalized field %q changed type", ErrRawResponseTextUnsupported, strings.Join(path, "."))
		}
		*edits = append(*edits, RawTextEdit{Path: append([]string(nil), path...), Before: beforeText, After: afterValue})
		return nil
	case map[string]any:
		beforeMap, beforeIsMap := before.(map[string]any)
		if !beforeIsMap && containsNonEmptyString(before) {
			return fmt.Errorf("%w: normalized field %q changed type", ErrRawResponseTextUnsupported, strings.Join(path, "."))
		}
		for key, child := range afterValue {
			if key == "extra_fields" {
				continue
			}
			if err := collectStringEdits(append(path, rawJSONPathKey(key)), beforeMap[key], child, edits); err != nil {
				return err
			}
		}
		for key, child := range beforeMap {
			if _, kept := afterValue[key]; !kept && key != "extra_fields" && containsNonEmptyString(child) {
				return fmt.Errorf("%w: normalized field %q was removed", ErrRawResponseTextUnsupported, strings.Join(append(path, rawJSONPathKey(key)), "."))
			}
		}
		return nil
	case []any:
		beforeSlice, beforeIsSlice := before.([]any)
		if !beforeIsSlice && containsNonEmptyString(before) {
			return fmt.Errorf("%w: normalized field %q changed type", ErrRawResponseTextUnsupported, strings.Join(path, "."))
		}
		for index, child := range afterValue {
			var beforeChild any
			if index < len(beforeSlice) {
				beforeChild = beforeSlice[index]
			}
			if err := collectStringEdits(append(path, strconv.Itoa(index)), beforeChild, child, edits); err != nil {
				return err
			}
		}
		for index := len(afterValue); index < len(beforeSlice); index++ {
			if containsNonEmptyString(beforeSlice[index]) {
				return fmt.Errorf("%w: normalized array %q lost an element", ErrRawResponseTextUnsupported, strings.Join(path, "."))
			}
		}
		return nil
	default:
		if text, beforeIsString := before.(string); beforeIsString && text != "" {
			return fmt.Errorf("%w: normalized field %q changed type", ErrRawResponseTextUnsupported, strings.Join(path, "."))
		}
		return nil
	}
}

// containsNonEmptyString reports whether a decoded JSON value carries any non-empty string.
func containsNonEmptyString(value any) bool {
	switch typed := value.(type) {
	case string:
		return typed != ""
	case map[string]any:
		for _, child := range typed {
			if containsNonEmptyString(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsNonEmptyString(child) {
				return true
			}
		}
	}
	return false
}

// splitRawJSONFrames parses a raw payload made of one or more JSON objects separated only by whitespace. The frames
// share one copy of raw, each capped at its own end so a rewrite of one frame never spills into the next.
func splitRawJSONFrames(raw string) (*rawJSONFrames, error) {
	data := []byte(raw)
	result := &rawJSONFrames{}
	previousEnd := 0
	valid := true
	gjson.ForEachLine(raw, func(frame gjson.Result) bool {
		start, end := frame.Index, frame.Index+len(frame.Raw)
		valid = frame.IsObject() && start >= previousEnd && end <= len(raw) && raw[start:end] == frame.Raw &&
			strings.TrimSpace(raw[previousEnd:start]) == "" && gjson.Valid(frame.Raw)
		if valid {
			result.separators = append(result.separators, raw[previousEnd:start])
			result.frames = append(result.frames, data[start:end:end])
			previousEnd = end
		}
		return valid
	})
	if !valid || strings.TrimSpace(raw[previousEnd:]) != "" {
		return nil, fmt.Errorf("%w: raw payload is not a sequence of JSON objects", ErrRawResponseTextUnsupported)
	}
	if len(result.frames) == 0 {
		return nil, fmt.Errorf("%w: raw payload is empty", ErrRawResponseTextUnsupported)
	}
	result.tail = raw[previousEnd:]
	return result, nil
}

// apply rewrites one normalized string change into the frames after verifying the raw text it replaces.
func (f *rawJSONFrames) apply(edit RawTextEdit, layout rawTextLayout) error {
	if layout.objects != nil && isArgumentsPath(edit.Path) {
		return f.applyObjects(edit, layout.objects)
	}
	if layout.carries != nil && !layout.carries(edit.Path) {
		return nil
	}
	slots := layout.slots
	if slots == nil {
		slots = sameShapeTextSlots
	}
	path := rawJSONPath(edit.Path)
	type candidate struct {
		frame int
		path  string
		value string
	}
	var candidates []candidate
	var joined strings.Builder
	for index, frame := range f.frames {
		for _, slot := range slots(frame, edit.Path) {
			field := gjson.GetBytes(frame, slot)
			if !field.Exists() {
				continue
			}
			if field.Type != gjson.String {
				return fmt.Errorf("%w: raw field %q is not a string", ErrRawResponseTextUnsupported, slot)
			}
			candidates = append(candidates, candidate{frame: index, path: slot, value: field.String()})
			joined.WriteString(field.String())
		}
	}

	if edit.Before != "" {
		if len(candidates) == 0 {
			return fmt.Errorf("%w: raw payload has no field for %q", ErrRawResponseTextUnsupported, path)
		}
		if joined.String() != edit.Before {
			return fmt.Errorf("raw text for %q does not match the normalized text being replaced", path)
		}
		last := -1
		for index, item := range candidates {
			if item.value != "" {
				last = index
			}
		}
		for index, item := range candidates {
			if item.value == "" {
				continue
			}
			value := ""
			if index == last {
				value = edit.After
			}
			if err := f.set(item.frame, item.path, value); err != nil {
				return err
			}
		}
		return nil
	}

	if joined.Len() > 0 {
		return fmt.Errorf("raw text for %q carries text the normalized payload did not", path)
	}
	if edit.After == "" {
		return nil
	}
	if len(candidates) > 0 {
		last := candidates[len(candidates)-1]
		return f.set(last.frame, last.path, edit.After)
	}
	if layout.slots != nil {
		// Mapped formats only fill a field their event already has: adding one elsewhere (for example to a
		// closing event) would produce an event shape the provider never sends.
		return fmt.Errorf("%w: raw payload has no field that can carry %q", ErrRawResponseTextUnsupported, path)
	}
	parent := rawJSONPath(edit.Path[:len(edit.Path)-1])
	for index := len(f.frames) - 1; index >= 0; index-- {
		if parent == "" || gjson.GetBytes(f.frames[index], parent).IsObject() {
			if gjson.GetBytes(f.frames[index], path).Exists() {
				// The field is there but holds no text slot (a content array without text blocks).
				return fmt.Errorf("%w: raw field %q has no text to replace", ErrRawResponseTextUnsupported, path)
			}
			return f.set(index, path, edit.After)
		}
	}
	return fmt.Errorf("raw payload has no object that can carry field %q", path)
}

// applyObjects replaces the raw tool-argument object equal to an arguments edit's original value with the edited
// arguments. Arguments the frames do not hold are left out, like other text a provider event does not carry; a
// partial fragment cannot be matched against whole objects, so it is unsupported wherever objects exist.
func (f *rawJSONFrames) applyObjects(edit RawTextEdit, objects func(frame []byte) []string) error {
	path := rawJSONPath(edit.Path)
	if edit.Before == "" {
		if edit.After == "" {
			return nil
		}
		return fmt.Errorf("%w: raw arguments objects cannot take arguments added to %q", ErrRawResponseTextUnsupported, path)
	}
	var original any
	if err := json.Unmarshal([]byte(edit.Before), &original); err != nil {
		for _, frame := range f.frames {
			if len(objects(frame)) > 0 {
				return fmt.Errorf("%w: partial arguments for %q cannot be matched to raw arguments objects", ErrRawResponseTextUnsupported, path)
			}
		}
		return nil
	}
	if !json.Valid([]byte(edit.After)) {
		return fmt.Errorf("%w: edited arguments for %q are not a JSON value", ErrRawResponseTextUnsupported, path)
	}
	for index := range f.frames {
		for _, slot := range objects(f.frames[index]) {
			var value any
			if err := json.Unmarshal([]byte(gjson.GetBytes(f.frames[index], slot).Raw), &value); err != nil || !reflect.DeepEqual(value, original) {
				continue
			}
			updated, err := sjson.SetRawBytes(f.frames[index], slot, []byte(edit.After))
			if err != nil {
				return fmt.Errorf("rewrite raw arguments %q: %w", slot, err)
			}
			f.frames[index] = updated
		}
	}
	return nil
}

// isArgumentsPath reports whether a normalized path holds a tool call's arguments: a Chat tool-call delta, or a
// Responses function call's accumulated arguments (on its done event, its output item, or the completed response).
func isArgumentsPath(path []string) bool {
	return len(path) > 0 && path[len(path)-1] == "arguments"
}

// sameShapeTextSlots returns the normalized path itself, or the text blocks of a content array found there:
// some OpenAI-compatible providers stream content as typed blocks that the normalized payload joins into one
// string.
func sameShapeTextSlots(frame []byte, path []string) []string {
	joined := rawJSONPath(path)
	field := gjson.GetBytes(frame, joined)
	if !field.IsArray() {
		return []string{joined}
	}
	var slots []string
	index := 0
	field.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "text" {
			slots = append(slots, joined+"."+strconv.Itoa(index)+".text")
		}
		index++
		return true
	})
	return slots
}

// isStreamedTextPath reports whether a normalized path is per-chunk streamed text: a Chat content or tool-argument
// delta, a text-completion choice, or a Responses event delta.
func isStreamedTextPath(path []string) bool {
	switch len(path) {
	case 1:
		return path[0] == "delta"
	case 3:
		return path[0] == "choices" && path[2] == "text"
	case 4:
		return path[0] == "choices" && path[2] == "delta" && path[3] == "content"
	default:
		return isToolArgumentsPath(path)
	}
}

// isToolArgumentsPath reports whether a normalized path is a Chat tool-call arguments delta.
func isToolArgumentsPath(path []string) bool {
	return len(path) == 7 && path[0] == "choices" && path[2] == "delta" && path[3] == "tool_calls" &&
		path[5] == "function" && path[6] == "arguments"
}

// chatFrameTextSlots maps the streamed text of a Text chunk or a Responses event served through chat to the chat
// frames carrying it: the choice's delta content (or its text blocks), and for a Responses delta also the tool-call
// arguments, since an arguments delta comes from those.
func chatFrameTextSlots(frame []byte, path []string) []string {
	switch {
	case len(path) == 3 && path[0] == "choices" && path[2] == "text":
		return sameShapeTextSlots(frame, []string{"choices", path[1], "delta", "content"})
	case len(path) == 1 && path[0] == "delta":
		slots := sameShapeTextSlots(frame, []string{"choices", "0", "delta", "content"})
		index := 0
		gjson.GetBytes(frame, "choices.0.delta.tool_calls").ForEach(func(_, _ gjson.Result) bool {
			slots = append(slots, "choices.0.delta.tool_calls."+strconv.Itoa(index)+".function.arguments")
			index++
			return true
		})
		return slots
	}
	return nil
}

// responsesEventTextSlots maps a Chat chunk served through Responses to the Responses delta event backing it: answer
// content to an output_text.delta, tool-call arguments to a function_call_arguments.delta.
func responsesEventTextSlots(frame []byte, path []string) []string {
	switch eventType := gjson.GetBytes(frame, "type").String(); {
	case len(path) == 4 && path[3] == "content" && eventType == "response.output_text.delta",
		isToolArgumentsPath(path) && eventType == "response.function_call_arguments.delta":
		return []string{"delta"}
	}
	return nil
}

// anthropicTextSlots maps streamed text to an Anthropic content_block_delta: text_delta text, or input_json_delta
// partial JSON for tool arguments and tool-backed structured output. Thinking, signatures, and lifecycle events
// (message_start, content_block_stop, message_delta, ...) hold no mirrored text.
func anthropicTextSlots(frame []byte, path []string) []string {
	if gjson.GetBytes(frame, "type").String() != "content_block_delta" {
		return nil
	}
	switch gjson.GetBytes(frame, "delta.type").String() {
	case "text_delta":
		if !isToolArgumentsPath(path) {
			return []string{"delta.text"}
		}
	case "input_json_delta":
		return []string{"delta.partial_json"}
	}
	return nil
}

// bedrockTextSlots maps streamed text to a Bedrock contentBlockDelta payload: delta.text, or delta.toolUse.input for
// tool arguments and tool-backed structured output. Reasoning deltas and other events hold no mirrored text.
func bedrockTextSlots(frame []byte, path []string) []string {
	if !isToolArgumentsPath(path) && gjson.GetBytes(frame, "delta.text").Exists() {
		return []string{"delta.text"}
	}
	if gjson.GetBytes(frame, "delta.toolUse.input").Exists() {
		return []string{"delta.toolUse.input"}
	}
	return nil
}

// cohereTextSlots maps streamed text to a Cohere v2 chat event: content-delta text, tool-call arguments, or the tool
// plan a Responses stream shows as text. Thinking holds no mirrored text.
func cohereTextSlots(frame []byte, path []string) []string {
	if !isToolArgumentsPath(path) && gjson.GetBytes(frame, "delta.message.content.text").Exists() {
		return []string{"delta.message.content.text"}
	}
	if (isToolArgumentsPath(path) || len(path) == 1) && gjson.GetBytes(frame, "delta.message.tool_calls.function.arguments").Exists() {
		return []string{"delta.message.tool_calls.function.arguments"}
	}
	// A Responses stream shows the tool plan as answer text; Chat keeps it as reasoning.
	if len(path) == 1 && gjson.GetBytes(frame, "delta.message.tool_plan").Exists() {
		return []string{"delta.message.tool_plan"}
	}
	return nil
}

// geminiTextSlots maps streamed answer text to the non-thought text parts of a Gemini candidate, in order. A
// chunk with parts that are rendered into the normalized text (executable code, code results, function
// responses) cannot be mirrored, and function-call arguments are JSON objects rather than text.
func geminiTextSlots(frame []byte, path []string) []string {
	candidate := "0"
	switch {
	case len(path) == 4 && path[3] == "content", len(path) == 3 && path[2] == "text":
		candidate = path[1]
	case len(path) == 1:
	default:
		return nil
	}
	prefix := "candidates." + candidate + ".content.parts."
	var slots []string
	mirrorable := true
	index := 0
	gjson.GetBytes(frame, "candidates."+candidate+".content.parts").ForEach(func(_, part gjson.Result) bool {
		if part.Get("executableCode").Exists() || part.Get("codeExecutionResult").Exists() || part.Get("functionResponse").Exists() {
			mirrorable = false
			return false
		}
		if part.Get("text").Exists() && !part.Get("thought").Bool() {
			slots = append(slots, prefix+strconv.Itoa(index)+".text")
		}
		index++
		return true
	})
	if !mirrorable {
		return nil
	}
	return slots
}

// geminiArgumentObjects returns the functionCall args of every Gemini candidate part: Gemini keeps tool-call
// arguments as JSON objects, which the normalized payload carries as their JSON text.
func geminiArgumentObjects(frame []byte) []string {
	var slots []string
	candidateIndex := 0
	gjson.GetBytes(frame, "candidates").ForEach(func(_, candidate gjson.Result) bool {
		partIndex := 0
		candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionCall.args").Exists() {
				slots = append(slots, "candidates."+strconv.Itoa(candidateIndex)+".content.parts."+strconv.Itoa(partIndex)+".functionCall.args")
			}
			partIndex++
			return true
		})
		candidateIndex++
		return true
	})
	return slots
}

// replicateTextSlots maps streamed text to the Data of a Replicate "output" event.
func replicateTextSlots(frame []byte, path []string) []string {
	if isToolArgumentsPath(path) || gjson.GetBytes(frame, "Event").String() != "output" {
		return nil
	}
	return []string{"Data"}
}

// set writes one string value into a frame at a GJSON/SJSON path.
func (f *rawJSONFrames) set(frame int, path string, value string) error {
	updated, err := sjson.SetBytes(f.frames[frame], path, value)
	if err != nil {
		return fmt.Errorf("rewrite raw field %q: %w", path, err)
	}
	f.frames[frame] = updated
	return nil
}

// String reassembles the frames with their original separators.
func (f *rawJSONFrames) String() string {
	var out strings.Builder
	for index, frame := range f.frames {
		out.WriteString(f.separators[index])
		out.Write(frame)
	}
	out.WriteString(f.tail)
	return out.String()
}

// rawJSONPath joins escaped segments into one GJSON/SJSON path.
func rawJSONPath(segments []string) string {
	return strings.Join(segments, ".")
}

// rawJSONPathKey escapes one object key for a GJSON/SJSON path, matching the request-side raw rewriter.
func rawJSONPathKey(key string) string {
	return strings.ReplaceAll(gjson.Escape(key), ":", `\:`)
}
