package logstore

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
)

// marshalOverheadBreakdown encodes buckets as {"<name>":<duration_us>,...} in the order
// given. Hand-rolled because a Go map would sort the keys and lose that order.
func marshalOverheadBreakdown(buckets []OverheadBucket) (string, error) {
	if len(buckets) == 0 {
		return "", nil
	}
	// ~24 bytes covers the common "<name>":<decimal us> member plus its comma.
	buf := make([]byte, 0, 2+len(buckets)*24)
	buf = append(buf, '{')
	for _, b := range buckets {
		// JSON has no NaN/±Inf literal; a bare one would void the whole column.
		if math.IsNaN(b.DurationUs) || math.IsInf(b.DurationUs, 0) {
			continue
		}
		if len(buf) > 1 {
			buf = append(buf, ',')
		}
		if overheadNameNeedsEscape(b.Name) {
			name, err := sonic.Marshal(b.Name)
			if err != nil {
				return "", err
			}
			buf = append(buf, name...)
		} else {
			buf = append(buf, '"')
			buf = append(buf, b.Name...)
			buf = append(buf, '"')
		}
		buf = append(buf, ':')
		// 'f'/-1 is shortest-round-trip and never emits an exponent.
		buf = strconv.AppendFloat(buf, b.DurationUs, 'f', -1, 64)
	}
	if len(buf) == 1 {
		return "", nil // all buckets dropped; store nothing, not "{}"
	}
	buf = append(buf, '}')
	return string(buf), nil
}

// overheadNameNeedsEscape reports whether a name needs a real JSON string encoder.
// Bifrost's own names never do; this guards an out-of-tree plugin's.
func overheadNameNeedsEscape(name string) bool {
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c == '"' || c == '\\' || c >= 0x7f {
			return true
		}
	}
	return false
}

// unmarshalOverheadBreakdown decodes the name-keyed object, or the array of objects
// older rows still carry. Returns nil on malformed input, like the other parsed fields.
// Kind stays unset: it is not persisted, and MetricComponent works from Name.
func unmarshalOverheadBreakdown(raw string) []OverheadBucket {
	switch overheadBreakdownForm(raw) {
	case '{':
		return decodeOverheadObject(raw)
	case '[':
		var out []OverheadBucket
		if err := sonic.Unmarshal([]byte(raw), &out); err != nil {
			return nil
		}
		return out
	}
	return nil
}

// decodeOverheadObject reads the object in document order. Streamed via json.Token, not
// unmarshalled into a map, because Go randomises map order and framework/warp truncates
// by position. Order still cannot survive anything that sorts keys (Postgres jsonb).
func decodeOverheadObject(raw string) []OverheadBucket {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	var out []OverheadBucket
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil
		}
		name, ok := keyTok.(string)
		if !ok {
			return nil
		}
		valTok, err := dec.Token()
		if err != nil {
			return nil
		}
		us, ok := overheadNumber(valTok)
		if !ok {
			continue // non-numeric duration: drop the member, keep the rest
		}
		out = append(out, OverheadBucket{Name: name, DurationUs: us})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// overheadNumber reads a decoded JSON number, whatever numeric type it arrived as.
func overheadNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

// overheadBreakdownForm returns the stored value's first structural byte: '{' for the
// name-keyed object, '[' for the legacy array, 0 for empty or malformed.
func overheadBreakdownForm(raw string) byte {
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; c {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return c
		default:
			return 0
		}
	}
	return 0
}
