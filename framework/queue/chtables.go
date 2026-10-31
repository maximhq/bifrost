package queue

import "time"

// ClickHouse table models. The logstore's EnsureClickHouseTable appends the
// ReplacingMergeTree `ver` column itself, so these structs never carry it;
// the queue writes `ver` explicitly in raw INSERTs. Times are unix
// microseconds where they are compared against the database clock. Strings
// are never NULL: an empty MsgKey means "no key".

// chQueueGroup records a consumer group registered on a topic.
type chQueueGroup struct {
	Topic     string
	GroupName string
	StartFrom string
	CreatedAt time.Time // database clock; when the group registered
}

// chQueueMessage is a published message, shared by its deliveries.
type chQueueMessage struct {
	ID          string
	Topic       string
	MsgKey      string
	Seq         int64 // publish order: database clock in ns at publish + position in its batch
	Payload     string
	Headers     string // JSON object
	PublishedAt time.Time
	DeliverAt   int64     // unix µs the message is scheduled for; 0 means at once
	CreatedAt   time.Time // database clock; drives the retention TTL
	DeadAt      int64     // unix µs a delivery of it was last dead-lettered; 0 if never
}

// chQueueDelivery is one message's delivery to one consumer group. Every
// state change inserts a new version of the whole row.
type chQueueDelivery struct {
	ID            string // DeliveryID(message, group)
	MessageID     string
	Topic         string
	GroupName     string
	MsgKey        string
	Seq           int64 // copied from the message
	Status        string
	Attempts      int64
	DeliverAt     int64 // unix µs the message is scheduled for; 0 means at once
	NextAttemptAt int64 // unix µs
	ClaimedBy     string
	ClaimToken    string
	ClaimedUntil  int64 // unix µs
	LastError     string
	CreatedAt     time.Time // its message's created_at; drives the retention TTL
	DeadAt        int64     // unix µs it, or a backfill's message, was dead-lettered; else 0
}
