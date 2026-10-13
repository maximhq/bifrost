package openapimcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	_, err = NewExecutor(ServerOptions{Synthesis: syn, BaseURL: "https://api.example.com/"})
	require.ErrorContains(t, err, "HTTP client", "the client carries the network policy; there is no safe default")
	exec, err := NewExecutor(ServerOptions{Synthesis: syn, BaseURL: "https://api.example.com/", HTTPClient: &http.Client{}})
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
	assert.JSONEq(t, `{"name":"Rex","tag":null,"owner":{"name":"Ann"}}`, cap.body, "an explicit null is sent; unknown properties are not")

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
	deadExec, err := NewExecutor(ServerOptions{Synthesis: qsyn, BaseURL: deadURL, HTTPClient: &http.Client{}, Credentials: map[string]schemas.MCPOpenAPICredential{"QueryKey": {Value: schemas.NewSecretVar("qsecret")}}})
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

// inlineExecutor builds an executor for an inline spec against ts.
func inlineExecutor(t *testing.T, ts *httptest.Server, spec string, mutate func(*ServerOptions)) (*Executor, *Synthesis) {
	t.Helper()
	syn := synthesizeSpec(t, spec)
	opts := ServerOptions{ClientName: "t", Synthesis: syn, BaseURL: ts.URL, BaseURLExplicit: true, HTTPClient: ts.Client()}
	if mutate != nil {
		mutate(&opts)
	}
	exec, err := NewExecutor(opts)
	require.NoError(t, err)
	return exec, syn
}

