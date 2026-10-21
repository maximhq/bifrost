package generic

import (
	"context"
	"errors"
	"net"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// passthroughTransportError maps a failure to reach the upstream onto the error the caller sees.
func passthroughTransportError(err error, latency time.Duration) *schemas.BifrostError {
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
