// Package queue is a durable, pluggable message queue with Kafka-shaped
// semantics: producers publish messages to a topic, and every consumer group
// subscribed to that topic receives every message, while the consumers inside
// one group compete for it.
//
// # Interfaces
//
// Callers depend only on [Queue]. Two kinds of implementation sit behind it:
//
//   - A broker (Kafka, SQS, ...) implements [Queue] directly and registers
//     itself with [Register]. The framework never imports a broker client.
//   - A database-backed queue implements the narrow [Store] SPI and lets the
//     generic engine provide polling, concurrency, lease heartbeats, retries
//     with backoff and dead-lettering. The built-in "logstore" type stores
//     messages in the logstore database (SQLite, Postgres or ClickHouse); the
//     "memory" type keeps them in process.
//
// # Delivery guarantees
//
// Delivery is at least once. A handler that returns nil acknowledges the
// delivery; an error schedules a retry with exponential backoff; an error
// wrapped with [Permanent], or the final allowed attempt, dead-letters it.
// A delivery whose lease is lost (the pod paused past the lease, or crashed)
// is redelivered to another consumer, so handlers must be idempotent.
// [Delivery.DeliveryID] is stable across redeliveries of the same message to
// the same group and is the intended deduplication key.
//
// # Ordering
//
// Messages without a key are delivered in best-effort publish order. Messages
// that carry a [Message.Key] are delivered strictly in order per (topic,
// group, key): the next message for a key is not handed out until the
// previous one is acknowledged or dead-lettered, including while it waits
// out a retry backoff. Order among concurrent publishers of the same key is
// the order in which their writes commit.
//
// # Scheduling and limits
//
// A message with [Message.DeliverAt] set is not handed out before that time;
// it runs on the first poll after it is due, so within the subscription's
// poll interval. It can be scheduled at most the configured retention ahead.
// A scheduled message that carries a key keeps its place in the key's order:
// later messages with the same key wait for it.
//
// [WithConcurrency] bounds the handlers one instance runs for a
// subscription. [WithMaxInFlight] bounds a consumer group's deliveries in
// flight across every instance, counted in the store, so it holds however
// many instances consume the group.
//
// # Multiple instances
//
// The Postgres and ClickHouse backends are safe to share between any number
// of Bifrost instances. Leases and retry times are computed on the database
// clock, every write after a claim is fenced on the claim's token, and
// per-key ordering is evaluated against committed state. ClickHouse has no
// row locks or transactions, so consuming on ClickHouse requires a shared
// distributed locker, through which claims, publishes and group
// registrations on a topic are also serialised. An instance without one can
// still publish, serialising only its own publishes, so per-key order between
// two such publishers is not guaranteed. The SQLite and memory backends are
// single-process only.
package queue
