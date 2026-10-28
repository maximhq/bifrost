package utils

import (
	"sync/atomic"
	"unicode/utf8"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// bytesPerTokenEstimate is the usual rule of thumb for BPE tokenizers on English
// text and code. It only fills in usage a provider never reported (see
// estimateUnreportedStreamUsage); it never replaces a reported count.
const bytesPerTokenEstimate = 4

// minInlineBinaryRun is the shortest run of base64-alphabet bytes treated as an
// inline binary payload (a data: URL image, input_audio, file_data) rather than
// prompt text. Text rarely has this many letters and digits in a row without a
// space or punctuation, and a payload's token cost depends on the model, not on
// its encoded size, so the prompt estimate leaves it out.
const minInlineBinaryRun = 64

// streamOutputTally counts the output one stream attempt received from its
// upstream. OpenAI-compatible upstreams report usage only in the trailing usage
// frame, and Anthropic reports a placeholder output count in message_start, so a
// stream cancelled or timed out mid-generation has no reported output to bill.
// The tally lets the cancel/timeout path bill it anyway (maximhq/bifrost#9219).
//
// ProcessAndSendResponse writes it and the cancel/timeout handlers read it, both
// on the provider's stream goroutine. The counters are atomic so a chunk sent
// from another goroutine on the same context cannot race.
type streamOutputTally struct {
	chunks        atomic.Int64 // every chunk the attempt sent; > 0 means the upstream answered
	deltas        atomic.Int64 // chunks that carried generated output
	asciiBytes    atomic.Int64 // generated output: ASCII bytes
	nonASCIIRunes atomic.Int64 // generated output: non-ASCII characters
}

// RegisterStreamUsageHandle registers a stream attempt's in-place usage handle
// (BifrostContextKeyStreamAccumulatedUsage) together with a fresh output tally.
// A mid-stream cancel or timeout then bills the usage the provider already
// reported plus an estimate of what it had not reported yet. Call it once per
// attempt, before the read loop.
func RegisterStreamUsageHandle(ctx *schemas.BifrostContext, usage *schemas.BifrostLLMUsage) {
	ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, usage)
	ctx.SetValue(schemas.BifrostContextKeyStreamOutputTally, &streamOutputTally{})
}

// observe counts the generated output one stream chunk carries: content,
// refusal, reasoning and tool-call deltas for chat, text for completions, and
// the delta of a Responses event.
func (t *streamOutputTally) observe(response *schemas.BifrostResponse) {
	if response == nil {
		return
	}
	t.chunks.Add(1)
	var text textCount
	switch {
	case response.ChatResponse != nil:
		for i := range response.ChatResponse.Choices {
			choice := &response.ChatResponse.Choices[i]
			if choice.ChatStreamResponseChoice == nil || choice.Delta == nil {
				continue
			}
			delta := choice.Delta
			text.addPtr(delta.Content)
			text.addPtr(delta.Refusal)
			text.addPtr(delta.Reasoning)
			for j := range delta.ToolCalls {
				text.addPtr(delta.ToolCalls[j].Function.Name)
				text.add(delta.ToolCalls[j].Function.Arguments)
			}
		}
	case response.TextCompletionResponse != nil:
		for i := range response.TextCompletionResponse.Choices {
			choice := &response.TextCompletionResponse.Choices[i]
			if choice.TextCompletionResponseChoice != nil {
				text.addPtr(choice.Text)
			}
		}
	case response.ResponsesStreamResponse != nil:
		text.addPtr(response.ResponsesStreamResponse.Delta)
	}
	if !text.seen {
		return
	}
	t.deltas.Add(1)
	t.asciiBytes.Add(text.ascii)
	t.nonASCIIRunes.Add(text.nonASCII)
}

// outputTokens estimates the output tokens the attempt received: at least one
// per output chunk (OpenAI, vLLM and SGLang stream about one token per chunk),
// or the text estimate when chunks carry several tokens each (Anthropic).
func (t *streamOutputTally) outputTokens() int {
	return max(int(t.deltas.Load()), estimateTextTokens(t.asciiBytes.Load(), t.nonASCIIRunes.Load()))
}

