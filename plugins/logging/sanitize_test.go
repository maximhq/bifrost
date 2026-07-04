package logging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

func errorWithRawPayloads() *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{Message: "provider rejected request"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RawRequest:  map[string]any{"messages": "RAW_REQUEST_MARKER"},
			RawResponse: map[string]any{"body": "RAW_RESPONSE_MARKER"},
		},
	}
}

// Regression test: logstore's SerializeFields serializes ErrorDetailsParsed
// into ErrorDetails on write. If the parsed field holds the unsanitized error,
// raw request/response payloads reach the store even when content logging is
// disabled.
func TestSanitizedErrorDetailsSurviveSerializeFields(t *testing.T) {
	entry := &logstore.Log{ID: "req-1"}
	entry.ErrorDetailsParsed = sanitizeErrorForLogging(errorWithRawPayloads(), false, false)

	if entry.ErrorDetailsParsed == nil {
		t.Fatal("ErrorDetailsParsed should be set")
	}
	if entry.ErrorDetailsParsed.ExtraFields.RawRequest != nil ||
		entry.ErrorDetailsParsed.ExtraFields.RawResponse != nil {
		t.Error("ErrorDetailsParsed should not retain raw payloads when content logging is disabled")
	}

	// Simulate the DB write path (BeforeCreate calls SerializeFields).
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error: %v", err)
	}
	if strings.Contains(entry.ErrorDetails, "RAW_REQUEST_MARKER") ||
		strings.Contains(entry.ErrorDetails, "RAW_RESPONSE_MARKER") {
		t.Error("serialized ErrorDetails must not contain raw payloads when content logging is disabled")
	}
	if !strings.Contains(entry.ErrorDetails, "provider rejected request") {
		t.Error("serialized ErrorDetails should still contain the error message")
	}
}

// When content logging and raw storage are both enabled, raw payloads are
// intentionally preserved.
func TestRawErrorDetailsPreservedWhenEnabled(t *testing.T) {
	entry := &logstore.Log{ID: "req-2"}
	entry.ErrorDetailsParsed = sanitizeErrorForLogging(errorWithRawPayloads(), true, true)

	if entry.ErrorDetailsParsed == nil {
		t.Fatal("ErrorDetailsParsed should be set")
	}
	if entry.ErrorDetailsParsed.ExtraFields.RawRequest == nil {
		t.Error("raw payloads should be preserved when content logging and raw storage are enabled")
	}
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error: %v", err)
	}
	if !strings.Contains(entry.ErrorDetails, "RAW_REQUEST_MARKER") {
		t.Error("serialized ErrorDetails should contain raw payloads when explicitly enabled")
	}
}

func TestSanitizeErrorForLoggingNilError(t *testing.T) {
	entry := &logstore.Log{ID: "req-3"}
	entry.ErrorDetailsParsed = sanitizeErrorForLogging(nil, false, false)
	if entry.ErrorDetailsParsed != nil {
		t.Error("nil error should leave ErrorDetailsParsed nil")
	}
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error: %v", err)
	}
	if entry.ErrorDetails != "" {
		t.Error("nil error should leave ErrorDetails empty")
	}
}

// MCPToolLog counterpart: same sanitization semantics through its own
// SerializeFields.
func TestSanitizedMCPErrorDetailsSurviveSerializeFields(t *testing.T) {
	entry := &logstore.MCPToolLog{ID: "mcp-1"}
	entry.ErrorDetailsParsed = sanitizeErrorForLogging(errorWithRawPayloads(), false, false)

	if entry.ErrorDetailsParsed == nil {
		t.Fatal("ErrorDetailsParsed should be set")
	}
	if entry.ErrorDetailsParsed.ExtraFields.RawRequest != nil ||
		entry.ErrorDetailsParsed.ExtraFields.RawResponse != nil {
		t.Error("ErrorDetailsParsed should not retain raw payloads when content logging is disabled")
	}
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error: %v", err)
	}
	if strings.Contains(entry.ErrorDetails, "RAW_REQUEST_MARKER") {
		t.Error("serialized ErrorDetails must not contain raw payloads when content logging is disabled")
	}
	if !strings.Contains(entry.ErrorDetails, "provider rejected request") {
		t.Error("serialized ErrorDetails should still contain the error message")
	}
}

// Update-path regression: updateLogEntry must sanitize UpdateLogData.ErrorDetails
// before SerializeFields copies it into the error_details column update.
func TestUpdateLogEntrySanitizesErrorDetails(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
	}

	requestID := "req-err-update"
	initial := &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}
	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", time.Now().UTC(), 0, nil, initial); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	update := &UpdateLogData{
		Status:       "error",
		ErrorDetails: errorWithRawPayloads(),
	}
	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, false); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if strings.Contains(logEntry.ErrorDetails, "RAW_REQUEST_MARKER") ||
		strings.Contains(logEntry.ErrorDetails, "RAW_RESPONSE_MARKER") {
		t.Errorf("stored error_details must not contain raw payloads when content logging is disabled, got %q", logEntry.ErrorDetails)
	}
	if !strings.Contains(logEntry.ErrorDetails, "provider rejected request") {
		t.Errorf("stored error_details should still contain the error message, got %q", logEntry.ErrorDetails)
	}
}
