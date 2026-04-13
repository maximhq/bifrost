package tracing

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestPopulateResponseAttributes_RawRequestResponse(t *testing.T) {
	rawReq := map[string]any{
		"model":    "gpt-4",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	rawResp := map[string]any{
		"id":      "chatcmpl-abc123",
		"object":  "chat.completion",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "hi"}}},
	}

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RawRequest:  rawReq,
				RawResponse: rawResp,
			},
		},
	}

	attrs := PopulateResponseAttributes(resp)

	rawReqAttr, ok := attrs[schemas.AttrRawRequest]
	if !ok {
		t.Fatal("expected gen_ai.raw_request attribute to be present")
	}
	rawRespAttr, ok := attrs[schemas.AttrRawResponse]
	if !ok {
		t.Fatal("expected gen_ai.raw_response attribute to be present")
	}

	// Verify they are valid JSON strings
	reqStr, ok := rawReqAttr.(string)
	if !ok {
		t.Fatalf("expected gen_ai.raw_request to be a string, got %T", rawReqAttr)
	}
	respStr, ok := rawRespAttr.(string)
	if !ok {
		t.Fatalf("expected gen_ai.raw_response to be a string, got %T", rawRespAttr)
	}

	var parsedReq map[string]any
	if err := json.Unmarshal([]byte(reqStr), &parsedReq); err != nil {
		t.Fatalf("gen_ai.raw_request is not valid JSON: %v", err)
	}
	if parsedReq["model"] != "gpt-4" {
		t.Errorf("expected model=gpt-4, got %v", parsedReq["model"])
	}

	var parsedResp map[string]any
	if err := json.Unmarshal([]byte(respStr), &parsedResp); err != nil {
		t.Fatalf("gen_ai.raw_response is not valid JSON: %v", err)
	}
	if parsedResp["id"] != "chatcmpl-abc123" {
		t.Errorf("expected id=chatcmpl-abc123, got %v", parsedResp["id"])
	}
}

func TestPopulateResponseAttributes_NilRawFields(t *testing.T) {
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RawRequest:  nil,
				RawResponse: nil,
			},
		},
	}

	attrs := PopulateResponseAttributes(resp)

	if _, ok := attrs[schemas.AttrRawRequest]; ok {
		t.Error("expected gen_ai.raw_request to be absent when RawRequest is nil")
	}
	if _, ok := attrs[schemas.AttrRawResponse]; ok {
		t.Error("expected gen_ai.raw_response to be absent when RawResponse is nil")
	}
}

func TestPopulateResponseAttributes_NilResponse(t *testing.T) {
	attrs := PopulateResponseAttributes(nil)

	if len(attrs) != 0 {
		t.Errorf("expected empty attributes for nil response, got %d attributes", len(attrs))
	}
}

func TestPopulateResponseAttributes_OnlyRawRequestSet(t *testing.T) {
	rawReq := map[string]any{"model": "claude-3"}

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RawRequest:  rawReq,
				RawResponse: nil,
			},
		},
	}

	attrs := PopulateResponseAttributes(resp)

	if _, ok := attrs[schemas.AttrRawRequest]; !ok {
		t.Error("expected gen_ai.raw_request to be present")
	}
	if _, ok := attrs[schemas.AttrRawResponse]; ok {
		t.Error("expected gen_ai.raw_response to be absent when RawResponse is nil")
	}
}

func TestPopulateResponseAttributes_OnlyRawResponseSet(t *testing.T) {
	rawResp := map[string]any{"id": "msg_123"}

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RawRequest:  nil,
				RawResponse: rawResp,
			},
		},
	}

	attrs := PopulateResponseAttributes(resp)

	if _, ok := attrs[schemas.AttrRawRequest]; ok {
		t.Error("expected gen_ai.raw_request to be absent when RawRequest is nil")
	}
	if _, ok := attrs[schemas.AttrRawResponse]; !ok {
		t.Error("expected gen_ai.raw_response to be present")
	}
}

func TestPopulateResponseAttributes_JsonRawMessageType(t *testing.T) {
	raw := json.RawMessage(`{"model":"gpt-4","messages":[]}`)

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RawRequest: raw,
			},
		},
	}

	attrs := PopulateResponseAttributes(resp)

	got, ok := attrs[schemas.AttrRawRequest].(string)
	if !ok {
		t.Fatal("expected gen_ai.raw_request to be a string")
	}
	if got != string(raw) {
		t.Errorf("expected %q, got %q", string(raw), got)
	}
}

func TestPopulateResponseAttributes_UnmarshalableRawRequest(t *testing.T) {
	// channels cannot be marshaled to JSON
	unmarshalable := make(chan int)

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RawRequest:  unmarshalable,
				RawResponse: map[string]any{"id": "msg_123"},
			},
		},
	}

	attrs := PopulateResponseAttributes(resp)

	// RawRequest should be absent due to marshal error
	if _, ok := attrs[schemas.AttrRawRequest]; ok {
		t.Error("expected gen_ai.raw_request to be absent when marshal fails")
	}
	// RawResponse should still be present
	if _, ok := attrs[schemas.AttrRawResponse]; !ok {
		t.Error("expected gen_ai.raw_response to be present")
	}
}