// TestBuildRequest_ExplicitNullIsSent: an explicit null clears a field in a
// PATCH; it is not the same as leaving the argument out.
func TestBuildRequest_ExplicitNullIsSent(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{
"/pets/{id}":{"patch":{"operationId":"patchPet","parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
  "requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{"tag":{"type":"string","nullable":true},"name":{"type":"string"}}}}}},"responses":{"200":{"description":"ok"}}}},
"/notes":{"put":{"operationId":"putNote","requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"string","nullable":true}}}},"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	_, err := exec.Execute(context.Background(), findTool(t, syn, "patchPet"), map[string]any{"id": "1", "tag": nil})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tag":null}`, cap.body)

	_, err = exec.Execute(context.Background(), findTool(t, syn, "putNote"), map[string]any{"body": nil})
	require.NoError(t, err)
	assert.Equal(t, "null", cap.body, "a present null single body is sent as JSON null")
}

// TestBuildRequest_ParameterStyles: deepObject, non-exploded form objects and
// label/matrix path styles are encoded the way the spec declares them.
func TestBuildRequest_ParameterStyles(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{
"/search":{"get":{"operationId":"search","parameters":[
  {"name":"filter","in":"query","style":"deepObject","explode":true,"schema":{"type":"object"}},
  {"name":"color","in":"query","style":"form","explode":false,"schema":{"type":"object"}}],"responses":{"200":{"description":"ok"}}}},
"/label/{id}":{"get":{"operationId":"byLabel","parameters":[{"name":"id","in":"path","required":true,"style":"label","schema":{"type":"array","items":{"type":"string"}}}],"responses":{"200":{"description":"ok"}}}},
"/matrix/{id}":{"get":{"operationId":"byMatrix","parameters":[{"name":"id","in":"path","required":true,"style":"matrix","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)

	_, err := exec.Execute(context.Background(), findTool(t, syn, "search"), map[string]any{
		"filter": map[string]any{"status": "open", "owner": "me"},
		"color":  map[string]any{"R": float64(100), "G": float64(200)},
	})
	require.NoError(t, err)
	assert.Equal(t, "color=G%2C200%2CR%2C100&filter%5Bowner%5D=me&filter%5Bstatus%5D=open", cap.query)

	_, err = exec.Execute(context.Background(), findTool(t, syn, "byLabel"), map[string]any{"id": []any{"a", "b"}})
	require.NoError(t, err)
	assert.Equal(t, "/label/.a,b", cap.path)

	_, err = exec.Execute(context.Background(), findTool(t, syn, "byMatrix"), map[string]any{"id": "5"})
	require.NoError(t, err)
	assert.Equal(t, "/matrix/;id=5", cap.path)
}

// TestBuildRequest_Cookies: cookie values are sent as configured (no URL
// escaping), and one cookie name appears once, the credential winning over
// the declared argument and the resolver.
func TestBuildRequest_Cookies(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},
"components":{"securitySchemes":{"Session":{"type":"apiKey","in":"cookie","name":"sid"}}},"security":[{"Session":[]}],
"paths":{"/me":{"get":{"operationId":"me","parameters":[{"name":"sid","in":"cookie","schema":{"type":"string"}},{"name":"pref","in":"cookie","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) {
		o.Credentials = map[string]schemas.MCPOpenAPICredential{"Session": {Value: schemas.NewSecretVar("a+b/c=")}}
		o.Headers = func(context.Context) (http.Header, error) {
			return http.Header{"Cookie": {"sid=resolver; theme=dark"}}, nil
		}
	})
	_, err := exec.Execute(context.Background(), findTool(t, syn, "me"), map[string]any{"sid": "model", "pref": "x y"})
	require.Error(t, err, "a space cannot be sent in a cookie value")
	assert.Contains(t, err.Error(), "cookie")

	_, err = exec.Execute(context.Background(), findTool(t, syn, "me"), map[string]any{"sid": "model", "pref": "compact"})
	require.NoError(t, err)
	got := map[string][]string{}
	for _, part := range strings.Split(cap.headers.Get("Cookie"), "; ") {
		name, value, _ := strings.Cut(part, "=")
		got[name] = append(got[name], value)
	}
	assert.Equal(t, map[string][]string{"sid": {"a+b/c="}, "pref": {"compact"}, "theme": {"dark"}}, got)
}

// TestBuildRequest_BaseURLWithQuery: a base URL's own query string is kept and
// the operation path goes into the URL path, not the query.
func TestBuildRequest_BaseURLWithQuery(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	exec, syn := petstoreExecutor(t, ts, func(o *ServerOptions) { o.BaseURL = ts.URL + "/v1?tenant=1#frag" })
	_, err := exec.Execute(context.Background(), findTool(t, syn, "listPets"), map[string]any{"limit": float64(2)})
	require.NoError(t, err)
	assert.Equal(t, "/v1/pets", cap.path)
	assert.Equal(t, "limit=2&tenant=1", cap.query)
}

// TestBuildRequest_MediaTypes: a parameter declared through content is encoded
// whole in its media type, and a vendor JSON body keeps its declared type.
func TestBuildRequest_MediaTypes(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{
"/find":{"get":{"operationId":"find","parameters":[{"name":"q","in":"query","content":{"application/json":{"schema":{"type":"object"}}}},{"name":"s","in":"query","content":{"application/json":{"schema":{"type":"string"}}}}],"responses":{"200":{"description":"ok"}}}},
"/things":{"post":{"operationId":"createThing","requestBody":{"content":{"application/vnd.api+json":{"schema":{"type":"object","properties":{"data":{"type":"object"}}}}}},"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	_, err := exec.Execute(context.Background(), findTool(t, syn, "find"), map[string]any{"q": map[string]any{"a": float64(1)}, "s": "hello"})
	require.NoError(t, err)
	assert.Equal(t, "q=%7B%22a%22%3A1%7D&s=%22hello%22", cap.query)

	_, err = exec.Execute(context.Background(), findTool(t, syn, "createThing"), map[string]any{"data": map[string]any{"id": "1"}})
	require.NoError(t, err)
	assert.Equal(t, "application/vnd.api+json", cap.headers.Get("Content-Type"))
	assert.JSONEq(t, `{"data":{"id":"1"}}`, cap.body)
}

// TestExecute_CrossHostRedirectKeepsCredentials: a redirect to another host is
// not followed when the request carries credentials or resolver headers; the
// model sees the redirect instead of the credentials reaching the new host.
func TestExecute_CrossHostRedirectKeepsCredentials(t *testing.T) {
	other, otherCap := newCaptureServer(t, nil)
	ts, _ := newCaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/stolen", http.StatusFound)
	})
	exec, syn := petstoreExecutor(t, ts, func(o *ServerOptions) {
		o.Credentials = map[string]schemas.MCPOpenAPICredential{"ApiKeyAuth": {Value: schemas.NewSecretVar("k-123")}}
		o.Headers = func(context.Context) (http.Header, error) { return http.Header{"X-Static": {"s"}}, nil }
	})
	res, err := exec.Execute(context.Background(), findTool(t, syn, "listPets"), map[string]any{})
	require.NoError(t, err)
	otherCap.mu.Lock()
	reached := otherCap.path
	otherCap.mu.Unlock()
	assert.Empty(t, reached, "the redirect target must not receive the request")
	assert.True(t, res.IsError, "the model sees the 3xx")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestExecute_HTTPSToHTTPRedirectKeepsCredentials: a redirect that keeps the
// host but drops to plain http is not followed, so credentials never travel
// unencrypted.
func TestExecute_HTTPSToHTTPRedirectKeepsCredentials(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.URL.Scheme+"://"+r.URL.Host+r.URL.Path+" key="+r.Header.Get("X-API-Key"))
		mu.Unlock()
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}
		if r.URL.Scheme == "https" {
			resp.StatusCode = http.StatusFound
			resp.Header.Set("Location", "http://api.example.com/plain")
		}
		return resp, nil
	})}
	syn, err := Synthesize(parseFixture(t, "petstore-3.0.yaml"), SynthesizeOptions{ClientName: "petstore"})
	require.NoError(t, err)
	exec, err := NewExecutor(ServerOptions{
		ClientName: "petstore", Synthesis: syn, BaseURL: "https://api.example.com/v1", BaseURLExplicit: true, HTTPClient: client,
		Credentials: map[string]schemas.MCPOpenAPICredential{"ApiKeyAuth": {Value: schemas.NewSecretVar("k-123")}},
	})
	require.NoError(t, err)
	res, err := exec.Execute(context.Background(), findTool(t, syn, "listPets"), map[string]any{})
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"https://api.example.com/v1/pets key=k-123"}, seen, "the plain-http redirect must not be followed")
	assert.True(t, res.IsError, "the model sees the 3xx")
}

// TestBuildRequest_CookieCredentialIsOneValue: a cookie credential is a single
// value. One the cookie header cannot carry is refused instead of being split
// into a shorter value or extra cookies.
func TestBuildRequest_CookieCredentialIsOneValue(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},
"components":{"securitySchemes":{"Session":{"type":"apiKey","in":"cookie","name":"sid"}}},"security":[{"Session":[]}],
"paths":{"/me":{"get":{"operationId":"me","responses":{"200":{"description":"ok"}}}}}}`
	for _, value := range []string{"a;admin=1", "a;b"} {
		exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) {
			o.Credentials = map[string]schemas.MCPOpenAPICredential{"Session": {Value: schemas.NewSecretVar(value)}}
		})
		_, err := exec.BuildRequest(context.Background(), findTool(t, syn, "me"), map[string]any{})
		require.Error(t, err, "credential %q", value)
		assert.Contains(t, err.Error(), `cookie "sid"`)
	}
}

// TestBuildRequest_Swagger2FormArrayCollectionFormat: Swagger 2.0 formData
// arrays follow their collectionFormat (csv by default); only multi repeats
// the key.
func TestBuildRequest_Swagger2FormArrayCollectionFormat(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"swagger":"2.0","info":{"title":"t","version":"1"},"consumes":["application/x-www-form-urlencoded"],"paths":{"/f":{"post":{"operationId":"submit","parameters":[
{"name":"csv","in":"formData","type":"array","items":{"type":"string"}},
{"name":"multi","in":"formData","type":"array","items":{"type":"string"},"collectionFormat":"multi"},
{"name":"pipes","in":"formData","type":"array","items":{"type":"string"},"collectionFormat":"pipes"}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	ab := []any{"a", "b"}
	_, err := exec.Execute(context.Background(), findTool(t, syn, "submit"), map[string]any{"csv": ab, "multi": ab, "pipes": ab})
	require.NoError(t, err)
	assert.Equal(t, "csv=a%2Cb&multi=a&multi=b&pipes=a%7Cb", cap.body)
}

// TestBuildRequest_HeaderSimpleStyle: header parameters use the simple style
// OpenAPI defines for them: arrays comma-joined (or with a Swagger 2.0
// collectionFormat delimiter), objects as key,value pairs, or key=value pairs
// when exploded.
func TestBuildRequest_HeaderSimpleStyle(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/h":{"get":{"operationId":"h","parameters":[
{"name":"X-Obj","in":"header","schema":{"type":"object"}},
{"name":"X-Obj-E","in":"header","explode":true,"schema":{"type":"object"}},
{"name":"X-List","in":"header","schema":{"type":"array","items":{"type":"string"}}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	obj := map[string]any{"role": "admin", "team": "x"}
	_, err := exec.Execute(context.Background(), findTool(t, syn, "h"), map[string]any{"X-Obj": obj, "X-Obj-E": obj, "X-List": []any{"a", "b"}})
	require.NoError(t, err)
	assert.Equal(t, "role,admin,team,x", cap.headers.Get("X-Obj"))
	assert.Equal(t, "role=admin,team=x", cap.headers.Get("X-Obj-E"))
	assert.Equal(t, "a,b", cap.headers.Get("X-List"))

	swagger := `{"swagger":"2.0","info":{"title":"t","version":"1"},"paths":{"/h":{"get":{"operationId":"h","parameters":[
{"name":"X-Pipes","in":"header","type":"array","items":{"type":"string"},"collectionFormat":"pipes"}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn = inlineExecutor(t, ts, swagger, nil)
	_, err = exec.Execute(context.Background(), findTool(t, syn, "h"), map[string]any{"X-Pipes": []any{"a", "b"}})
	require.NoError(t, err)
	assert.Equal(t, "a|b", cap.headers.Get("X-Pipes"))
}

// TestBuildRequest_OpenAPI3FormEncoding: an OpenAPI 3 form body follows the
// encoding object declared for each field; without one, arrays repeat the key.
func TestBuildRequest_OpenAPI3FormEncoding(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/f":{"post":{"operationId":"submit","requestBody":{"content":{"application/x-www-form-urlencoded":{
"schema":{"type":"object","properties":{"joined":{"type":"array","items":{"type":"string"}},"piped":{"type":"array","items":{"type":"string"}},"repeated":{"type":"array","items":{"type":"string"}}}},
"encoding":{"joined":{"style":"form","explode":false},"piped":{"style":"pipeDelimited"}}}}},"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	ab := []any{"a", "b"}
	_, err := exec.Execute(context.Background(), findTool(t, syn, "submit"), map[string]any{"joined": ab, "piped": ab, "repeated": ab})
	require.NoError(t, err)
	assert.Equal(t, "joined=a%2Cb&piped=a%7Cb&repeated=a&repeated=b", cap.body)
}

// TestBuildRequest_Swagger2PathArrayDelimiter: a Swagger 2.0 path array keeps
// its collectionFormat delimiter, like query and header arrays.
func TestBuildRequest_Swagger2PathArrayDelimiter(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"swagger":"2.0","info":{"title":"t","version":"1"},"paths":{"/items/{ids}":{"get":{"operationId":"get","parameters":[
{"name":"ids","in":"path","required":true,"type":"array","items":{"type":"string"},"collectionFormat":"pipes"}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	_, err := exec.Execute(context.Background(), findTool(t, syn, "get"), map[string]any{"ids": []any{"a", "b"}})
	require.NoError(t, err)
	assert.Contains(t, []string{"/items/a%7Cb", "/items/a|b"}, cap.path)
}

// TestBuildRequest_CookieFormStyle: cookie parameters use the form style: an
// exploded object becomes one cookie per member, an unexploded one and arrays
// become one comma-joined value.
func TestBuildRequest_CookieFormStyle(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/c":{"get":{"operationId":"c","parameters":[
{"name":"prefs","in":"cookie","schema":{"type":"object"}},
{"name":"packed","in":"cookie","explode":false,"schema":{"type":"object"}},
{"name":"ids","in":"cookie","explode":false,"schema":{"type":"array","items":{"type":"string"}}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	_, err := exec.Execute(context.Background(), findTool(t, syn, "c"), map[string]any{
		"prefs": map[string]any{"role": "admin"}, "packed": map[string]any{"a": "1"}, "ids": []any{"x", "y"},
	})
	require.NoError(t, err)
	got := map[string]string{}
	for _, part := range strings.Split(cap.headers.Get("Cookie"), "; ") {
		name, value, _ := strings.Cut(part, "=")
		got[name] = value
	}
	assert.Equal(t, map[string]string{"role": "admin", "packed": "a,1", "ids": "x,y"}, got)
}

// TestBuildRequest_FormObjectEncoding: an object field of a form body follows
// the style declared in its encoding entry (exploded form, packed form or
// deepObject); without an entry it stays a JSON value, the OpenAPI default.
func TestBuildRequest_FormObjectEncoding(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/f":{"post":{"operationId":"submit","requestBody":{"required":true,"content":{"application/x-www-form-urlencoded":{
"schema":{"type":"object","properties":{"filter":{"type":"object"},"packed":{"type":"object"},"meta":{"type":"object"},"plain":{"type":"object"}}},
"encoding":{"filter":{"style":"form","explode":true},"packed":{"style":"form","explode":false},"meta":{"style":"deepObject","explode":true}}}}},"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	_, err := exec.Execute(context.Background(), findTool(t, syn, "submit"), map[string]any{
		"filter": map[string]any{"role": "admin"}, "packed": map[string]any{"a": "1"}, "meta": map[string]any{"k": "v"}, "plain": map[string]any{"x": "y"},
	})
	require.NoError(t, err)
	vals, err := url.ParseQuery(cap.body)
	require.NoError(t, err)
	assert.Equal(t, "admin", vals.Get("role"))
	assert.Empty(t, vals.Get("filter"))
	assert.Equal(t, "a,1", vals.Get("packed"))
	assert.Equal(t, "v", vals.Get("meta[k]"))
	assert.JSONEq(t, `{"x":"y"}`, vals.Get("plain"))
}

// TestBuildRequest_JSONParameterSendsNull: an explicit null for a parameter
// declared through content: application/json is a JSON value and is sent.
func TestBuildRequest_JSONParameterSendsNull(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/s":{"get":{"operationId":"s","parameters":[
{"name":"q","in":"query","required":true,"content":{"application/json":{"schema":{"type":"object","nullable":true}}}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	req, err := exec.BuildRequest(context.Background(), findTool(t, syn, "s"), map[string]any{"q": nil})
	require.NoError(t, err)
	assert.Equal(t, "q=null", req.URL.RawQuery)
}

// TestBuildRequest_CookieNameMustBeAToken: a cookie name taken from model
// arguments (an exploded object's keys) that is not a valid cookie name is
// refused, so it cannot smuggle in a cookie that shadows the credential.
func TestBuildRequest_CookieNameMustBeAToken(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"components":{"securitySchemes":{"S":{"type":"apiKey","in":"cookie","name":"sid"}}},"security":[{"S":[]}],
"paths":{"/c":{"get":{"operationId":"c","parameters":[{"name":"prefs","in":"cookie","schema":{"type":"object"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) {
		o.Credentials = map[string]schemas.MCPOpenAPICredential{"S": {Value: schemas.NewSecretVar("REAL")}}
	})
	_, err := exec.BuildRequest(context.Background(), findTool(t, syn, "c"), map[string]any{"prefs": map[string]any{"x; sid": "evil"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cookie name")
}

// TestBuildRequest_OptionalBodyRequiresItsFieldsWhenSent: an optional body may
// be left out entirely, but a body that is sent must carry its required fields.
func TestBuildRequest_OptionalBodyRequiresItsFieldsWhenSent(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/t":{"post":{"operationId":"t","requestBody":{"content":{"application/json":{"schema":{"type":"object","required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"string"}}}}}},"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	_, err := exec.BuildRequest(context.Background(), findTool(t, syn, "t"), map[string]any{"b": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"a"`)
	_, err = exec.BuildRequest(context.Background(), findTool(t, syn, "t"), map[string]any{})
	require.NoError(t, err, "no body at all is still allowed")
	_, err = exec.BuildRequest(context.Background(), findTool(t, syn, "t"), map[string]any{"a": "1"})
	require.NoError(t, err)
}

// TestBuildRequest_QueryCredentialIsTheOnlyValue: a query credential is the
// one value sent under its name, even when the base URL's own query or the
// model's raw querystring carries the same name.
func TestBuildRequest_QueryCredentialIsTheOnlyValue(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	security := `"components":{"securitySchemes":{"Q":{"type":"apiKey","in":"query","name":"api_key"}}},"security":[{"Q":[]}]`
	creds := func(o *ServerOptions) {
		o.Credentials = map[string]schemas.MCPOpenAPICredential{"Q": {Value: schemas.NewSecretVar("secret")}}
	}

	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},` + security + `,"paths":{"/a":{"get":{"operationId":"a","responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) { creds(o); o.BaseURL = ts.URL + "?api_key=old&tenant=1" })
	req, err := exec.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, []string{"secret"}, req.URL.Query()["api_key"])
	assert.Equal(t, "1", req.URL.Query().Get("tenant"))

	qs := `{"openapi":"3.2.0","info":{"title":"t","version":"1"},` + security + `,"paths":{"/b":{"get":{"operationId":"b","parameters":[{"name":"q","in":"querystring","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn = inlineExecutor(t, ts, qs, creds)
	req, err = exec.BuildRequest(context.Background(), findTool(t, syn, "b"), map[string]any{"q": "api_key=evil&x=1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"secret"}, req.URL.Query()["api_key"])
	assert.Equal(t, "1", req.URL.Query().Get("x"))
}

// TestBuildRequest_JSONQuerystring: an OpenAPI 3.2 querystring declared
// through content: application/json keeps its schema and is sent as the
// percent-encoded JSON document.
func TestBuildRequest_JSONQuerystring(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.com"}],"paths":{"/s":{"get":{"operationId":"s","parameters":[
{"name":"filter","in":"querystring","content":{"application/json":{"schema":{"type":"object","properties":{"a":{"type":"integer"}}}}}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	tool := findTool(t, syn, "s")
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
	assert.Equal(t, "object", schema["properties"].(map[string]any)["filter"].(map[string]any)["type"], "the JSON querystring keeps its object schema")
	req, err := exec.BuildRequest(context.Background(), tool, map[string]any{"filter": map[string]any{"a": float64(1)}})
	require.NoError(t, err)
	assert.Equal(t, url.QueryEscape(`{"a":1}`), req.URL.RawQuery)
}

// TestBuildRequest_ResolverHeadersWinOverArguments: a header or cookie the
// header resolver sets (static config headers, the credential store) is the
// configured credential; a declared parameter of the same name cannot replace
// it with a model-chosen value.
func TestBuildRequest_ResolverHeadersWinOverArguments(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/a":{"get":{"operationId":"a","parameters":[
{"name":"X-API-Key","in":"header","schema":{"type":"string"}},
{"name":"sid","in":"cookie","schema":{"type":"string"}},
{"name":"X-Trace","in":"header","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) {
		o.Headers = func(context.Context) (http.Header, error) {
			return http.Header{"X-Api-Key": {"configured"}, "Cookie": {"sid=real"}}, nil
		}
	})
	_, err := exec.Execute(context.Background(), findTool(t, syn, "a"), map[string]any{"X-API-Key": "YOUR_API_KEY", "sid": "fake", "X-Trace": "t-1"})
	require.NoError(t, err)
	assert.Equal(t, "configured", cap.headers.Get("X-API-Key"))
	assert.Equal(t, "sid=real", cap.headers.Get("Cookie"))
	assert.Equal(t, "t-1", cap.headers.Get("X-Trace"), "a header the resolver does not set still carries the argument")
}

// TestBuildRequest_UnresolvedOperationServerIsNotReplaced: an operation whose
// own server is relative and could not be resolved is not silently sent to the
// document's server, which may be a different endpoint; an explicit base URL
// override still serves it.
func TestBuildRequest_UnresolvedOperationServerIsNotReplaced(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.com/v1"}],"paths":{"/a":{"servers":[{"url":"/v2"}],"get":{"operationId":"a","responses":{"200":{"description":"ok"}}}}}}`
	syn := synthesizeSpec(t, spec)
	exec, err := NewExecutor(ServerOptions{ClientName: "t", Synthesis: syn, BaseURL: "https://api.example.com/v1", HTTPClient: ts.Client()})
	require.NoError(t, err)
	_, err = exec.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/v2")

	explicit, err := NewExecutor(ServerOptions{ClientName: "t", Synthesis: syn, BaseURL: ts.URL, BaseURLExplicit: true, HTTPClient: ts.Client()})
	require.NoError(t, err)
	_, err = explicit.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{})
	require.NoError(t, err, "an explicit base URL overrides every operation server")
}

// TestBuildRequest_BaseURLQueryIsAuthoritative: the base URL's own query keys
// are configuration; a model argument (an exploded object's key, or the raw
// querystring) cannot add a value under them.
func TestBuildRequest_BaseURLQueryIsAuthoritative(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"paths":{
"/a":{"get":{"operationId":"a","parameters":[{"name":"filter","in":"query","schema":{"type":"object"}}],"responses":{"200":{"description":"ok"}}}},
"/b":{"get":{"operationId":"b","parameters":[{"name":"q","in":"querystring","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) { o.BaseURL = ts.URL + "?key=ADMIN" })
	req, err := exec.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{"filter": map[string]any{"key": "evil", "x": "1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"ADMIN"}, req.URL.Query()["key"])
	assert.Equal(t, "1", req.URL.Query().Get("x"))
	req, err = exec.BuildRequest(context.Background(), findTool(t, syn, "b"), map[string]any{"q": "key=evil&y=2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"ADMIN"}, req.URL.Query()["key"])
	assert.Equal(t, "2", req.URL.Query().Get("y"))
}

// TestBuildRequest_ConfiguredValueSatisfiesRequiredParameter: a required
// header or cookie parameter that configuration supplies (resolver headers or
// a spec-mapped credential) does not have to come from the model.
func TestBuildRequest_ConfiguredValueSatisfiesRequiredParameter(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},
"components":{"securitySchemes":{"S":{"type":"apiKey","in":"cookie","name":"sid"}}},"security":[{"S":[]}],
"paths":{"/a":{"get":{"operationId":"a","parameters":[
{"name":"X-API-Key","in":"header","required":true,"schema":{"type":"string"}},
{"name":"sid","in":"cookie","required":true,"schema":{"type":"string"}},
{"name":"X-Other","in":"header","required":true,"schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) {
		o.Headers = func(context.Context) (http.Header, error) { return http.Header{"X-Api-Key": {"configured"}}, nil }
		o.Credentials = map[string]schemas.MCPOpenAPICredential{"S": {Value: schemas.NewSecretVar("real")}}
	})
	_, err := exec.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{})
	require.Error(t, err, "a required header nothing configures is still required")
	assert.Contains(t, err.Error(), "X-Other")
	_, err = exec.Execute(context.Background(), findTool(t, syn, "a"), map[string]any{"X-Other": "o"})
	require.NoError(t, err)
	assert.Equal(t, "configured", cap.headers.Get("X-API-Key"))
	assert.Equal(t, "sid=real", cap.headers.Get("Cookie"))
}

// TestBuildRequest_ConfiguredQueryValueSatisfiesRequiredParameter: a required
// query parameter that a query credential or the base URL's own query supplies
// does not have to come from the model, the same rule headers and cookies follow.
func TestBuildRequest_ConfiguredQueryValueSatisfiesRequiredParameter(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},
"components":{"securitySchemes":{"K":{"type":"apiKey","in":"query","name":"api_key"}}},"security":[{"K":[]}],
"paths":{"/a":{"get":{"operationId":"a","parameters":[
{"name":"api_key","in":"query","required":true,"schema":{"type":"string"}},
{"name":"tenant","in":"query","required":true,"schema":{"type":"string"}},
{"name":"q","in":"query","required":true,"schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) {
		o.BaseURL = ts.URL + "?tenant=acme"
		o.Credentials = map[string]schemas.MCPOpenAPICredential{"K": {Value: schemas.NewSecretVar("real-key")}}
	})
	_, err := exec.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{})
	require.Error(t, err, "a required query parameter nothing configures is still required")
	assert.Contains(t, err.Error(), `"q"`)
	req, err := exec.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{"q": "x"})
	require.NoError(t, err)
	assert.Equal(t, "real-key", req.URL.Query().Get("api_key"))
	assert.Equal(t, "acme", req.URL.Query().Get("tenant"))
	assert.Equal(t, "x", req.URL.Query().Get("q"))
}

// TestBuildRequest_CallerHeaderSatisfiesItsSchemeInARequirement: a requirement
// that pairs a header scheme each caller forwards (per-user headers, so no
// stored credential) with a stored query credential still applies: the
// forwarded header counts for its scheme and the stored query key is sent.
func TestBuildRequest_CallerHeaderSatisfiesItsSchemeInARequirement(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},
"components":{"securitySchemes":{"User":{"type":"apiKey","in":"header","name":"X-User-Key"},"App":{"type":"apiKey","in":"query","name":"app_key"},"Bearer":{"type":"http","scheme":"bearer"}}},
"security":[{"User":[],"App":[]}],
"paths":{"/a":{"get":{"operationId":"a","responses":{"200":{"description":"ok"}}}},
"/b":{"get":{"operationId":"b","security":[{"Bearer":[],"App":[]}],"responses":{"200":{"description":"ok"}}}}}}`
	forwarded := http.Header{}
	exec, syn := inlineExecutor(t, ts, spec, func(o *ServerOptions) {
		o.Credentials = map[string]schemas.MCPOpenAPICredential{"App": {Value: schemas.NewSecretVar("app-secret")}}
		o.Headers = func(context.Context) (http.Header, error) { return forwarded.Clone(), nil }
	})

	req, err := exec.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{})
	require.NoError(t, err)
	assert.Empty(t, req.URL.Query().Get("app_key"), "without the caller's header the requirement is not met")

	forwarded.Set("X-User-Key", "caller-key")
	forwarded.Set("Authorization", "Bearer caller-token")
	req, err = exec.BuildRequest(context.Background(), findTool(t, syn, "a"), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, "caller-key", req.Header.Get("X-User-Key"))
	assert.Equal(t, "app-secret", req.URL.Query().Get("app_key"), "the stored credential of the same requirement is sent")

	req, err = exec.BuildRequest(context.Background(), findTool(t, syn, "b"), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, "Bearer caller-token", req.Header.Get("Authorization"))
	assert.Equal(t, "app-secret", req.URL.Query().Get("app_key"))
}

// TestBuildRequest_PathValueCannotFormADotSegment: a path parameter is checked
// as it will be sent, after its style is applied, so no value can turn into an
// empty, "." or ".." segment that walks the request off the operation's path.
func TestBuildRequest_PathValueCannotFormADotSegment(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/allowed/{id}/action":{"post":{"operationId":"act","parameters":[
{"name":"id","in":"path","required":true,"style":"label","explode":true,"schema":{"type":"array","items":{"type":"string"}}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	for _, value := range []any{[]any{"", ""}, []any{"."}, []any{""}} {
		_, err := exec.BuildRequest(context.Background(), findTool(t, syn, "act"), map[string]any{"id": value})
		require.Error(t, err, "value %v", value)
	}
	req, err := exec.BuildRequest(context.Background(), findTool(t, syn, "act"), map[string]any{"id": []any{"a", "b"}})
	require.NoError(t, err)
	assert.Equal(t, "/allowed/.a.b/action", req.URL.EscapedPath())

	// A "/" in a value is escaped as %2F, but an upstream that decodes it and
	// then normalizes the path would still walk up: no part may be "." or "..".
	plain := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/v1/items/{id}":{"get":{"operationId":"get","parameters":[
{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn = inlineExecutor(t, ts, plain, nil)
	for _, value := range []string{"../admin", "../../admin", "a/..", "a/./b", ".."} {
		_, err := exec.BuildRequest(context.Background(), findTool(t, syn, "get"), map[string]any{"id": value})
		require.Error(t, err, "value %q", value)
	}
	req, err = exec.BuildRequest(context.Background(), findTool(t, syn, "get"), map[string]any{"id": "a/b"})
	require.NoError(t, err)
	assert.Equal(t, "/v1/items/a%2Fb", req.URL.EscapedPath())
}

// TestBuildRequest_OptionalQuerystringRequiresItsFieldsWhenSent: an optional
// OpenAPI 3.2 querystring may be left out entirely, but once any of its fields
// is sent, its required fields must be sent too.
func TestBuildRequest_OptionalQuerystringRequiresItsFieldsWhenSent(t *testing.T) {
	ts, _ := newCaptureServer(t, nil)
	spec := `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"paths":{"/s":{"get":{"operationId":"s","parameters":[
{"name":"qs","in":"querystring","schema":{"type":"object","required":["q"],"properties":{"q":{"type":"string"},"page":{"type":"integer"}}}}],"responses":{"200":{"description":"ok"}}}}}}`
	exec, syn := inlineExecutor(t, ts, spec, nil)
	tool := findTool(t, syn, "s")
	_, err := exec.BuildRequest(context.Background(), tool, map[string]any{"page": float64(2)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"q"`)
	_, err = exec.BuildRequest(context.Background(), tool, map[string]any{})
	require.NoError(t, err, "the whole querystring can be left out")
	req, err := exec.BuildRequest(context.Background(), tool, map[string]any{"q": "x", "page": float64(2)})
	require.NoError(t, err)
	assert.Equal(t, "x", req.URL.Query().Get("q"))
}
