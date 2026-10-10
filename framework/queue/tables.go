package queue

import "time"

// SQL table models for the database-backed queue. Lease and due times are
// unix microseconds so they compare the same way on every dialect and can be
// computed from the database clock.

// tableQueueTopic has one row per topic. Publishers share-lock it and group
// registration exclusively locks it, which orders the two.
type tableQueueTopic struct {
	Topic     string    `gorm:"primaryKey;type:varchar(200)"`
	CreatedAt time.Time `gorm:"not null"`
}

func (tableQueueTopic) TableName() string { return "queue_topics" }

// tableQueueGroup records a consumer group registered on a topic.
type tableQueueGroup struct {
	Topic     string    `gorm:"primaryKey;type:varchar(200)"`
	GroupName string    `gorm:"primaryKey;type:varchar(200)"`
	CreatedAt time.Time `gorm:"not null"`
}

func (tableQueueGroup) TableName() string { return "queue_groups" }

// tableQueueMessage is a published message, shared by its deliveries. Seq
// records publish order for backfills; ID is the caller-visible identity.
type tableQueueMessage struct {
	Seq         int64     `gorm:"primaryKey;autoIncrement"`
	ID          string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_queue_messages_id"`
	Topic       string    `gorm:"type:varchar(200);not null;index:idx_queue_messages_topic_created,priority:1"`
	MsgKey      *string   `gorm:"type:varchar(255)"`
	Payload     []byte    `gorm:""`
	Headers     string    `gorm:"type:text"` // JSON object
	PublishedAt time.Time `gorm:"not null"`
	DeliverAt   int64     `gorm:"not null;default:0"` // unix µs; 0 means at once
	CreatedAt   time.Time `gorm:"not null;index:idx_queue_messages_topic_created,priority:2;index:idx_queue_messages_created"`
}

func (tableQueueMessage) TableName() string { return "queue_messages" }

// tableQueueDelivery is one message's delivery to one consumer group.
// Acknowledged deliveries are deleted.
type tableQueueDelivery struct {
	Seq           int64     `gorm:"primaryKey;autoIncrement;index:idx_queue_deliveries_ready,priority:3,where:status <> 'dead' AND status <> 'waiting';index:idx_queue_deliveries_key,priority:4,where:status <> 'dead'"`
	ID            string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_queue_deliveries_id"` // DeliveryID(message, group)
	MessageID     string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_queue_deliveries_message_group,priority:1"`
	Topic         string    `gorm:"type:varchar(200);not null;index:idx_queue_deliveries_ready,priority:1;index:idx_queue_deliveries_key,priority:1"`
	GroupName     string    `gorm:"type:varchar(200);not null;uniqueIndex:idx_queue_deliveries_message_group,priority:2;index:idx_queue_deliveries_ready,priority:2;index:idx_queue_deliveries_key,priority:2"`
	MsgKey        *string   `gorm:"type:varchar(255);index:idx_queue_deliveries_key,priority:3,where:status <> 'dead'"`
	Status        string    `gorm:"type:varchar(16);not null"`
	Attempts      int       `gorm:"not null;default:0"`
	DeliverAt     int64     `gorm:"not null;default:0"` // unix µs the message is scheduled for; 0 means at once
	NextAttemptAt int64     `gorm:"not null"`           // unix µs
	ClaimedBy     *string   `gorm:"type:varchar(255)"`
	ClaimToken    *string   `gorm:"type:varchar(128);index:idx_queue_deliveries_claim_token"`
	ClaimedUntil  *int64    // unix µs
	DeadAt        int64     `gorm:"not null;default:0"` // unix µs it was dead-lettered; dead retention runs from here
	LastError     *string   `gorm:"type:text"`
	CreatedAt     time.Time `gorm:"not null;index:idx_queue_deliveries_created"`
}

func (tableQueueDelivery) TableName() string { return "queue_deliveries" }
