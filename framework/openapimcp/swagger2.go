package openapimcp

import (
	"fmt"
	"net/url"
	"strings"
)

// methodsV2 is the Swagger 2.0 method set in walk order.
var methodsV2 = []string{"get", "put", "post", "delete", "patch", "head", "options"}

// swagger2SchemaKeys are the parameter fields that form an inline schema in 2.0.
var swagger2SchemaKeys = []string{
	"type", "format", "items", "enum", "default", "minimum", "maximum",
	"exclusiveMinimum", "exclusiveMaximum", "minLength", "maxLength", "pattern",
	"minItems", "maxItems", "uniqueItems", "multipleOf",
}

// parseSwagger2 fills doc from a Swagger 2.0 tree, mapping host/basePath/schemes
// to servers, body/formData parameters to a request body, and
// securityDefinitions to security schemes.
func parseSwagger2(tree map[string]any, res *resolver, doc *Document, opts ParseOptions) error {
	host := strings.TrimSpace(asString(tree["host"]))
	basePath := strings.TrimSpace(asString(tree["basePath"]))
	schemes := asStringSlice(tree["schemes"])
	scheme := ""
	for _, s := range schemes {
		if s == "https" {
			scheme = "https"
			break
		}
		if s == "http" && scheme == "" {
			scheme = "http"
		}
	}
	if host == "" && opts.SpecURL != "" {
		if u, err := url.Parse(opts.SpecURL); err == nil && u.Host != "" {
			host = u.Host
			if scheme == "" {
				scheme = u.Scheme
			}
		}
	}
	if scheme == "" {
		scheme = "https"
	}
	switch {
	case host != "":
		doc.Servers = []string{scheme + "://" + host + strings.TrimSuffix(basePath, "/")}
	case basePath != "":
		doc.Servers = []string{basePath}
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("swagger basePath %q has no host; set openapi_config.base_url", basePath))
	}

	defs := asMap(tree["securityDefinitions"])
	for _, name := range sortedKeys(defs) {
		node, err := res.resolve(defs[name])
		if err != nil {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("security definition %q skipped: %v", name, err))
			continue
		}
		doc.SecuritySchemes[name] = parseSecurityScheme(name, asMap(node), true)
	}
	doc.GlobalSecurity = parseSecurityRequirements(tree["security"])
	globalConsumes := asStringSlice(tree["consumes"])

	paths := asMap(tree["paths"])
	for _, p := range sortedKeys(paths) {
		itemRaw, err := res.resolveShallow(paths[p])
		if err != nil {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("path %s skipped: %v", p, err))
			continue
		}
		item := asMap(itemRaw)
		pathParamsRaw := asSlice(item["parameters"])
		for _, method := range methodsV2 {
			opRaw, ok := item[method]
			if !ok {
				continue
			}
			doc.Operations = append(doc.Operations, buildOperationV2(strings.ToUpper(method), p, asMap(opRaw), pathParamsRaw, globalConsumes, res))
		}
	}
	return nil
}

