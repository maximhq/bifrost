package antigravity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxSchemaDepth    = 24
	maxSchemaNodes    = 1024
	maxSchemaRefDepth = 16
)

// allowedSchemaTypes is the JSON Schema type subset Cloud Code Assist accepts in a
// function declaration's parameters.
var allowedSchemaTypes = map[string]bool{
	"string": true, "integer": true, "number": true, "boolean": true, "array": true, "object": true,
}

// allowedSchemaFormats lists, per type, the formats Gemini's Schema accepts.
var allowedSchemaFormats = map[string]map[string]bool{
	"string":  {"enum": true, "date-time": true},
	"integer": {"int32": true, "int64": true},
	"number":  {"float": true, "double": true},
}

// emptyObjectSchema is the parameters value for a tool that takes no arguments, or whose
// schema could not be used.
func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// schemaSanitizer rewrites a JSON Schema into the subset Gemini's function declarations
// accept: type, nullable, description, format, enum, properties, items, required and
// anyOf, with $ref inlined.
type schemaSanitizer struct {
	defs  map[string]any
	nodes int
}

// sanitizeToolParameters returns a parameters schema safe to send upstream. The root is
// always an object with properties.
func sanitizeToolParameters(raw any) map[string]any {
	root, ok := raw.(map[string]any)
	if !ok {
		return emptyObjectSchema()
	}
	s := &schemaSanitizer{defs: map[string]any{}}
	for _, key := range []string{"definitions", "$defs"} {
		if defs, ok := root[key].(map[string]any); ok {
			for name, def := range defs {
				s.defs["#/"+key+"/"+name] = def
			}
		}
	}
	out := s.node(root, 0, 0)
	if out == nil || out["type"] != "object" {
		return emptyObjectSchema()
	}
	delete(out, "nullable")
	if _, ok := out["properties"].(map[string]any); !ok {
		out["properties"] = map[string]any{}
	}
	return out
}

