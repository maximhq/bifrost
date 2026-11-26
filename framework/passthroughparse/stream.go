package passthroughparse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/streaming"
	"github.com/tidwall/gjson"
)

var (
	sseDoneMarker = []byte("[DONE]")
	sseDataField  = []byte("data:")
)

// ErrUpstreamStreamFailed reports a recorded stream that ended on an error event the upstream sent,
// such as Anthropic's overloaded_error, rather than on bytes that did not decode. The response
// Parse returns alongside it holds everything that streamed before the error.
var ErrUpstreamStreamFailed = errors.New("upstream ended the stream with an error")

// parseStreamResponse rebuilds the final response from a recorded stream body: the raw SSE text
// the passthrough accumulator concatenated, or a JSON array for Gemini without alt=sse. Each event
// is decoded the way the provider's own stream loop decodes it, then the deltas are assembled with
// the same builders the live streaming accumulator uses, so a parsed passthrough stream reads
// exactly like a typed one.
func parseStreamResponse(format wireFormat, requestType schemas.RequestType, body []byte) (*schemas.BifrostResponse, error) {
	switch format {
	case wireGeminiInteractions:
		return assembleInteractionsStream(body)
	case wireSageMaker:
		return assembleSageMakerStream(body)
	case wireOpenAI:
		switch requestType {
		case schemas.ChatCompletionRequest:
			return assembleOpenAIChatStream(body)
		case schemas.TextCompletionRequest:
			return assembleOpenAITextStream(body)
		case schemas.ResponsesRequest:
			return assembleResponsesStream(body, decodeOpenAIResponsesEvent)
		}
	case wireAnthropic:
		if requestType == schemas.ResponsesRequest {
			return assembleAnthropicStream(body)
		}
	case wireGemini:
		if requestType == schemas.ResponsesRequest {
			return assembleGeminiStream(body)
		}
	}
	return nil, nil
}

// forEachSSEEvent calls fn with every SSE event's data payload in order, skipping the [DONE]
// terminator. Comments and id/retry fields are dropped by the shared reader. A JSON-lines body (see
// isJSONLines) has no event names: each non-empty line is one payload.
func forEachSSEEvent(body []byte, fn func(eventType string, data []byte) error) error {
	if isJSONLines(body) {
		for _, line := range bytes.Split(body, []byte("\n")) {
			if line = bytes.TrimSpace(line); len(line) > 0 {
				if err := fn("", line); err != nil {
					return err
				}
			}
		}
		return nil
	}
	reader := providerUtils.GetSSEEventReader(nil, bytes.NewReader(body))
	for {
		eventType, data, err := reader.ReadEvent()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), sseDoneMarker) {
			continue
		}
		if err := fn(eventType, data); err != nil {
			return err
		}
	}
}

// choiceIndexes returns the choice indexes seen in a stream, in ascending order.
func choiceIndexes[T any](byIndex map[int]T) []int {
	indexes := make([]int, 0, len(byIndex))
	for index := range byIndex {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	return indexes
}

// assembleOpenAIChatStream rebuilds a chat completion stream. With n>1 OpenAI interleaves events for
// different choices, so deltas and finish reasons are accumulated per choice index, never by position
// in an event's choices array.
func assembleOpenAIChatStream(body []byte) (*schemas.BifrostResponse, error) {
	type choiceState struct {
		chunks       []*streaming.ChatStreamChunk
		finishReason *string
	}
	var (
		choices = map[int]*choiceState{}
		usage   *schemas.BifrostLLMUsage
		first   *schemas.BifrostChatResponse
	)
	err := forEachSSEEvent(body, func(_ string, data []byte) error {
		var event schemas.BifrostChatResponse
		if err := sonic.Unmarshal(data, &event); err != nil {
			return err
		}
		if first == nil {
			first = &event
		}
		if event.Usage != nil {
			usage = event.Usage
		}
		for _, choice := range event.Choices {
			state := choices[choice.Index]
			if state == nil {
				state = &choiceState{}
				choices[choice.Index] = state
			}
			if choice.FinishReason != nil {
				state.finishReason = choice.FinishReason
			}
			if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
				state.chunks = append(state.chunks, &streaming.ChatStreamChunk{ChunkIndex: len(state.chunks), Delta: choice.ChatStreamResponseChoice.Delta})
			}
		}
		return nil
	})
	if first == nil {
		return nil, errors.Join(errNoEvents, err)
	}
	if len(choices) == 0 {
		choices[0] = &choiceState{}
	}
	resp := &schemas.BifrostChatResponse{
		ID:      first.ID,
		Object:  "chat.completion",
		Created: first.Created,
		Model:   first.Model,
		Usage:   usage,
	}
	for _, index := range choiceIndexes(choices) {
		state := choices[index]
		resp.Choices = append(resp.Choices, schemas.BifrostResponseChoice{
			Index:                       index,
			FinishReason:                state.finishReason,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: streaming.BuildChatMessageFromChunks(state.chunks)},
		})
	}
	return &schemas.BifrostResponse{ChatResponse: resp}, err
}

