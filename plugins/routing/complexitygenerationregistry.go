package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

const (
	// complexityGenerationHeartbeat is how often a node restates the generation
	// it is using. It bounds nothing on its own; the expiry below is what makes
	// a registration stale, and this simply has to be comfortably shorter.
	complexityGenerationHeartbeat = 2 * time.Minute
	// complexityGenerationExpiry is how long a registration outlives its last
	// heartbeat. Several missed heartbeats rather than one, so a slow database
	// or a paused process does not make a live node's generation collectable.
	complexityGenerationExpiry = 10 * time.Minute
	// complexityGenerationSweepInterval is how often retired generations are
	// collected. Reclamation is housekeeping, so this is deliberately far longer
	// than the heartbeat.
	complexityGenerationSweepInterval = 15 * time.Minute
	// complexityGenerationLockKey serializes both the registration row's
	// read-modify-write and the sweep itself, so exactly one node sweeps.
	complexityGenerationLockKey = "complexity-semantic-generations"
	// complexityGenerationLockTTL bounds how long a crashed holder blocks the
	// next one. Every operation under it is a small handful of queries.
	complexityGenerationLockTTL = 60 * time.Second
	// complexityGenerationUnlockTimeout bounds the release, which runs on its own
	// context so shutdown mid-sweep still lets go of the lock.
	complexityGenerationUnlockTimeout = 5 * time.Second
)

// complexityGenerationClaim is one node's statement that it is using a
// generation. It records no request data — only which node, which namespace,
// and when the node last said so.
//
// A node can hold more than one at a time: while warming it is serving the
// previous generation and building the next, and both must be protected. Claims
// are therefore keyed by node and namespace together rather than by node alone,
// so recording one cannot displace the other.
type complexityGenerationClaim struct {
	NodeID    string `json:"node_id"`
	Namespace string `json:"namespace"`
	SeenAt    int64  `json:"seen_at"`
}

// complexityGenerationClaimKey identifies one node's claim on one namespace.
func complexityGenerationClaimKey(nodeID, namespace string) string {
	return nodeID + "\x00" + namespace
}

// complexityGenerationRegistry reclaims exemplar generations no node is using.
//
// Every configuration change mints a new fingerprinted generation and leaves
// the previous one behind. A node-local store can drop it immediately, because
// the node that created it is the only one that could be reading it. A shared
// store cannot: some other node may still be serving it, and the deleting node
// has no way to see that.
//
// So generations are reclaimed by consensus of absence. Each node registers
// what it is using and refreshes that periodically; a sweep deletes only the
// namespaces no live registration names. Nothing here depends on nodes agreeing
// on configuration, which matters because in OSS they frequently do not.
type complexityGenerationRegistry struct {
	store       configstore.ConfigStore
	locks       *configstore.DistributedLockManager
	logger      schemas.Logger
	nodeID      string
	generations generationLister

	mu      sync.Mutex
	current string
	// targets are namespaces this node claimed ahead of activating them: a
	// warm in progress. A claim made once would expire under a warm slower than
	// complexityGenerationExpiry, so each heartbeat restates these until the
	// warm reports back through ReleaseGeneration.
	targets map[string]struct{}
	// unclaimedLastSweep is what the previous pass found nobody using. A
	// generation must look unclaimed twice, a sweep interval apart, before it is
	// removed. That covers the gap between a node activating a generation and
	// its next heartbeat recording the claim: during it the generation is live
	// on that node but invisible to every other, and a single-pass sweep would
	// delete it out from under them.
	unclaimedLastSweep map[string]struct{}
}

// generationLister is the classifier's view of the vector store, kept narrow so
// the registry cannot reach past listing and deleting.
type generationLister interface {
	ListComplexityGenerations(ctx context.Context) ([]complexity.GenerationInfo, error)
	// deleteComplexityGenerationUnchecked deletes without re-consulting the
	// claims registry. The sweep calls it while already holding the registry
	// lock, having just established that nothing claims the namespace; the
	// claim-checking entry point would block on this node's own lease.
	deleteComplexityGenerationUnchecked(ctx context.Context, namespace string) error
}

