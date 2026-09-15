package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Google documents that a non-function-call streaming turn may deliver its thought
// signature in a part whose text is present but empty, and tells stream parsers to
// look for signatures "even if the text field is empty"
// (https://ai.google.dev/gemini-api/docs/generate-content/gemini-3#thought-signatures,
// https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures).
//
// `text` sits inside Part's data union, so proto3 JSON prints it once it is set --
// Google's wire bytes are {"text":"","thoughtSignature":"..."}. Bifrost declares Text
// with `omitempty`, so re-marshalling that part on the native /genai egress drops the
// data field and hands the client a metadata-only object. Google's own clients tolerate
// it, but adapters that require a representable payload do not: pydantic-ai's Google
// adapter raises UnexpectedModelBehavior before the visible answer is delivered.
//
// The empty text belongs only to a part that has no other data field. A signature riding
// on a functionCall, toolCall, toolResponse, inlineData or fileData part is already
// representable, and adding text there would put two members of the data union on one
// part -- which the API rejects.
func TestSignatureOnlyPartKeepsEmptyText(t *testing.T) {
	sig := []byte("signature")
	const encoded = "c2lnbmF0dXJl"

	t.Run("standalone signature part keeps its empty text payload", func(t *testing.T) {
		got, err := json.Marshal(Part{ThoughtSignature: sig})
		require.NoError(t, err)

		assert.True(t, gjson.GetBytes(got, "text").Exists(),
			"the empty text data field must survive: %s", got)
		assert.Equal(t, "", gjson.GetBytes(got, "text").String())
		assert.Equal(t, encoded, gjson.GetBytes(got, "thoughtSignature").String())
	})

	t.Run("a thought-marked signature part is the same shape", func(t *testing.T) {
		got, err := json.Marshal(Part{Thought: true, ThoughtSignature: sig})
		require.NoError(t, err)

		assert.True(t, gjson.GetBytes(got, "text").Exists(),
			"thought is metadata, not a data field: %s", got)
		assert.True(t, gjson.GetBytes(got, "thought").Bool())
	})

	t.Run("parts that already carry a data field are untouched", func(t *testing.T) {
		cases := map[string]Part{
			"functionCall":     {ThoughtSignature: sig, FunctionCall: &FunctionCall{Name: "get_weather"}},
			"toolCall":         {ThoughtSignature: sig, ToolCall: &ToolCall{ToolType: "GOOGLE_SEARCH_WEB", ID: "abc"}},
			"toolResponse":     {ThoughtSignature: sig, ToolResponse: &ToolResponse{ToolType: "GOOGLE_SEARCH_WEB", ID: "abc"}},
			"functionResponse": {ThoughtSignature: sig, FunctionResponse: &FunctionResponse{Name: "get_weather"}},
			"inlineData":       {ThoughtSignature: sig, InlineData: &Blob{MIMEType: "image/png", Data: "AQ=="}},
			"fileData":         {ThoughtSignature: sig, FileData: &FileData{MIMEType: "image/png", FileURI: "gs://x"}},
			"executableCode":   {ThoughtSignature: sig, ExecutableCode: &ExecutableCode{Code: "print(1)"}},
		}
		for name, part := range cases {
			t.Run(name, func(t *testing.T) {
				got, err := json.Marshal(part)
				require.NoError(t, err)
				assert.False(t, gjson.GetBytes(got, "text").Exists(),
					"%s already carries a data field, empty text would make the part invalid: %s", name, got)
			})
		}
	})

	// skip_thought_signature_validator is a Bifrost-internal request-path value, and both
	// sites that set it (responses.go and utils.go) attach it to a part that already
	// carries a FunctionCall. It therefore never reaches the empty-text branch. Pinned
	// here because a sentinel part that gained an empty text would be an outbound request
	// shape Gemini has never been asked to accept.
	t.Run("the skip-validator sentinel rides on a function call and gains no text", func(t *testing.T) {
		got, err := json.Marshal(Part{
			ThoughtSignature: []byte(skipThoughtSignatureValidator),
			FunctionCall:     &FunctionCall{Name: "get_weather"},
		})
		require.NoError(t, err)
		assert.Equal(t, skipThoughtSignatureValidator, gjson.GetBytes(got, "thoughtSignature").String())
		assert.False(t, gjson.GetBytes(got, "text").Exists(), "sentinel part must stay as it is: %s", got)
	})

	t.Run("non-empty text is preserved verbatim", func(t *testing.T) {
		got, err := json.Marshal(Part{ThoughtSignature: sig, Text: "hello"})
		require.NoError(t, err)
		assert.Equal(t, "hello", gjson.GetBytes(got, "text").String())
	})

	// dropEmptyGeminiParts exists because a payload-free part reaching a client is
	// noise, and it recognises one by the `{}` it marshals to. Injecting text into a
	// part with no signature would defeat that filter, so the two invariants are
	// pinned together here.
	t.Run("payload-free parts still marshal to the empty object the filter looks for", func(t *testing.T) {
		zero, err := json.Marshal(Part{})
		require.NoError(t, err)
		assert.Equal(t, "{}", string(zero))

		emptySig, err := json.Marshal(Part{ThoughtSignature: []byte{}})
		require.NoError(t, err)
		assert.Equal(t, "{}", string(emptySig),
			"a zero-length signature is no signature, so it earns no text either")
	})
}

// The defect the issue reports is on the native /genai SSE surface, so the round trip is
// asserted end to end as well: Google's documented empty-text signature chunk must come
// back out of Bifrost in the shape it went in.
func TestSignatureOnlyPartSurvivesGenAIStreamRoundTrip(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"","thoughtSignature":"c2lnbmF0dXJl"}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Lando Norris won."}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash"}`,
	}

	state := acquireGeminiResponsesStreamState()
	defer releaseGeminiResponsesStreamState(state)
	outState := NewBifrostToGeminiStreamState()

	var emitted []string
	seq := 0
	for _, c := range chunks {
		var resp GenerateContentResponse
		require.NoError(t, json.Unmarshal([]byte(c), &resp))

		events, bifrostErr := resp.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr)

		for _, e := range events {
			out := ToGeminiResponsesStreamResponse(e, outState)
			if out == nil {
				continue
			}
			for _, cand := range out.Candidates {
				if cand.Content == nil {
					continue
				}
				for _, part := range cand.Content.Parts {
					encoded, err := json.Marshal(part)
					require.NoError(t, err)
					emitted = append(emitted, string(encoded))
				}
			}
		}
		seq += len(events)
	}

	var signatureParts []string
	for _, p := range emitted {
		if gjson.Get(p, "thoughtSignature").Exists() {
			signatureParts = append(signatureParts, p)
		}
	}

	require.NotEmpty(t, signatureParts,
		"the signature must reach the client at all; emitted parts: %v", emitted)
	for _, p := range signatureParts {
		assert.True(t, gjson.Get(p, "text").Exists(),
			"a relayed signature part must keep the empty text Google sent: %s (all parts: %v)", p, emitted)
	}
}
