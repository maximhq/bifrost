package openapimcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// schemaKeyAllowlist is the JSON Schema vocabulary passed through to tools.
// Everything else (xml, externalDocs, discriminator, examples, readOnly, vendor
// extensions, ...) is dropped: several model providers reject unknown keywords
// in tool parameter schemas.
var schemaKeyAllowlist = map[string]bool{
	"type": true, "description": true, "properties": true, "required": true,
	"additionalProperties": true, "enum": true, "items": true, "minItems": true,
	"maxItems": true, "uniqueItems": true, "anyOf": true, "oneOf": true, "allOf": true,
	"format": true, "pattern": true, "minLength": true, "maxLength": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"multipleOf": true, "title": true, "default": true, "nullable": true, "const": true,
}

// bodyContentPreference orders the request content types the executor can
// encode; the first one present in a requestBody wins.
var bodyContentPreference = []string{ContentTypeJSON, ContentTypeForm, ContentTypeText}

// unsupportedBodyPrefixes name request bodies that cannot be encoded from JSON
// tool arguments.
var unsupportedBodyPrefixes = []string{"multipart/", "application/octet-stream", "image/", "audio/", "video/"}

// streamingBodyTypes are 3.2 sequential media types.
var streamingBodyTypes = map[string]bool{"text/event-stream": true, "application/jsonl": true, "application/json-seq": true, "multipart/mixed": true}

// SelectBodyContent picks the media type of a requestBody.content map that the
// executor can produce, returning its (dereferenced) schema. ok is false, with a
// reason, when the body can only be sent in a form this package does not
// support (multipart, binary, streaming).
func SelectBodyContent(content map[string]any) (contentType string, schema map[string]any, ok bool, reason string) {
	if len(content) == 0 {
		return "", nil, false, "request body declares no content"
	}
	normalized := make(map[string]map[string]any, len(content))
	var declared []string
	for mt, raw := range content {
		key := strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0]))
		normalized[key] = asMap(raw)
		declared = append(declared, key)
	}
	sort.Strings(declared)
	pick := func(key string) (string, map[string]any, bool, string) {
		media := normalized[key]
		if _, streaming := media["itemSchema"]; streaming {
			return "", nil, false, fmt.Sprintf("streaming request body (%s with itemSchema) is not supported", key)
		}
		schema := asMap(media["schema"])
		if schema == nil {
			if key == ContentTypeText {
				schema = map[string]any{"type": "string"}
			} else {
				schema = map[string]any{"type": "object"}
			}
		}
		return key, schema, true, ""
	}
	for _, pref := range bodyContentPreference {
		if _, found := normalized[pref]; found {
			return pick(pref)
		}
	}
	for _, key := range declared {
		if key == "*/*" || strings.HasSuffix(key, "+json") || (strings.HasPrefix(key, "application/") && strings.Contains(key, "json")) {
			ct, schema, ok, reason := pick(key)
			if ok {
				ct = ContentTypeJSON
			}
			return ct, schema, ok, reason
		}
	}
	for _, key := range declared {
		if streamingBodyTypes[key] {
			return "", nil, false, fmt.Sprintf("streaming request body (%s) is not supported", key)
		}
		for _, prefix := range unsupportedBodyPrefixes {
			if strings.HasPrefix(key, prefix) {
				return "", nil, false, fmt.Sprintf("%s request body is not supported (multipart/binary uploads cannot be expressed as tool arguments)", key)
			}
		}
	}
	return "", nil, false, fmt.Sprintf("request body content type %v is not supported (JSON, form-urlencoded or text/plain only)", declared)
}

// NormalizeSchema returns a copy of s in the 3.0-style dialect tool consumers
// expect: 3.1 null-type arrays become nullable, multi-type arrays become anyOf,
// allOf-of-objects is merged, boolean exclusive bounds are dropped, readOnly
// properties are removed, and only allow-listed keywords survive.
func NormalizeSchema(s map[string]any) map[string]any {
	if s == nil {
		return map[string]any{"type": "object"}
	}
	out, _ := normalizeNode(s, 0).(map[string]any)
	if out == nil {
		return map[string]any{"type": "object"}
	}
	return out
}

