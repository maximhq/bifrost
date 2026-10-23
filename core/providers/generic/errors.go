package generic

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// redactTransportError strips the request URL from a *url.Error. The URL carries the query
// string, which for query auth holds the injected key, and the caller and the log row must never
// see it. The host and path are kept so the error still says where the request was going.
func redactTransportError(err error) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || urlErr.URL == "" {
		return err
	}
	target := urlErr.URL
	if parsed, parseErr := url.Parse(target); parseErr == nil {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.User = nil
		target = parsed.String()
	} else if idx := strings.IndexAny(target, "?#"); idx >= 0 {
		target = target[:idx]
	}
	return &url.Error{Op: urlErr.Op, URL: target, Err: urlErr.Err}
}

// redactKeyFromHeaders removes the query credential from upstream response headers before they
// reach the caller or the log row. An upstream that redirects echoes the request query in
// Location, and the injected key rides along. A header that carries the key inside a URL loses
// the parameter; any other carrier is replaced wholesale. It is a no-op for other auth types.
func redactKeyFromHeaders(headers map[string]string, param, value string) {
	if param == "" || value == "" {
		return
	}
	escaped := url.QueryEscape(value)
	for name, hv := range headers {
		if !strings.Contains(hv, value) && !strings.Contains(hv, escaped) {
			continue
		}
		headers[name] = stripQueryParam(hv, param, value, escaped)
	}
}

func stripQueryParam(raw, param, value, escaped string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.RawQuery != "" {
		if values, qerr := url.ParseQuery(parsed.RawQuery); qerr == nil {
			delete(values, param)
			parsed.RawQuery = values.Encode()
			if out := parsed.String(); !strings.Contains(out, value) && !strings.Contains(out, escaped) {
				return out
			}
		}
	}
	return "[redacted]"
}

// passthroughTransportError maps a failure to reach the upstream onto the error the caller sees.
func passthroughTransportError(err error, latency time.Duration) *schemas.BifrostError {
	err = redactTransportError(err)
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return providerUtils.SetErrorLatency(&schemas.BifrostError{
			Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: err},
		}, latency)
	case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
	}
	return providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err), latency)
}
