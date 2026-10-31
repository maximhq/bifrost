// The KVStore-backed WarmCoordinator: the concrete claim and publish mechanism
// behind the interface the complexity package declares.
package routing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// newRoutingNodeID returns a value distinguishing this process from any other
// sharing the KVStore. Random rather than derived from host or PID: nodes on
// different hosts can share a PID, and this is only ever compared for
// inequality, never resolved back to a machine. It mirrors the per-instance id
// the job sweeper uses for the same purpose.
func newRoutingNodeID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(raw[:])
}

const (
	complexityWarmClaimKeyPrefix   = "complexity-warm-claim:v1:"
	complexityWarmResultKeyPrefix  = "complexity-warm-result:v1:"
	complexityWarmRecordSeparator  = "|"
	complexityWarmRecordFieldCount = 2
)

// kvComplexityWarmCoordinator elects one exemplar warmer per generation across
// the nodes sharing a KVStore.
//
// The store replicates by gossip with last-writer-wins, which makes this an
// advisory claim rather than a lock: two nodes can both see an absent key and
// both proceed. That is the right trade here — duplicate warms write identical
// content-addressed records and cost only embeddings, so the goal is to make
// the common case cheap, not to make the racy case impossible.
type kvComplexityWarmCoordinator struct {
	store  schemas.KVStore
	nodeID string
}

// newKVComplexityWarmCoordinator returns nil when there is no store to
// coordinate through, so the classifier falls back to warming independently.
func newKVComplexityWarmCoordinator(store schemas.KVStore, nodeID string) *kvComplexityWarmCoordinator {
	if store == nil {
		return nil
	}
	return &kvComplexityWarmCoordinator{store: store, nodeID: nodeID}
}

func (c *kvComplexityWarmCoordinator) claimKey(key string) string {
	return complexityWarmClaimKeyPrefix + key
}

func (c *kvComplexityWarmCoordinator) resultKey(key string) string {
	return complexityWarmResultKeyPrefix + key
}

// Claim reports whether this node should warm. An error is returned rather than
// swallowed so the caller can distinguish "someone else has it" from "the store
// could not say", and warm anyway in the second case.
func (c *kvComplexityWarmCoordinator) Claim(key string, ttl time.Duration) (bool, error) {
	if c == nil || c.store == nil {
		return true, nil
	}
	return c.store.SetNXWithTTL(c.claimKey(key), c.nodeID, ttl)
}

// ClaimHeld reports whether any node still holds the claim. An unreadable store
// answers "no", which sends a waiting node back to warming for itself — the
// same outcome as no coordination at all.
func (c *kvComplexityWarmCoordinator) ClaimHeld(key string) bool {
	if c == nil || c.store == nil {
		return false
	}
	_, err := c.store.Get(c.claimKey(key))
	return err == nil
}

func (c *kvComplexityWarmCoordinator) Release(key string) {
	if c == nil || c.store == nil {
		return
	}
	_, _ = c.store.Delete(c.claimKey(key))
}

// Publish records a completed generation. The value is a plain string rather
// than a struct because a gossiped value arrives at a peer as raw JSON bytes
// unless a decoder is registered for its key prefix; encoding two scalars
// ourselves keeps both the local and the replicated read on one code path.
func (c *kvComplexityWarmCoordinator) Publish(key string, generation complexity.WarmGeneration, ttl time.Duration) error {
	if c == nil || c.store == nil {
		return nil
	}
	if generation.Dimension <= 0 || strings.TrimSpace(generation.Namespace) == "" {
		return fmt.Errorf("refusing to publish incomplete warm generation")
	}
	if strings.Contains(generation.Namespace, complexityWarmRecordSeparator) {
		return fmt.Errorf("namespace %q contains the record separator", generation.Namespace)
	}
	record := strconv.Itoa(generation.Dimension) + complexityWarmRecordSeparator + generation.Namespace
	return c.store.SetWithTTL(c.resultKey(key), record, ttl)
}

func (c *kvComplexityWarmCoordinator) Lookup(key string) (complexity.WarmGeneration, bool) {
	if c == nil || c.store == nil {
		return complexity.WarmGeneration{}, false
	}
	value, err := c.store.Get(c.resultKey(key))
	if err != nil {
		return complexity.WarmGeneration{}, false
	}
	return decodeStoredWarmGeneration(value)
}

// decodeStoredWarmGeneration reads a record written by any node. A value
// written locally comes back as the string it was stored as; one replicated
// from a peer arrives as the raw JSON bytes of that string, because no decoder
// is registered for this key prefix. Both shapes are handled, and anything else
// is treated as absent rather than as an error worth failing a warm over.
func decodeStoredWarmGeneration(value any) (complexity.WarmGeneration, bool) {
	var record string
	switch typed := value.(type) {
	case string:
		record = typed
	case []byte:
		if err := json.Unmarshal(typed, &record); err != nil {
			record = string(typed)
		}
	default:
		return complexity.WarmGeneration{}, false
	}

	dimensionText, namespace, found := strings.Cut(record, complexityWarmRecordSeparator)
	if !found || strings.TrimSpace(namespace) == "" {
		return complexity.WarmGeneration{}, false
	}
	dimension, err := strconv.Atoi(dimensionText)
	if err != nil || dimension <= 0 {
		return complexity.WarmGeneration{}, false
	}
	return complexity.WarmGeneration{Dimension: dimension, Namespace: namespace}, true
}
