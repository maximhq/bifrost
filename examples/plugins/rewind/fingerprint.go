package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	reTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?`)
	reUUID      = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reCallID    = regexp.MustCompile(`\b(call|toolu|chatcmpl)_[A-Za-z0-9]+\b`)
	reLongNum   = regexp.MustCompile(`\b\d{10,}\b`)
	dropKeys    = map[string]bool{"stream": true, "user": true, "metadata": true, "request_id": true, "stream_options": true}
)

// Fingerprint hashes a request after removing values that change between
// otherwise identical runs: timestamps, UUIDs, tool-call ids and long numbers.
// It returns the hash and the normalised value it was computed from.
func Fingerprint(path string, body []byte) (string, any) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		v = normString(string(body))
	} else {
		v = normalize(v)
	}
	canon, _ := json.Marshal(map[string]any{"path": path, "body": v})
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), v
}

func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if dropKeys[k] {
				continue
			}
			if k == "id" || k == "tool_call_id" {
				out[k] = "<id>"
				continue
			}
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalize(t[i])
		}
		return out
	case string:
		return normString(t)
	default:
		return v
	}
}

func normString(s string) string {
	s = reTimestamp.ReplaceAllString(s, "<ts>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reCallID.ReplaceAllString(s, "<call>")
	s = reLongNum.ReplaceAllString(s, "<n>")
	return s
}

// DiffJSON lists the fields that differ between two normalised requests.
func DiffJSON(expected, got any) []FieldDiff {
	var out []FieldDiff
	diffWalk("", expected, got, &out)
	if len(out) > 25 {
		out = out[:25]
	}
	return out
}

func diffWalk(path string, a, b any, out *[]FieldDiff) {
	if len(*out) > 25 {
		return
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			diffWalk(join(path, k), am[k], bm[k], out)
		}
		return
	}
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok && bok {
		n := len(as)
		if len(bs) > n {
			n = len(bs)
		}
		for i := 0; i < n; i++ {
			var x, y any
			if i < len(as) {
				x = as[i]
			}
			if i < len(bs) {
				y = bs[i]
			}
			diffWalk(fmt.Sprintf("%s[%d]", path, i), x, y, out)
		}
		return
	}
	ea, _ := json.Marshal(a)
	eb, _ := json.Marshal(b)
	if string(ea) != string(eb) {
		*out = append(*out, FieldDiff{Path: strings.TrimPrefix(path, "."), Expected: clip(string(ea)), Got: clip(string(eb))})
	}
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func clip(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// rawJSON keeps valid JSON as is and wraps anything else as a JSON string.
func rawJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(b) {
		c := make([]byte, len(b))
		copy(c, b)
		return c
	}
	s, _ := json.Marshal(string(b))
	return s
}

func modelOf(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

func setModel(body []byte, model string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["model"] = model
	return json.Marshal(m)
}

// poisonSearch appends text to the first result of a recorded search response.
func poisonSearch(resp json.RawMessage, text string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(resp, &m); err != nil {
		return nil, err
	}
	results, _ := m["results"].([]any)
	if len(results) == 0 {
		return nil, fmt.Errorf("no results to poison")
	}
	first, _ := results[0].(map[string]any)
	if first == nil {
		return nil, fmt.Errorf("unexpected result shape")
	}
	content, _ := first["content"].(string)
	first["content"] = content + "\n\n" + text
	return json.Marshal(m)
}
