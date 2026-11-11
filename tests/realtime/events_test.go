package realtime

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Realtime API events. Client events have one builder per event type, named after it; provider
// events are read with the helpers at the bottom. Shapes follow OpenAI's GA Realtime API.

type event map[string]any

// tokens is a response's usage as the provider reported it in response.done.
type tokens struct{ input, output int }

// Client events.

func sessionUpdate(session map[string]any) event {
	return event{"type": "session.update", "session": session}
}

func userMessage(text string) event {
	return event{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}}
}

func functionCallOutput(callID, output string) event {
	return event{"type": "conversation.item.create", "item": map[string]any{
		"type": "function_call_output", "call_id": callID, "output": output,
	}}
}

func responseCreate() event { return event{"type": "response.create"} }

// responseCreateWith asks for a response with per-response overrides, such as tool_choice.
func responseCreateWith(response map[string]any) event {
	return event{"type": "response.create", "response": response}
}

// inputAudioClear is harmless at any point, and the provider answers it with
// input_audio_buffer.cleared, which makes it a probe that the session is still alive.
func inputAudioClear() event { return event{"type": "input_audio_buffer.clear"} }

func inputAudioAppend(base64PCM string) event {
	return event{"type": "input_audio_buffer.append", "audio": base64PCM}
}

// Session configurations.

const instructions = "You are a terse assistant. Answer in at most five words."

// textSession keeps OpenAI responses to text, which is cheaper and faster than audio.
func textSession() map[string]any {
	return map[string]any{"type": "realtime", "output_modalities": []string{"text"}, "instructions": instructions}
}

// weatherTool is a function the model is told to call for weather questions.
var weatherTool = map[string]any{
	"type": "function", "name": "get_weather", "description": "Get the current weather for a city.",
	"parameters": map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
		"required":   []string{"city"},
	},
}

// Provider events.

// usageOf reads the usage a response.done reported.
func usageOf(done gjson.Result) tokens {
	return tokens{
		input:  int(done.Get("response.usage.input_tokens").Int()),
		output: int(done.Get("response.usage.output_tokens").Int()),
	}
}

// replyText is what a response.done says: its text, or the transcript of its audio.
func replyText(done gjson.Result) string {
	var texts []string
	for _, item := range done.Get("response.output").Array() {
		for _, part := range item.Get("content").Array() {
			if text := part.Get("text").Str; text != "" {
				texts = append(texts, text)
			} else if transcript := part.Get("transcript").Str; transcript != "" {
				texts = append(texts, transcript)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// functionCall is the first function call a response.done carries.
func functionCall(done gjson.Result) gjson.Result {
	return done.Get(`response.output.#(type=="function_call")`)
}