func buildOperationV2(method, path string, op map[string]any, pathParamsRaw []any, globalConsumes []string, res *resolver) Operation {
	operation := Operation{
		ID:          strings.TrimSpace(asString(op["operationId"])),
		Method:      method,
		Path:        path,
		Summary:     strings.TrimSpace(asString(op["summary"])),
		Description: strings.TrimSpace(asString(op["description"])),
		Deprecated:  asBool(op["deprecated"]),
		Tags:        asStringSlice(op["tags"]),
	}
	if _, has := op["security"]; has {
		operation.HasSecurity = true
		operation.Security = parseSecurityRequirements(op["security"])
	}
	consumes := asStringSlice(op["consumes"])
	if len(consumes) == 0 {
		consumes = globalConsumes
	}

	// Resolve and merge parameters: operation-level overrides path-level by name+in.
	resolveList := func(list []any) ([]map[string]any, error) {
		out := make([]map[string]any, 0, len(list))
		for _, raw := range list {
			resolved, err := res.resolve(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, asMap(resolved))
		}
		return out, nil
	}
	pathParams, err := resolveList(pathParamsRaw)
	if err != nil {
		operation.Unsupported = "parameters: " + err.Error()
		return operation
	}
	opParams, err := resolveList(asSlice(op["parameters"]))
	if err != nil {
		operation.Unsupported = "parameters: " + err.Error()
		return operation
	}
	key := func(p map[string]any) string { return asString(p["in"]) + "\x00" + asString(p["name"]) }
	override := map[string]bool{}
	for _, p := range opParams {
		override[key(p)] = true
	}
	merged := make([]map[string]any, 0, len(pathParams)+len(opParams))
	for _, p := range pathParams {
		if !override[key(p)] {
			merged = append(merged, p)
		}
	}
	merged = append(merged, opParams...)

	var bodyParam map[string]any
	formProps := map[string]any{}
	var formRequired []string
	formSeen := false
	for _, p := range merged {
		in := strings.ToLower(strings.TrimSpace(asString(p["in"])))
		name := strings.TrimSpace(asString(p["name"]))
		switch in {
		case "body":
			bodyParam = p
		case "formdata":
			formSeen = true
			if asString(p["type"]) == "file" {
				operation.Unsupported = "multipart/binary request body (file upload) is not supported"
				return operation
			}
			formProps[name] = swagger2ParamSchema(p)
			if asBool(p["required"]) {
				formRequired = append(formRequired, name)
			}
		case InPath, InQuery, InHeader:
			param := Parameter{
				Name:        name,
				In:          in,
				Description: strings.TrimSpace(asString(p["description"])),
				Required:    asBool(p["required"]) || in == InPath,
				Schema:      swagger2ParamSchema(p),
			}
			explode := false
			switch asString(p["collectionFormat"]) {
			case "multi":
				explode = true
			case "ssv":
				param.Style = "spaceDelimited"
			case "tsv":
				param.Style = "tabDelimited"
			case "pipes":
				param.Style = "pipeDelimited"
			}
			param.Explode = &explode
			operation.Params = append(operation.Params, param)
		}
	}

	contentType, ok, reason := swagger2BodyContentType(consumes, formSeen)
	switch {
	case bodyParam != nil:
		if !ok {
			operation.Unsupported = reason
			return operation
		}
		if contentType == ContentTypeForm {
			// A body parameter with a form consumes type is contradictory; send JSON.
			contentType = ContentTypeJSON
		}
		operation.Body = &RequestBody{
			ContentType: contentType,
			Required:    asBool(bodyParam["required"]),
			Description: strings.TrimSpace(asString(bodyParam["description"])),
			Schema:      asMap(bodyParam["schema"]),
		}
		if operation.Body.Schema == nil {
			operation.Body.Schema = map[string]any{"type": "object"}
		}
	case formSeen:
		if !ok {
			operation.Unsupported = reason
			return operation
		}
		schema := map[string]any{"type": "object", "properties": formProps}
		if len(formRequired) > 0 {
			schema["required"] = toAnySlice(formRequired)
		}
		operation.Body = &RequestBody{ContentType: ContentTypeForm, Required: len(formRequired) > 0, Schema: schema}
	}
	return operation
}

// swagger2BodyContentType picks the request content type from consumes.
func swagger2BodyContentType(consumes []string, form bool) (string, bool, string) {
	if len(consumes) == 0 {
		if form {
			return ContentTypeForm, true, ""
		}
		return ContentTypeJSON, true, ""
	}
	var hasJSON, hasForm, hasText bool
	for _, c := range consumes {
		c = strings.ToLower(strings.TrimSpace(c))
		switch {
		case strings.Contains(c, "json"), c == "*/*":
			hasJSON = true
		case c == ContentTypeForm:
			hasForm = true
		case strings.HasPrefix(c, "text/plain"):
			hasText = true
		}
	}
	switch {
	case form && hasForm:
		return ContentTypeForm, true, ""
	case form && !hasForm && !hasJSON:
		return "", false, fmt.Sprintf("formData parameters with consumes %v are not supported (multipart/binary request bodies are not supported)", consumes)
	case form:
		return ContentTypeForm, true, ""
	case hasJSON:
		return ContentTypeJSON, true, ""
	case hasForm:
		return ContentTypeForm, true, ""
	case hasText:
		return ContentTypeText, true, ""
	default:
		return "", false, fmt.Sprintf("request body content type %v is not supported (JSON, form-urlencoded or text/plain only)", consumes)
	}
}

// swagger2ParamSchema lifts the inline schema fields of a 2.0 parameter (or
// items) into a schema object. x-nullable becomes nullable.
func swagger2ParamSchema(p map[string]any) map[string]any {
	schema := map[string]any{}
	for _, k := range swagger2SchemaKeys {
		if v, ok := p[k]; ok {
			schema[k] = v
		}
	}
	if items := asMap(p["items"]); items != nil {
		schema["items"] = swagger2ParamSchema(items)
	}
	if nullable, ok := p["x-nullable"].(bool); ok && nullable {
		schema["nullable"] = true
	}
	if desc := strings.TrimSpace(asString(p["description"])); desc != "" {
		if _, isParam := p["in"]; !isParam {
			schema["description"] = desc
		}
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "string"
	}
	return schema
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
