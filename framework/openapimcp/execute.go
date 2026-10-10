package openapimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// forbiddenArgHeaders can never be set from tool arguments or resolver output.
var forbiddenArgHeaders = map[string]bool{
	"Host": true, "Content-Length": true, "Transfer-Encoding": true, "Connection": true,
}

// defaultAccept prefers JSON but accepts anything the upstream wants to send.
const defaultAccept = "application/json, text/*;q=0.9, */*;q=0.1"

// Executor turns tool calls into HTTP requests against one upstream.
type Executor struct {
	baseURL         string
	baseURLExplicit bool
	client          *http.Client
	headers         HeaderResolver
	creds           map[string]schemas.MCPOpenAPICredential
	doc             *Document
	maxBytes        int
	logger          schemas.Logger
}

// NewExecutor validates ServerOptions and returns the executor the synthesized
// tool handlers share.
func NewExecutor(opts ServerOptions) (*Executor, error) {
	if opts.Synthesis == nil || opts.Synthesis.Document == nil {
		return nil, errors.New("synthesis is required")
	}
	base := strings.TrimSpace(opts.BaseURL)
	if !isAbsoluteHTTPURL(base) {
		return nil, fmt.Errorf("base URL %q must be an absolute http(s) URL", base)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	maxBytes := opts.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	return &Executor{
		baseURL:         strings.TrimSuffix(base, "/"),
		baseURLExplicit: opts.BaseURLExplicit,
		client:          client,
		headers:         opts.Headers,
		creds:           opts.Credentials,
		doc:             opts.Synthesis.Document,
		maxBytes:        maxBytes,
		logger:          opts.Logger,
	}, nil
}

// BuildRequest assembles the HTTP request for one tool call from its arguments.
func (e *Executor) BuildRequest(ctx context.Context, tool *Tool, args map[string]any) (*http.Request, error) {
	if tool == nil {
		return nil, errors.New("tool is nil")
	}
	if args == nil {
		args = map[string]any{}
	}
	op := tool.Operation

	base := e.baseURL
	if !e.baseURLExplicit && len(op.Servers) > 0 && isAbsoluteHTTPURL(op.Servers[0]) {
		base = strings.TrimSuffix(op.Servers[0], "/")
	}

	path := op.Path
	query := url.Values{}
	headers := http.Header{}
	var cookies []string
	rawQuerystring := ""

	for _, arg := range tool.Args {
		value, present := args[arg.ArgName]
		if !present || value == nil {
			if arg.Required {
				return nil, fmt.Errorf("missing required %s parameter %q", arg.Location, arg.Name)
			}
			continue
		}
		switch arg.Location {
		case InPath:
			segment := stringify(value, ",")
			if segment == "" || segment == "." || segment == ".." {
				return nil, fmt.Errorf("invalid value %q for path parameter %q", segment, arg.Name)
			}
			path = strings.ReplaceAll(path, "{"+arg.Name+"}", url.PathEscape(segment))
		case InQuery:
			addQueryValue(query, arg, value)
		case InHeader:
			name := http.CanonicalHeaderKey(arg.Name)
			if forbiddenArgHeaders[name] {
				return nil, fmt.Errorf("header parameter %q cannot be set from tool arguments", arg.Name)
			}
			headers.Set(name, stringify(value, ","))
		case InCookie:
			cookies = append(cookies, arg.Name+"="+url.QueryEscape(stringify(value, ",")))
		case InQuerystring:
			rawQuerystring = strings.TrimPrefix(stringify(value, "&"), "?")
		}
	}
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("path %q has unresolved parameters", path)
	}

	bodyReader, contentType, err := e.encodeBody(tool, args)
	if err != nil {
		return nil, err
	}

	// Spec-mapped credentials: query/cookie ones must land before the URL is final.
	schemes := SelectSecurity(op, e.doc, e.creds)
	credHeaders := &http.Request{Header: http.Header{}}
	InjectCredentials(credHeaders, query, &cookies, schemes, e.creds)

	target := base + ensureLeadingSlash(path)
	encodedQuery := query.Encode()
	switch {
	case encodedQuery != "" && rawQuerystring != "":
		target += "?" + encodedQuery + "&" + rawQuerystring
	case encodedQuery != "":
		target += "?" + encodedQuery
	case rawQuerystring != "":
		target += "?" + rawQuerystring
	}

	req, err := http.NewRequestWithContext(ctx, op.Method, target, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	// Layer 1: resolver headers (static config headers, credential store, extras).
	if e.headers != nil {
		resolved, err := e.headers(ctx)
		if err != nil {
			return nil, err
		}
		for name, values := range resolved {
			canonical := http.CanonicalHeaderKey(name)
			if forbiddenArgHeaders[canonical] || len(values) == 0 {
				continue
			}
			if canonical == "Cookie" {
				cookies = append(cookies, strings.Join(values, "; "))
				continue
			}
			req.Header[canonical] = append([]string(nil), values...)
		}
	}
	// Layer 2: declared header parameters carry the model's value.
	for name, values := range headers {
		req.Header[name] = values
	}
	// Layer 3: spec-mapped credentials win for the names they own.
	for name, values := range credHeaders.Header {
		req.Header[name] = values
	}
	if len(cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(cookies, "; "))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", defaultAccept)
	}
	return req, nil
}

