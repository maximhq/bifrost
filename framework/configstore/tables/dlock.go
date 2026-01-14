package tables

import "time"

// TableDistributedLock represents a distributed lock entry in the database.
// This table is used to implement distributed locking across multiple instances.
type TableDistributedLock struct {
	LockKey   string    `gorm:"primaryKey;column:lock_key;size:255" json:"lock_key"`
	HolderID  string    `gorm:"column:holder_id;size:255;not null" json:"holder_id"`
	ExpiresAt time.Time `gorm:"column:expires_at;not null;index" json:"expires_at"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}

// TableName returns the table name for the distributed lock table.
func (TableDistributedLock) TableName() string {
	return "distributed_locks"
}
