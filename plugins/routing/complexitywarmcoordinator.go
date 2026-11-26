// The table-backed WarmCoordinator: the concrete claim and publish mechanism
// behind the interface the complexity package declares.
package routing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// newRoutingNodeID returns a value distinguishing this process from any other
// sharing the warm table. Random rather than derived from host or PID: processes on
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
	// ComplexityWarmTable names the table warm claims and results share.
	ComplexityWarmTable = "routing_complexity_warm"
	// ComplexityWarmKeyPrefix is the prefix both claim and result keys start with.
	ComplexityWarmKeyPrefix        = "complexity-warm-"
	complexityWarmClaimKeyPrefix   = "complexity-warm-claim:v1:"
	complexityWarmResultKeyPrefix  = "complexity-warm-result:v1:"
	complexityWarmRecordSeparator  = "|"
	complexityWarmRecordFieldCount = 2
)

// complexityWarmCoordinator elects one exemplar warmer per generation across
// the processes sharing the warm table.
//
// Writers share the table without a lock between them, which makes this an
// advisory claim rather than a lock: two nodes can both see an absent key and
// both proceed. That is the right trade here — duplicate warms write identical
// content-addressed records and cost only embeddings, so the goal is to make
// the common case cheap, not to make the racy case impossible.
type complexityWarmCoordinator struct {
	warm   *livestore.Table[string, string]
	nodeID string
}

// newComplexityWarmCoordinator returns nil when there is no table to
// coordinate through, so the classifier falls back to warming independently.
func newComplexityWarmCoordinator(warm *livestore.Table[string, string], nodeID string) *complexityWarmCoordinator {
	if warm == nil {
		return nil
	}
	return &complexityWarmCoordinator{warm: warm, nodeID: nodeID}
}

// claimKey is the table key a warm claim for key is held under.
func (c *complexityWarmCoordinator) claimKey(key string) string {
	return complexityWarmClaimKeyPrefix + key
}

// resultKey is the table key a completed generation for key is published under.
func (c *complexityWarmCoordinator) resultKey(key string) string {
	return complexityWarmResultKeyPrefix + key
}

// Claim reports whether this process should warm. The table always answers, so the
// error the interface allows is always nil here.
func (c *complexityWarmCoordinator) Claim(key string, ttl time.Duration) (bool, error) {
	if c == nil || c.warm == nil {
		return true, nil
	}
	return c.warm.PutIfAbsent(c.claimKey(key), c.nodeID, ttl), nil
}

// ClaimHeld reports whether anyone still holds the claim.
func (c *complexityWarmCoordinator) ClaimHeld(key string) bool {
	if c == nil || c.warm == nil {
		return false
	}
	_, ok := c.warm.Get(c.claimKey(key))
	return ok
}

// Release drops this process's claim on key, so another process may warm it.
func (c *complexityWarmCoordinator) Release(key string) {
	if c == nil || c.warm == nil {
		return
	}
	c.warm.Delete(c.claimKey(key))
}

// Publish records a completed generation. The value is two scalars in one
// string, the form every version reads.
func (c *complexityWarmCoordinator) Publish(key string, generation complexity.WarmGeneration, ttl time.Duration) error {
	if c == nil || c.warm == nil {
		return nil
	}
	if generation.Dimension <= 0 || strings.TrimSpace(generation.Namespace) == "" {
		return fmt.Errorf("refusing to publish incomplete warm generation")
	}
	if strings.Contains(generation.Namespace, complexityWarmRecordSeparator) {
		return fmt.Errorf("namespace %q contains the record separator", generation.Namespace)
	}
	record := strconv.Itoa(generation.Dimension) + complexityWarmRecordSeparator + generation.Namespace
	c.warm.PutWithTTL(c.resultKey(key), record, ttl)
	return nil
}

// Lookup returns the generation published for key, and false when none is or it does not decode.
func (c *complexityWarmCoordinator) Lookup(key string) (complexity.WarmGeneration, bool) {
	if c == nil || c.warm == nil {
		return complexity.WarmGeneration{}, false
	}
	value, ok := c.warm.Get(c.resultKey(key))
	if !ok {
		return complexity.WarmGeneration{}, false
	}
	return decodeStoredWarmGeneration(value)
}

// decodeStoredWarmGeneration reads a stored record. A malformed
// record is treated as absent rather than as an error worth failing a warm over.
func decodeStoredWarmGeneration(record string) (complexity.WarmGeneration, bool) {
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