// encodeBody serializes the body arguments per the tool's binding.
func (e *Executor) encodeBody(tool *Tool, args map[string]any) (io.Reader, string, error) {
	binding := tool.Body
	if binding == nil {
		return nil, "", nil
	}
	required := tool.Operation.Body != nil && tool.Operation.Body.Required
	var payload any
	present := false
	switch binding.Mode {
	case "flattened":
		m := map[string]any{}
		for _, name := range binding.ArgNames {
			if v, ok := args[name]; ok && v != nil {
				m[name] = v
			}
		}
		present = len(m) > 0
		payload = m
	default:
		v, ok := args[binding.SingleArg]
		present = ok && v != nil
		payload = v
	}
	if !present {
		if required {
			if binding.Mode == "flattened" {
				payload = map[string]any{}
			} else {
				return nil, "", fmt.Errorf("missing required request body argument %q", binding.SingleArg)
			}
		} else {
			return nil, "", nil
		}
	}
	switch binding.ContentType {
	case ContentTypeForm:
		values := url.Values{}
		obj, ok := payload.(map[string]any)
		if !ok {
			return nil, "", errors.New("form request body must be an object")
		}
		for _, key := range sortedKeys(obj) {
			switch v := obj[key].(type) {
			case []any:
				for _, item := range v {
					values.Add(key, stringify(item, ","))
				}
			default:
				values.Add(key, stringify(v, ","))
			}
		}
		return strings.NewReader(values.Encode()), ContentTypeForm, nil
	case ContentTypeText:
		return strings.NewReader(stringify(payload, "\n")), "text/plain; charset=utf-8", nil
	default:
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, "", fmt.Errorf("encoding JSON body: %w", err)
		}
		return bytes.NewReader(data), ContentTypeJSON, nil
	}
}

// Execute sends the tool call upstream and converts the response into a tool
// result. Transport failures and non-2xx statuses become error results (the
// model sees them); only argument/binding problems surface as Go errors.
func (e *Executor) Execute(ctx context.Context, tool *Tool, args map[string]any) (*mcp.CallToolResult, error) {
	req, err := e.BuildRequest(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	if e.logger != nil {
		e.logger.Debug("[OpenAPI MCP] %s %s%s", req.Method, req.URL.Host, req.URL.Path)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("request to upstream failed: %v", sanitizeURLError(err))), nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(e.maxBytes)+1))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("reading upstream response failed: %v", err)), nil
	}
	truncated := len(body) > e.maxBytes
	if truncated {
		body = body[:e.maxBytes]
	}
	return formatResponse(resp, body, truncated, e.maxBytes), nil
}

// sanitizeURLError strips the query string from url.Error messages so a
// credential carried as a query parameter never reaches logs or the model.
func sanitizeURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		if u, perr := url.Parse(uerr.URL); perr == nil && u.RawQuery != "" {
			u.RawQuery = ""
			return &url.Error{Op: uerr.Op, URL: u.String(), Err: uerr.Err}
		}
	}
	return err
}

// textualContentTypes lists response media types returned verbatim.
func isTextualContentType(ct string, body []byte) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch {
	case ct == "":
		return utf8.Valid(body)
	case strings.HasPrefix(ct, "text/"):
		return true
	case strings.Contains(ct, "json"), strings.Contains(ct, "xml"), strings.Contains(ct, "yaml"):
		return true
	case ct == ContentTypeForm, ct == "application/javascript", ct == "application/x-ndjson":
		return true
	}
	return false
}

func formatResponse(resp *http.Response, body []byte, truncated bool, limit int) *mcp.CallToolResult {
	ct := resp.Header.Get("Content-Type")
	var text string
	if isTextualContentType(ct, body) {
		text = string(body)
		if truncated {
			text += fmt.Sprintf("\n…[response truncated at %d bytes]", limit)
		}
	} else {
		size := strconv.Itoa(len(body))
		if truncated {
			size = "more than " + size
		}
		text = fmt.Sprintf("[binary response: %s, %s bytes]", ct, size)
	}
	status := fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if text == "" {
			return mcp.NewToolResultError(status)
		}
		return mcp.NewToolResultError(status + "\n" + text)
	}
	if len(body) == 0 {
		return mcp.NewToolResultText(status)
	}
	return mcp.NewToolResultText(text)
}

// addQueryValue serializes one query argument honoring explode/style.
func addQueryValue(q url.Values, arg ArgBinding, value any) {
	switch v := value.(type) {
	case []any:
		if arg.Explode {
			for _, item := range v {
				q.Add(arg.Name, stringify(item, ","))
			}
			return
		}
		q.Add(arg.Name, stringify(v, delimiterFor(arg.Style)))
	case map[string]any:
		if arg.Explode {
			for _, key := range sortedKeys(v) {
				q.Add(key, stringify(v[key], ","))
			}
			return
		}
		data, _ := json.Marshal(v)
		q.Add(arg.Name, string(data))
	default:
		q.Add(arg.Name, stringify(v, ","))
	}
}

func delimiterFor(style string) string {
	switch style {
	case "spaceDelimited":
		return " "
	case "pipeDelimited":
		return "|"
	case "tabDelimited":
		return "\t"
	}
	return ","
}

// stringify renders an argument for the wire: scalars as text, arrays joined by
// sep, objects as JSON.
func stringify(v any, sep string) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, stringify(item, sep))
		}
		return strings.Join(parts, sep)
	case []string:
		return strings.Join(t, sep)
	case map[string]any:
		data, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(data)
	default:
		data, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return strings.Trim(string(data), `"`)
	}
}

func ensureLeadingSlash(p string) string {
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}
