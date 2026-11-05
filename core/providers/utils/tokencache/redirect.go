package tokencache

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// maxTokenRedirects mirrors net/http's default limit, kept when the caller set no policy.
const maxTokenRedirects = 10

// guardRedirects returns a shallow copy of hc whose redirect policy refuses to carry a token
// request anywhere the original token_url check would have refused. A 307/308 replays the
// POST body, and that body (or the Basic header) is the client secret or a signed assertion,
// so a hop to plain http on a non-loopback host, any https-to-http downgrade, or a hop to
// another origin is cut off before the request is sent. Hops that pass are handed to the caller's own CheckRedirect, or
// to net/http's default ten-hop limit when there is none. Runs once per minter, never per
// request; the copy shares the Transport, so proxy and TLS settings are unchanged.
func guardRedirects(hc *http.Client) *http.Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	guarded := *hc
	inner := hc.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := checkTokenRedirect(req, via); err != nil {
			return err
		}
		if inner != nil {
			return inner(req, via)
		}
		if len(via) >= maxTokenRedirects {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &guarded
}

// checkTokenRedirect is the policy itself: the destination must be https or loopback http, an
// https hop may never lead to http (loopback included), and every hop must stay on the origin
// of the first request, the token_url the operator configured. A 307/308 replays the POST, so
// a hop to any other origin would hand it the client secret (body auth) or the signed
// assertion. The URLs stay out of the error because a redirect target can carry the credential
// in its query.
func checkTokenRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || req == nil || req.URL == nil {
		return nil
	}
	if !httpsOrLoopbackURL(req.URL.String()) {
		return errors.New("oauth token endpoint redirect to a non-https, non-loopback url refused")
	}
	prev := via[len(via)-1].URL
	if prev != nil && strings.EqualFold(prev.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return errors.New("oauth token endpoint redirect from https to http refused")
	}
	if first := via[0].URL; first != nil && origin(first) != origin(req.URL) {
		return errors.New("oauth token endpoint redirect to another origin refused")
	}
	return nil
}

// origin is a URL's scheme, host and port, lowercased, with the scheme's default port filled
// in so https://idp and https://idp:443 compare equal.
func origin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}