// textCount sizes the generated text in one chunk without allocating.
type textCount struct {
	ascii    int64
	nonASCII int64
	seen     bool
}

func (c *textCount) addPtr(s *string) {
	if s != nil {
		c.add(*s)
	}
}

func (c *textCount) add(s string) {
	if s == "" {
		return
	}
	c.seen = true
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < utf8.RuneSelf {
			c.ascii++
		} else if b&0xC0 != 0x80 { // first byte of a multi-byte character
			c.nonASCII++
		}
	}
}

// estimateTextTokens estimates the tokens in text of the given size:
// bytesPerTokenEstimate ASCII bytes per token, and one token per non-ASCII
// character (CJK, emoji and accented letters rarely merge with their neighbours).
func estimateTextTokens(asciiBytes, nonASCIIRunes int64) int {
	return int((asciiBytes+bytesPerTokenEstimate-1)/bytesPerTokenEstimate + nonASCIIRunes)
}

// estimatePromptTokens estimates the prompt tokens of a marshalled provider
// request body. It counts the whole body, since tool definitions and the JSON
// around messages are prompted too, and skips inline binary payloads (see
// minInlineBinaryRun). An empty body estimates to 0.
func estimatePromptTokens(body []byte) int {
	var ascii, nonASCII, run int64
	for _, b := range body {
		if isBase64Byte(b) {
			run++
		} else {
			if run >= minInlineBinaryRun {
				ascii -= run
			}
			run = 0
		}
		if b < utf8.RuneSelf {
			ascii++
		} else if b&0xC0 != 0x80 {
			nonASCII++
		}
	}
	if run >= minInlineBinaryRun {
		ascii -= run
	}
	return estimateTextTokens(ascii, nonASCII)
}

func isBase64Byte(b byte) bool {
	return 'A' <= b && b <= 'Z' || 'a' <= b && b <= 'z' || '0' <= b && b <= '9' || b == '+' || b == '/' || b == '='
}

// estimateUnreportedStreamUsage fills in the usage a provider had not reported
// when its stream was cancelled or timed out, on top of what
// attachBilledUsageFromContext copied from the usage handle. It only runs once
// the upstream has answered (a chunk was received): by then the prompt was
// processed and output generated, and the provider bills both.
//
//   - Prompt: estimated from the request body when the provider reported no
//     prompt usage at all. Anthropic's message_start and continuous-usage
//     upstreams report it, and those values are kept.
//   - Output: a provider count below the number of output chunks received is a
//     stale snapshot (nothing yet, or Anthropic's message_start placeholder) and
//     is raised to the estimate. A count that keeps pace with the stream
//     (vLLM's continuous_usage_stats) is kept.
//
// Reported values are never lowered, and anything filled in marks the usage
// Estimated.
func estimateUnreportedStreamUsage(ctx *schemas.BifrostContext, bifrostErr *schemas.BifrostError, jsonBody []byte) {
	if ctx == nil || bifrostErr == nil {
		return
	}
	tally, ok := ctx.Value(schemas.BifrostContextKeyStreamOutputTally).(*streamOutputTally)
	if !ok || tally == nil || tally.chunks.Load() == 0 {
		return
	}
	// attachBilledUsageFromContext stored a snapshot, so it is safe to fill in place.
	billed := bifrostErr.ExtraFields.BilledUsage
	if billed == nil {
		billed = &schemas.BifrostLLMUsage{}
	}
	estimated := false
	if billed.PromptTokens == 0 && billed.PromptTokensDetails == nil {
		if prompt := estimatePromptTokens(jsonBody); prompt > 0 {
			billed.PromptTokens = prompt
			billed.TotalTokens += prompt
			estimated = true
		}
	}
	if billed.CompletionTokens < int(tally.deltas.Load()) {
		output := tally.outputTokens()
		billed.TotalTokens += output - billed.CompletionTokens
		billed.CompletionTokens = output
		estimated = true
	}
	if !estimated {
		return
	}
	billed.TotalTokens = max(billed.TotalTokens, billed.PromptTokens+billed.CompletionTokens)
	billed.Estimated = true
	bifrostErr.ExtraFields.BilledUsage = billed
}
