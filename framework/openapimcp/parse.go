package openapimcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// methodsV3 is the deterministic walk order of path-item methods.
var methodsV3 = []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}

// Parse decodes a JSON or YAML spec into the version-independent Document.
func Parse(data []byte, opts ParseOptions) (*Document, error) {
	limit := opts.MaxBytes
	if limit <= 0 {
		limit = MaxSpecBytes
	}
	if len(data) > limit {
		return nil, ErrSpecTooLarge
	}
	tree, err := decodeTree(data)
	if err != nil {
		return nil, err
	}
	version, warnings, err := DetectVersion(tree)
	if err != nil {
		return nil, err
	}
	doc := &Document{
		OpenAPIVersion:  version.Raw,
		SecuritySchemes: map[string]SecurityScheme{},
		Warnings:        warnings,
	}
	info := asMap(tree["info"])
	doc.Title = strings.TrimSpace(asString(info["title"]))
	doc.Version = strings.TrimSpace(asString(info["version"]))
	doc.Description = strings.TrimSpace(asString(info["description"]))

	res := &resolver{root: tree}
	if version.Swagger2 {
		err = parseSwagger2(tree, res, doc, opts)
	} else {
		err = parseV3(tree, version, res, doc, opts)
	}
	if err != nil {
		return nil, err
	}
	doc.Warnings = append(doc.Warnings, res.warnings...)
	if len(doc.Operations) == 0 {
		return nil, errors.New("spec declares no operations under paths")
	}
	return doc, nil
}

// decodeTree turns JSON or YAML bytes into a string-keyed tree. JSON numbers are
// kept as json.Number so re-encoding does not reformat them; YAML scalars keep
// their native Go types and non-string keys (e.g. 200:) become strings.
func decodeTree(data []byte) (map[string]any, error) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	trimmed = bytes.TrimPrefix(trimmed, []byte{0xEF, 0xBB, 0xBF})
	if len(trimmed) == 0 {
		return nil, errors.New("openapi spec is empty")
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, errors.New("invalid JSON: trailing data after the document")
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("invalid spec: top level must be an object")
		}
		return m, nil
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	m, ok := normalizeYAML(v).(map[string]any)
	if !ok {
		return nil, errors.New("invalid spec: top level must be a mapping")
	}
	return m, nil
}

func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAML(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeYAML(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeYAML(val)
		}
		return out
	case time.Time:
		return t.Format(time.RFC3339)
	default:
		return t
	}
}

// parseV3 fills doc from an OpenAPI 3.x tree.
func parseV3(tree map[string]any, version Version, res *resolver, doc *Document, opts ParseOptions) error {
	if version.HasSelf() {
		doc.Self = strings.TrimSpace(asString(tree["$self"]))
	}
	doc.Servers = parseServers(tree["servers"], doc, opts)

	components := asMap(tree["components"])
	for _, name := range sortedKeys(asMap(components["securitySchemes"])) {
		node, err := res.resolve(asMap(components["securitySchemes"])[name])
		if err != nil {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("security scheme %q skipped: %v", name, err))
			continue
		}
		doc.SecuritySchemes[name] = parseSecurityScheme(name, asMap(node), false)
	}
	doc.GlobalSecurity = parseSecurityRequirements(tree["security"])

	paths := asMap(tree["paths"])
	for _, p := range sortedKeys(paths) {
		// Only the path item's own $ref is followed here; parameters and bodies
		// are resolved per operation so one unsupported reference costs one
		// operation, not the whole path.
		itemRaw, err := res.resolveShallow(paths[p])
		if err != nil {
			if errors.Is(err, errExternalRef) {
				doc.Warnings = append(doc.Warnings, fmt.Sprintf("path %s skipped: %v", p, err))
				continue
			}
			return fmt.Errorf("path %s: %w", p, err)
		}
		item := asMap(itemRaw)
		pathParams, pathParamErr := parseParametersV3(asSlice(item["parameters"]), res)
		pathServers := parseServers(item["servers"], doc, opts)
		methods := methodsV3
		if version.HasQueryMethod() {
			methods = append(append([]string(nil), methodsV3...), "query")
		}
		for _, method := range methods {
			opRaw, ok := item[method]
			if !ok {
				continue
			}
			doc.Operations = append(doc.Operations, buildOperationV3(strings.ToUpper(method), p, asMap(opRaw), pathParams, pathParamErr, pathServers, version, res, doc, opts))
		}
		if version.HasAdditionalOperations() {
			additional := asMap(item["additionalOperations"])
			for _, method := range sortedKeys(additional) {
				doc.Operations = append(doc.Operations, buildOperationV3(strings.ToUpper(method), p, asMap(additional[method]), pathParams, pathParamErr, pathServers, version, res, doc, opts))
			}
		}
	}
	return nil
}