// assembleOpenAITextStream rebuilds a legacy completion stream, accumulating text and finish reasons
// per choice index for the same reason as assembleOpenAIChatStream.
func assembleOpenAITextStream(body []byte) (*schemas.BifrostResponse, error) {
	type choiceState struct {
		text         strings.Builder
		finishReason *string
	}
	var (
		choices = map[int]*choiceState{}
		usage   *schemas.BifrostLLMUsage
		first   *schemas.BifrostTextCompletionResponse
	)
	err := forEachSSEEvent(body, func(_ string, data []byte) error {
		var event schemas.BifrostTextCompletionResponse
		if err := sonic.Unmarshal(data, &event); err != nil {
			return err
		}
		if first == nil {
			first = &event
		}
		if event.Usage != nil {
			usage = event.Usage
		}
		for _, choice := range event.Choices {
			state := choices[choice.Index]
			if state == nil {
				state = &choiceState{}
				choices[choice.Index] = state
			}
			if choice.FinishReason != nil {
				state.finishReason = choice.FinishReason
			}
			if choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
				state.text.WriteString(*choice.TextCompletionResponseChoice.Text)
			}
		}
		return nil
	})
	if first == nil {
		return nil, errors.Join(errNoEvents, err)
	}
	if len(choices) == 0 {
		choices[0] = &choiceState{}
	}
	resp := &schemas.BifrostTextCompletionResponse{
		ID:     first.ID,
		Object: "text_completion",
		Model:  first.Model,
		Usage:  usage,
	}
	for _, index := range choiceIndexes(choices) {
		state := choices[index]
		completion := state.text.String()
		resp.Choices = append(resp.Choices, schemas.BifrostResponseChoice{
			Index:                        index,
			FinishReason:                 state.finishReason,
			TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{Text: &completion},
		})
	}
	return &schemas.BifrostResponse{TextCompletionResponse: resp}, err
}

var errNoEvents = errors.New("stream body carried no decodable events")

// errInvalidEvent ends a stream read at an event that is not valid JSON, such as one a cut stream left unfinished; what
// was read before it is kept.
var errInvalidEvent = errors.New("stream event is not valid JSON")

// responsesEventDecoder turns one recorded event into zero or more Responses stream events.
type responsesEventDecoder func(eventType string, data []byte) ([]*schemas.BifrostResponsesStreamResponse, error)

func decodeOpenAIResponsesEvent(_ string, data []byte) ([]*schemas.BifrostResponsesStreamResponse, error) {
	var event schemas.BifrostResponsesStreamResponse
	if err := sonic.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	return []*schemas.BifrostResponsesStreamResponse{&event}, nil
}

// responsesAssembler collects Responses stream events into the chunks the accumulator's builder
// consumes, and keeps the terminal envelope (status, stop reason, usage) from the last event that
// carried one.
type responsesAssembler struct {
	chunks   []*streaming.ResponsesStreamChunk
	envelope *schemas.BifrostResponsesResponse
}

func (a *responsesAssembler) add(events []*schemas.BifrostResponsesStreamResponse) {
	for _, event := range events {
		if event == nil {
			continue
		}
		if event.Response != nil {
			a.envelope = event.Response
		}
		a.chunks = append(a.chunks, &streaming.ResponsesStreamChunk{ChunkIndex: len(a.chunks), StreamResponse: event})
	}
}

func (a *responsesAssembler) result() *schemas.BifrostResponse {
	if len(a.chunks) == 0 {
		return nil
	}
	output := streaming.BuildResponsesMessagesFromChunks(a.chunks)
	resp := &schemas.BifrostResponsesResponse{}
	if a.envelope != nil {
		envelope := *a.envelope
		resp = &envelope
		// A completed event that already carries the full output (OpenAI) is authoritative;
		// converters that emit a bare completed envelope get the assembled deltas.
		if len(resp.Output) > 0 {
			return &schemas.BifrostResponse{ResponsesResponse: resp}
		}
	}
	resp.Output = output
	return &schemas.BifrostResponse{ResponsesResponse: resp}
}