func normalizeNode(v any, depth int) any {
	m, ok := v.(map[string]any)
	if !ok || depth > maxRefDepth*2 {
		return v
	}
	out := make(map[string]any, len(m))

	// type: string | [..] handling (3.1); Swagger 2 spelled nullable as x-nullable.
	nullable := asBool(m["nullable"]) || asBool(m["x-nullable"])
	switch t := m["type"].(type) {
	case string:
		if t == "null" {
			nullable = true
		} else {
			out["type"] = t
		}
	case []any:
		var types []string
		for _, item := range t {
			s := asString(item)
			if s == "null" {
				nullable = true
				continue
			}
			if s != "" {
				types = append(types, s)
			}
		}
		switch len(types) {
		case 0:
		case 1:
			out["type"] = types[0]
		default:
			alts := make([]any, 0, len(types))
			for _, ty := range types {
				alts = append(alts, map[string]any{"type": ty})
			}
			out["anyOf"] = alts
		}
	}
	if nullable {
		out["nullable"] = true
	}

	for k, val := range m {
		if k == "type" || k == "nullable" || !schemaKeyAllowlist[k] {
			continue
		}
		switch k {
		case "exclusiveMinimum", "exclusiveMaximum":
			if _, isBool := val.(bool); isBool {
				continue
			}
			out[k] = val
		case "properties":
			props := asMap(val)
			normalized := make(map[string]any, len(props))
			for name, prop := range props {
				if pm := asMap(prop); pm != nil && asBool(pm["readOnly"]) {
					continue
				}
				normalized[name] = normalizeNode(prop, depth+1)
			}
			out[k] = normalized
		case "items", "additionalProperties":
			if _, isMap := val.(map[string]any); isMap {
				out[k] = normalizeNode(val, depth+1)
			} else {
				out[k] = val
			}
		case "anyOf", "oneOf", "allOf":
			list := asSlice(val)
			normalized := make([]any, 0, len(list))
			for _, item := range list {
				normalized = append(normalized, normalizeNode(item, depth+1))
			}
			if k == "allOf" {
				if merged, ok := mergeAllOf(normalized); ok {
					for mk, mv := range merged {
						if _, exists := out[mk]; !exists || mk == "properties" || mk == "required" {
							out[mk] = mv
						}
					}
					continue
				}
			}
			out[k] = normalized
		case "required":
			out[k] = val
		default:
			out[k] = val
		}
	}

	if props, ok := out["properties"].(map[string]any); ok {
		if _, hasType := out["type"]; !hasType {
			if _, hasAnyOf := out["anyOf"]; !hasAnyOf {
				out["type"] = "object"
			}
		}
		// Drop required entries whose property no longer exists (e.g. readOnly).
		if req := asStringSlice(out["required"]); req != nil {
			kept := make([]any, 0, len(req))
			for _, name := range req {
				if _, exists := props[name]; exists {
					kept = append(kept, name)
				}
			}
			if len(kept) > 0 {
				out["required"] = kept
			} else {
				delete(out, "required")
			}
		}
	}
	return out
}

// mergeAllOf folds allOf members that are all object schemas into one object:
// properties are unioned (later members win) and required lists are unioned.
func mergeAllOf(members []any) (map[string]any, bool) {
	if len(members) == 0 {
		return nil, false
	}
	props := map[string]any{}
	requiredSet := map[string]bool{}
	var description string
	for _, member := range members {
		m := asMap(member)
		if m == nil || !isObjectSchema(m) {
			return nil, false
		}
		for name, prop := range asMap(m["properties"]) {
			props[name] = prop
		}
		for _, name := range asStringSlice(m["required"]) {
			requiredSet[name] = true
		}
		if description == "" {
			description = asString(m["description"])
		}
	}
	merged := map[string]any{"type": "object", "properties": props}
	if len(requiredSet) > 0 {
		req := make([]string, 0, len(requiredSet))
		for name := range requiredSet {
			req = append(req, name)
		}
		sort.Strings(req)
		merged["required"] = toAnySlice(req)
	}
	if description != "" {
		merged["description"] = description
	}
	return merged, true
}

// isObjectSchema reports whether a normalized schema describes an object with
// (potentially) named properties.
func isObjectSchema(s map[string]any) bool {
	if s == nil {
		return false
	}
	if t, _ := s["type"].(string); t == "object" {
		return true
	}
	if _, has := s["properties"]; has {
		return true
	}
	return false
}