func newComplexityGenerationRegistry(store configstore.ConfigStore, logger schemas.Logger, nodeID string, generations generationLister) *complexityGenerationRegistry {
	if store == nil || generations == nil {
		return nil
	}
	return &complexityGenerationRegistry{
		store:       store,
		locks:       configstore.NewDistributedLockManager(store, logger, configstore.WithDefaultTTL(complexityGenerationLockTTL)),
		logger:      logger,
		nodeID:      nodeID,
		generations: generations,
	}
}

// Run keeps this node's registration fresh and sweeps retired generations until
// ctx is cancelled.
func (r *complexityGenerationRegistry) Run(ctx context.Context) {
	if r == nil {
		return
	}
	heartbeat := time.NewTicker(complexityGenerationHeartbeat)
	defer heartbeat.Stop()
	sweep := time.NewTicker(complexityGenerationSweepInterval)
	defer sweep.Stop()

	// Register immediately, though at process start this usually records
	// nothing: the classifier is handed its vector store later in bootstrap, so
	// there is no active generation to claim yet. What guarantees a claim is in
	// place before any sweep is the interval gap — heartbeats land minutes
	// before the first sweep, on this node and on every peer.
	r.heartbeatOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			r.heartbeatOnce(ctx)
		case <-sweep.C:
			r.heartbeatOnce(ctx)
			r.sweepOnce(ctx)
		}
	}
}

// heartbeatOnce records the generation this node is using now.
func (r *complexityGenerationRegistry) heartbeatOnce(ctx context.Context) {
	generations, err := r.generations.ListComplexityGenerations(ctx)
	if err != nil {
		// An unreadable store says nothing about what this node is serving, and
		// dropping the claim would invite a sweep to collect it.
		return
	}
	var active string
	for _, generation := range generations {
		if generation.Active {
			active = generation.Namespace
			break
		}
	}

	r.mu.Lock()
	r.current = active
	held := make([]string, 0, len(r.targets)+1)
	if active != "" {
		held = append(held, active)
	}
	for namespace := range r.targets {
		if namespace != active {
			held = append(held, namespace)
		}
	}
	r.mu.Unlock()

	if len(held) == 0 {
		return
	}
	if err := r.recordClaims(ctx, held...); err != nil && r.logger != nil {
		r.logger.Debug("[Governance] Failed to register semantic complexity generation: %v", err)
	}
}

// recordClaims stamps this node's claim on each namespace, under the lock.
func (r *complexityGenerationRegistry) recordClaims(ctx context.Context, namespaces ...string) error {
	return r.withLock(ctx, func(_ *configstore.DistributedLock) error {
		claims, err := r.readClaims(ctx)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, namespace := range namespaces {
			claims[complexityGenerationClaimKey(r.nodeID, namespace)] = complexityGenerationClaim{
				NodeID:    r.nodeID,
				Namespace: namespace,
				SeenAt:    now.Unix(),
			}
		}
		return r.writeClaims(ctx, pruneExpiredClaims(claims, now))
	})
}