func buildOperationV3(method, path string, op map[string]any, pathParams []Parameter, pathParamErr error, pathServers []string, version Version, res *resolver, doc *Document, opts ParseOptions) Operation {
	operation := Operation{
		ID:          strings.TrimSpace(asString(op["operationId"])),
		Method:      method,
		Path:        path,
		Summary:     strings.TrimSpace(asString(op["summary"])),
		Description: strings.TrimSpace(asString(op["description"])),
		Deprecated:  asBool(op["deprecated"]),
		Tags:        asStringSlice(op["tags"]),
	}
	if servers := parseServers(op["servers"], doc, opts); len(servers) > 0 {
		operation.Servers = servers
	} else if len(pathServers) > 0 {
		operation.Servers = pathServers
	}
	if _, has := op["security"]; has {
		operation.HasSecurity = true
		operation.Security = parseSecurityRequirements(op["security"])
	}
	if pathParamErr != nil {
		operation.Unsupported = pathParamErr.Error()
		return operation
	}
	opParams, err := parseParametersV3(asSlice(op["parameters"]), res)
	if err != nil {
		operation.Unsupported = err.Error()
		return operation
	}
	operation.Params = mergeParameters(pathParams, opParams)

	if rbRaw, ok := op["requestBody"]; ok && rbRaw != nil {
		resolved, err := res.resolve(rbRaw)
		if err != nil {
			operation.Unsupported = "request body: " + err.Error()
			return operation
		}
		rb := asMap(resolved)
		contentType, schema, ok, reason := SelectBodyContent(asMap(rb["content"]))
		if !ok {
			operation.Unsupported = reason
			return operation
		}
		operation.Body = &RequestBody{
			ContentType: contentType,
			Required:    asBool(rb["required"]),
			Description: strings.TrimSpace(asString(rb["description"])),
			Schema:      schema,
		}
	}
	return operation
}

// parseParametersV3 converts a 3.x parameter list, resolving $refs. An external
// $ref makes the whole list unusable (returned as an error for the operation).
func parseParametersV3(list []any, res *resolver) ([]Parameter, error) {
	params := make([]Parameter, 0, len(list))
	for _, raw := range list {
		resolved, err := res.resolve(raw)
		if err != nil {
			return nil, fmt.Errorf("parameters: %w", err)
		}
		p := asMap(resolved)
		param := Parameter{
			Name:        strings.TrimSpace(asString(p["name"])),
			In:          strings.ToLower(strings.TrimSpace(asString(p["in"]))),
			Description: strings.TrimSpace(asString(p["description"])),
			Required:    asBool(p["required"]),
			Style:       strings.TrimSpace(asString(p["style"])),
		}
		if param.Name == "" || param.In == "" {
			continue
		}
		if param.In == InPath {
			param.Required = true
		}
		if explode, ok := p["explode"].(bool); ok {
			param.Explode = &explode
		}
		if schema := asMap(p["schema"]); len(schema) > 0 {
			param.Schema = schema
		} else if content := asMap(p["content"]); len(content) > 0 {
			for _, mt := range sortedKeys(content) {
				param.Schema = asMap(asMap(content[mt])["schema"])
				break
			}
		}
		if param.Schema == nil {
			param.Schema = map[string]any{"type": "string"}
		}
		params = append(params, param)
	}
	return params, nil
}

// mergeParameters overlays operation parameters on path-item ones (same name
// and location wins at the operation level), keeping path-item order first.
func mergeParameters(pathParams, opParams []Parameter) []Parameter {
	key := func(p Parameter) string { return p.In + "\x00" + p.Name }
	override := make(map[string]bool, len(opParams))
	for _, p := range opParams {
		override[key(p)] = true
	}
	out := make([]Parameter, 0, len(pathParams)+len(opParams))
	for _, p := range pathParams {
		if !override[key(p)] {
			out = append(out, p)
		}
	}
	return append(out, opParams...)
}

