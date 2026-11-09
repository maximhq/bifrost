package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/tidwall/gjson"
)

// API evaluator defaults.
const (
	defaultAPITimeout          = 30 * time.Second
	defaultAPIMaxResponseBytes = 1 << 20
	apiErrorBodyPreviewBytes   = 300
)

// APIOptions configures API-based evaluators.
type APIOptions struct {
	// Client makes the calls. Nil takes an SSRF-safe client that refuses loopback,
	// private and link-local addresses, since the URL is user supplied.
	Client           *http.Client
	Timeout          time.Duration // per call when the evaluator sets none
	MaxResponseBytes int64         // response bodies larger than this are an error
}

// RegisterAPIEvaluator installs the API-based evaluator on an engine.
func RegisterAPIEvaluator(engine *Engine, opts APIOptions) {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultAPITimeout
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = defaultAPIMaxResponseBytes
	}
	if opts.Client == nil {
		// The per-call deadline comes from the request context, so the client has none.
		opts.Client = network.NewSSRFSafeHTTPClient(0)
	}
	limiter := &apiLimiter{slots: make(map[string]*apiSlots)}
	engine.Register(EvaluatorTypeAPI, "", func(def Definition, cfg Config) (Evaluator, error) {
		api, ok := cfg.(*APIConfig)
		if !ok {
			return nil, fmt.Errorf("an API evaluator needs an API config, got %T", cfg)
		}
		return newAPIEvaluator(def.ID, api, opts, limiter)
	})
}

// apiSlots is the in-flight limit of one evaluator.
type apiSlots struct {
	size int
	ch   chan struct{}
}

// apiLimiter caps calls in flight per evaluator. The cap is per instance: it protects
// the customer's endpoint from one gateway, not from the whole fleet.
type apiLimiter struct {
	mu    sync.Mutex
	slots map[string]*apiSlots
}

// acquire takes a slot for the evaluator and returns its release. An evaluator with no
// ID (an unsaved draft) is not limited.
func (l *apiLimiter) acquire(ctx context.Context, id string, size int) (func(), error) {
	if id == "" || size <= 0 {
		return func() {}, nil
	}
	l.mu.Lock()
	slots, ok := l.slots[id]
	if !ok || slots.size != size {
		// A changed concurrency starts a fresh pool; calls on the old one finish on it.
		slots = &apiSlots{size: size, ch: make(chan struct{}, size)}
		l.slots[id] = slots
	}
	l.mu.Unlock()
	select {
	case slots.ch <- struct{}{}:
		return func() { <-slots.ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// apiEvaluator scores by calling a user's HTTP endpoint and reading the score out of
// the JSON it returns.
type apiEvaluator struct {
	id      string
	cfg     *APIConfig
	opts    APIOptions
	limiter *apiLimiter

	// Templates are split once when the evaluator is built and reused for every call.
	urlPath    template
	urlQuery   template
	hasQuery   bool
	payload    template
	hasPayload bool
	headers    []namedTemplate
	params     []namedTemplate
	required   []string
}

// namedTemplate is a header or query parameter whose value is a template.
type namedTemplate struct {
	key   string
	value template
}

func newAPIEvaluator(id string, cfg *APIConfig, opts APIOptions, limiter *apiLimiter) (*apiEvaluator, error) {
	e := &apiEvaluator{id: id, cfg: cfg, opts: opts, limiter: limiter, hasPayload: cfg.Payload != ""}
	// Path and query escape differently, so the URL template is kept in two halves.
	pathPart, queryPart, hasQuery := strings.Cut(cfg.URL, "?")
	e.urlPath, e.urlQuery, e.hasQuery = compileTemplate(pathPart), compileTemplate(queryPart), hasQuery
	e.payload = compileTemplate(cfg.Payload)
	for _, header := range cfg.Headers {
		// Header values are encrypted at rest; the plain value lives only in this evaluator.
		value, err := unsealHeaderValue(header.Value.GetValue())
		if err != nil {
			return nil, fmt.Errorf("the stored value of header %q could not be decrypted: %w", header.Key, err)
		}
		e.headers = append(e.headers, namedTemplate{key: header.Key, value: compileTemplate(value)})
	}
	for _, param := range cfg.Params {
		e.params = append(e.params, namedTemplate{key: param.Key, value: compileTemplate(param.Value)})
	}

	seen := make(map[string]struct{})
	add := func(t template) {
		for _, name := range t.names {
			if _, optional := cfg.OptionalVariables[name]; optional {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			e.required = append(e.required, name)
		}
	}
	add(e.urlPath)
	add(e.urlQuery)
	add(e.payload)
	for _, header := range e.headers {
		add(header.value)
	}
	for _, param := range e.params {
		add(param.value)
	}
	return e, nil
}

// RequiredVariables lists every placeholder used in the request that is not optional.
func (e *apiEvaluator) RequiredVariables() []string { return e.required }

// Evaluate calls the endpoint and maps its response to a score.
func (e *apiEvaluator) Evaluate(ctx context.Context, in Input) (Result, error) {
	timeout := e.opts.Timeout
	if e.cfg.TimeoutMs > 0 {
		timeout = time.Duration(e.cfg.TimeoutMs) * time.Millisecond
	}
	release, err := e.limiter.acquire(ctx, e.id, e.cfg.Concurrency)
	if err != nil {
		return Result{}, err
	}
	defer release()

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// The request body is rendered into a pooled buffer that must outlive the call.
	requestBody := getBuffer()
	defer putBuffer(requestBody)
	req, err := e.buildRequest(callCtx, in, requestBody)
	if err != nil {
		return Result{}, err
	}
	resp, err := e.opts.Client.Do(req)
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("the evaluator endpoint timed out after %s", timeout)
		}
		return Result{}, fmt.Errorf("the evaluator endpoint could not be reached: %w", err)
	}
	defer resp.Body.Close()

	// gjson copies what it returns, so nothing below keeps a reference into this buffer.
	responseBody := getBuffer()
	defer putBuffer(responseBody)
	_, err = responseBody.ReadFrom(io.LimitReader(resp.Body, e.opts.MaxResponseBytes+1))
	body := responseBody.Bytes()
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("the evaluator endpoint timed out after %s", timeout)
		}
		return Result{}, fmt.Errorf("failed to read the evaluator endpoint's response: %w", err)
	}
	if int64(len(body)) > e.opts.MaxResponseBytes {
		return Result{}, fmt.Errorf("the evaluator endpoint's response is larger than %d bytes", e.opts.MaxResponseBytes)
	}
	if resp.StatusCode > 299 {
		return Result{}, fmt.Errorf("the evaluator endpoint returned status %d: %s", resp.StatusCode, previewBody(body))
	}
	if !gjson.ValidBytes(body) {
		return Result{}, fmt.Errorf("the evaluator endpoint did not return JSON: %s", previewBody(body))
	}

	raw := gjson.GetBytes(body, e.cfg.OutputMapping.Score)
	if !raw.Exists() {
		return Result{}, fmt.Errorf("the response has no value at the score path %q", e.cfg.OutputMapping.Score)
	}
	var score Score
	switch raw.Type {
	case gjson.True, gjson.False:
		score = BoolScore(raw.Bool())
	case gjson.Number:
		score = NumberScore(raw.Float())
	case gjson.String:
		score = StringScore(raw.String())
	default:
		return Result{}, fmt.Errorf("the value at the score path %q must be a boolean, a number or a string", e.cfg.OutputMapping.Score)
	}
	result := Result{Score: score}
	if path := e.cfg.OutputMapping.Reasoning; path != "" {
		if reasoning := gjson.GetBytes(body, path); reasoning.Exists() && reasoning.Type != gjson.Null {
			result.Reasoning = reasoning.String()
		}
	}
	return result, nil
}

