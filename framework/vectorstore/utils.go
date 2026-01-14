package vectorstore

import (
	"context"
	"time"
)

// withTimeout adds a timeout to the context if it is set.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	// No-op cancel to simplify call sites.
	return ctx, func() {}
}
