package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unsafe"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// bodyOverride is one forced request body field and its compacted JSON value.
type bodyOverride struct {
	path     string
	segments []string
	value    string
}

// compileBodyOverrides builds the per-request list, sorted so the sent bytes never depend on map order.
func compileBodyOverrides(overrides map[string]json.RawMessage) []bodyOverride {
	if len(overrides) == 0 {
		return nil
	}
	out := make([]bodyOverride, 0, len(overrides))
	for path, value := range overrides {
		segments, err := schemas.RelayBodyOverridePath(path)
		if err != nil {
			// Validate ran on the config, so this is a programming error; the field is skipped.
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, value); err != nil {
			continue
		}
		out = append(out, bodyOverride{path: path, segments: segments, value: compact.String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// bodylessMethods never get a created body; many servers ignore or refuse one.
var bodylessMethods = map[string]struct{}{"GET": {}, "HEAD": {}, "DELETE": {}}

// applyBodyOverrides returns the body with every forced field set, and whether it was created from
// nothing. A body it cannot guarantee the fields in (not a JSON object, a forced key sent twice or
// in another case) is refused rather than forwarded. Compressed bodies never get here: the HTTP
// gateway decompresses them before routing.
func applyBodyOverrides(overrides []bodyOverride, method string, body []byte) ([]byte, bool, error) {
	if len(overrides) == 0 {
		return body, false, nil
	}
	created := false
	if len(bytes.TrimSpace(body)) == 0 {
		if _, ok := bodylessMethods[strings.ToUpper(method)]; ok {
			return body, false, nil
		}
		body, created = []byte("{}"), true
	} else if trimmed := bytes.TrimLeft(body, " \t\r\n"); trimmed[0] != '{' || !gjson.ValidBytes(body) {
		return nil, false, errors.New("this route sets fields in the request body, which must be a JSON object")
	}
	for _, o := range overrides {
		current, err := forcedFieldValue(body, o)
		if err != nil {
			return nil, false, err
		}
		if current.Exists() && current.Raw == o.value {
			continue
		}
		updated, err := providerUtils.SetRawJSONField(body, o.path, []byte(o.value))
		if err != nil {
			return nil, false, errors.New("this route could not set " + o.path + " in the request body: " + err.Error())
		}
		// sjson copies, so the caller's buffer is never modified.
		body = updated
	}
	return body, created, nil
}

// forcedFieldValue returns the body's current value at a forced path, checking only the objects on
// that path for duplicate or case-variant keys.
func forcedFieldValue(body []byte, o bodyOverride) (gjson.Result, error) {
	// Read-only walk, so the body is viewed as a string without a copy.
	obj := gjson.Parse(unsafe.String(unsafe.SliceData(body), len(body)))
	for i, segment := range o.segments {
		var match gjson.Result
		exact, folded := 0, 0
		obj.ForEach(func(key, value gjson.Result) bool {
			if strings.EqualFold(key.Str, segment) {
				folded++
				if key.Str == segment {
					exact++
					match = value
				}
			}
			return true
		})
		// Built only on a refusal, so the accepted path allocates nothing.
		name := func() string { return strings.Join(o.segments[:i+1], ".") }
		if folded > 1 {
			return gjson.Result{}, errors.New("the request body names " + name() + " more than once (case-insensitively); send it once so the forced value is the one the upstream reads")
		}
		if folded == 1 && exact == 0 {
			return gjson.Result{}, errors.New("the request body names " + name() + " with different letter case; use the exact key so the forced value is the one the upstream reads")
		}
		if exact == 0 || i == len(o.segments)-1 {
			return match, nil
		}
		if !match.IsObject() {
			return gjson.Result{}, errors.New("the request body holds a non-object at " + name() + ", where this route sets " + o.path)
		}
		obj = match
	}
	return gjson.Result{}, nil
}

// withJSONContentType returns a copy of headers with Content-Type set to JSON, for a created body.
func withJSONContentType(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		if !strings.EqualFold(name, "content-type") {
			out[name] = value
		}
	}
	out["content-type"] = "application/json"
	return out
}