// buildRequest renders the templates. Each part of the request escapes values the way
// that part needs, so a value can never change the request's shape. The payload is
// rendered into bodyBuf, which the caller keeps alive until the call is done.
func (e *apiEvaluator) buildRequest(ctx context.Context, in Input, bodyBuf *bytes.Buffer) (*http.Request, error) {
	urlBuf := getBuffer()
	e.urlPath.render(urlBuf, in, url.PathEscape)
	if e.hasQuery {
		urlBuf.WriteByte('?')
		e.urlQuery.render(urlBuf, in, url.QueryEscape)
	}
	target, err := url.Parse(urlBuf.String())
	putBuffer(urlBuf)
	if err != nil {
		return nil, fmt.Errorf("the evaluator URL is invalid after substitution: %w", err)
	}
	if len(e.params) > 0 {
		query := target.Query()
		for _, param := range e.params {
			query.Add(param.key, param.value.renderString(in, nil))
		}
		target.RawQuery = query.Encode()
	}

	var body io.Reader
	if e.hasPayload {
		e.payload.render(bodyBuf, in, jsonStringEscape)
		body = bytes.NewReader(bodyBuf.Bytes())
	}
	req, err := http.NewRequestWithContext(ctx, e.cfg.Method, target.String(), body)
	if err != nil {
		return nil, err
	}
	for _, header := range e.headers {
		req.Header.Set(header.key, header.value.renderString(in, stripLineBreaks))
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", JudgeUserAgent)
	}
	return req, nil
}

// jsonStringEscape escapes a value for use inside a JSON string literal.
func jsonStringEscape(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) < 2 {
		return ""
	}
	return string(encoded[1 : len(encoded)-1])
}

// stripLineBreaks removes the characters that would let a value add a header.
func stripLineBreaks(value string) string {
	if !strings.ContainsAny(value, "\r\n") {
		return value
	}
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, value)
}

// previewBody returns the start of a response body for an error message.
func previewBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > apiErrorBodyPreviewBytes {
		return text[:apiErrorBodyPreviewBytes] + "..."
	}
	return text
}
