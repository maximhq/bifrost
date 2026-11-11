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
		// OpenAPI 3.1 allows boolean schemas: true accepts any value, false none.
		switch media["schema"] {
		case true:
			return key, map[string]any{}, true, ""
		case false:
			return "", nil, false, fmt.Sprintf("request body %s declares schema: false, which accepts no value", key)
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
	// Candidates in preference order; one that cannot be sent (a streaming
	// itemSchema, schema false) falls through to the next, and its reason is
	// reported only when none of them can.
	var firstReason string
	for _, pref := range bodyContentPreference {
		if _, found := normalized[pref]; found {
			ct, schema, ok, reason := pick(pref)
			if ok {
				return ct, schema, ok, ""
			}
			if firstReason == "" {
				firstReason = reason
			}
		}
	}
	for _, key := range declared {
		if key == "*/*" || isJSONMediaType(key) {
			// The body is encoded as JSON and sent under the declared type, so an
			// upstream that requires application/vnd.api+json gets it. A wildcard
			// is not a sendable type, so it goes out as application/json.
			ct, schema, ok, reason := pick(key)
			if ok {
				if key == "*/*" {
					ct = ContentTypeJSON
				}
				return ct, schema, ok, ""
			}
			if firstReason == "" {
				firstReason = reason
			}
		}
	}
	if firstReason != "" {
		return "", nil, false, firstReason
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

// isJSONMediaType reports whether a media type carries a JSON document
// (application/json, any +json suffix, or another application/*json type).
// Sequential types such as application/jsonl and application/json-seq are not
// one JSON document, so they are excluded.
func isJSONMediaType(mt string) bool {
	mt = strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0]))
	if streamingBodyTypes[mt] {
		return false
	}
	return mt == ContentTypeJSON || strings.HasSuffix(mt, "+json") || (strings.HasPrefix(mt, "application/") && strings.Contains(mt, "json"))
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

	var allOfMerged map[string]any
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
					allOfMerged = merged
					continue
				}
			}
			out[k] = normalized
		case "required":
			// Only a list of property names is valid; `required: true` on a
			// property schema is a common mistake that strict providers reject.
			if _, isList := val.([]any); isList {
				out[k] = val
			}
		default:
			out[k] = val
		}
	}

	// The allOf members are folded in after the loop, once the schema's own
	// keywords are all in place: properties and required are unioned (the
	// schema's own property definition wins a name clash) and any other merged
	// keyword fills in only where the schema has none. Merging inside the loop
	// made the result depend on map iteration order.
	if allOfMerged != nil {
		for mk, mv := range allOfMerged {
			switch mk {
			case "properties":
				props := map[string]any{}
				for name, prop := range asMap(mv) {
					props[name] = prop
				}
				for name, prop := range asMap(out["properties"]) {
					props[name] = prop
				}
				out["properties"] = props
			case "required":
				out["required"] = unionStrings(asStringSlice(out["required"]), asStringSlice(mv))
			default:
				if _, exists := out[mk]; !exists {
					out[mk] = mv
				}
			}
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
// A member with a choice (oneOf/anyOf), open extra keys or nullable cannot be
// expressed as plain properties, so allOf is kept as is.
func mergeAllOf(members []any) (map[string]any, bool) {
	if len(members) == 0 {
		return nil, false
	}
	props := map[string]any{}
	requiredSet := map[string]bool{}
	var description string
	for _, member := range members {
		m := asMap(member)
		if m == nil || !isObjectSchema(m) || hasFreeFormOnly(m) {
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
				// An optional body keeps its arguments optional (the body may be left
				// out), but its required fields are enforced when one is sent.
				var requiredIfSent []string
				for _, name := range asStringSlice(schema["required"]) {
					if _, exists := bodyProps[name]; !exists {
						continue
					}
					if op.Body.Required {
						requiredSet[name] = true
					} else {
						requiredIfSent = append(requiredIfSent, name)
					}
				}
				body = &BodyBinding{Mode: "flattened", ArgNames: names, ContentType: op.Body.ContentType, RequiredIfSent: requiredIfSent}
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
			qsArgs, qsWarnings, qsErr := flattenQuerystring(p, props, requiredSet)
			if qsErr != nil {
				return nil, nil, nil, nil, qsErr
			}
			args = append(args, qsArgs...)
			warnings = append(warnings, qsWarnings...)
			continue
		default:
			warnings = append(warnings, fmt.Sprintf("parameter %q has unsupported location %q and was skipped", p.Name, p.In))
			continue
		}
		argName := p.Name
		if _, taken := props[argName]; taken || nameCount[p.Name] > 1 {
			argName = uniqueArgName(props, p.Name+"__"+p.In)
		}
		schema := NormalizeSchema(p.Schema)
		schema["description"] = joinDescription(p.Description, fmt.Sprintf("(%s parameter)", p.In))
		props[argName] = schema
		if p.Required {
			requiredSet[argName] = true
		}
		args = append(args, ArgBinding{
			ArgName:     argName,
			Location:    p.In,
			Name:        p.Name,
			Required:    p.Required,
			Explode:     explodeFor(p),
			Style:       p.Style,
			ContentType: p.ContentType,
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
func flattenQuerystring(p Parameter, props map[string]any, requiredSet map[string]bool) ([]ArgBinding, []string, error) {
	schema := NormalizeSchema(p.Schema)
	var args []ArgBinding
	var warnings []string
	// Extra keys, choices or a JSON-encoded query string cannot be split into
	// named arguments without losing keys, so those stay one argument.
	if isObjectSchema(schema) && len(asMap(schema["properties"])) > 0 && !hasFreeFormOnly(schema) && !isJSONMediaType(p.ContentType) {
		qsProps := asMap(schema["properties"])
		required := map[string]bool{}
		for _, name := range asStringSlice(schema["required"]) {
			required[name] = true
		}
		// An optional querystring that requires a field accepting no value can
		// never be sent, but it can be left out, so the tool keeps working.
		if !p.Required {
			for _, name := range asStringSlice(schema["required"]) {
				if v, isBool := qsProps[name].(bool); isBool && !v {
					return nil, []string{fmt.Sprintf("optional querystring %q requires field %q, whose schema is false; it was left out of the tool", p.Name, name)}, nil
				}
			}
		}
		for _, name := range sortedKeys(qsProps) {
			// Boolean schemas: true accepts any value, false accepts none, so a
			// false field is not offered (and cannot be satisfied if required).
			var prop map[string]any
			switch v := qsProps[name].(type) {
			case bool:
				if !v {
					if required[name] {
						return nil, nil, fmt.Errorf("querystring %q requires field %q, whose schema is false and accepts no value", p.Name, name)
					}
					continue
				}
				prop = map[string]any{}
			default:
				prop, _ = normalizeNode(v, 0).(map[string]any)
				if prop == nil {
					prop = map[string]any{"type": "string"}
				}
			}
			argName := name
			if _, taken := props[argName]; taken {
				argName = uniqueArgName(props, name+"__querystring")
			}
			prop["description"] = joinDescription(asString(prop["description"]), "(query parameter)")
			props[argName] = prop
			if required[name] && p.Required {
				requiredSet[argName] = true
			}
			// An optional querystring keeps its fields optional (it may be left out),
			// but its required fields are enforced once any of its fields is sent.
			binding := ArgBinding{ArgName: argName, Location: InQuery, Name: name, Required: required[name] && p.Required, Explode: true}
			if !p.Required {
				binding.Group = InQuerystring + ":" + p.Name
				binding.RequiredIfGroupSent = required[name]
			}
			args = append(args, binding)
		}
		return args, warnings, nil
	}
	argName := p.Name
	if argName == "" {
		argName = "querystring"
	}
	if _, taken := props[argName]; taken {
		argName = uniqueArgName(props, argName+"__querystring")
	}
	if isJSONMediaType(p.ContentType) {
		// The whole query string is one JSON document: its schema is the
		// argument's, and it is JSON-encoded when sent.
		jsonSchema := schema
		if jsonSchema == nil {
			jsonSchema = map[string]any{}
		}
		jsonSchema["description"] = joinDescription(asString(jsonSchema["description"]), joinDescription(p.Description, "(query string, sent as JSON)"))
		props[argName] = jsonSchema
	} else {
		props[argName] = map[string]any{"type": "string", "description": joinDescription(p.Description, "(raw query string, sent verbatim)")}
	}
	if p.Required {
		requiredSet[argName] = true
	}
	args = append(args, ArgBinding{ArgName: argName, Location: InQuerystring, Name: p.Name, Required: p.Required, ContentType: p.ContentType})
	return args, warnings, nil
}

// hasFreeFormOnly reports an object schema with additionalProperties but whose
// named properties are all absent, i.e. a dictionary body.
// hasFreeFormOnly reports whether an object body cannot be flattened into its
// named properties without losing something: it allows keys beyond the ones it
// names (or names none), carries a oneOf/anyOf whose members define more
// fields, or may be null as a whole. Such a body travels as one `body`.
func hasFreeFormOnly(schema map[string]any) bool {
	if _, choice := schema["oneOf"]; choice {
		return true
	}
	if _, choice := schema["anyOf"]; choice {
		return true
	}
	if asBool(schema["nullable"]) {
		return true
	}
	if len(asMap(schema["properties"])) == 0 {
		return schema["additionalProperties"] != nil
	}
	switch extra := schema["additionalProperties"].(type) {
	case bool:
		return extra
	case map[string]any:
		return true
	}
	return false
}

// uniqueArgName returns name, or name with the first free "_2", "_3", ...
// suffix when a body property or an earlier parameter already took it.
func uniqueArgName(props map[string]any, name string) string {
	if _, taken := props[name]; !taken {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", name, i)
		if _, taken := props[candidate]; !taken {
			return candidate
		}
	}
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