// node sanitizes one schema node. It returns nil for a node that cannot be expressed;
// callers substitute {} or a default.
func (s *schemaSanitizer) node(v any, depth, refDepth int) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		if b, isBool := v.(bool); isBool && b {
			return map[string]any{}
		}
		return nil
	}
	s.nodes++
	if s.nodes > maxSchemaNodes || depth > maxSchemaDepth {
		return map[string]any{}
	}

	if ref, ok := m["$ref"].(string); ok {
		def, found := s.defs[ref]
		if !found || refDepth >= maxSchemaRefDepth {
			return map[string]any{}
		}
		out := s.node(def, depth, refDepth+1)
		if out == nil {
			return map[string]any{}
		}
		if desc, ok := m["description"].(string); ok && desc != "" {
			out["description"] = desc
		}
		return out
	}

	nullable := m["nullable"] == true

	// allOf: merge the branches into one node; a single-branch allOf is the common
	// wrapper generators emit around a $ref.
	if branches, ok := m["allOf"].([]any); ok && len(branches) > 0 {
		merged := make(map[string]any, len(m))
		for k, val := range m {
			if k != "allOf" {
				merged[k] = val
			}
		}
		for _, branch := range branches {
			b := s.resolveRefs(branch, refDepth)
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			mergeSchemaInto(merged, bm)
		}
		return s.node(merged, depth, refDepth)
	}

	for _, unionKey := range []string{"anyOf", "oneOf"} {
		branches, ok := m[unionKey].([]any)
		if !ok || len(branches) == 0 {
			continue
		}
		return s.union(m, branches, nullable, depth, refDepth)
	}

	typ, typeNullable, typeUnion := schemaType(m)
	nullable = nullable || typeNullable
	if len(typeUnion) > 1 {
		branches := make([]any, 0, len(typeUnion))
		for _, t := range typeUnion {
			branch := make(map[string]any, len(m))
			for k, val := range m {
				branch[k] = val
			}
			branch["type"] = t
			branches = append(branches, branch)
		}
		return s.union(m, branches, nullable, depth, refDepth)
	}
	if typ == "" {
		switch {
		case m["properties"] != nil:
			typ = "object"
		case m["items"] != nil:
			typ = "array"
		case m["enum"] != nil || m["const"] != nil:
			typ = "string"
		}
	}
	if typ != "" && !allowedSchemaTypes[typ] {
		return nil
	}

	out := map[string]any{}
	if typ != "" {
		out["type"] = typ
	}
	if nullable {
		out["nullable"] = true
	}
	if desc, ok := m["description"].(string); ok && desc != "" {
		out["description"] = desc
	}
	if format, ok := m["format"].(string); ok && allowedSchemaFormats[typ][format] {
		out["format"] = format
	}

	if typ == "string" || typ == "" {
		var values []any
		if enum, ok := m["enum"].([]any); ok {
			for _, e := range enum {
				if str, ok := e.(string); ok {
					values = append(values, str)
				}
			}
		} else if c, ok := m["const"].(string); ok {
			values = append(values, c)
		}
		if len(values) > 0 {
			out["enum"] = values
			out["type"] = "string"
		}
	}

	switch typ {
	case "object":
		props := map[string]any{}
		if rawProps, ok := m["properties"].(map[string]any); ok {
			for name, prop := range rawProps {
				sanitized := s.node(prop, depth+1, refDepth)
				if sanitized == nil {
					sanitized = map[string]any{}
				}
				props[name] = sanitized
			}
		}
		if len(props) > 0 {
			out["properties"] = props
		}
		if rawRequired, ok := m["required"].([]any); ok {
			var required []any
			seen := map[string]bool{}
			for _, r := range rawRequired {
				name, ok := r.(string)
				if !ok || seen[name] {
					continue
				}
				if _, exists := props[name]; exists {
					required = append(required, name)
					seen[name] = true
				}
			}
			if len(required) > 0 {
				out["required"] = required
			}
		}
	case "array":
		items := m["items"]
		if tuple, ok := items.([]any); ok {
			items = nil
			if len(tuple) > 0 {
				items = tuple[0]
			}
		}
		sanitized := s.node(items, depth+1, refDepth)
		if len(sanitized) == 0 {
			sanitized = map[string]any{"type": "string"}
		}
		out["items"] = sanitized
	}
	return out
}

// union normalises anyOf/oneOf: null branches become nullable, a single remaining
// branch is inlined, enum-only branches of one type merge, anything else stays anyOf.
func (s *schemaSanitizer) union(parent map[string]any, branches []any, nullable bool, depth, refDepth int) map[string]any {
	var kept []map[string]any
	for _, branch := range branches {
		resolved := s.resolveRefs(branch, refDepth)
		if bm, ok := resolved.(map[string]any); ok && bm["type"] == "null" {
			nullable = true
			continue
		}
		sanitized := s.node(resolved, depth+1, refDepth)
		if sanitized == nil {
			continue
		}
		if sanitized["nullable"] == true {
			nullable = true
			delete(sanitized, "nullable")
		}
		kept = append(kept, sanitized)
	}

	var out map[string]any
	switch {
	case len(kept) == 0:
		out = map[string]any{}
	case len(kept) == 1:
		out = kept[0]
	default:
		if merged := mergeEnumBranches(kept); merged != nil {
			out = merged
		} else {
			anyOf := make([]any, len(kept))
			for i, k := range kept {
				anyOf[i] = k
			}
			out = map[string]any{"anyOf": anyOf}
		}
	}
	if nullable {
		out["nullable"] = true
	}
	if desc, ok := parent["description"].(string); ok && desc != "" {
		if _, has := out["description"]; !has {
			out["description"] = desc
		}
	}
	return out
}

// resolveRefs follows a top-level $ref chain without sanitizing, so union and allOf can
// inspect the target's shape.
func (s *schemaSanitizer) resolveRefs(v any, refDepth int) any {
	for i := refDepth; i < maxSchemaRefDepth; i++ {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return v
		}
		def, found := s.defs[ref]
		if !found {
			return map[string]any{}
		}
		v = def
	}
	return v
}

