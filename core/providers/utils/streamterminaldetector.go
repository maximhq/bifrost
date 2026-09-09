package utils

import (
	"bytes"
	"strings"

	"github.com/bytedance/sonic"
)

const maxTerminalDetectorBufferBytes = 256 * 1024

var (
	sseFrameDelimiterLF   = []byte("\n\n")
	sseFrameDelimiterCRLF = []byte("\r\n\r\n")
)

// StreamTerminalDetector incrementally parses stream frames and detects
// semantic completion markers such as finishReason or [DONE].
type StreamTerminalDetector struct {
	pending bytes.Buffer
}

// ObserveChunk ingests a new raw stream chunk and returns true if a terminal
// marker was detected in a parsed frame payload.
func (d *StreamTerminalDetector) ObserveChunk(chunk []byte) bool {
	if len(chunk) == 0 {
		return false
	}

	// Fast path: detect terminal markers when a single chunk already contains
	// a complete payload (SSE data line or plain JSON body).
	// Skip this when the chunk already contains full SSE frame delimiters,
	// because multi-event chunks need frame-by-frame parsing.
	if !containsSSEFrameDelimiter(chunk) && d.detectInFrame(chunk) {
		return true
	}

	d.pending.Write(chunk)

	for {
		data := d.pending.Bytes()
		delimIdx, delimLen := findFirstSSEFrameDelimiter(data)
		if delimIdx < 0 {
			break
		}

		frame := append([]byte(nil), data[:delimIdx]...)
		d.pending.Next(delimIdx + delimLen)
		if d.detectInFrame(frame) {
			return true
		}
	}

	// Some passthrough streams emit plain JSON chunks (no SSE "\n\n" framing).
	// Try parsing the current pending buffer as a whole JSON payload.
	if d.detectInUndelimitedPending() {
		return true
	}

	// Keep memory bounded if the upstream never emits a frame delimiter.
	if d.pending.Len() > maxTerminalDetectorBufferBytes {
		drain := d.pending.Bytes()
		if idx, delimLen := findLastSSEFrameDelimiter(drain); idx >= 0 {
			d.pending.Next(idx + delimLen)
		} else {
			trimTo := maxTerminalDetectorBufferBytes / 2
			keptPrefix := append([]byte(nil), drain[:trimTo]...)
			d.pending.Reset()
			d.pending.Write(keptPrefix)
		}
	}
	return false
}

func (d *StreamTerminalDetector) detectInUndelimitedPending() bool {
	if d.pending.Len() == 0 {
		return false
	}
	payload := bytes.TrimSpace(d.pending.Bytes())
	if len(payload) == 0 {
		return false
	}
	return hasFinishReasonMarker(payload)
}

func (d *StreamTerminalDetector) detectInFrame(frame []byte) bool {
	payload := extractSSEDataPayload(frame)
	if len(payload) == 0 {
		return false
	}

	text := strings.TrimSpace(string(payload))
	if text == "" {
		return false
	}
	if text == "[DONE]" {
		return true
	}

	return hasFinishReasonMarker([]byte(text))
}

func extractSSEDataPayload(frame []byte) []byte {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return nil
	}
	if !hasSSEDataLinePrefix(trimmed) {
		return trimmed
	}

	lines := bytes.Split(trimmed, []byte("\n"))
	var payload bytes.Buffer
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(data) == 0 {
				continue
			}
			if payload.Len() > 0 {
				payload.WriteByte('\n')
			}
			payload.Write(data)
		}
	}
	if payload.Len() == 0 {
		return nil
	}
	return payload.Bytes()
}

func hasSSEDataLinePrefix(frame []byte) bool {
	lines := bytes.Split(frame, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			return true
		}
	}
	return false
}

func hasFinishReasonMarker(payload []byte) bool {
	var root any
	if err := sonic.Unmarshal(payload, &root); err != nil {
		return false
	}

	switch v := root.(type) {
	case map[string]any:
		return hasTerminalMarkerInTopLevelObject(v)
	case []any:
		for _, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if hasTerminalMarkerInTopLevelObject(obj) {
				return true
			}
		}
	}
	return false
}

