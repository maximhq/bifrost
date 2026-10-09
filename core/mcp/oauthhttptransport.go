package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Shared OAuth connections outlive access tokens. Resolve credentials at the
// HTTP boundary so calls, pings and tool discovery all use the current token.
type refreshingOAuthTransport struct {
	base    http.RoundTripper
	origin  *url.URL
	resolve func(context.Context) (http.Header, error)
}

func (t *refreshingOAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	fail := func(err error) (*http.Response, error) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	// A request that is already cancelled must never reach the base transport,
	// even when the credential store can still produce a usable header. Surface
	// the context error so callers can match errors.Is(err, context.Canceled).
	if err := req.Context().Err(); err != nil {
		return fail(err)
	}
	// Never reattach credentials to a cross-origin redirect.
	if !strings.EqualFold(req.URL.Scheme, t.origin.Scheme) || !strings.EqualFold(req.URL.Host, t.origin.Host) {
		return fail(fmt.Errorf("MCP OAuth request changed origin"))
	}
	headers, err := t.resolve(req.Context())
	if err != nil {
		return fail(fmt.Errorf("resolve MCP OAuth credentials: %w", err))
	}
	// The resolver may ignore cancellation or cancel the context itself while
	// still returning valid credentials. Re-check before touching the network.
	if err := req.Context().Err(); err != nil {
		return fail(err)
	}
	if strings.TrimSpace(headers.Get("Authorization")) == "" {
		return fail(fmt.Errorf("MCP OAuth credentials missing authorization"))
	}
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", headers.Get("Authorization"))
	return t.base.RoundTrip(cloned)
}

func newRefreshingOAuthHTTPClient(existing *http.Client, target string, resolve func(context.Context) (http.Header, error)) (*http.Client, error) {
	origin, err := url.Parse(target)
	if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		return nil, fmt.Errorf("invalid MCP OAuth HTTP target")
	}
	result := &http.Client{}
	if existing != nil {
		*result = *existing
	}
	base := result.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	result.Transport = &refreshingOAuthTransport{base: base, origin: origin, resolve: resolve}
	return result, nil
}
