package gemini

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

// TestCodeExecutionRoundTrip tests that code execution with thought signatures
// is correctly converted from Gemini -> Bifrost -> Gemini
func TestCodeExecutionRoundTrip(t *testing.T) {
	// Create a raw Gemini response with code execution
	thoughtSig1 := []byte("test-thought-signature-1")
	thoughtSig2 := []byte("test-thought-signature-2")

	originalResponse := &GenerateContentResponse{
		ModelVersion: "gemini-3-flash-preview",
		ResponseID:   "test-response-id",
		Candidates: []*Candidate{
			{
				Index: 0,
				Content: &Content{
					Parts: []*Part{
						{
							ExecutableCode: &ExecutableCode{
								Language: "PYTHON",
								Code:     "def is_prime(n):\n    if n < 2:\n        return False\n    return True\n",
							},
							ThoughtSignature: thoughtSig1,
						},
						{
							CodeExecutionResult: &CodeExecutionResult{
								Outcome: OutcomeOK,
								Output:  "Primes: [2, 3, 5, 7]\nSum: 17\n",
							},
						},
						{
							Text:             "The sum of the first 4 primes is 17.",
							ThoughtSignature: thoughtSig2,
						},
					},
					Role: "model",
				},
				FinishReason: FinishReasonStop,
			},
		},
		UsageMetadata: &GenerateContentResponseUsageMetadata{
			ThoughtsTokenCount:   100,
			PromptTokenCount:     50,
			CandidatesTokenCount: 150,
			TotalTokenCount:      300,
		},
	}

	// Step 1: Convert Gemini -> Bifrost
	bifrostResp := originalResponse.ToResponsesBifrostResponsesResponse()
	assert.NotNil(t, bifrostResp)
	assert.NotNil(t, bifrostResp.Output)

	// Verify we have the expected messages:
	// - A reasoning message with encrypted content (thought signature for code)
	// - A code_interpreter_call message
	// - A message with text content (with thought signature in the content block)
	foundReasoning := false
	foundCodeInterpreter := false
	foundText := false

	for _, msg := range bifrostResp.Output {
		if msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeReasoning:
				if msg.ResponsesReasoning != nil && msg.ResponsesReasoning.EncryptedContent != nil {
					foundReasoning = true
					// Verify the thought signature matches
					decoded, err := base64.StdEncoding.DecodeString(*msg.ResponsesReasoning.EncryptedContent)
					assert.NoError(t, err)
					assert.Equal(t, thoughtSig1, decoded)
				}
			case schemas.ResponsesMessageTypeCodeInterpreterCall:
				foundCodeInterpreter = true
				assert.NotNil(t, msg.ResponsesToolMessage)
				assert.NotNil(t, msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall)
				assert.NotNil(t, msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Code)
				assert.Contains(t, *msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Code, "is_prime")
				// Verify outputs
				assert.Len(t, msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Outputs, 1)
			case schemas.ResponsesMessageTypeMessage:
				if msg.Content != nil && len(msg.Content.ContentBlocks) > 0 {
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							foundText = true
							// Verify the thought signature is in the content block
							assert.NotNil(t, block.Signature)
							decoded, err := base64.StdEncoding.DecodeString(*block.Signature)
							assert.NoError(t, err)
							assert.Equal(t, thoughtSig2, decoded)
						}
					}
				}
			}
		}
	}

	assert.True(t, foundReasoning, "Should have a reasoning message with thought signature")
	assert.True(t, foundCodeInterpreter, "Should have a code_interpreter_call message")
	assert.True(t, foundText, "Should have a text message with thought signature")

	// Step 2: Convert Bifrost -> Gemini
	reconstructedResponse := ToGeminiResponsesResponse(bifrostResp)
	assert.NotNil(t, reconstructedResponse)
	assert.Len(t, reconstructedResponse.Candidates, 1)

	candidate := reconstructedResponse.Candidates[0]
	assert.NotNil(t, candidate.Content)
	assert.Len(t, candidate.Content.Parts, 3, "Should have exactly 3 parts: executableCode, codeExecutionResult, text")

	// Verify Part 0: ExecutableCode with thought signature
	part0 := candidate.Content.Parts[0]
	assert.NotNil(t, part0.ExecutableCode, "Part 0 should have ExecutableCode")
	assert.Equal(t, "PYTHON", part0.ExecutableCode.Language)
	assert.Contains(t, part0.ExecutableCode.Code, "is_prime")
	assert.NotNil(t, part0.ThoughtSignature, "Part 0 should have ThoughtSignature attached to ExecutableCode")
	assert.Equal(t, thoughtSig1, part0.ThoughtSignature)
	// Verify it's NOT a standalone part with only thoughtSignature
	assert.NotNil(t, part0.ExecutableCode, "Part 0 should have ExecutableCode, not be a standalone thoughtSignature")

	// Verify Part 1: CodeExecutionResult (no thought signature)
	part1 := candidate.Content.Parts[1]
	assert.NotNil(t, part1.CodeExecutionResult, "Part 1 should have CodeExecutionResult")
	assert.Equal(t, OutcomeOK, part1.CodeExecutionResult.Outcome)
	assert.Contains(t, part1.CodeExecutionResult.Output, "Primes:")
	assert.Nil(t, part1.ThoughtSignature, "Part 1 should NOT have ThoughtSignature")

	// Verify Part 2: Text with thought signature
	part2 := candidate.Content.Parts[2]
	assert.NotEmpty(t, part2.Text, "Part 2 should have text")
	assert.Contains(t, part2.Text, "The sum")
	assert.NotNil(t, part2.ThoughtSignature, "Part 2 should have ThoughtSignature attached to text")
	assert.Equal(t, thoughtSig2, part2.ThoughtSignature)

	// Verify usage metadata is preserved
	assert.NotNil(t, reconstructedResponse.UsageMetadata)
	assert.Equal(t, int32(100), reconstructedResponse.UsageMetadata.ThoughtsTokenCount)
}

