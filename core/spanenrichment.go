package bifrost

import (
	"context"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// applyContextSpanAttributes attaches the governance / identity enrichment
// carried on ctx to span, as both typed dimensions and attribute keys.
//
// It is the single live emitter of the context-sourced dimensions in
// schemas.EnrichmentDims, so TestContextSpanAttributesCoverRegistry pins this
// set to that registry: a dimension cannot be added to the registry (and read by
// the curated connectors) without being emitted here, and a renamed key surfaces
// as a mismatch.
func applyContextSpanAttributes(span *schemas.Span, ctx context.Context) {
	schemas.SpanEnrichmentFromContext(ctx).ApplyToSpan(span)
}