func hasTerminalMarkerInTopLevelObject(root map[string]any) bool {
	if hasValidFinishReasonValue(root) {
		return true
	}

	// Anthropic (incl. via Vertex :streamRawPredict) ends with message_stop and carries no
	// finishReason. message_delta is deliberately not terminal: it holds the final usage.
	if eventType, ok := root["type"].(string); ok {
		switch eventType {
		case "message_stop", "error":
			return true
		}
	}

	// Google/Vertex error payloads ({"error":{"code":...,"status":...}}, sometimes wrapped in a
	// top-level array) are terminal: nothing follows them, but the upstream body can stay open.
	if hasGoogleErrorObject(root) {
		return true
	}

	// Gemini/Vertex streamGenerateContent often signals terminal state in
	// top-level candidates[*].finishReason.
	if candidatesValue, ok := root["candidates"]; ok {
		if candidates, ok := candidatesValue.([]any); ok {
			return allCandidatesFinished(candidates)
		}
	}

	// usageMetadata can show up before terminal chunks in long-running streams.
	// Treat it as terminal only when all candidates are finished.
	if usageMetadata, ok := root["usageMetadata"]; ok {
		if usageMap, ok := usageMetadata.(map[string]any); ok && len(usageMap) > 0 {
			if candidatesValue, ok := root["candidates"]; ok {
				if candidates, ok := candidatesValue.([]any); ok && allCandidatesFinished(candidates) {
					return true
				}
			}
		}
	}
	if promptFeedback, ok := root["promptFeedback"]; ok {
		if feedbackMap, ok := promptFeedback.(map[string]any); ok {
			if reason, ok := feedbackMap["blockReason"].(string); ok && strings.TrimSpace(reason) != "" {
				return true
			}
		}
	}
	return false
}

func allCandidatesFinished(candidates []any) bool {
	if len(candidates) == 0 {
		return false
	}
	for _, candidate := range candidates {
		candidateMap, ok := candidate.(map[string]any)
		if !ok {
			return false
		}
		if !hasValidFinishReasonValue(candidateMap) {
			return false
		}
	}
	return true
}

func hasValidFinishReasonValue(node map[string]any) bool {
	for _, key := range []string{"finishReason", "finish_reason"} {
		value, ok := node[key]
		if !ok {
			continue
		}
		if str, ok := value.(string); ok && strings.TrimSpace(str) != "" && str != "FINISH_REASON_UNSPECIFIED" {
			return true
		}
	}
	return false
}

func findFirstSSEFrameDelimiter(data []byte) (idx int, delimLen int) {
	idxLF := bytes.Index(data, sseFrameDelimiterLF)
	idxCRLF := bytes.Index(data, sseFrameDelimiterCRLF)

	switch {
	case idxLF < 0 && idxCRLF < 0:
		return -1, 0
	case idxLF < 0:
		return idxCRLF, len(sseFrameDelimiterCRLF)
	case idxCRLF < 0:
		return idxLF, len(sseFrameDelimiterLF)
	case idxCRLF < idxLF:
		return idxCRLF, len(sseFrameDelimiterCRLF)
	default:
		return idxLF, len(sseFrameDelimiterLF)
	}
}

func findLastSSEFrameDelimiter(data []byte) (idx int, delimLen int) {
	idxLF := bytes.LastIndex(data, sseFrameDelimiterLF)
	idxCRLF := bytes.LastIndex(data, sseFrameDelimiterCRLF)

	switch {
	case idxLF < 0 && idxCRLF < 0:
		return -1, 0
	case idxLF < 0:
		return idxCRLF, len(sseFrameDelimiterCRLF)
	case idxCRLF < 0:
		return idxLF, len(sseFrameDelimiterLF)
	case idxCRLF > idxLF:
		return idxCRLF, len(sseFrameDelimiterCRLF)
	default:
		return idxLF, len(sseFrameDelimiterLF)
	}
}

func containsSSEFrameDelimiter(data []byte) bool {
	return bytes.Contains(data, sseFrameDelimiterLF) || bytes.Contains(data, sseFrameDelimiterCRLF)
}

// hasGoogleErrorObject reports whether a payload is a Google/Vertex API error envelope. It
// requires code or status alongside a message so a benign "error" string field in a normal
// event is not mistaken for a terminal error.
func hasGoogleErrorObject(root map[string]any) bool {
	errValue, ok := root["error"]
	if !ok {
		return false
	}
	errMap, ok := errValue.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := errMap["code"]; ok {
		return true
	}
	if status, ok := errMap["status"].(string); ok && strings.TrimSpace(status) != "" {
		return true
	}
	return false
}