func assembleResponsesStream(body []byte, decode responsesEventDecoder) (*schemas.BifrostResponse, error) {
	var assembler responsesAssembler
	err := forEachSSEEvent(body, func(eventType string, data []byte) error {
		events, err := decode(eventType, data)
		if err != nil {
			return err
		}
		assembler.add(events)
		return nil
	})
	resp := assembler.result()
	if resp == nil {
		return nil, errors.Join(errNoEvents, err)
	}
	return resp, err
}

// assembleAnthropicStream rebuilds an Anthropic Messages stream as a Responses response, the way the
// provider's own stream loop converts its events. An error event ends it with ErrUpstreamStreamFailed.
func assembleAnthropicStream(body []byte) (*schemas.BifrostResponse, error) {
	ctx := newConverterContext()
	state := anthropic.AcquireAnthropicResponsesStreamState()
	defer anthropic.ReleaseAnthropicResponsesStreamState(state)
	sequence := 0
	return assembleResponsesStream(body, func(_ string, data []byte) ([]*schemas.BifrostResponsesStreamResponse, error) {
		var event anthropic.AnthropicStreamEvent
		if err := sonic.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		events, bifrostErr, _ := event.ToBifrostResponsesStream(ctx, sequence, state)
		if bifrostErr != nil {
			return events, fmt.Errorf("%w: %s", ErrUpstreamStreamFailed, bifrostErr.Error.Message)
		}
		sequence += len(events)
		return events, nil
	})
}

// assembleGeminiStream rebuilds a Gemini generateContent stream, SSE or a JSON array, as a Responses
// response, the way the provider's own stream loop converts its chunks.
func assembleGeminiStream(body []byte) (*schemas.BifrostResponse, error) {
	state := gemini.AcquireGeminiResponsesStreamState()
	defer gemini.ReleaseGeminiResponsesStreamState(state)
	sequence := 0
	convert := func(chunk *gemini.GenerateContentResponse) ([]*schemas.BifrostResponsesStreamResponse, error) {
		events, bifrostErr := chunk.ToBifrostResponsesStream(sequence, state)
		if bifrostErr != nil {
			return events, errors.New(bifrostErr.Error.Message)
		}
		sequence += len(events)
		return events, nil
	}

	// Without alt=sse, :streamGenerateContent returns one JSON array of response chunks.
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		return assembleGeminiJSONArray(trimmed, convert)
	}

	return assembleResponsesStream(body, func(_ string, data []byte) ([]*schemas.BifrostResponsesStreamResponse, error) {
		var chunk gemini.GenerateContentResponse
		if err := sonic.Unmarshal(data, &chunk); err != nil {
			return nil, err
		}
		return convert(&chunk)
	})
}

// assembleGeminiJSONArray rebuilds a Gemini alt=json stream: one JSON array of response chunks. The
// array is framed one element at a time, so a stream cut part-way (or followed by trailing data)
// keeps every chunk that arrived whole and reports the bad part, as the SSE path does.
func assembleGeminiJSONArray(body []byte, convert func(*gemini.GenerateContentResponse) ([]*schemas.BifrostResponsesStreamResponse, error)) (*schemas.BifrostResponse, error) {
	var assembler responsesAssembler
	finish := func(err error) (*schemas.BifrostResponse, error) {
		if resp := assembler.result(); resp != nil {
			return resp, err
		}
		return nil, errors.Join(errNoEvents, err)
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil { // the opening '['
		return finish(err)
	}
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return finish(err)
		}
		var chunk gemini.GenerateContentResponse
		if err := sonic.Unmarshal(element, &chunk); err != nil {
			return finish(err)
		}
		events, err := convert(&chunk)
		assembler.add(events)
		if err != nil {
			return finish(err)
		}
	}
	if _, err := dec.Token(); err != nil { // the closing ']'
		return finish(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return finish(fmt.Errorf("trailing data after the stream array"))
	}
	return finish(nil)
}

