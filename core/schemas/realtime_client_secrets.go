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
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, NewRealtimeClientSecretBodyError(400, "invalid_request_error", "invalid JSON body", err)
	}
	return root, nil
}

// ExtractRealtimeClientSecretModel extracts the model from either session.model
// or the legacy top-level model field.
func ExtractRealtimeClientSecretModel(root map[string]json.RawMessage) (string, *BifrostError) {
	if sessionJSON, ok := root["session"]; ok && len(sessionJSON) > 0 && !bytes.Equal(sessionJSON, []byte("null")) {
		var session map[string]json.RawMessage
		if err := json.Unmarshal(sessionJSON, &session); err != nil {
			return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session must be an object", err)
		}
		if modelJSON, ok := session["model"]; ok {
			var sessionModel string
			if err := json.Unmarshal(modelJSON, &sessionModel); err != nil {
				return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.model must be a string", err)
			}
			if strings.TrimSpace(sessionModel) != "" {
				return strings.TrimSpace(sessionModel), nil
			}
		}
	}

	if modelJSON, ok := root["model"]; ok {
		var model string
		if err := json.Unmarshal(modelJSON, &model); err != nil {
			return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "model must be a string", err)
		}
		if strings.TrimSpace(model) != "" {
			return strings.TrimSpace(model), nil
		}
	}

	return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.model is required", nil)
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