// parseServers expands server variables and resolves relative URLs against
// $self, then the spec URL. Unresolvable relative entries are kept (so the
// preview can show them) with a warning; ResolveBaseURL insists on an override.
func parseServers(raw any, doc *Document, opts ParseOptions) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range asSlice(raw) {
		srv := asMap(item)
		u := strings.TrimSpace(asString(srv["url"]))
		if u == "" {
			continue
		}
		for _, name := range sortedKeys(asMap(srv["variables"])) {
			v := asMap(asMap(srv["variables"])[name])
			def := asString(v["default"])
			if def == "" {
				if enum := asStringSlice(v["enum"]); len(enum) > 0 {
					def = enum[0]
				}
			}
			u = strings.ReplaceAll(u, "{"+name+"}", def)
		}
		u = resolveRelativeURL(u, doc.Self, opts.SpecURL)
		if !isAbsoluteHTTPURL(u) {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("server url %q is relative and could not be resolved; set openapi_config.base_url", u))
		}
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

func resolveRelativeURL(u, self, specURL string) string {
	if isAbsoluteHTTPURL(u) {
		return u
	}
	ref, err := url.Parse(u)
	if err != nil {
		return u
	}
	for _, baseStr := range []string{self, specURL} {
		if baseStr == "" {
			continue
		}
		base, err := url.Parse(baseStr)
		if err != nil || !isAbsoluteHTTPURL(baseStr) {
			continue
		}
		return base.ResolveReference(ref).String()
	}
	return u
}

func isAbsoluteHTTPURL(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// parseSecurityScheme normalizes one scheme. swagger2 maps the 2.0 "basic" type
// onto http/basic so the executor has one shape to inject.
func parseSecurityScheme(name string, m map[string]any, swagger2 bool) SecurityScheme {
	scheme := SecurityScheme{
		Name:        name,
		Type:        strings.TrimSpace(asString(m["type"])),
		Description: strings.TrimSpace(asString(m["description"])),
	}
	switch scheme.Type {
	case "apiKey":
		scheme.In = strings.ToLower(strings.TrimSpace(asString(m["in"])))
		scheme.ParamName = strings.TrimSpace(asString(m["name"]))
		switch scheme.In {
		case InHeader, InQuery, InCookie:
			scheme.Supported = scheme.ParamName != ""
			if !scheme.Supported {
				scheme.Reason = "apiKey scheme has no parameter name"
			}
		default:
			scheme.Reason = fmt.Sprintf("apiKey location %q is not supported", scheme.In)
		}
	case "basic":
		if swagger2 {
			scheme.Type = "http"
			scheme.Scheme = "basic"
			scheme.Supported = true
		} else {
			scheme.Reason = fmt.Sprintf("security scheme type %q is not supported", scheme.Type)
		}
	case "http":
		scheme.Scheme = strings.ToLower(strings.TrimSpace(asString(m["scheme"])))
		switch scheme.Scheme {
		case "bearer", "basic":
			scheme.Supported = true
		default:
			scheme.Reason = fmt.Sprintf("http authentication scheme %q is not supported (bearer and basic only)", scheme.Scheme)
		}
	case "oauth2", "openIdConnect", "mutualTLS":
		scheme.Reason = fmt.Sprintf("%s security schemes are not supported; configure an API key or bearer/basic credential, or static headers", scheme.Type)
	default:
		scheme.Reason = fmt.Sprintf("security scheme type %q is not supported", scheme.Type)
	}
	return scheme
}

func parseSecurityRequirements(raw any) []SecurityRequirement {
	list := asSlice(raw)
	out := make([]SecurityRequirement, 0, len(list))
	for _, item := range list {
		req := SecurityRequirement{}
		for name, scopes := range asMap(item) {
			req[name] = asStringSlice(scopes)
		}
		out = append(out, req)
	}
	return out
}

// ---- generic tree helpers ----

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asStringSlice(v any) []string {
	list := asSlice(v)
	if list == nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s := asString(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
