package schemas

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration value that accepts both human-readable duration
// strings and plain integer nanosecond values on JSON unmarshal, providing
// backward compatibility with the default Go JSON encoding of time.Duration.
//
// Accepted unmarshal formats:
//   - String: "5s", "500ms", "1m30s", "2h", "0s" — parsed via time.ParseDuration
//   - Integer: nanosecond count, identical to the default Go JSON encoding of
//     time.Duration (e.g. 5000000000 = 5s)
//
// MarshalJSON is intentionally NOT overridden. The type marshals as its
// underlying int64 nanosecond value (identical to time.Duration), preserving the
// existing JSON API contract for all consumers that read these fields as integers.
type Duration time.Duration

// D returns the underlying time.Duration value.
func (d Duration) D() time.Duration {
	return time.Duration(d)
}

// String returns a human-readable representation of the duration.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// UnmarshalJSON implements json.Unmarshaler. Accepts both a Go duration string
// (e.g. "5s", "500ms") and an integer nanosecond value for backward
// compatibility. MarshalJSON is NOT overridden — values encode as int64
// nanoseconds, identical to time.Duration.
func (d *Duration) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	dur, err := ParseFlexibleDuration(data, "duration")
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}

// ParseFlexibleDuration parses a JSON token (raw bytes) as a time.Duration.
//
// Supported formats:
//   - JSON string "5s", "500ms", "1m30s" — forwarded to time.ParseDuration
//   - JSON integer (e.g. 5000000000) — treated as nanoseconds, identical to the
//     default Go JSON encoding of time.Duration
//
// fieldName is used only in error messages (e.g. "context_timeout").
func ParseFlexibleDuration(data json.RawMessage, fieldName string) (time.Duration, error) {
	if len(data) == 0 || string(data) == "null" {
		return 0, nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return 0, err
		}
		dur, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid %s %q: use a Go duration string like \"5s\", \"500ms\", \"1m30s\"", fieldName, s)
		}
		return dur, nil
	}
	// Numeric: nanoseconds — backward compatible with the default time.Duration JSON encoding.
	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return 0, fmt.Errorf("invalid %s: expected a duration string (e.g. \"5s\") or integer nanoseconds: %w", fieldName, err)
	}
	return time.Duration(n), nil
}
