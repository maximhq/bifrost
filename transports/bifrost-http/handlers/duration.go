package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// requestDurationError marks safe duration-validation messages for HTTP responses.
type requestDurationError string

func (e requestDurationError) Error() string { return string(e) }

func requestDurationDecodeError(err error) string {
	var durationErr requestDurationError
	if errors.As(err, &durationErr) {
		return durationErr.Error()
	}
	return "Invalid request payload"
}

// parseRequestDuration accepts Go duration strings or integers in numericUnit.
// Callers supply a positive unit and its plural label, preserving each field's
// legacy numeric contract. Blanks, negative values and overflow are rejected;
// precision and zero semantics belong to the field's validation and resolver.
func parseRequestDuration(data []byte, field string, numericUnit time.Duration, unitLabel string) (time.Duration, error) {
	if numericUnit <= 0 {
		return 0, fmt.Errorf("invalid numeric duration unit")
	}
	raw := strings.TrimSpace(string(data))
	invalidType := requestDurationError(fmt.Sprintf("%s must be a duration string or a number of %s", field, unitLabel))
	if raw == "" {
		return 0, invalidType
	}
	if raw == "null" {
		return 0, nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return 0, invalidType
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return 0, requestDurationError(fmt.Sprintf("%s must not be blank; use an explicit zero duration to reset it", field))
		}
		duration, err := time.ParseDuration(value)
		if err != nil {
			return 0, requestDurationError(fmt.Sprintf(`%s: expected a duration such as "30s" or "5m"`, field))
		}
		if duration < 0 {
			return 0, requestDurationError(fmt.Sprintf("%s must be a non-negative duration", field))
		}
		return duration, nil
	}
	if (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')) && strings.ContainsAny(raw, ".eE") {
		return 0, requestDurationError(fmt.Sprintf("%s: a bare number must be a whole number of %s, or use a duration string", field, unitLabel))
	}
	var number int64
	if err := json.Unmarshal([]byte(raw), &number); err != nil {
		return 0, invalidType
	}
	if number < 0 {
		return 0, requestDurationError(fmt.Sprintf("%s must be a non-negative duration", field))
	}
	maxNumber := int64(math.MaxInt64) / int64(numericUnit)
	if number > maxNumber {
		return 0, requestDurationError(fmt.Sprintf("%s must be at most %d %s", field, maxNumber, unitLabel))
	}
	return time.Duration(number) * numericUnit, nil
}
