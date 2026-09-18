package schemas

import "testing"

func TestSpeechBackfillParamsPreservesProviderCharacterUsage(t *testing.T) {
	request := &BifrostSpeechRequest{Input: &SpeechInput{Input: "provider-normalized text"}}

	response := &BifrostSpeechResponse{Usage: &SpeechUsage{InputChars: 7}}
	response.BackfillParams(request)
	if response.Usage.InputChars != 7 {
		t.Fatalf("non-streaming InputChars = %d, want provider-reported 7", response.Usage.InputChars)
	}

	streamResponse := &BifrostSpeechStreamResponse{Usage: &SpeechUsage{InputChars: 9}}
	streamResponse.BackfillParams(request)
	if streamResponse.Usage.InputChars != 9 {
		t.Fatalf("streaming InputChars = %d, want provider-reported 9", streamResponse.Usage.InputChars)
	}
}

func TestSpeechBackfillParamsFillsMissingCharacterUsage(t *testing.T) {
	request := &BifrostSpeechRequest{Input: &SpeechInput{Input: "你好a"}}

	response := &BifrostSpeechResponse{}
	response.BackfillParams(request)
	if response.Usage == nil || response.Usage.InputChars != 3 {
		t.Fatalf("non-streaming InputChars = %v, want 3", response.Usage)
	}

	streamResponse := &BifrostSpeechStreamResponse{}
	streamResponse.BackfillParams(request)
	if streamResponse.Usage == nil || streamResponse.Usage.InputChars != 3 {
		t.Fatalf("streaming InputChars = %v, want 3", streamResponse.Usage)
	}
}