// RedactStreamBody applies literal replacements to the text of a recorded stream body written in
// format: an SSE body, or a Gemini JSON array of chunks. Values split across events are matched
// whole (see schemas.RedactRawResponseText). Only the rewritten JSON strings change; framing,
// events without text, and every other byte are kept. ok reports framing only: it is false, with
// body unchanged, when an event's data spans several data: lines, which leaves its payload without a
// single byte range. A payload that is not a valid JSON object ([DONE], a cut-off last event) is
// left as it is, so callers that must not release such text check the result themselves.
func RedactStreamBody(body []byte, format schemas.RawResponseFormat, replacements map[string]string) ([]byte, bool) {
	spans, ok := streamPayloadSpans(body)
	if !ok {
		return body, false
	}
	if len(spans) == 0 || len(replacements) == 0 {
		return body, true
	}
	var joined strings.Builder
	joined.Grow(len(body))
	for _, span := range spans {
		joined.Write(body[span.start:span.end])
		joined.WriteByte('\n')
	}
	payloads := joined.String()
	redacted := schemas.RedactRawResponseText(payloads, format, replacements)
	if redacted == payloads {
		return body, true
	}
	// Redaction rewrites string tokens only, so redacted holds the same payloads in the same order.
	var out bytes.Buffer
	out.Grow(len(body))
	cursor, next, offset := 0, 0, 0
	gjson.ForEachLine(redacted, func(payload gjson.Result) bool {
		span := spans[next]
		original := payloads[offset : offset+span.end-span.start]
		offset += span.end - span.start + 1
		next++
		if payload.Raw != original {
			out.Write(body[cursor:span.start])
			out.WriteString(payload.Raw)
			cursor = span.end
		}
		return next < len(spans)
	})
	if next != len(spans) {
		return body, false
	}
	out.Write(body[cursor:])
	return out.Bytes(), true
}

// payloadSpan is the byte range of one event's JSON payload in a recorded stream body.
type payloadSpan struct{ start, end int }

// streamPayloadSpans returns the range of every JSON object payload in a recorded stream body, in
// order: each element of a Gemini JSON array, or each SSE event's data. Other payloads ([DONE],
// non-JSON data) are skipped. It reports false for an event with several data: lines.
func streamPayloadSpans(body []byte) ([]payloadSpan, bool) {
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		return jsonArrayPayloadSpans(body), true
	}
	if isJSONLines(body) {
		return jsonLinesPayloadSpans(body), true
	}
	return ssePayloadSpans(body)
}

