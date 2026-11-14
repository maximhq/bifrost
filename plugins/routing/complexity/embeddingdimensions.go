// Remembering the vector width a provider and model return, so a restart need not re-measure it.
package complexity

import (
	"context"

	"github.com/maximhq/bifrost/framework/vectorstore"
)

// EmbeddingDimensionStore remembers the vector width a provider and model
// return, across restarts.
//
// The width is the one input to a generation's identity that cannot be derived
// from configuration — it has to be observed by embedding something. Without a
// memory of it, every boot spends a batch of provider calls re-learning a
// constant, even when the generation those calls identify is already complete
// in the vector store and needs nothing.
//
// A remembered width is a hint and never authority: it is only ever used to
// attempt to adopt an already-complete generation, never to decide the shape of
// vectors being written. A stale or wrong value therefore costs one failed
// lookup and falls back to measuring, which is what would have happened anyway.
type EmbeddingDimensionStore interface {
	// Dimension returns the remembered width for an embedding identity.
	Dimension(identity string) (int, bool)
	// Remember records a width observed from a completed warm.
	Remember(identity string, dimension int)
}

// adoptRememberedGeneration reports whether a previously observed width lets
// this configuration's generation be adopted with no provider call.
//
// A remembered width that no longer matches what the provider returns — a model
// re-versioned under the same name, say — simply fails to find its marker here
// and falls through to measuring, so a stale memory costs one lookup and never
// produces a wrong generation.
func adoptRememberedGeneration(
	ctx context.Context,
	dimensions EmbeddingDimensionStore,
	store vectorstore.VectorStore,
	config *AnalyzerConfig,
	exemplars []semanticExemplar,
) (string, int, bool) {
	if dimensions == nil || config == nil || config.Semantic == nil {
		return "", 0, false
	}
	dimension, ok := dimensions.Dimension(semanticEmbeddingIdentity(config.Semantic))
	if !ok || dimension < 2 {
		return "", 0, false
	}
	namespace, adopted, err := adoptSemanticGeneration(ctx, store, config, exemplars, dimension)
	if err != nil || !adopted {
		return "", 0, false
	}
	return namespace, dimension, true
}