// TestRealGeminiCodeExecutionResponse tests with a real Gemini API response format
// This mimics the exact scenario from the user's bug report
func TestRealGeminiCodeExecutionResponse(t *testing.T) {
	// This is based on the actual Gemini API response format from the user's example
	responseJSON := `{
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "executableCode": {
              "language": "PYTHON",
              "code": "def is_prime(n):\n    if n < 2:\n        return False\n    for i in range(2, int(n**0.5) + 1):\n        if n % i == 0:\n            return False\n    return True\n\nprimes = []\nnum = 2\nwhile len(primes) < 50:\n    if is_prime(num):\n        primes.append(num)\n    num += 1\n\nsum_primes = sum(primes)\nprint(f\"Primes: {primes}\")\nprint(f\"Sum: {sum_primes}\")\n"
            },
            "thoughtSignature": "EuwECukEAXLI2nwr9f360fnlN/uEDL6wJ7+EwKhtt18hOp/oZCpTTasGUoESz9+xnYSaixBB2LB/EKwsSUctFq1IvE9uDimfhaDIGuwgCPMeNQq1lXOGbIygrQuJPCQsQZTk5WlK0FT1c3ZFlDC0uJwCSgDUGdfzD+wcJJ33hR0i6nt6XTQutza0CgmySerlgFUagXiVbP/9iTLVQnSmdT/VLtFIs0Ekf0StxfdV8jqank5MESI1qNR+YoMF+04IgkvTMvY8jDNgvmwBoslDbqtnDN0411bpWq5SVTQ+yv4m9RtVjUn8cz3kerdoUjSIf7d26XtXOuCDTu0HXkna7RX9Bovlwc1YM1hzmZ8sPqAj/Da7QfABH02//be/UUYGYXA10raamU1mkYeYK0hXpA/JG90Vpjbp9BIwLptnDGwZShwVg8m9zb5xmZZFC19XkzhH9hfcUlKos1TosCTWCfwEwkhs/8AuEhyZ/0/MQEvy4iZ7D3FmWXSTqM/8sjyKTPLn8WIJlFDnbQ4sUSPqd3qirQhTjgaeMWXHKnAFdWdukUYM7OIuQy913hhpLc0mI4mJc7wlTdMz+K9yx85uRquLTVci8V4+S0/iOxXxAxaGk1aklS99S5G4wj7MBT/hUNhPmxN7qvV2ujmmVJPi6cDKg4oonYhXwPWFjlvEPQedRMQAZD6edtl3Xm0N97YtsuHjny7TFQkL1Q2DgHF9LVFnyE7uTuGK/5y2HXeB40AAi5dLWyb+gGFu8mwXqxReVHZL27S0Q2qcmgBzlzhM4aMTodJWJ/jUIjQnWNLz2EJ8pkEb31lqwMONVTFJ8m4="
          },
          {
            "codeExecutionResult": {
              "outcome": "OUTCOME_OK",
              "output": "Primes: [2, 3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43, 47, 53, 59, 61, 67, 71, 73, 79, 83, 89, 97, 101, 103, 107, 109, 113, 127, 131, 137, 139, 149, 151, 157, 163, 167, 173, 179, 181, 191, 193, 197, 199, 211, 223, 227, 229]\nSum: 5117\n"
            }
          },
          {
            "text": "The sum of the first 50 prime numbers is **5,117**.",
            "thoughtSignature": "Ev4CCvsCAXLI2nwD2FnAquYRanHjC6DTz+yxDNQ/SUTjvh+nD4HpPHMUcma/tTTHc9LQWVwjfPhUvZc44NzRh0sIq8m2Mdfpl+KyB/cQHl0gbPoqOnVk8Yh6R9EIF8EIx4J6OSAnU5lUq8l/6blwST7LzB3OeCgL4saVBs0dGIvv24LveNdWC6PbjUdSExAeyx+i7+JqLjzkcsV9m9S0hDZXImKpCTFydisY+/pFXM5R362KPGT1bCT2Jvs/i+SEkuMuPcXHabADPpCtnv7BOX+5RnD7A46n93peL2AlJnxkK1wdRuOEObABy/wGPbLqwRszUt7z0yzZS5igkH2KndcfuWzeo45OsMTMy+J461cLfGcJwBjVnmdgDNGWQROW4mTb6LIz1QeL0D/WUEHldWwbYgmGYf9RBzCeMGTBPPkkA3oAiYIgtsXmHuCAvNM2zkJUi9XZEQfRxBnyZNLsLIWZ/yY3FArZzDdHglHuYZX1FVi/1OEKlSTgPJPdAr5xxw=="
          }
        ],
        "role": "model"
      },
      "finishReason": "STOP",
      "index": 0
    }
  ],
  "usageMetadata": {
    "thoughtsTokenCount": 224,
    "promptTokenCount": 194,
    "candidatesTokenCount": 419,
    "totalTokenCount": 1557
  },
  "modelVersion": "gemini-3-flash-preview",
  "responseId": "jaVnaYS1MZGjqfkPpM2m-AQ"
}`

	// Parse the JSON into a Gemini response
	var geminiResp GenerateContentResponse
	err := json.Unmarshal([]byte(responseJSON), &geminiResp)
	assert.NoError(t, err)

	// Verify the original response has the expected structure
	assert.Len(t, geminiResp.Candidates, 1)
	assert.Len(t, geminiResp.Candidates[0].Content.Parts, 3)

	// Part 0: executableCode with thoughtSignature
	assert.NotNil(t, geminiResp.Candidates[0].Content.Parts[0].ExecutableCode)
	assert.NotNil(t, geminiResp.Candidates[0].Content.Parts[0].ThoughtSignature)

	// Part 1: codeExecutionResult (no thoughtSignature)
	assert.NotNil(t, geminiResp.Candidates[0].Content.Parts[1].CodeExecutionResult)
	assert.Nil(t, geminiResp.Candidates[0].Content.Parts[1].ThoughtSignature)

	// Part 2: text with thoughtSignature
	assert.NotEmpty(t, geminiResp.Candidates[0].Content.Parts[2].Text)
	assert.NotNil(t, geminiResp.Candidates[0].Content.Parts[2].ThoughtSignature)

	// Convert to Bifrost format
	bifrostResp := geminiResp.ToResponsesBifrostResponsesResponse()
	assert.NotNil(t, bifrostResp)

	// Convert back to Gemini format
	reconstructed := ToGeminiResponsesResponse(bifrostResp)
	assert.NotNil(t, reconstructed)
	assert.Len(t, reconstructed.Candidates, 1)
	assert.Len(t, reconstructed.Candidates[0].Content.Parts, 3, "Should have exactly 3 parts")

	// Verify the reconstructed response matches the original structure
	parts := reconstructed.Candidates[0].Content.Parts

	// Part 0: executableCode with thoughtSignature (NOT a standalone thoughtSignature part!)
	assert.NotNil(t, parts[0].ExecutableCode, "Part 0 must have executableCode")
	assert.Equal(t, "PYTHON", parts[0].ExecutableCode.Language)
	assert.Contains(t, parts[0].ExecutableCode.Code, "is_prime")
	assert.NotNil(t, parts[0].ThoughtSignature, "Part 0 must have thoughtSignature attached to executableCode")
	assert.Equal(t, geminiResp.Candidates[0].Content.Parts[0].ThoughtSignature, parts[0].ThoughtSignature)

	// Part 1: codeExecutionResult (no thoughtSignature)
	assert.NotNil(t, parts[1].CodeExecutionResult, "Part 1 must have codeExecutionResult")
	assert.Equal(t, OutcomeOK, parts[1].CodeExecutionResult.Outcome)
	assert.Contains(t, parts[1].CodeExecutionResult.Output, "Primes:")
	assert.Nil(t, parts[1].ThoughtSignature, "Part 1 should NOT have thoughtSignature")

	// Part 2: text with thoughtSignature
	assert.NotEmpty(t, parts[2].Text, "Part 2 must have text")
	assert.Contains(t, parts[2].Text, "5,117")
	assert.NotNil(t, parts[2].ThoughtSignature, "Part 2 must have thoughtSignature attached to text")
	assert.Equal(t, geminiResp.Candidates[0].Content.Parts[2].ThoughtSignature, parts[2].ThoughtSignature)

	// Ensure NO standalone thoughtSignature parts were created
	for i, part := range parts {
		// A standalone thoughtSignature part would have ONLY thoughtSignature set, nothing else
		if part.ThoughtSignature != nil {
			// It should have either executableCode, text, or functionCall
			hasContent := part.ExecutableCode != nil || part.Text != "" || part.FunctionCall != nil
			assert.True(t, hasContent, "Part %d has thoughtSignature but no content - this is a standalone thoughtSignature part which is incorrect", i)
		}
	}
}
