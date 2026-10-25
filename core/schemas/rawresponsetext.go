package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

// ErrRawResponseTextUnsupported reports a raw payload whose declared format, Go type, or field shape is
// not handled by SyncRawResponseText; callers keep their previous handling for that chunk.
var ErrRawResponseTextUnsupported = errors.New("raw response text synchronization is not supported for this payload")

// rawTextEdit is one normalized string field whose value a plugin changed. path holds escaped
// GJSON/SJSON segments: object keys are escaped and array positions are bare indexes.
type rawTextEdit struct {
	path   []string
	before string
	after  string
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
// before and after must be the normalized type matching format: *BifrostChatResponse for the OpenAI chat
// format, *BifrostTextCompletionResponse for the OpenAI text format, *BifrostResponsesStreamResponse for the
// OpenAI Responses format. These formats use the same JSON field paths as the normalized payload, so each
// changed field is rewritten at the same path after verifying its original value in the raw payload. Text
// spread over several bundled frames moves to the last of them; text added where the normalized payload had
// none goes into the last frame whose parent object exists. Every other raw byte is preserved.
//
// It returns an error wrapping ErrRawResponseTextUnsupported when the format, payload type, or raw field
// shape is not handled, and a plain error when the raw text does not match the normalized original.
func SyncRawResponseText(raw any, format RawResponseFormat, before, after any) (any, error) {
	if !rawResponseFormatMatchesPayload(format, before, after) {
		return raw, fmt.Errorf("%w: format %q does not describe this payload", ErrRawResponseTextUnsupported, format)
	}
	rawText, ok := raw.(string)
	if !ok {
		return raw, fmt.Errorf("%w: raw payload is %T, not a string", ErrRawResponseTextUnsupported, raw)
	}
	edits, err := normalizedStringEdits(before, after)
	if err != nil || len(edits) == 0 {
		return raw, err
	}
	frames, err := splitRawJSONFrames(rawText)
	if err != nil {
		return raw, err
	}
	for _, edit := range edits {
		if err := frames.apply(edit); err != nil {
			return raw, err
		}
	}
	return frames.String(), nil
}

// rawResponseFormatMatchesPayload reports whether before/after are the non-nil normalized type a format describes.
func rawResponseFormatMatchesPayload(format RawResponseFormat, before, after any) bool {
	switch format {
	case RawResponseFormatOpenAIChatCompletionChunk:
		b, beforeOK := before.(*BifrostChatResponse)
		a, afterOK := after.(*BifrostChatResponse)
		return beforeOK && afterOK && b != nil && a != nil
	case RawResponseFormatOpenAITextCompletionChunk:
		b, beforeOK := before.(*BifrostTextCompletionResponse)
		a, afterOK := after.(*BifrostTextCompletionResponse)
		return beforeOK && afterOK && b != nil && a != nil
	case RawResponseFormatOpenAIResponsesStreamEvent:
		b, beforeOK := before.(*BifrostResponsesStreamResponse)
		a, afterOK := after.(*BifrostResponsesStreamResponse)
		return beforeOK && afterOK && b != nil && a != nil
	default:
		return false
	}
}

// normalizedStringEdits returns every string field whose value differs between the two normalized payloads,
// sorted by path. extra_fields objects are ignored because they never reach the provider-native payload.
func normalizedStringEdits(before, after any) ([]rawTextEdit, error) {
	beforeJSON, err := Marshal(before)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized payload before change: %w", err)
	}
	afterJSON, err := Marshal(after)
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
	var edits []rawTextEdit
	if err := collectStringEdits(nil, beforeValue, afterValue, &edits); err != nil {
		return nil, err
	}
	sort.Slice(edits, func(i, j int) bool {
		return strings.Join(edits[i].path, "\x00") < strings.Join(edits[j].path, "\x00")
	})
	return edits, nil
}

// collectStringEdits walks after against before and records changed or added string leaves.
// A string that disappears or changes type is not a text edit the raw payload can mirror.
func collectStringEdits(path []string, before, after any, edits *[]rawTextEdit) error {
	switch afterValue := after.(type) {
	case string:
		beforeText, beforeIsString := before.(string)
		if beforeIsString && beforeText == afterValue {
			return nil
		}
		if before != nil && !beforeIsString {
			return fmt.Errorf("%w: normalized field %q changed type", ErrRawResponseTextUnsupported, strings.Join(path, "."))
		}
		*edits = append(*edits, rawTextEdit{path: append([]string(nil), path...), before: beforeText, after: afterValue})
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

// splitRawJSONFrames parses a raw payload made of one or more JSON objects separated only by whitespace.
func splitRawJSONFrames(raw string) (*rawJSONFrames, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	result := &rawJSONFrames{}
	previousEnd := 0
	for {
		var frame json.RawMessage
		if err := decoder.Decode(&frame); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("%w: raw payload is not a sequence of JSON values: %v", ErrRawResponseTextUnsupported, err)
		}
		end := int(decoder.InputOffset())
		start := end - len(frame)
		if start < previousEnd || strings.TrimSpace(raw[previousEnd:start]) != "" || len(frame) == 0 || frame[0] != '{' {
			return nil, fmt.Errorf("%w: raw payload frame is not a JSON object", ErrRawResponseTextUnsupported)
		}
		result.separators = append(result.separators, raw[previousEnd:start])
		result.frames = append(result.frames, []byte(raw[start:end]))
		previousEnd = end
	}
	if len(result.frames) == 0 {
		return nil, fmt.Errorf("%w: raw payload is empty", ErrRawResponseTextUnsupported)
	}
	result.tail = raw[previousEnd:]
	return result, nil
}

// apply rewrites one normalized string change into the frames after verifying the raw text it replaces.
func (f *rawJSONFrames) apply(edit rawTextEdit) error {
	path := rawJSONPath(edit.path)
	type candidate struct {
		frame int
		value string
	}
	var candidates []candidate
	var joined strings.Builder
	for index, frame := range f.frames {
		field := gjson.GetBytes(frame, path)
		if !field.Exists() {
			continue
		}
		if field.Type != gjson.String {
			return fmt.Errorf("%w: raw field %q is not a string", ErrRawResponseTextUnsupported, path)
		}
		candidates = append(candidates, candidate{frame: index, value: field.String()})
		joined.WriteString(field.String())
	}

	if edit.before != "" {
		if len(candidates) == 0 {
			return fmt.Errorf("%w: raw payload has no field %q", ErrRawResponseTextUnsupported, path)
		}
		if joined.String() != edit.before {
			return fmt.Errorf("raw field %q does not match the normalized text being replaced", path)
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
				value = edit.after
			}
			if err := f.set(item.frame, path, value); err != nil {
				return err
			}
		}
		return nil
	}

	if joined.Len() > 0 {
		return fmt.Errorf("raw field %q carries text the normalized payload did not", path)
	}
	if edit.after == "" {
		return nil
	}
	target := -1
	if len(candidates) > 0 {
		target = candidates[len(candidates)-1].frame
	} else {
		parent := rawJSONPath(edit.path[:len(edit.path)-1])
		for index := len(f.frames) - 1; index >= 0; index-- {
			if parent == "" || gjson.GetBytes(f.frames[index], parent).IsObject() {
				target = index
				break
			}
		}
	}
	if target < 0 {
		return fmt.Errorf("raw payload has no object that can carry field %q", path)
	}
	return f.set(target, path, edit.after)
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
