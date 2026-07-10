package schemas

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ParseRealtimeClientSecretBody parses a realtime client-secret request body
// into a mutable raw JSON map while preserving unknown fields.
func ParseRealtimeClientSecretBody(raw json.RawMessage) (map[string]json.RawMessage, *BifrostError) {
	var root map[string]json.RawMessage
	if err := Unmarshal(raw, &root); err != nil {
		return nil, NewRealtimeClientSecretBodyError(400, "invalid_request_error", "invalid JSON body", err)
	}
	return root, nil
}

// ExtractRealtimeClientSecretModel extracts the model from either session.model
// or the legacy top-level model field. Also falls back to the GA transcription
// shape (session.audio.input.transcription.model) since /v1/realtime/client_secrets
// mints both full realtime sessions (session.model) AND transcription-only
// sessions (session.type == "transcription", RealtimeTranscriptionSessionCreateRequestGA
// — which has no top-level session.model at all, only the nested transcription
// model) through this same endpoint.
func ExtractRealtimeClientSecretModel(root map[string]json.RawMessage) (string, *BifrostError) {
	if sessionJSON, ok := root["session"]; ok && len(sessionJSON) > 0 && !bytes.Equal(sessionJSON, []byte("null")) {
		var session map[string]json.RawMessage
		if err := Unmarshal(sessionJSON, &session); err != nil {
			return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session must be an object", err)
		}
		if modelJSON, ok := session["model"]; ok {
			var sessionModel string
			if err := Unmarshal(modelJSON, &sessionModel); err != nil {
				return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.model must be a string", err)
			}
			if strings.TrimSpace(sessionModel) != "" {
				return strings.TrimSpace(sessionModel), nil
			}
		}
		if model, found, err := extractGATranscriptionModel(session); err != nil || found {
			return model, err
		}
	}

	if modelJSON, ok := root["model"]; ok {
		var model string
		if err := Unmarshal(modelJSON, &model); err != nil {
			return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "model must be a string", err)
		}
		if strings.TrimSpace(model) != "" {
			return strings.TrimSpace(model), nil
		}
	}

	return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.model or model is required", nil)
}

// extractGATranscriptionModel reads session.audio.input.transcription.model —
// the GA transcription-session shape (RealtimeTranscriptionSessionCreateRequestGA),
// which has no top-level session.model at all. The bool return distinguishes
// "found a non-empty model" from "absent — not a transcription-shaped session".
func extractGATranscriptionModel(session map[string]json.RawMessage) (string, bool, *BifrostError) {
	audioJSON, ok := session["audio"]
	if !ok || len(audioJSON) == 0 || bytes.Equal(audioJSON, []byte("null")) {
		return "", false, nil
	}
	var audio map[string]json.RawMessage
	if err := Unmarshal(audioJSON, &audio); err != nil {
		return "", true, NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.audio must be an object", err)
	}
	inputJSON, ok := audio["input"]
	if !ok || len(inputJSON) == 0 || bytes.Equal(inputJSON, []byte("null")) {
		return "", false, nil
	}
	var input map[string]json.RawMessage
	if err := Unmarshal(inputJSON, &input); err != nil {
		return "", true, NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.audio.input must be an object", err)
	}
	transJSON, ok := input["transcription"]
	if !ok || len(transJSON) == 0 || bytes.Equal(transJSON, []byte("null")) {
		return "", false, nil
	}
	var transcription map[string]json.RawMessage
	if err := Unmarshal(transJSON, &transcription); err != nil {
		return "", true, NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.audio.input.transcription must be an object", err)
	}
	modelJSON, ok := transcription["model"]
	if !ok {
		return "", false, nil
	}
	var model string
	if err := Unmarshal(modelJSON, &model); err != nil {
		return "", true, NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.audio.input.transcription.model must be a string", err)
	}
	model = strings.TrimSpace(model)
	return model, model != "", nil
}

// NewRealtimeClientSecretBodyError builds a standard invalid-request style error
// for HTTP realtime client-secret request parsing/validation.
func NewRealtimeClientSecretBodyError(status int, errorType, message string, err error) *BifrostError {
	return &BifrostError{
		IsBifrostError: false,
		StatusCode:     Ptr(status),
		Error: &ErrorField{
			Type:    Ptr(errorType),
			Message: message,
			Error:   err,
		},
		ExtraFields: BifrostErrorExtraFields{
			RequestType: RealtimeRequest,
		},
	}
}
