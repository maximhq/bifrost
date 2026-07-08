package schemas

import (
	"encoding/json"
	"testing"
)

// TestBifrostTranscriptionResponse_MarshalJSON_DiarizedEmptySegments ensures
// that a diarized_json response with zero segments (e.g. silent/very short
// audio) still emits "segments":[] instead of omitting the key. The OpenAI
// Python SDK's TranscriptionDiarized model treats "segments" as a required,
// non-optional field (unlike TranscriptionVerbose's Optional segments), so a
// missing key would fail client-side validation.
func TestBifrostTranscriptionResponse_MarshalJSON_DiarizedEmptySegments(t *testing.T) {
	duration := 0.5
	task := "transcribe"
	resp := &BifrostTranscriptionResponse{
		Duration:         &duration,
		Task:             &task,
		Text:             "",
		DiarizedSegments: []TranscriptionDiarizedSegment{}, // non-nil, zero-length
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	segments, present := decoded["segments"]
	if !present {
		t.Fatalf(`expected "segments" key to be present even when empty, got: %s`, data)
	}
	arr, ok := segments.([]interface{})
	if !ok || len(arr) != 0 {
		t.Fatalf(`expected "segments" to be an empty array, got: %+v`, segments)
	}
}

// TestBifrostTranscriptionResponse_MarshalJSON_DiarizedSegments confirms the
// populated (non-empty) diarized case still emits the segment data under
// "segments" with the string id/speaker/type fields intact.
func TestBifrostTranscriptionResponse_MarshalJSON_DiarizedSegments(t *testing.T) {
	resp := &BifrostTranscriptionResponse{
		Text: "Hi",
		DiarizedSegments: []TranscriptionDiarizedSegment{
			{ID: "seg_1", Type: "transcript.text.segment", Speaker: "A", Start: 0, End: 1.2, Text: "Hi"},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	segments, ok := decoded["segments"].([]interface{})
	if !ok || len(segments) != 1 {
		t.Fatalf(`expected 1 segment under "segments", got: %+v`, decoded["segments"])
	}
	seg, ok := segments[0].(map[string]interface{})
	if !ok || seg["id"] != "seg_1" {
		t.Fatalf(`expected string id "seg_1", got: %+v`, seg)
	}
}

// TestBifrostTranscriptionResponse_MarshalJSON_VerboseJSONUnaffected ensures
// the default (non-diarized) marshal path is untouched: nil DiarizedSegments
// falls through to the normal Segments field with its own omitempty tag.
func TestBifrostTranscriptionResponse_MarshalJSON_VerboseJSONUnaffected(t *testing.T) {
	resp := &BifrostTranscriptionResponse{
		Text:     "Hi",
		Segments: []TranscriptionSegment{{ID: 0, Text: "Hi"}},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	segments, ok := decoded["segments"].([]interface{})
	if !ok || len(segments) != 1 {
		t.Fatalf(`expected 1 verbose-json segment, got: %+v`, decoded["segments"])
	}
	seg, ok := segments[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected segment object, got: %+v", segments[0])
	}
	if _, isFloat := seg["id"].(float64); !isFloat {
		t.Fatalf("expected numeric id for verbose-json segment, got: %+v (%T)", seg["id"], seg["id"])
	}
}

// TestBifrostTranscriptionResponse_RoundTrip_Diarized simulates
// framework/logstore's persist-then-reload cycle (Marshal on write, Unmarshal
// on GORM's AfterFind read): a diarized response must decode back into
// DiarizedSegments (string id/speaker/type) rather than being misdirected
// into Segments (int id), which would either fail to unmarshal or silently
// corrupt the reloaded log.
func TestBifrostTranscriptionResponse_RoundTrip_Diarized(t *testing.T) {
	original := &BifrostTranscriptionResponse{
		Text: "Hi there",
		DiarizedSegments: []TranscriptionDiarizedSegment{
			{ID: "seg_0", Type: "transcript.text.segment", Speaker: "A", Start: 0, End: 1.2, Text: "Hi"},
			{ID: "seg_1", Type: "transcript.text.segment", Speaker: "B", Start: 1.2, End: 2.5, Text: "there"},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var reloaded BifrostTranscriptionResponse
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal error (this is the regression: diarized logs would fail or corrupt on DB reload): %v", err)
	}

	if len(reloaded.Segments) != 0 {
		t.Fatalf("expected verbose Segments to stay empty after reloading a diarized response, got %+v", reloaded.Segments)
	}
	if len(reloaded.DiarizedSegments) != 2 {
		t.Fatalf("expected 2 diarized segments after round-trip, got %d", len(reloaded.DiarizedSegments))
	}
	if reloaded.DiarizedSegments[0].ID != "seg_0" || reloaded.DiarizedSegments[1].ID != "seg_1" {
		t.Fatalf("expected string ids to survive round-trip, got %+v", reloaded.DiarizedSegments)
	}
	if reloaded.DiarizedSegments[1].Speaker != "B" {
		t.Fatalf("expected speaker to survive round-trip, got %+v", reloaded.DiarizedSegments[1])
	}
	if reloaded.Text != "Hi there" {
		t.Fatalf("expected text to survive round-trip, got %q", reloaded.Text)
	}
}

// TestBifrostTranscriptionResponse_RoundTrip_Verbose is the same simulated
// persist/reload cycle for the pre-existing verbose_json shape, confirming
// UnmarshalJSON's shape-detection doesn't regress the common case.
func TestBifrostTranscriptionResponse_RoundTrip_Verbose(t *testing.T) {
	original := &BifrostTranscriptionResponse{
		Text: "Hi there",
		Segments: []TranscriptionSegment{
			{ID: 0, Text: "Hi", Start: 0, End: 1.2},
			{ID: 1, Text: "there", Start: 1.2, End: 2.5},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var reloaded BifrostTranscriptionResponse
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(reloaded.DiarizedSegments) != 0 {
		t.Fatalf("expected DiarizedSegments to stay empty after reloading a verbose response, got %+v", reloaded.DiarizedSegments)
	}
	if len(reloaded.Segments) != 2 || reloaded.Segments[1].ID != 1 {
		t.Fatalf("expected 2 int-id segments after round-trip, got %+v", reloaded.Segments)
	}
}

// TestBifrostTranscriptionResponse_RoundTrip_NoSegments confirms a response
// with neither segment kind (e.g. plain response_format=json) round-trips
// cleanly with both fields left empty.
func TestBifrostTranscriptionResponse_RoundTrip_NoSegments(t *testing.T) {
	original := &BifrostTranscriptionResponse{Text: "Hi there"}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var reloaded BifrostTranscriptionResponse
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(reloaded.Segments) != 0 || len(reloaded.DiarizedSegments) != 0 {
		t.Fatalf("expected both segment fields to stay empty, got Segments=%+v DiarizedSegments=%+v",
			reloaded.Segments, reloaded.DiarizedSegments)
	}
	if reloaded.Text != "Hi there" {
		t.Fatalf("expected text to survive round-trip, got %q", reloaded.Text)
	}
}

// TestBifrostTranscriptionResponse_RoundTrip_DiarizedEmpty is the regression
// case flagged in review: an empty JSON array unmarshals successfully into
// either segment type, so without the "is_diarized" marker a diarized
// response with zero segments (e.g. silent/very short audio) would always be
// misclassified as an empty verbose Segments on reload, silently losing its
// diarized identity (and, on the next re-marshal, losing the "segments" key
// entirely too, since OpenAI's diarized_json treats it as required).
func TestBifrostTranscriptionResponse_RoundTrip_DiarizedEmpty(t *testing.T) {
	original := &BifrostTranscriptionResponse{
		Text:             "",
		DiarizedSegments: []TranscriptionDiarizedSegment{}, // non-nil, zero-length
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var reloaded BifrostTranscriptionResponse
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if reloaded.DiarizedSegments == nil {
		t.Fatalf("expected DiarizedSegments to stay non-nil (empty) after reloading an empty diarized response, got nil (misclassified as verbose)")
	}
	if len(reloaded.DiarizedSegments) != 0 {
		t.Fatalf("expected 0 diarized segments, got %d", len(reloaded.DiarizedSegments))
	}
	if len(reloaded.Segments) != 0 {
		t.Fatalf("expected verbose Segments to stay empty, got %+v", reloaded.Segments)
	}

	// The reloaded value must itself still round-trip with "segments":[]
	// present (not omitted), matching OpenAI's diarized_json contract.
	reMarshaled, err := json.Marshal(&reloaded)
	if err != nil {
		t.Fatalf("re-marshal error: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(reMarshaled, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, present := decoded["segments"]; !present {
		t.Fatalf(`expected "segments" key to survive a second round-trip, got: %s`, reMarshaled)
	}
}

// TestBifrostTranscriptionResponse_UnmarshalJSON_LegacyDiarizedNoMarker
// confirms backward compatibility with diarized data persisted before the
// "is_diarized" marker existed: non-empty segments still get shape-sniffed
// correctly by attempting the verbose int-id shape first and falling back to
// diarized on a type mismatch, exactly as before this fix.
func TestBifrostTranscriptionResponse_UnmarshalJSON_LegacyDiarizedNoMarker(t *testing.T) {
	legacyJSON := `{"text":"Hi","segments":[{"id":"seg_0","type":"transcript.text.segment","speaker":"A","start":0,"end":1.2,"text":"Hi"}]}`

	var reloaded BifrostTranscriptionResponse
	if err := json.Unmarshal([]byte(legacyJSON), &reloaded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(reloaded.DiarizedSegments) != 1 || reloaded.DiarizedSegments[0].ID != "seg_0" {
		t.Fatalf("expected legacy (unmarked) diarized data to still be sniffed correctly, got %+v", reloaded.DiarizedSegments)
	}
}