// sweepOnce deletes every generation no node has claimed.
func (r *complexityGenerationRegistry) sweepOnce(ctx context.Context) {
	if err := r.withLock(ctx, func(lock *configstore.DistributedLock) error {
		claims, err := r.readClaims(ctx)
		if err != nil {
			return err
		}
		claims = pruneExpiredClaims(claims, time.Now())

		inUse := make(map[string]struct{}, len(claims))
		for _, claim := range claims {
			inUse[claim.Namespace] = struct{}{}
		}
		// This node's own generation may not have reached the row yet on a first
		// pass, so it is protected directly as well as by its claim.
		r.mu.Lock()
		if r.current != "" {
			inUse[r.current] = struct{}{}
		}
		for namespace := range r.targets {
			inUse[namespace] = struct{}{}
		}
		r.mu.Unlock()

		generations, err := r.generations.ListComplexityGenerations(ctx)
		if err != nil {
			return err
		}
		unclaimed := make(map[string]struct{}, len(generations))
		for _, generation := range generations {
			if generation.Active {
				continue
			}
			if _, claimed := inUse[generation.Namespace]; claimed {
				continue
			}
			unclaimed[generation.Namespace] = struct{}{}
		}
		previouslyUnclaimed := r.previousUnclaimed()
		r.rememberUnclaimed(unclaimed)

		for _, generation := range generations {
			if _, stillUnclaimed := unclaimed[generation.Namespace]; !stillUnclaimed {
				continue
			}
			if _, seenBefore := previouslyUnclaimed[generation.Namespace]; !seenBefore {
				// First pass that has seen it unused. Give the node that may have
				// just activated it a chance to record its claim.
				continue
			}
			// Deleting is network I/O against the vector store, and enough of it
			// can outlast the lock's lease. Each delete starts on a fresh lease so
			// the claim check it relies on is still current when it runs, and the
			// write below still happens under a lock this node holds.
			if err := lock.Extend(ctx); err != nil {
				if r.logger != nil {
					r.logger.Debug("[Governance] Could not extend the complexity generation lock: %v", err)
				}
				continue
			}
			if err := r.generations.deleteComplexityGenerationUnchecked(ctx, generation.Namespace); err != nil {
				if r.logger != nil {
					r.logger.Warn("[Governance] Failed to reclaim retired semantic complexity generation %s: %v", generation.Namespace, err)
				}
				continue
			}
			if r.logger != nil {
				r.logger.Info("[Governance] Reclaimed retired semantic complexity generation %s", generation.Namespace)
			}
		}
		// The claims snapshot was read at the top of this sweep. If the lease
		// lapsed meanwhile, a peer may have taken the lock and recorded its own
		// heartbeat, and writing the stale snapshot back would erase it. Losing
		// the write costs nothing: every node restates its claim on the next
		// heartbeat.
		held, err := lock.IsHeld(ctx)
		if err != nil || !held {
			if r.logger != nil {
				r.logger.Debug("[Governance] Skipped persisting complexity generation claims: lock no longer held")
			}
			return nil
		}
		return r.writeClaims(ctx, claims)
	}); err != nil && r.logger != nil {
		r.logger.Debug("[Governance] Skipped semantic complexity generation sweep: %v", err)
	}
}

// ClaimGeneration records that this node is about to use a namespace, before it
// has anything to show for it.
//
// A warm creates its namespace early and only finishes minutes later; until the
// generation is activated and the next heartbeat records it, no peer can tell
// the namespace is in use, and a long enough warm can outlast the two sweep
// passes that would otherwise protect it. Claiming the target as soon as it is
// known covers the whole construction rather than only the part after
// activation.
//
// The claim is restated by every heartbeat until ReleaseGeneration, because a
// warm can run longer than a single claim lives.
//
// Deliberately returns nothing: a warm must never be gated on the config store
// being reachable. A failure here costs the protection, not the warm, and the
// two-pass sweep rule remains the backstop.
func (r *complexityGenerationRegistry) ClaimGeneration(ctx context.Context, namespace string) {
	if r == nil || namespace == "" {
		return
	}
	r.mu.Lock()
	if r.targets == nil {
		r.targets = make(map[string]struct{})
	}
	r.targets[namespace] = struct{}{}
	r.mu.Unlock()
	if err := r.recordClaims(ctx, namespace); err != nil && r.logger != nil {
		r.logger.Debug("[Governance] Could not claim semantic complexity generation %s in advance: %v", namespace, err)
	}
}

// ReleaseGeneration stops restating an advance claim. The warm that made it has
// reported back: if it succeeded the namespace is active now and the heartbeat
// covers it on that basis; if it failed the namespace is not this node's to
// protect. The row is left as it is — the claim simply ages out.
func (r *complexityGenerationRegistry) ReleaseGeneration(namespace string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.targets, namespace)
	r.mu.Unlock()
}

