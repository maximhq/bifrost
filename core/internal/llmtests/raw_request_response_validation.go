package llmtests

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// validateRawFields checks raw request/response fields and integrates errors into the ValidationResult.
func validateRawFields(expectations ResponseExpectations, rawRequest, rawResponse interface{}, result *ValidationResult) {
	if expectations.ShouldHaveRawRequest {
		if err := ValidateRawField(rawRequest, "RawRequest"); err != nil {
			result.Passed = false
			result.Errors = append(result.Errors, err.Error())
		}
	}
	if expectations.ShouldHaveRawResponse {
		if err := ValidateRawField(rawResponse, "RawResponse"); err != nil {
			result.Passed = false
			result.Errors = append(result.Errors, err.Error())
		}
	}
}

// ValidateRawField checks that a raw request/response field is:
// 1. Non-nil
// 2. Valid JSON (parseable)
// 3. Compact JSON (no unnecessary whitespace)
// Returns an error describing the validation failure, or nil if valid.
func ValidateRawField(field interface{}, fieldName string) error {
	if field == nil {
		return fmt.Errorf("%s should be non-nil when raw request/response is enabled", fieldName)
	}

	// Get the raw bytes depending on the underlying type
	var rawBytes []byte
	var err error

	switch v := field.(type) {
	case json.RawMessage:
		rawBytes = []byte(v)
	case []byte:
		rawBytes = v
	case string:
		rawBytes = []byte(v)
	default:
		// For other types (e.g., map[string]interface{}), marshal to JSON first
		rawBytes, err = sonic.Marshal(field)
		if err != nil {
			return fmt.Errorf("%s failed to marshal to JSON: %v", fieldName, err)
		}
	}

	if len(rawBytes) == 0 {
		return fmt.Errorf("%s is empty", fieldName)
	}

	// Verify parseable as valid JSON
	if !json.Valid(rawBytes) {
		return fmt.Errorf("%s is not valid JSON: %s", fieldName, truncateForError(rawBytes))
	}

	// Verify compact: compact the original and compare (preserves key order)
	var buf bytes.Buffer
	if err := schemas.Compact(&buf, rawBytes); err != nil {
		return fmt.Errorf("%s failed to compact: %v", fieldName, err)
	}
	if !bytes.Equal(rawBytes, buf.Bytes()) {
		return fmt.Errorf("%s is not compact JSON.\nGot:      %s\nExpected: %s", fieldName, truncateForError(rawBytes), truncateForError(buf.Bytes()))
	}

	return nil
}

// truncateForError truncates long byte slices for readable error messages
func truncateForError(b []byte) string {
	const maxLen = 200
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "... (truncated)"
}

// ValidateExtraFieldsRaw validates rawRequest and rawResponse on BifrostResponseExtraFields
func ValidateExtraFieldsRaw(extraFields schemas.BifrostResponseExtraFields) []error {
	var errs []error
	if err := ValidateRawField(extraFields.RawRequest, "RawRequest"); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateRawField(extraFields.RawResponse, "RawResponse"); err != nil {
		errs = append(errs, err)
	}
	return errs
}