// isJSONLines reports whether a recorded stream is JSON lines, one object per line, as LMI containers stream by
// default, rather than SSE, whose lines start with a field name or a comment.
func isJSONLines(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// jsonLinesPayloadSpans frames a JSON-lines body line by line, the way forEachSSEEvent reads it.
func jsonLinesPayloadSpans(body []byte) []payloadSpan {
	var spans []payloadSpan
	for start := 0; start < len(body); {
		end := len(body)
		if newline := bytes.IndexByte(body[start:], '\n'); newline >= 0 {
			end = start + newline
		}
		spans = appendObjectSpan(spans, body, payloadSpan{start: start, end: end})
		start = end + 1
	}
	return spans
}

// jsonArrayPayloadSpans frames a JSON array the way assembleGeminiJSONArray does: element by element,
// so a stream cut part-way keeps every element that arrived whole.
func jsonArrayPayloadSpans(body []byte) []payloadSpan {
	var spans []payloadSpan
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil {
		return nil
	}
	for dec.More() {
		var element json.RawMessage
		if dec.Decode(&element) != nil {
			break
		}
		end := int(dec.InputOffset())
		spans = appendObjectSpan(spans, body, payloadSpan{start: end - len(element), end: end})
	}
	return spans
}

// ssePayloadSpans frames SSE the way forEachSSEEvent's reader does: lines end in LF with an optional
// CR before it, a blank line ends an event, an unterminated last event still counts, and comments
// and fields other than data: are skipped.
func ssePayloadSpans(body []byte) ([]payloadSpan, bool) {
	var spans []payloadSpan
	var data payloadSpan
	dataLines := 0
	endEvent := func() bool {
		if dataLines == 1 {
			spans = appendObjectSpan(spans, body, data)
		}
		ok := dataLines <= 1
		dataLines = 0
		return ok
	}
	for start := 0; start < len(body); {
		end, next := len(body), len(body)
		if newline := bytes.IndexByte(body[start:], '\n'); newline >= 0 {
			end, next = start+newline, start+newline+1
		}
		if end > start && body[end-1] == '\r' {
			end--
		}
		switch line := body[start:end]; {
		case len(line) == 0:
			if !endEvent() {
				return nil, false
			}
		case bytes.HasPrefix(line, sseDataField):
			value := start + len(sseDataField)
			if value < end && body[value] == ' ' {
				value++
			}
			data, dataLines = payloadSpan{start: value, end: end}, dataLines+1
		}
		start = next
	}
	if !endEvent() {
		return nil, false
	}
	return spans, true
}

// appendObjectSpan appends span, trimmed of surrounding whitespace, when it holds a valid JSON object.
func appendObjectSpan(spans []payloadSpan, body []byte, span payloadSpan) []payloadSpan {
	payload := body[span.start:span.end]
	trimmed := bytes.TrimLeft(payload, " \t\r\n")
	span.start += len(payload) - len(trimmed)
	trimmed = bytes.TrimRight(trimmed, " \t\r\n")
	span.end = span.start + len(trimmed)
	if len(trimmed) > 0 && trimmed[0] == '{' && gjson.ValidBytes(trimmed) {
		spans = append(spans, span)
	}
	return spans
}

// assembleInteractionsStream rebuilds an Interactions stream: each model output step's text, from the text its start
// event carries and its text deltas, and each function call step's argument deltas, or the arguments its start event
// carries when no delta follows, in step order. Thought summaries are not read. An error event or a failed status ends
// the stream as an upstream failure.
func assembleInteractionsStream(body []byte) (*schemas.BifrostResponse, error) {
	type stepState struct {
		kind, id, name string
		text, args     strings.Builder
		startArgs      string // a function call's arguments as its step.start carried them, used when no delta follows
	}
	steps := map[int]*stepState{}
	stepAt := func(index int) *stepState {
		if steps[index] == nil {
			steps[index] = &stepState{}
		}
		return steps[index]
	}
	seen := false
	err := forEachSSEEvent(body, func(eventType string, data []byte) error {
		if !gjson.ValidBytes(data) {
			return errInvalidEvent
		}
		seen = true
		event := gjson.ParseBytes(data)
		if kind := event.Get("event_type").String(); kind != "" {
			eventType = kind
		}
		switch eventType {
		case "step.start":
			step, state := event.Get("step"), stepAt(int(event.Get("index").Int()))
			state.kind, state.id, state.name = step.Get("type").String(), step.Get("id").String(), step.Get("name").String()
			switch state.kind {
			case "model_output":
				step.Get("content").ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() == "text" {
						state.text.WriteString(part.Get("text").String())
					}
					return true
				})
			case "function_call":
				if arguments := step.Get("arguments"); arguments.IsObject() {
					state.startArgs = strings.Clone(arguments.Raw)
				}
			}
		case "step.delta":
			delta, state := event.Get("delta"), stepAt(int(event.Get("index").Int()))
			switch delta.Get("type").String() {
			case "text":
				state.text.WriteString(delta.Get("text").String())
			case "arguments_delta":
				// The Gemini API sends the fragment as arguments, Vertex as partial_arguments.
				arguments := delta.Get("arguments")
				if !arguments.Exists() {
					arguments = delta.Get("partial_arguments")
				}
				state.args.WriteString(arguments.String())
			}
		case "error":
			return fmt.Errorf("%w: %s", ErrUpstreamStreamFailed, event.Get("error.message").String())
		case "interaction.status_update", "interaction.completed":
			// A status update carries the status at the top level; the completed event nests it in its interaction.
			status := event.Get("status")
			if !status.Exists() {
				status = event.Get("interaction.status")
			}
			if status.String() == "failed" {
				return fmt.Errorf("%w: the interaction failed", ErrUpstreamStreamFailed)
			}
		}
		return nil
	})
	if !seen {
		return nil, errors.Join(errNoEvents, err)
	}
	var output []schemas.ResponsesMessage
	indexes := make([]int, 0, len(steps))
	for index := range steps {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	for _, index := range indexes {
		state := steps[index]
		switch {
		// A stream resumed after a function call's step.start (last_event_id) carries its argument deltas without the
		// step's type; they are still a function call's arguments.
		case state.kind == "function_call" || state.kind == "" && state.args.Len() > 0:
			arguments := state.args.String()
			if arguments == "" {
				arguments = state.startArgs
			}
			output = append(output, responsesFunctionCall(state.id, state.name, arguments))
		case (state.kind == "model_output" || state.kind == "") && state.text.Len() > 0:
			output = append(output, responsesOutputMessage([]string{state.text.String()}))
		}
	}
	return &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Output: output}}, err
}