// FlattenInputSchema builds a tool's single input schema from an operation:
// path/query/header/cookie parameters and the request body's properties are all
// top-level properties. The body keeps plain names; a parameter whose name
// collides with a body property or another parameter is suffixed "__<in>".
// Non-object bodies (arrays, primitives, oneOf/anyOf, text) travel as one
// "body" property.
func FlattenInputSchema(op Operation) (json.RawMessage, []ArgBinding, *BodyBinding, []string, error) {
	props := map[string]any{}
	requiredSet := map[string]bool{}
	var warnings []string
	var body *BodyBinding

	if op.Body != nil {
		switch op.Body.ContentType {
		case ContentTypeText:
			props["body"] = map[string]any{"type": "string", "description": bodyDescription(op.Body, "Raw request body (text/plain)")}
			body = &BodyBinding{Mode: "single", SingleArg: "body", ContentType: op.Body.ContentType}
			if op.Body.Required {
				requiredSet["body"] = true
			}
		default:
			schema := NormalizeSchema(op.Body.Schema)
			bodyProps := asMap(schema["properties"])
			if isObjectSchema(schema) && len(bodyProps) > 0 && !hasFreeFormOnly(schema) {
				names := sortedKeys(bodyProps)
				for _, name := range names {
					props[name] = bodyProps[name]
				}
				if op.Body.Required {
					for _, name := range asStringSlice(schema["required"]) {
						if _, exists := bodyProps[name]; exists {
							requiredSet[name] = true
						}
					}
				}
				body = &BodyBinding{Mode: "flattened", ArgNames: names, ContentType: op.Body.ContentType}
			} else {
				if desc := bodyDescription(op.Body, "Request body"); desc != "" {
					if _, has := schema["description"]; !has {
						schema["description"] = desc
					}
				}
				props["body"] = schema
				body = &BodyBinding{Mode: "single", SingleArg: "body", ContentType: op.Body.ContentType}
				if op.Body.Required {
					requiredSet["body"] = true
				}
			}
		}
	}

	// Parameters sharing a name across locations are all suffixed, so neither
	// side silently wins.
	nameCount := map[string]int{}
	for _, p := range op.Params {
		nameCount[p.Name]++
	}
	var args []ArgBinding
	for _, p := range op.Params {
		switch p.In {
		case InPath, InQuery, InHeader, InCookie:
		case InQuerystring:
			qsArgs, qsWarnings := flattenQuerystring(p, props, requiredSet)
			args = append(args, qsArgs...)
			warnings = append(warnings, qsWarnings...)
			continue
		default:
			warnings = append(warnings, fmt.Sprintf("parameter %q has unsupported location %q and was skipped", p.Name, p.In))
			continue
		}
		argName := p.Name
		if _, taken := props[argName]; taken || nameCount[p.Name] > 1 {
			argName = p.Name + "__" + p.In
		}
		schema := NormalizeSchema(p.Schema)
		schema["description"] = joinDescription(p.Description, fmt.Sprintf("(%s parameter)", p.In))
		props[argName] = schema
		if p.Required {
			requiredSet[argName] = true
		}
		args = append(args, ArgBinding{
			ArgName:  argName,
			Location: p.In,
			Name:     p.Name,
			Required: p.Required,
			Explode:  explodeFor(p),
			Style:    p.Style,
		})
	}

	root := map[string]any{"type": "object", "properties": props}
	if len(requiredSet) > 0 {
		required := make([]string, 0, len(requiredSet))
		for name := range requiredSet {
			required = append(required, name)
		}
		sort.Strings(required)
		root["required"] = toAnySlice(required)
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("encoding input schema: %w", err)
	}
	return raw, args, body, warnings, nil
}

// flattenQuerystring handles a 3.2 `in: querystring` parameter: an object schema
// contributes one query argument per property (suffixed on collision); any other
// schema becomes a single argument sent verbatim as the query string.
func flattenQuerystring(p Parameter, props map[string]any, requiredSet map[string]bool) ([]ArgBinding, []string) {
	schema := NormalizeSchema(p.Schema)
	var args []ArgBinding
	var warnings []string
	if isObjectSchema(schema) && len(asMap(schema["properties"])) > 0 {
		qsProps := asMap(schema["properties"])
		required := map[string]bool{}
		for _, name := range asStringSlice(schema["required"]) {
			required[name] = true
		}
		for _, name := range sortedKeys(qsProps) {
			argName := name
			if _, taken := props[argName]; taken {
				argName = name + "__querystring"
			}
			prop, _ := normalizeNode(qsProps[name], 0).(map[string]any)
			if prop == nil {
				prop = map[string]any{"type": "string"}
			}
			prop["description"] = joinDescription(asString(prop["description"]), "(query parameter)")
			props[argName] = prop
			if required[name] && p.Required {
				requiredSet[argName] = true
			}
			args = append(args, ArgBinding{ArgName: argName, Location: InQuery, Name: name, Required: required[name] && p.Required, Explode: true})
		}
		return args, warnings
	}
	argName := p.Name
	if argName == "" {
		argName = "querystring"
	}
	if _, taken := props[argName]; taken {
		argName += "__querystring"
	}
	props[argName] = map[string]any{"type": "string", "description": joinDescription(p.Description, "(raw query string, sent verbatim)")}
	if p.Required {
		requiredSet[argName] = true
	}
	args = append(args, ArgBinding{ArgName: argName, Location: InQuerystring, Name: p.Name, Required: p.Required})
	return args, warnings
}

// hasFreeFormOnly reports an object schema with additionalProperties but whose
// named properties are all absent, i.e. a dictionary body.
func hasFreeFormOnly(schema map[string]any) bool {
	return len(asMap(schema["properties"])) == 0 && schema["additionalProperties"] != nil
}

func explodeFor(p Parameter) bool {
	if p.Explode != nil {
		return *p.Explode
	}
	// OpenAPI defaults: form style (query/cookie) explodes; simple style (path/header) does not.
	switch p.Style {
	case "spaceDelimited", "pipeDelimited", "tabDelimited", "simple":
		return false
	}
	return p.In == InQuery || p.In == InCookie
}

func bodyDescription(body *RequestBody, fallback string) string {
	if body != nil && body.Description != "" {
		return body.Description
	}
	return fallback
}

func joinDescription(desc, suffix string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return suffix
	}
	return desc + " " + suffix
}
