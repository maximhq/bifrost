package tokencache

import (
	"errors"
	"net/http"
	"strings"
)

// maxTokenRedirects mirrors net/http's default limit, kept when the caller set no policy.
const maxTokenRedirects = 10

// guardRedirects returns a shallow copy of hc whose redirect policy refuses to carry a token
// request anywhere the original token_url check would have refused. A 307/308 replays the
// POST body, and that body (or the Basic header) is the client secret or a signed assertion,
// so a hop to plain http on a non-loopback host, or any https-to-http downgrade, is cut off
// before the request is sent. Hops that pass are handed to the caller's own CheckRedirect, or
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

// checkTokenRedirect is the policy itself: the destination must be https or loopback http,
// and an https hop may never lead to http, loopback included. The URLs stay out of the error
// because a redirect target can carry the credential in its query.
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
	return nil
}
