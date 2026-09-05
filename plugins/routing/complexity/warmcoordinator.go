// Electing one warmer per generation, so peers sharing a vector store do not all embed the same phrases.
package complexity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
)

const (
	// semanticWarmClaimTTL bounds how long one node may be presumed to be
	// warming before its peers stop waiting. It has to exceed a realistic warm:
	// the phrase ceiling is 750, which is 24 sequential provider batches, so a
	// slow provider can legitimately take several minutes. Overshooting only
	// delays a peer's takeover after a crash; undershooting reintroduces exactly
	// the duplicate warm this coordination exists to remove.
	semanticWarmClaimTTL = 15 * time.Minute
	// semanticWarmPublishTTL keeps a completed generation discoverable long
	// after its warm, so a node that restarts or joins later still adopts it
	// instead of re-embedding. Entries are keyed by content, so a stale one is
	// unreachable rather than wrong.
	semanticWarmPublishTTL = 24 * time.Hour
	// semanticWarmWaitTimeout caps how long a waiting node defers to a peer that
	// still holds its claim but has published nothing. Reaching it means warming
	// locally, which is what would have happened with no coordination at all.
	semanticWarmWaitTimeout = 2 * time.Minute
	// semanticWarmPollInterval is how often a waiting node re-checks. Warms take
	// seconds at minimum, so polling faster buys nothing.
	semanticWarmPollInterval = 500 * time.Millisecond
)

// WarmGeneration is what one node tells its peers about a generation it warmed.
// It carries no phrase text and no vectors — only the two facts a peer cannot
// derive on its own without embedding: the vector width the provider returned,
// and the namespace that width produced.
type WarmGeneration struct {
	Dimension int
	Namespace string
}

// WarmCoordinator lets one node warm an exemplar generation while its peers
// wait for the result instead of embedding the same phrases against the same
// shared store.
//
// Every method is best-effort by design. The backing store replicates
// asynchronously with last-writer-wins, so two nodes can both win a claim and
// a published generation can arrive late or not at all. That is tolerable
// because coordination here is a cost optimization, not a correctness
// mechanism: duplicate warms converge on identical, content-addressed records,
// and any failure falls back to warming locally.
type WarmCoordinator interface {
	// Claim reports whether this node should do the warming for key.
	Claim(key string, ttl time.Duration) (bool, error)
	// ClaimHeld reports whether any node still holds the claim. A waiting node
	// uses this to notice that the warmer died rather than waiting out its own
	// timeout.
	ClaimHeld(key string) bool
	// Release drops the claim so a peer can take over promptly.
	Release(key string)
	// Publish records a completed generation for peers to adopt.
	Publish(key string, generation WarmGeneration, ttl time.Duration) error
	// Lookup returns a previously published generation, if one is visible.
	Lookup(key string) (WarmGeneration, bool)
}