// DeleteIfUnclaimed removes a generation, but only while holding the lifecycle
// lock and only if no other node claims it.
//
// The check and the deletion are one operation deliberately. Asking first and
// deleting afterwards leaves a gap in which a peer can record a claim on the
// very namespace about to be removed — the peer would believe it is protected,
// and it would already be gone. Holding the lock across both means any claim
// arriving meanwhile lands either wholly before the read or wholly after the
// delete.
//
// A registry that cannot read the claims refuses: a refused deletion is
// recoverable, deleting a generation in use is not.
func (r *complexityGenerationRegistry) DeleteIfUnclaimed(ctx context.Context, namespace string) error {
	if r == nil {
		return nil
	}
	return r.withLock(ctx, func(lock *configstore.DistributedLock) error {
		claims, err := r.readClaims(ctx)
		if err != nil {
			return fmt.Errorf("could not check whether another node is serving %s: %w", namespace, err)
		}
		for _, claim := range pruneExpiredClaims(claims, time.Now()) {
			if claim.NodeID != r.nodeID && claim.Namespace == namespace {
				return fmt.Errorf("%w: another node is serving %s", complexity.ErrGenerationActive, namespace)
			}
		}
		if err := lock.Extend(ctx); err != nil {
			return fmt.Errorf("could not renew the complexity generation lock before deleting %s: %w", namespace, err)
		}
		// Unchecked because the check just happened, under the lock still held.
		return r.generations.deleteComplexityGenerationUnchecked(ctx, namespace)
	})
}

// previousUnclaimed returns what the last sweep found unused.
func (r *complexityGenerationRegistry) previousUnclaimed() map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unclaimedLastSweep
}

// rememberUnclaimed records what this sweep found unused, for the next one to
// confirm against.
func (r *complexityGenerationRegistry) rememberUnclaimed(unclaimed map[string]struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unclaimedLastSweep = unclaimed
}

// withLock runs fn while holding the shared lock. Losing the race is ordinary:
// another node is doing the same work, and this one simply waits for its turn.
func (r *complexityGenerationRegistry) withLock(ctx context.Context, fn func(lock *configstore.DistributedLock) error) error {
	lock, err := r.locks.NewLockWithTTL(complexityGenerationLockKey, complexityGenerationLockTTL)
	if err != nil {
		return err
	}
	acquired, err := lock.TryLock(ctx)
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("another node holds the complexity generation lock")
	}
	// Released on a context that outlives ctx: Cleanup cancels the loop, and an
	// unlock issued on the cancelled context would fail and leave the lock held
	// for the rest of its TTL.
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), complexityGenerationUnlockTimeout)
		defer cancel()
		_ = lock.Unlock(unlockCtx)
	}()
	return fn(lock)
}

// readClaims returns what every node has registered.
//
// An empty result means "nobody has claimed anything", which is a licence for
// the sweep to delete every retired generation — so it is reserved for the one
// case that actually means that: a missing or empty row on first boot. A read
// or decode failure returns an error instead, because a sweep that mistook it
// for an empty registry would delete generations peers are still serving.
func (r *complexityGenerationRegistry) readClaims(ctx context.Context) (map[string]complexityGenerationClaim, error) {
	entry, err := r.store.GetConfig(ctx, tables.ConfigComplexitySemanticGenerationsKey)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return map[string]complexityGenerationClaim{}, nil
		}
		return nil, fmt.Errorf("read semantic complexity generation claims: %w", err)
	}
	if entry == nil || entry.Value == "" {
		return map[string]complexityGenerationClaim{}, nil
	}
	var claims map[string]complexityGenerationClaim
	if err := json.Unmarshal([]byte(entry.Value), &claims); err != nil {
		return nil, fmt.Errorf("decode semantic complexity generation claims: %w", err)
	}
	if claims == nil {
		claims = map[string]complexityGenerationClaim{}
	}
	return claims, nil
}

func (r *complexityGenerationRegistry) writeClaims(ctx context.Context, claims map[string]complexityGenerationClaim) error {
	encoded, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	return r.store.UpdateConfig(ctx, &tables.TableGovernanceConfig{
		Key:   tables.ConfigComplexitySemanticGenerationsKey,
		Value: string(encoded),
	})
}

// pruneExpiredClaims drops registrations whose node has stopped heartbeating,
// which is what eventually makes a departed node's generation collectable.
func pruneExpiredClaims(claims map[string]complexityGenerationClaim, now time.Time) map[string]complexityGenerationClaim {
	cutoff := now.Add(-complexityGenerationExpiry).Unix()
	live := make(map[string]complexityGenerationClaim, len(claims))
	for key, claim := range claims {
		if claim.Namespace != "" && claim.SeenAt >= cutoff {
			live[key] = claim
		}
	}
	return live
}
