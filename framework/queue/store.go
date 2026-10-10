package queue

import (
	"context"
	"time"
)

// Store is the storage SPI behind the generic queue engine. A backend that
// implements it gets polling, concurrency, lease heartbeats, retries and
// dead-lettering from the engine for free.
//
// Durations, never wall-clock instants, cross this interface: each store turns
// them into deadlines on its own clock. Shared backends use the database
// clock, which is what keeps leases correct when instances' clocks disagree.
//
// Every operation after Claim is fenced on Claimed.ClaimToken. When the lease
// behind a token has expired and the delivery was claimed again, operations
// with the old token change nothing and report it as not held.
type Store interface {
	// EnsureGroup registers group on topic. When the group is new and startFrom
	// is StartFromEarliest, it also creates deliveries for every retained
	// message on the topic. Registering an existing group is a no-op.
	EnsureGroup(ctx context.Context, topic, group string, startFrom StartFrom) error
	// Append stores msgs, which already carry IDs and publish times, and
	// creates one pending delivery per group registered on each message's
	// topic. It is idempotent: messages and deliveries that already exist are
	// left untouched, including their state.
	Append(ctx context.Context, msgs []*Message) error
	// Claim leases up to req.Max due deliveries of req.Group on req.Topic. A
	// delivery is due when its message's DeliverAt and any retry backoff have
	// passed, or its previous lease expired, and, when it has a key, no earlier
	// delivery with the same key is still pending or leased. With
	// req.MaxInFlight set, the group's unexpired leases across every instance
	// never exceed it.
	Claim(ctx context.Context, req ClaimRequest) ([]*Claimed, error)
	// Ack completes deliveries in one round trip and returns the delivery IDs
	// that were still held; the rest had lost their lease and are untouched.
	Ack(ctx context.Context, cs []*Claimed) ([]string, error)
	// Retry counts a failed attempt and makes the delivery due again after
	// backoff. It reports false when c no longer holds it.
	Retry(ctx context.Context, c *Claimed, backoff time.Duration, lastErr string) (bool, error)
	// Kill counts a failed attempt and dead-letters the delivery. It reports
	// false when c no longer holds it.
	Kill(ctx context.Context, c *Claimed, lastErr string) (bool, error)
	// Extend renews the leases of cs for lease and returns the delivery IDs
	// still held. The caller treats every other delivery as lost.
	Extend(ctx context.Context, cs []*Claimed, lease time.Duration) ([]string, error)
	// Release gives deliveries back without counting an attempt, so another
	// consumer can claim them immediately.
	Release(ctx context.Context, cs []*Claimed) error
	// Purge deletes up to batch rows of each kind policy says have outlived
	// their use, returning how many it removed.
	Purge(ctx context.Context, policy PurgePolicy, batch int) (int64, error)
	// Stats counts group's deliveries on topic.
	Stats(ctx context.Context, topic, group string) (Stats, error)
	// Ping checks that the store is reachable.
	Ping(ctx context.Context) error
	// Close releases the store's own resources. It must not close a database
	// handle the store borrowed.
	Close(ctx context.Context) error
}

// ClaimRequest describes one claim.
type ClaimRequest struct {
	Topic       string
	Group       string
	RunnerID    string        // recorded as the holder, for debugging
	Max         int           // deliveries to lease at most
	Lease       time.Duration // how long the leases last without renewal
	MaxInFlight int           // cap on the group's unexpired leases across all instances; 0 means none
}

// PurgePolicy says how long finished rows are kept.
type PurgePolicy struct {
	DeadRetention  time.Duration // dead-lettered deliveries, kept for inspection
	AckedRetention time.Duration // messages no delivery needs any more, kept for late Earliest groups
}

// Claimed is a delivery leased to one consumer.
type Claimed struct {
	DeliveryID string   // deterministic, see DeliveryID
	ClaimToken string   // fences every write made under this claim
	Group      string   // consumer group the delivery belongs to
	Attempt    int      // 1-based attempt this claim represents
	Message    *Message // the delivered message
}