// assembleSageMakerStream rebuilds a SageMaker container stream, SSE or JSON lines: OpenAI chat or completion chunks
// with the OpenAI assemblers, TGI and LMI token events, whose token texts are joined, special tokens skipped, unless
// the closing event repeats the whole generated text, or LMI dynamic-batch {"outputs": [...]} events, whose pieces are
// joined per prompt. A whole OpenAI reply, which a container sends when the body did not ask it to stream, is read as
// one. A TGI error event or LMI's error finish ends it as an upstream failure, and a stream with no event of these
// kinds, such as a custom container's, is reported as unreadable.
func assembleSageMakerStream(body []byte) (*schemas.BifrostResponse, error) {
	var first gjson.Result
	probeErr := forEachSSEEvent(body, func(_ string, data []byte) error {
		if !gjson.ValidBytes(data) {
			return errInvalidEvent
		}
		first = gjson.ParseBytes(data)
		return errStopReading
	})
	switch {
	case probeErr != nil && !errors.Is(probeErr, errStopReading):
		return nil, probeErr
	case !first.Exists():
		return nil, errNoEvents
	case first.Get("choices.0.message").Exists():
		// A whole reply on the stream route: TGI and LMI stream only when the body asks for it.
		return parseSageMakerResponse([]byte(first.Raw))
	case first.Get("choices").Exists() && schemas.IsTextCompletionChoice(first, "delta"):
		return assembleOpenAITextStream(body)
	case first.Get("choices").Exists():
		return assembleOpenAIChatStream(body)
	}
	var text strings.Builder
	var outputs []*strings.Builder
	generated, hasGenerated, recognized := "", false, false
	err := forEachSSEEvent(body, func(_ string, data []byte) error {
		if !gjson.ValidBytes(data) {
			return errInvalidEvent
		}
		event := gjson.ParseBytes(data)
		// TGI and LMI keep the 200 they started with when generation fails: TGI sends an error event, and LMI ends the
		// stream on an error finish.
		// An error ends the stream but is not text of its own: a stream whose other events were none of the shapes below
		// is still unreadable.
		if failure := event.Get("error"); failure.Exists() && failure.Type != gjson.Null {
			return fmt.Errorf("%w: %s", ErrUpstreamStreamFailed, failure.String())
		}
		if event.Get("details.finish_reason").String() == "error" {
			return fmt.Errorf("%w: the model server reported a generation error", ErrUpstreamStreamFailed)
		}
		// Only string text is a token's or a closing event's text, as the raw redactor reads it; a null closing text,
		// which TGI sends with every token, is no text of its own.
		if token := event.Get("token"); token.Get("text").Type == gjson.String {
			recognized = true
			// TGI and LMI's TGI-compatible formatter mark a special token special; LMI's own formatter marks it special_token.
			if !token.Get("special").Bool() && !token.Get("special_token").Bool() {
				text.WriteString(token.Get("text").Str)
			}
		}
		if whole := event.Get("generated_text"); whole.Type == gjson.String {
			recognized, hasGenerated = true, true
			generated = whole.Str
		}
		if pieces := event.Get("outputs"); pieces.IsArray() {
			// LMI streams strings; anything else is not its text and is skipped, keeping each prompt's position, as
			// the raw redactor skips it too.
			prompt := 0
			pieces.ForEach(func(_, piece gjson.Result) bool {
				if piece.Type == gjson.String {
					recognized = true
					for len(outputs) <= prompt {
						outputs = append(outputs, &strings.Builder{})
					}
					outputs[prompt].WriteString(piece.Str)
				}
				prompt++
				return true
			})
		}
		return nil
	})
	if !recognized {
		return nil, errors.Join(errNoEvents, err)
	}
	if len(outputs) > 0 {
		texts := make([]string, len(outputs))
		for i, output := range outputs {
			texts[i] = output.String()
		}
		return textCompletionResponse(texts), err
	}
	// The closing text, when one came, is the answer even when it is empty; the tokens stand in only without one.
	if !hasGenerated {
		generated = text.String()
	}
	return textCompletionResponse([]string{generated}), err
}

// errStopReading ends a forEachSSEEvent walk after the event a caller needed.
var errStopReading = errors.New("stop reading")
