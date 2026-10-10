package openapimcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capture records what the upstream received.
type capture struct {
	mu      sync.Mutex
	method  string
	path    string
	query   string
	headers http.Header
	body    string
}

func newCaptureServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.method, c.path, c.query, c.headers, c.body = r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), string(body)
		c.mu.Unlock()
		if respond != nil {
			respond(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(ts.Close)
	return ts, c
}

func petstoreExecutor(t *testing.T, ts *httptest.Server, mutate func(*ServerOptions)) (*Executor, *Synthesis) {
	t.Helper()
	syn, err := Synthesize(parseFixture(t, "petstore-3.0.yaml"), SynthesizeOptions{ClientName: "petstore", IncludeDeprecated: true})
	require.NoError(t, err)
	opts := ServerOptions{
		ClientName:      "petstore",
		Synthesis:       syn,
		BaseURL:         ts.URL + "/v1",
		BaseURLExplicit: true,
		HTTPClient:      ts.Client(),
	}
	if mutate != nil {
		mutate(&opts)
	}
	exec, err := NewExecutor(opts)
	require.NoError(t, err)
	return exec, syn
}

func TestNewExecutorValidation(t *testing.T) {
	_, err := NewExecutor(ServerOptions{})
	require.ErrorContains(t, err, "synthesis")
	syn := &Synthesis{Document: &Document{}}
	_, err = NewExecutor(ServerOptions{Synthesis: syn, BaseURL: "/relative"})
	require.ErrorContains(t, err, "absolute http(s) URL")
	exec, err := NewExecutor(ServerOptions{Synthesis: syn, BaseURL: "https://api.example.com/"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com", exec.baseURL, "trailing slash trimmed")
	assert.Equal(t, DefaultMaxResponseBytes, exec.maxBytes)
	assert.NotNil(t, exec.client)
}

func TestBuildRequest_ParamsAndHeaders(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	exec, syn := petstoreExecutor(t, ts, func(o *ServerOptions) {
		o.Headers = func(context.Context) (http.Header, error) {
			return http.Header{"X-Static": {"static"}, "Authorization": {"Bearer resolver"}, "Host": {"evil"}, "Cookie": {"session=abc"}}, nil
		}
	})

	list := findTool(t, syn, "listPets")
	res, err := exec.Execute(context.Background(), list, map[string]any{
		"limit":        float64(10),
		"tags":         []any{"a", "b c"},
		"X-Request-Id": "req-1",
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Equal(t, http.MethodGet, cap.method)
	assert.Equal(t, "/v1/pets", cap.path)
	assert.Equal(t, "limit=10&tags=a%2Cb+c", cap.query, "explode:false joins with a comma")
	assert.Equal(t, "req-1", cap.headers.Get("X-Request-Id"))
	assert.Equal(t, "static", cap.headers.Get("X-Static"))
	assert.Equal(t, "Bearer resolver", cap.headers.Get("Authorization"))
	assert.Equal(t, "session=abc", cap.headers.Get("Cookie"))
	assert.Equal(t, defaultAccept, cap.headers.Get("Accept"))
	assert.NotEqual(t, "evil", cap.headers.Get("Host"), "resolver cannot override Host")

	get := findTool(t, syn, "getPetById")
	_, err = exec.Execute(context.Background(), get, map[string]any{"petId__path": "a/b c", "petId__query": "q"})
	require.NoError(t, err)
	assert.Equal(t, "/v1/pets/a%2Fb%20c", cap.path, "path values are escaped, slashes included")
	assert.Equal(t, "petId=q", cap.query)

	_, err = exec.BuildRequest(context.Background(), get, map[string]any{})
	require.ErrorContains(t, err, "missing required path parameter")
	_, err = exec.BuildRequest(context.Background(), get, map[string]any{"petId__path": ".."})
	require.ErrorContains(t, err, "invalid value")
}

func TestBuildRequest_Bodies(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	exec, syn := petstoreExecutor(t, ts, nil)

	create := findTool(t, syn, "createPet")
	_, err := exec.Execute(context.Background(), create, map[string]any{"name": "Rex", "tag": nil, "owner": map[string]any{"name": "Ann"}, "ignored": 1})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, cap.method)
	assert.Equal(t, ContentTypeJSON, cap.headers.Get("Content-Type"))
	assert.JSONEq(t, `{"name":"Rex","owner":{"name":"Ann"}}`, cap.body, "nil and unknown properties are not sent")

	_, err = exec.Execute(context.Background(), create, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, `{}`, cap.body, "a required flattened body with no fields is still sent as an object")

	note := findTool(t, syn, "putNote")
	_, err = exec.Execute(context.Background(), note, map[string]any{"body": "hello"})
	require.NoError(t, err)
	assert.Equal(t, "hello", cap.body)
	assert.True(t, strings.HasPrefix(cap.headers.Get("Content-Type"), "text/plain"))
	_, err = exec.BuildRequest(context.Background(), note, map[string]any{})
	require.ErrorContains(t, err, "missing required request body")

	// Form body via the swagger 2 fixture.
	syn2, err := Synthesize(parseFixture(t, "petstore-swagger2.json"), SynthesizeOptions{ClientName: "legacy"})
	require.NoError(t, err)
	exec2, err := NewExecutor(ServerOptions{Synthesis: syn2, BaseURL: ts.URL, HTTPClient: ts.Client()})
	require.NoError(t, err)
	_, err = exec2.Execute(context.Background(), findTool(t, syn2, "login"), map[string]any{"username": "u", "password": "p&q"})
	require.NoError(t, err)
	assert.Equal(t, ContentTypeForm, cap.headers.Get("Content-Type"))
	assert.Equal(t, "password=p%26q&username=u", cap.body)
	assert.Equal(t, "/login", cap.path)

	// Non-explode arrays with pipes and multi arrays.
	_, err = exec2.Execute(context.Background(), findTool(t, syn2, "listPets"), map[string]any{"tags": []any{"x", "y"}, "ids": []any{float64(1), float64(2)}})
	require.NoError(t, err)
	assert.Equal(t, "ids=1&ids=2&tags=x%2Cy", cap.query)
}

func TestBuildRequest_CredentialsAndServers(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	creds := map[string]schemas.MCPOpenAPICredential{"ApiKeyAuth": {Value: schemas.NewSecretVar("secret-key")}}
	exec, syn := petstoreExecutor(t, ts, func(o *ServerOptions) {
		o.Credentials = creds
		o.Headers = func(context.Context) (http.Header, error) {
			return http.Header{"X-Api-Key": {"from-resolver"}}, nil
		}
	})
	_, err := exec.Execute(context.Background(), findTool(t, syn, "listPets"), nil)
	require.NoError(t, err)
	assert.Equal(t, "secret-key", cap.headers.Get("X-API-Key"), "spec-mapped credential wins over the resolver's same-named header")

	_, err = exec.Execute(context.Background(), findTool(t, syn, "getPetById"), map[string]any{"petId__path": "1"})
	require.NoError(t, err)
	assert.Equal(t, "from-resolver", cap.headers.Get("X-API-Key"), "an anonymous operation (security: []) gets no spec credential, so the resolver header stands")

	// Query credential lands in the URL; a network error must not echo it.
	qdoc := parseFixture(t, "petstore-3.0.yaml")
	qdoc.GlobalSecurity = []SecurityRequirement{{"QueryKey": {}}}
	qsyn, err := Synthesize(qdoc, SynthesizeOptions{ClientName: "petstore"})
	require.NoError(t, err)
	qexec, err := NewExecutor(ServerOptions{Synthesis: qsyn, BaseURL: ts.URL, HTTPClient: ts.Client(), Credentials: map[string]schemas.MCPOpenAPICredential{"QueryKey": {Value: schemas.NewSecretVar("qsecret")}}})
	require.NoError(t, err)
	_, err = qexec.Execute(context.Background(), findTool(t, qsyn, "listPets"), map[string]any{"limit": float64(1)})
	require.NoError(t, err)
	assert.Equal(t, "api_key=qsecret&limit=1", cap.query)

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	deadExec, err := NewExecutor(ServerOptions{Synthesis: qsyn, BaseURL: deadURL, Credentials: map[string]schemas.MCPOpenAPICredential{"QueryKey": {Value: schemas.NewSecretVar("qsecret")}}})
	require.NoError(t, err)
	res, err := deadExec.Execute(context.Background(), findTool(t, qsyn, "listPets"), nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.NotContains(t, fmt.Sprint(res.Content), "qsecret", "query credentials never leak through error text")

	// Operation-level servers win when base_url was not set explicitly.
	alt, altCap := newCaptureServer(t, nil)
	sdoc := parseFixture(t, "petstore-3.0.yaml")
	for i := range sdoc.Operations {
		if sdoc.Operations[i].ID == "listPets" {
			sdoc.Operations[i].Servers = []string{alt.URL + "/alt"}
		}
	}
	ssyn, err := Synthesize(sdoc, SynthesizeOptions{ClientName: "petstore"})
	require.NoError(t, err)
	implicit, err := NewExecutor(ServerOptions{Synthesis: ssyn, BaseURL: ts.URL + "/v1", BaseURLExplicit: false, HTTPClient: ts.Client()})
	require.NoError(t, err)
	_, err = implicit.Execute(context.Background(), findTool(t, ssyn, "listPets"), nil)
	require.NoError(t, err)
	assert.Equal(t, "/alt/pets", altCap.path)
	explicit, err := NewExecutor(ServerOptions{Synthesis: ssyn, BaseURL: ts.URL + "/v1", BaseURLExplicit: true, HTTPClient: ts.Client()})
	require.NoError(t, err)
	_, err = explicit.Execute(context.Background(), findTool(t, ssyn, "listPets"), nil)
	require.NoError(t, err)
	assert.Equal(t, "/v1/pets", cap.path, "an explicit base_url overrides operation servers")
}

func TestBuildRequest_HeaderResolverErrorSurfaces(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	exec, syn := petstoreExecutor(t, ts, func(o *ServerOptions) {
		o.Headers = func(context.Context) (http.Header, error) { return nil, fmt.Errorf("no credential for caller") }
	})
	_, err := exec.BuildRequest(context.Background(), findTool(t, syn, "listPets"), nil)
	require.ErrorContains(t, err, "no credential for caller")
}

func TestExecute_Responses(t *testing.T) {
	var mode string
	ts, _ := newCaptureServer(t, func(w http.ResponseWriter, _ *http.Request) {
		switch mode {
		case "error":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		case "empty":
			w.WriteHeader(http.StatusNoContent)
		case "binary":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0, 1, 2})
		case "big":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		case "untyped":
			_, _ = w.Write([]byte("plain words"))
		default:
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{"pets":[]}`))
		}
	})
	exec, syn := petstoreExecutor(t, ts, func(o *ServerOptions) { o.MaxResponseBytes = 40 })
	list := findTool(t, syn, "listPets")
	text := func(res interface{ GetContent() []interface{} }) string { return "" }
	_ = text

	call := func() (bool, string) {
		res, err := exec.Execute(context.Background(), list, nil)
		require.NoError(t, err)
		raw, err := json.Marshal(res.Content)
		require.NoError(t, err)
		return res.IsError, string(raw)
	}

	mode = ""
	isErr, content := call()
	assert.False(t, isErr)
	assert.Contains(t, content, `{\"pets\":[]}`)

	mode = "error"
	isErr, content = call()
	assert.True(t, isErr)
	assert.Contains(t, content, "HTTP 418")
	assert.Contains(t, content, "nope")

	mode = "empty"
	isErr, content = call()
	assert.False(t, isErr)
	assert.Contains(t, content, "HTTP 204 No Content")

	mode = "binary"
	isErr, content = call()
	assert.False(t, isErr)
	assert.Contains(t, content, "[binary response: image/png, 7 bytes]")

	mode = "big"
	isErr, content = call()
	assert.False(t, isErr)
	assert.Contains(t, content, "truncated at 40 bytes")
	assert.NotContains(t, content, strings.Repeat("x", 41))

	mode = "untyped"
	_, content = call()
	assert.Contains(t, content, "plain words", "untyped UTF-8 bodies are returned as text")
}

func TestStringify(t *testing.T) {
	assert.Equal(t, "1.5", stringify(1.5, ","))
	assert.Equal(t, "3", stringify(float64(3), ","))
	assert.Equal(t, "true", stringify(true, ","))
	assert.Equal(t, "", stringify(nil, ","))
	assert.Equal(t, "a|b", stringify([]any{"a", "b"}, "|"))
	assert.Equal(t, `{"k":"v"}`, stringify(map[string]any{"k": "v"}, ","))
	assert.Equal(t, "42", stringify(json.Number("42"), ","))
}
