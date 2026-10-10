package vectorstore

import (
	"context"
	"math"
	"time"
)

// boundedPageLimit narrows a caller's limit to the uint32 page size the Qdrant
// and Pinecone clients accept, substituting fallback for a non-positive one.
//
// Converting directly truncates. Nothing bounds limit, so a value above
// MaxUint32 wraps to an unrelated small page — and an exact multiple of 2^32
// wraps to zero, which a caller cannot distinguish from asking for nothing.
// Clamping instead keeps an oversized limit meaning what the caller intended:
// as much as the backend is willing to return.
func boundedPageLimit(limit int64, fallback uint32) uint32 {
	if limit <= 0 {
		return fallback
	}
	if limit > int64(math.MaxUint32) {
		return math.MaxUint32
	}
	return uint32(limit)
}

// withTimeout adds a timeout to the context if it is set.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	// No-op cancel to simplify call sites.
	return ctx, func() {}
}
