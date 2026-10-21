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
