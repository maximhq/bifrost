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
// mints both full realtime sessions (session.model, or the legacy top-level
// model field) AND transcription-only sessions (RealtimeTranscriptionSessionCreateRequestGA
// — which has no top-level session.model at all, only the nested transcription
// model) through this same endpoint. The GA-nested fallback is only ever
// consulted via IsGATranscriptionSessionBody, which itself requires both
// session.model and the legacy root model to be absent — a full realtime
// session must never be reclassified as transcription-only just because it
// also enables live input-audio transcription as a sibling feature.
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

	if IsGATranscriptionSessionBody(root) {
		if sessionJSON, ok := root["session"]; ok && len(sessionJSON) > 0 && !bytes.Equal(sessionJSON, []byte("null")) {
			var session map[string]json.RawMessage
			if err := Unmarshal(sessionJSON, &session); err == nil {
				if model, found, err := extractGATranscriptionModel(session); err != nil || found {
					return model, err
				}
			}
		}
	}

	return "", NewRealtimeClientSecretBodyError(400, "invalid_request_error", "session.model or model is required", nil)
}

// IsGATranscriptionSessionBody reports whether root represents a GA
// transcription-only session (RealtimeTranscriptionSessionCreateRequestGA)
// rather than a full realtime session. This is the single canonical
// classifier shared by extraction (above) and both normalization layers
// (transports/bifrost-http/handlers and core/providers/openai) so they
// cannot diverge on the same input — each layer previously ran its own
// ad hoc classification logic, which could disagree.
//
// Priority (checked in order, first match wins):
//  1. explicit session.type is present -> authoritative, since it's the
//     schema's actual discriminator (oneOf RealtimeSessionCreateRequestGA /
//     RealtimeTranscriptionSessionCreateRequestGA): "transcription" -> true,
//     anything else -> false, regardless of what other fields are also
//     present (e.g. a stray session.model alongside type=="transcription"
//     is invalid input the normalizer should clean up, not a signal to
//     reclassify the session).
//  2. no explicit type: session.model present -> full realtime session (false).
//  3. no explicit type: legacy top-level "model" present -> full realtime
//     session (false). Checked before any transcription-shape inspection so
//     a full session that also enables live input-audio transcription as a
//     sibling feature is never misclassified.
//  4. no explicit type, no model anywhere: session.audio.input.transcription
//     is present and non-null -> transcription session (true); otherwise false.
func IsGATranscriptionSessionBody(root map[string]json.RawMessage) bool {
	var session map[string]json.RawMessage
	if sessionJSON, ok := root["session"]; ok && len(sessionJSON) > 0 && !bytes.Equal(sessionJSON, []byte("null")) {
		_ = Unmarshal(sessionJSON, &session)
	}
	if session == nil {
		return false
	}
	if typeJSON, ok := session["type"]; ok {
		var sessionType string
		if Unmarshal(typeJSON, &sessionType) == nil {
			return sessionType == "transcription"
		}
		return false
	}
	if _, hasModel := session["model"]; hasModel {
		return false
	}
	if _, hasRootModel := root["model"]; hasRootModel {
		return false
	}
	return hasNonNullAudioInputTranscription(session)
}

// hasNonNullAudioInputTranscription reports whether session.audio.input.transcription
// is present and not explicitly null. Null must be excluded: a client can
// send audio.input.transcription: null to explicitly disable transcription
// on a full realtime session, which must not be misread as "this is a
// transcription-only session."
func hasNonNullAudioInputTranscription(session map[string]json.RawMessage) bool {
	audioJSON, ok := session["audio"]
	if !ok || len(audioJSON) == 0 || bytes.Equal(audioJSON, []byte("null")) {
		return false
	}
	var audio map[string]json.RawMessage
	if Unmarshal(audioJSON, &audio) != nil {
		return false
	}
	inputJSON, ok := audio["input"]
	if !ok || len(inputJSON) == 0 || bytes.Equal(inputJSON, []byte("null")) {
		return false
	}
	var input map[string]json.RawMessage
	if Unmarshal(inputJSON, &input) != nil {
		return false
	}
	transJSON, ok := input["transcription"]
	return ok && len(transJSON) > 0 && !bytes.Equal(transJSON, []byte("null"))
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
