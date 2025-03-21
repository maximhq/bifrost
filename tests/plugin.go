package tests

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/interfaces"

	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

// Define a custom type for context key to avoid collisions
type contextKey string

const (
	traceIDKey contextKey = "traceID"
)

type Plugin struct {
	logger *logging.Logger
}

func (plugin *Plugin) PreHook(ctx *context.Context, req *interfaces.BifrostRequest) (*interfaces.BifrostRequest, error) {
	traceID := time.Now().Format("20060102_150405000")

	trace := plugin.logger.Trace(&logging.TraceConfig{
		Id:   traceID,
		Name: maxim.StrPtr("bifrost"),
	})

	trace.SetInput(fmt.Sprintf("New Request Incoming: %v", req))

	if ctx != nil {
		// Store traceID in context
		*ctx = context.WithValue(*ctx, traceIDKey, traceID)
	}

	return req, nil
}

func (plugin *Plugin) PostHook(ctxRef *context.Context, res *interfaces.BifrostResponse) (*interfaces.BifrostResponse, error) {
	// Get traceID from context
	if ctxRef != nil {
		ctx := *ctxRef
		traceID, ok := ctx.Value(traceIDKey).(string)
		if !ok {
			return res, fmt.Errorf("traceID not found in context")
		}

		plugin.logger.SetTraceOutput(traceID, fmt.Sprintf("Response: %v", res))
	}

	return res, nil
}
