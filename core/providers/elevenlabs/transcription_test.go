package elevenlabs

import (
	"testing"
)

// TestConvertWords_PropagatesSpeakerID confirms that ElevenLabs' diarization
// output (speaker_id per word, per https://elevenlabs.io/docs/api-reference/speech-to-text/convert)
// is carried through into Bifrost's canonical TranscriptionWord.Speaker field
// instead of being silently dropped.
func TestConvertWords_PropagatesSpeakerID(t *testing.T) {
	speakerA := "speaker_0"
	speakerB := "speaker_1"
	start1, end1 := 0.0, 1.2
	start2, end2 := 1.2, 2.5

	words := []ElevenlabsSpeechToTextWord{
		{Text: "Hello", Type: "word", Start: &start1, End: &end1, SpeakerID: &speakerA, LogProb: -0.1},
		{Text: " ", Type: "spacing", Start: &end1, End: &end1, SpeakerID: &speakerA},
		{Text: "there", Type: "word", Start: &start2, End: &end2, SpeakerID: &speakerB, LogProb: -0.2},
	}

	converted, logProbs, duration := convertWords(words)

	if len(converted) != 2 {
		t.Fatalf("expected 2 words (spacing dropped), got %d", len(converted))
	}
	if converted[0].Speaker == nil || *converted[0].Speaker != "speaker_0" {
		t.Fatalf("expected first word speaker %q, got %+v", "speaker_0", converted[0].Speaker)
	}
	if converted[1].Speaker == nil || *converted[1].Speaker != "speaker_1" {
		t.Fatalf("expected second word speaker %q, got %+v", "speaker_1", converted[1].Speaker)
	}
	if len(logProbs) != 2 {
		t.Fatalf("expected 2 logprobs, got %d", len(logProbs))
	}
	if duration == nil || *duration != 2.5 {
		t.Fatalf("expected duration 2.5, got %v", duration)
	}
}

// TestToBifrostTranscriptionResponse_RoleBasedSpeakers confirms role-based
// diarization (detect_speaker_roles=true, speaker_id values "agent"/"customer")
// also flows through unchanged, since Speaker is a plain string passthrough.
func TestToBifrostTranscriptionResponse_RoleBasedSpeakers(t *testing.T) {
	agent := "agent"
	start, end := 0.0, 1.0

	chunks := []ElevenlabsSpeechToTextChunkResponse{
		{
			LanguageCode: "en",
			Text:         "Hello",
			Words: []ElevenlabsSpeechToTextWord{
				{Text: "Hello", Type: "word", Start: &start, End: &end, SpeakerID: &agent, LogProb: -0.05},
			},
		},
	}

	resp := ToBifrostTranscriptionResponse(chunks)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Words) != 1 {
		t.Fatalf("expected 1 word, got %d", len(resp.Words))
	}
	if resp.Words[0].Speaker == nil || *resp.Words[0].Speaker != "agent" {
		t.Fatalf("expected speaker %q, got %+v", "agent", resp.Words[0].Speaker)
	}
}
