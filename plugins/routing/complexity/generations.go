// The lifecycle of a stored exemplar generation: recognising a complete one, listing what a store holds, and removing what is retired.
package complexity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/framework/vectorstore"
)

// adoptSemanticGeneration reports whether the generation this configuration
// implies at the given dimension is already complete in the store.
//
// The namespace is created first for the same reason warmup does it: Qdrant and
// Weaviate answer reads against a missing namespace with backend-specific
// errors rather than an empty result, and creation is idempotent everywhere.
func adoptSemanticGeneration(ctx context.Context, store vectorstore.VectorStore, config *AnalyzerConfig, exemplars []semanticExemplar, dimension int) (string, bool, error) {
	if store == nil || dimension < 2 || len(exemplars) == 0 {
		return "", false, nil
	}
	fingerprint := semanticFingerprint(config, exemplars, dimension)
	namespace := semanticGenerationNamespace(fingerprint)
	if err := store.CreateNamespace(ctx, namespace, dimension, semanticVectorStoreProperties); err != nil {
		return namespace, false, fmt.Errorf("%w: create complexity generation namespace: %v", errSemanticVectorStoreUnavailable, err)
	}
	markers, err := store.GetChunks(ctx, namespace, []string{semanticMarkerID(fingerprint)})
	if err != nil {
		if errors.Is(err, vectorstore.ErrNotFound) {
			return namespace, false, nil
		}
		return namespace, false, fmt.Errorf("%w: check complexity warmup marker: %v", errSemanticVectorStoreUnavailable, err)
	}
	if len(markers) > 0 && markers[0].Properties[semanticMetadataFingerprint] == fingerprint {
		return namespace, true, nil
	}
	return namespace, false, nil
}

// GenerationClaimer records that this node intends to use a namespace, so peers
// reclaiming unused generations can see it before there is anything to show.
//
// A warm creates its namespace early and finishes minutes later. Until then no
// peer can tell it is in use, and a warm slower than the reclamation interval
// can have its half-built namespace collected. Announcing the target as soon as
// it is known closes that.
//
// It returns nothing on purpose. Warmup must not be gated on the registry being
// reachable: a missed claim costs the early protection, never the warm itself.
//
// The claim is kept alive until ReleaseGeneration says the warm is over — on
// success the namespace is active and protected on that basis, on failure it
// is not this node's to protect.
type GenerationClaimer interface {
	ClaimGeneration(ctx context.Context, namespace string)
	ReleaseGeneration(namespace string)
}

// GenerationInfo describes one exemplar generation held in the vector store.
type GenerationInfo struct {
	// Namespace is the fingerprinted namespace holding the generation.
	Namespace string `json:"namespace"`
	// Active reports whether classification currently queries this generation.
	// Exactly one namespace is active while the classifier is ready; every other
	// one is a retired generation that no request can reach.
	Active bool `json:"active"`
}

// ErrGenerationActive is returned when a caller asks to remove the generation
// that is currently serving.
var ErrGenerationActive = errors.New("generation is currently serving classification requests")

// ErrNotAGeneration is returned for a namespace outside the classifier's own
// naming scheme.
var ErrNotAGeneration = errors.New("namespace is not a complexity routing generation")

// ErrClassifierUnavailable is returned when there is no semantic classifier to
// answer for the vector store at all, so neither success nor failure of an
// operation on it can be reported honestly.
var ErrClassifierUnavailable = errors.New("semantic complexity classifier is not available")

// ListGenerations reports every exemplar generation this classifier's store is
// holding, flagging the one being served.
//
// Retired generations are not reclaimed on shared external stores — another
// replica may still be serving one, and nothing here can know — so they
// accumulate with every configuration change. Enumeration is what turns that
// from an invisible leak into something an operator can see and act on: the
// store holds no phrase text, so a namespace is otherwise just an opaque hash.
func (c *SemanticClassifier) ListGenerations(ctx context.Context) ([]GenerationInfo, error) {
	c.mu.Lock()
	store := c.store
	var active string
	if c.active != nil {
		active = c.active.namespace
	}
	c.mu.Unlock()

	if store == nil {
		return []GenerationInfo{}, nil
	}
	namespaces, err := store.ListNamespaces(ctx, SemanticVectorStoreNamespace+"_")
	if err != nil {
		return nil, fmt.Errorf("%w: list complexity generations: %v", errSemanticVectorStoreUnavailable, err)
	}
	generations := make([]GenerationInfo, 0, len(namespaces))
	for _, namespace := range namespaces {
		generations = append(generations, GenerationInfo{Namespace: namespace, Active: namespace == active})
	}
	return generations, nil
}

// DeleteGeneration removes one retired generation.
//
// It refuses the serving generation and anything outside this classifier's
// naming scheme: the store is shared with other Bifrost features and, on an
// external backend, with whatever else the operator runs there, so a
// mistyped name must not be able to drop an unrelated collection.
//
// Removing a generation a peer replica is still serving is possible and not
// guarded against — no node can see another's state — but such a peer only
// loses its exemplars until its next warm.
func (c *SemanticClassifier) DeleteGeneration(ctx context.Context, namespace string) error {
	if !strings.HasPrefix(namespace, SemanticVectorStoreNamespace+"_") {
		return fmt.Errorf("%w: %s", ErrNotAGeneration, namespace)
	}

	c.mu.Lock()
	store := c.store
	// A warm in flight is building toward a namespace this classifier cannot
	// name yet: the fingerprint is only known once the embedding width has been
	// measured, inside the warm itself. Deleting during that window can remove
	// the very generation the warm is about to activate, leaving the classifier
	// serving records that no longer exist until the next configuration change.
	// Refusing for the duration is a coarse guard, but warms are short and both
	// callers retry — the sweep on its next pass, an operator at will.
	if c.warming {
		c.mu.Unlock()
		return fmt.Errorf("%w: a warmup is in progress", ErrGenerationActive)
	}
	if c.active != nil && c.active.namespace == namespace {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrGenerationActive, namespace)
	}
	// A generation released by the classifier can still be answering a request
	// that started before the swap.
	for generation, count := range c.localInFlight {
		if count > 0 && generation.namespace == namespace {
			c.mu.Unlock()
			return fmt.Errorf("%w: %s", ErrGenerationActive, namespace)
		}
	}
	// Marked before the mutex is released, and cleared only once the store has
	// answered. The warm worker refuses to activate a namespace carrying this
	// mark, which is what stops a warm that finishes mid-delete from serving
	// records the delete is removing.
	if c.deleting == nil {
		c.deleting = map[string]int{}
	}
	c.deleting[namespace]++
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.deleting[namespace] <= 1 {
			delete(c.deleting, namespace)
		} else {
			c.deleting[namespace]--
		}
		// Wake any warm that finished into this namespace and is waiting for the
		// store to be done with it.
		c.deletionDone.Broadcast()
		c.mu.Unlock()
	}()

	if store == nil {
		return nil
	}
	if err := store.DeleteNamespace(ctx, namespace); err != nil {
		return fmt.Errorf("%w: delete complexity generation: %v", errSemanticVectorStoreUnavailable, err)
	}
	return nil
}
