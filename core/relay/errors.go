package relay

import (
	"context"
	"errors"
	"fmt"
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
// Location, and the injected key rides along, possibly re-encoded (lower-case escapes, + for a
// space). A header that carries the key inside a URL loses the parameter, matched by name; any
// other carrier is replaced wholesale. It is a no-op for other auth types.
func redactKeyFromHeaders(headers map[string]string, param, value string) {
	if param == "" || value == "" {
		return
	}
	for name, hv := range headers {
		if !mentionsKey(hv, value) {
			continue
		}
		headers[name] = stripQueryParam(hv, param, value)
	}
}

// mentionsKey reports whether a header value carries the key, literally or under any valid
// percent-encoding of it. Only a value with an escape is decoded.
func mentionsKey(hv, value string) bool {
	if strings.Contains(hv, value) {
		return true
	}
	if !strings.ContainsAny(hv, "%+") {
		return false
	}
	decoded, err := url.QueryUnescape(hv)
	return err == nil && strings.Contains(decoded, value)
}

func stripQueryParam(raw, param, value string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.RawQuery != "" {
		if values, qerr := url.ParseQuery(parsed.RawQuery); qerr == nil {
			delete(values, param)
			parsed.RawQuery = values.Encode()
			if out := parsed.String(); !mentionsKey(out, value) {
				return out
			}
		}
	}
	return "[redacted]"
}

// transportError maps a failure to reach the upstream onto the error the caller sees.
func transportError(err error, latency time.Duration, limits relayTimeouts) *schemas.BifrostError {
	err = redactTransportError(err)
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return providerUtils.SetErrorLatency(&schemas.BifrostError{
			Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: err},
		}, latency)
	// A stream that goes quiet past the idle timeout is a timeout, as largeResponseReadError
	// classifies the same stall on an LLM reply; the sentinel is a plain error, not a net.Error.
	case errors.Is(err, providerUtils.ErrStreamIdleTimeout), errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(limits.message(err), err), latency)
	}
	// Any other failure to reach the upstream is a connection failure, as on the LLM path: 502
	// with provider_connection_failed, not an internal error.
	return providerUtils.SetErrorLatency(providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), latency)
}

// relayTimeouts are a relay's own timeouts, so a timeout error names the setting that fired rather
// than the provider network config, which relays do not use.
type relayTimeouts struct {
	relay         string
	wholeSeconds  int // timeout_seconds: a buffered reply's whole response
	streamSeconds int // stream_idle_timeout_in_seconds: silence between stream chunks
}

func (t relayTimeouts) message(err error) string {
	if errors.Is(err, providerUtils.ErrStreamIdleTimeout) {
		return fmt.Sprintf("the upstream sent nothing for %s; raise stream_idle_timeout_in_seconds in relay %q's network settings", seconds(t.streamSeconds), t.relay)
	}
	return fmt.Sprintf("the upstream did not answer within %s; raise timeout_seconds in relay %q's network settings", seconds(t.wholeSeconds), t.relay)
}

func seconds(n int) string {
	if n == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", n)
}