// semanticWarmKey identifies the embedding work one configuration implies,
// without depending on its outcome.
//
// It deliberately excludes the vector dimension that semanticFingerprint
// includes. The dimension is only knowable by embedding something, so a key
// containing it could not be computed until every node had already paid for a
// batch of embeddings — which is most of the cost this coordination removes.
// Everything that decides what gets embedded is here: the provider, the model,
// and the phrases themselves, order-insensitively.
func semanticWarmKey(config *AnalyzerConfig, exemplars []semanticExemplar) string {
	if config == nil || config.Semantic == nil || len(exemplars) == 0 {
		return ""
	}
	canonical := append([]semanticExemplar(nil), exemplars...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Tier != canonical[j].Tier {
			return canonical[i].Tier < canonical[j].Tier
		}
		return canonical[i].Phrase < canonical[j].Phrase
	})

	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "semantic-warm-v1\x00%s\x00%s\x00", config.Semantic.Provider, config.Semantic.EmbeddingModel)
	for _, exemplar := range canonical {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", exemplar.Tier, exemplar.Phrase)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// coordinatedWarmSemanticExemplars warms one generation, deferring to a peer
// that is already warming the same one.
//
// Without this, a configuration save fans out to every node at once — enterprise
// gossips the change, so they all reload within milliseconds — and each one
// embeds the identical phrase set against the identical shared namespace. The
// completion marker that would let a late node skip the work is written last,
// so nodes warming concurrently never see each other's.
//
// Coordination is advisory throughout. Any error, a missing coordinator, a lost
// race, or a peer that claims and then dies all end in the same place: warming
// locally, exactly as if this function were not here.
func coordinatedWarmSemanticExemplars(
	ctx context.Context,
	coordinator WarmCoordinator,
	dimensions EmbeddingDimensionStore,
	claimer GenerationClaimer,
	store vectorstore.VectorStore,
	config *AnalyzerConfig,
	embed EmbeddingFunc,
	embedBatch BatchEmbeddingFunc,
	cache *semanticEmbeddingCache,
) (int, string, int, error) {
	exemplars := semanticExemplars(config)
	if len(exemplars) == 0 {
		return warmSemanticExemplars(ctx, claimer, store, config, embed, embedBatch, cache)
	}

	// Nothing below needs a provider if this generation is already complete and
	// its width is remembered. This is the ordinary restart: the configuration
	// has not changed, the vectors are still in the store, and the only reason
	// to call the provider at all would be to re-measure a constant.
	if namespace, dimension, ok := adoptRememberedGeneration(ctx, dimensions, store, config, exemplars); ok {
		cache.useIdentity(semanticEmbeddingIdentity(config.Semantic))
		cache.retain(exemplars)
		if claimer != nil {
			claimer.ClaimGeneration(ctx, namespace)
		}
		return len(exemplars), namespace, dimension, nil
	}

	// Every path that actually measures a width records it, so the next boot can
	// take the fast path above instead of measuring again.
	warmAndRemember := func() (int, string, int, error) {
		loaded, namespace, dimension, err := warmSemanticExemplars(ctx, claimer, store, config, embed, embedBatch, cache)
		if err == nil && dimensions != nil && dimension >= 2 {
			dimensions.Remember(semanticEmbeddingIdentity(config.Semantic), dimension)
		}
		return loaded, namespace, dimension, err
	}

	if coordinator == nil {
		return warmAndRemember()
	}
	key := semanticWarmKey(config, exemplars)
	if key == "" {
		return warmAndRemember()
	}

	// A coordinator that cannot answer must never be able to stop a warm, so an
	// error means this node proceeds anyway. It does not mean this node holds
	// the claim: some peer may, and releasing a claim we never acquired would
	// delete theirs.
	owned, err := coordinator.Claim(key, semanticWarmClaimTTL)
	if err != nil {
		owned = false
	}
	proceed := owned || err != nil

	if proceed {
		loaded, namespace, dimension, warmErr := warmAndRemember()
		if warmErr != nil {
			// Hand the claim back rather than making peers wait out its TTL for a
			// generation that is not coming — but only if it is ours to hand back.
			if owned {
				coordinator.Release(key)
			}
			return loaded, namespace, dimension, warmErr
		}
		if err := coordinator.Publish(key, WarmGeneration{Dimension: dimension, Namespace: namespace}, semanticWarmPublishTTL); err != nil && owned {
			// Nothing was published, so a waiting peer would sit out the full
			// wait timeout before giving up and warming anyway. Dropping the
			// claim tells it immediately that no result is coming. This does not
			// race the publish the way a release on success would: there is no
			// publish to arrive. Only our own claim is ever dropped.
			coordinator.Release(key)
		}
		// The claim is deliberately not released after a successful publish:
		// peers read the published generation, and a lingering claim for work
		// already done is harmless, while releasing it races the publish for
		// peers that treat a vanished claim as "the warmer died".
		return loaded, namespace, dimension, nil
	}

	if namespace, dimension, ok := awaitWarmedGeneration(ctx, coordinator, store, config, exemplars, key); ok {
		// Adopting embeds nothing, so this node gains no vectors here — its next
		// configuration change re-embeds from scratch unless it wins that warm
		// too, the same position a restarted node is in. The cache is still
		// reconciled: whatever this node holds from an earlier generation is
		// pruned to the phrases this one references, exactly as a warm that ran
		// to completion would leave it.
		cache.useIdentity(semanticEmbeddingIdentity(config.Semantic))
		cache.retain(exemplars)
		// A peer measured this width, so it is worth remembering here too: the
		// next restart of this node can then adopt without waiting on anyone.
		if dimensions != nil {
			dimensions.Remember(semanticEmbeddingIdentity(config.Semantic), dimension)
		}
		if claimer != nil {
			claimer.ClaimGeneration(ctx, namespace)
		}
		return len(exemplars), namespace, dimension, nil
	}
	return warmAndRemember()
}

// awaitWarmedGeneration waits for the node holding the claim to finish, and
// reports the generation it produced.
//
// It stops as soon as the claim disappears: that means the warmer failed or
// died, and continuing to wait would just delay this node's own attempt. One
// last adoption check runs first, because the publish and the claim's
// disappearance can arrive out of order.
func awaitWarmedGeneration(
	ctx context.Context,
	coordinator WarmCoordinator,
	store vectorstore.VectorStore,
	config *AnalyzerConfig,
	exemplars []semanticExemplar,
	key string,
) (string, int, bool) {
	deadline := time.Now().Add(semanticWarmWaitTimeout)
	ticker := time.NewTicker(semanticWarmPollInterval)
	defer ticker.Stop()

	for {
		if generation, ok := coordinator.Lookup(key); ok {
			namespace, adopted, err := adoptSemanticGeneration(ctx, store, config, exemplars, generation.Dimension)
			if err == nil && adopted {
				return namespace, generation.Dimension, true
			}
		}
		if !coordinator.ClaimHeld(key) {
			return "", 0, false
		}
		if time.Now().After(deadline) {
			return "", 0, false
		}
		select {
		case <-ctx.Done():
			return "", 0, false
		case <-ticker.C:
		}
	}
}
