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
)

var sseDoneMarker = []byte("[DONE]")

// parseStreamResponse rebuilds the final response from a recorded stream body: the raw SSE text
// the passthrough accumulator concatenated, or a JSON array for Gemini without alt=sse. Each event
// is decoded the way the provider's own stream loop decodes it, then the deltas are assembled with
// the same builders the live streaming accumulator uses, so a parsed passthrough stream reads
// exactly like a typed one.
func parseStreamResponse(format wireFormat, requestType schemas.RequestType, body []byte) (*schemas.BifrostResponse, error) {
	switch format {
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
// terminator. Comments and id/retry fields are dropped by the shared reader.
func forEachSSEEvent(body []byte, fn func(eventType string, data []byte) error) error {
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
			return events, errors.New(bifrostErr.Error.Message)
		}
		sequence += len(events)
		return events, nil
	})
}

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
