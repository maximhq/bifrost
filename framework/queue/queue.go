package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Message is a unit of data published to a topic.
type Message struct {
	ID          string            `json:"id"`                   // set by Publish when empty
	Topic       string            `json:"topic"`                // required
	Key         string            `json:"key"`                  // optional; non-empty means strict FIFO per (topic, group, key)
	Payload     []byte            `json:"payload"`              // opaque to the queue
	Headers     map[string]string `json:"headers"`              // optional metadata
	PublishedAt time.Time         `json:"published_at"`         // set by Publish
	DeliverAt   time.Time         `json:"deliver_at,omitempty"` // optional; not handed out before this time
}

// Delivery is a message handed to one consumer group's handler.
type Delivery struct {
	Message
	DeliveryID string `json:"delivery_id"` // stable across redeliveries; use it to deduplicate
	Group      string `json:"group"`
	Attempt    int    `json:"attempt"` // 1-based
}

// Handler processes one delivery. Returning nil acknowledges it, returning an
// error schedules a retry, and returning an error wrapped with Permanent
// dead-letters it immediately. The context is cancelled when the subscription
// closes or when the delivery's lease is lost to another consumer.
type Handler func(ctx context.Context, d *Delivery) error

// Publisher publishes messages to topics.
type Publisher interface {
	// Publish durably stores msgs and fans each one out to every consumer group
	// registered on its topic. Publishing a message ID that already exists is a
	// no-op, so a producer may safely retry a failed Publish.
	Publish(ctx context.Context, msgs ...*Message) error
}

// Subscriber attaches handlers to topics as members of a consumer group.
type Subscriber interface {
	// Subscribe registers group on topic (if it is not registered yet) and starts
	// delivering its messages to h until the returned Subscription is closed.
	Subscribe(ctx context.Context, topic, group string, h Handler, opts ...SubscribeOption) (Subscription, error)
}

// Subscription is a running consumer.
type Subscription interface {
	// Close stops claiming new deliveries, waits for in-flight handlers until
	// ctx is done, and releases any leases still held so other consumers can
	// take them over immediately.
	Close(ctx context.Context) error
}

// Stats is a point-in-time count of one consumer group's deliveries.
type Stats struct {
	Pending int64 `json:"pending"` // waiting to be claimed: due, in retry backoff, or queued behind their key
	Leased  int64 `json:"leased"`  // currently held by a consumer
	Dead    int64 `json:"dead"`    // dead-lettered and not yet purged
}

// Queue is the contract every queue implementation satisfies.
type Queue interface {
	Publisher
	Subscriber
	// Stats reports delivery counts for one consumer group on a topic.
	Stats(ctx context.Context, topic, group string) (Stats, error)
	// Ping checks that the backing store is reachable.
	Ping(ctx context.Context) error
	// Close closes every subscription and then the queue's own resources. It
	// never closes stores the queue was handed as dependencies.
	Close(ctx context.Context) error
}

var (
	// ErrClosed is returned by operations on a closed queue or subscription.
	ErrClosed = errors.New("queue: closed")
	// ErrUnsupported is returned when a backend cannot provide an operation or
	// a dependency is of a kind the queue cannot use.
	ErrUnsupported = errors.New("queue: unsupported")
	// ErrNoLocker is returned when a backend needs a distributed locker to
	// consume safely and none was supplied.
	ErrNoLocker = errors.New("queue: distributed locker required")
	// ErrInvalidName is returned for a malformed topic or group name.
	ErrInvalidName = errors.New("queue: invalid name")
	// ErrPayloadTooLarge is returned when a message payload exceeds the
	// configured maximum.
	ErrPayloadTooLarge = errors.New("queue: payload too large")
	// ErrScheduleTooFar is returned for a message scheduled further ahead than
	// the queue retains messages.
	ErrScheduleTooFar = errors.New("queue: scheduled too far ahead")
)

// permanentError marks a handler error as not worth retrying.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err so the delivery is dead-lettered without further
// retries. Permanent(nil) returns nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err, or any error it wraps, was marked with
// Permanent.
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// MaxNameLength bounds topic and group names. Names become lock keys and
// index columns, so they are kept short.
const MaxNameLength = 200

// MaxIDLength bounds caller-supplied message IDs and keys, which are stored
// in indexed columns.
const MaxIDLength = 255

// ValidateName checks a topic or group name: 1 to MaxNameLength characters
// from [A-Za-z0-9._-].
func ValidateName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidName, kind)
	}
	if len(name) > MaxNameLength {
		return fmt.Errorf("%w: %s is longer than %d characters", ErrInvalidName, kind, MaxNameLength)
	}
	for i := 0; i < len(name); i++ {
		if !nameChars[name[i]] {
			return fmt.Errorf("%w: %s %q contains %q; allowed characters are [A-Za-z0-9._-]", ErrInvalidName, kind, name, name[i])
		}
	}
	return nil
}

// nameChars marks the bytes a topic or group name may contain, so validating
// a name costs one table load per byte.
var nameChars = func() (t [256]bool) {
	for c := 'a'; c <= 'z'; c++ {
		t[c] = true
	}
	for c := 'A'; c <= 'Z'; c++ {
		t[c] = true
	}
	for c := '0'; c <= '9'; c++ {
		t[c] = true
	}
	t['.'], t['_'], t['-'] = true, true, true
	return t
}()

// messageLimits bounds what Publish accepts. Zero fields disable a check.
type messageLimits struct {
	maxPayload int           // payload bytes
	maxDelay   time.Duration // how far past now DeliverAt may be
	now        time.Time     // the reference for maxDelay
}

// validateMessage checks a message before it is published.
func validateMessage(m *Message, limits messageLimits) error {
	if m == nil {
		return errors.New("queue: nil message")
	}
	if err := ValidateName("topic", m.Topic); err != nil {
		return err
	}
	if len(m.ID) > MaxIDLength {
		return fmt.Errorf("queue: message id is longer than %d bytes", MaxIDLength)
	}
	if len(m.Key) > MaxIDLength {
		return fmt.Errorf("queue: message key is longer than %d bytes", MaxIDLength)
	}
	if limits.maxPayload > 0 && len(m.Payload) > limits.maxPayload {
		return fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrPayloadTooLarge, len(m.Payload), limits.maxPayload)
	}
	if limits.maxDelay > 0 && m.DeliverAt.After(limits.now.Add(limits.maxDelay)) {
		return fmt.Errorf("%w: %s is more than %s ahead; messages are retained for %s", ErrScheduleTooFar,
			m.DeliverAt.UTC().Format(time.RFC3339), limits.maxDelay, limits.maxDelay)
	}
	return nil
}

// deliveryNamespace scopes the deterministic delivery and dead-letter IDs.
var deliveryNamespace = uuid.MustParse("6f0c2a52-3c1e-4b8f-9a57-2f3a1e5d7c90")

// DeliveryID returns the deterministic ID of messageID's delivery to group.
// Every backend derives delivery rows from it, so re-publishing a message or
// backfilling a group can never create a second delivery for the same pair.
func DeliveryID(messageID, group string) string {
	return uuid.NewSHA1(deliveryNamespace, []byte(messageID+"\x00"+group)).String()
}

// deadLetterMessageID returns the ID of the copy of a dead delivery published
// to a dead-letter topic. It is deterministic so that a crash between
// publishing the copy and marking the delivery dead cannot publish it twice.
func deadLetterMessageID(deliveryID string) string {
	return uuid.NewSHA1(deliveryNamespace, []byte("dlq\x00"+deliveryID)).String()
}