// mergeEnumBranches merges branches that are all string enums into one enum, or returns
// nil when the union is not of that shape.
func mergeEnumBranches(branches []map[string]any) map[string]any {
	var values []any
	seen := map[string]bool{}
	for _, b := range branches {
		if b["type"] != "string" {
			return nil
		}
		enum, ok := b["enum"].([]any)
		if !ok {
			return nil
		}
		for _, e := range enum {
			str := e.(string)
			if !seen[str] {
				seen[str] = true
				values = append(values, str)
			}
		}
	}
	return map[string]any{"type": "string", "enum": values}
}

// mergeSchemaInto folds one allOf branch into the accumulated node.
func mergeSchemaInto(dst, src map[string]any) {
	for k, v := range src {
		switch k {
		case "properties":
			props, _ := dst["properties"].(map[string]any)
			if props == nil {
				props = map[string]any{}
			}
			if srcProps, ok := v.(map[string]any); ok {
				for name, prop := range srcProps {
					props[name] = prop
				}
			}
			dst["properties"] = props
		case "required":
			existing, _ := dst["required"].([]any)
			if srcReq, ok := v.([]any); ok {
				existing = append(existing, srcReq...)
			}
			dst["required"] = existing
		default:
			if _, has := dst[k]; !has {
				dst[k] = v
			}
		}
	}
}

// schemaType reads a node's type: a single type, whether null was part of a type array,
// and the non-null types of a multi-type array.
func schemaType(m map[string]any) (string, bool, []string) {
	switch t := m["type"].(type) {
	case string:
		lower := strings.ToLower(t)
		if lower == "null" {
			return "", true, nil
		}
		return lower, false, nil
	case []any:
		nullable := false
		var types []string
		for _, entry := range t {
			str, ok := entry.(string)
			if !ok {
				continue
			}
			lower := strings.ToLower(str)
			if lower == "null" {
				nullable = true
				continue
			}
			types = append(types, lower)
		}
		switch len(types) {
		case 0:
			return "", nullable, nil
		case 1:
			return types[0], nullable, nil
		default:
			return "", nullable, types
		}
	}
	return "", false, nil
}

// decodeSchema parses a JSON schema keeping numbers exact.
func decodeSchema(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := unmarshalUseNumber(raw, &v); err != nil {
		return nil
	}
	return v
}

var validToolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
var invalidToolNameChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// toolNameCodec rewrites tool names Cloud Code Assist rejects into valid ones for the
// duration of one request, and maps them back on the response.
type toolNameCodec struct {
	encoded map[string]string // original -> wire
	decoded map[string]string // wire -> original
}

func newToolNameCodec() *toolNameCodec {
	return &toolNameCodec{encoded: map[string]string{}, decoded: map[string]string{}}
}

// encode returns the wire name for an original tool name.
func (c *toolNameCodec) encode(name string) string {
	if wire, ok := c.encoded[name]; ok {
		return wire
	}
	wire := name
	if !validToolName.MatchString(name) {
		wire = invalidToolNameChars.ReplaceAllString(name, "_")
		if wire == "" || !(wire[0] == '_' || (wire[0] >= 'A' && wire[0] <= 'Z') || (wire[0] >= 'a' && wire[0] <= 'z')) {
			wire = "_" + wire
		}
		if len(wire) > 64 {
			wire = wire[:55] + "_" + shortHash(name)
		}
	}
	base := wire
	if len(base) > 55 {
		base = base[:55]
	}
	for salt := 1; ; salt++ {
		owner, taken := c.decoded[wire]
		if !taken || owner == name {
			break
		}
		wire = base + "_" + shortHash(name+"#"+strconv.Itoa(salt))
	}
	c.encoded[name] = wire
	c.decoded[wire] = name
	return wire
}

// decode maps a wire name back to the caller's tool name.
func (c *toolNameCodec) decode(wire string) string {
	if c == nil {
		return wire
	}
	if name, ok := c.decoded[wire]; ok {
		return name
	}
	return wire
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}
