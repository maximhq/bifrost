package tables

import "time"

// Provider object kinds: the families of provider-side objects whose creating
// virtual key is recorded so later requests can be bound to it.
const (
	ProviderObjectKindBatch         = "batch"
	ProviderObjectKindVideo         = "video"
	ProviderObjectKindFile          = "file"
	ProviderObjectKindContainer     = "container"
	ProviderObjectKindCachedContent = "cached_content"
)

// TableProviderObject records which virtual key created a provider-side object.
//
// Objects such as files, videos, containers and cached contents are created
// through the operator's provider key, which every virtual key allowed on the
// provider shares, and are afterwards addressed by their provider-side id alone.
// This row is the only place the gateway remembers who created one. It is written
// once, when the create response comes back, and removed when the object is
// deleted through the gateway. Batches and videos also have a TableProviderJob
// row for accounting; this table is the ownership record for every kind.
type TableProviderObject struct {
	ID           string    `gorm:"primaryKey;type:varchar(768)" json:"id"`
	Kind         string    `gorm:"type:varchar(50);not null;index:idx_provider_objects_kind_provider,priority:1" json:"kind"`
	Provider     string    `gorm:"type:varchar(255);not null;index:idx_provider_objects_kind_provider,priority:2" json:"provider"`
	ObjectID     string    `gorm:"type:varchar(512);not null" json:"object_id"`
	VirtualKeyID string    `gorm:"type:varchar(255);not null;index:idx_provider_objects_virtual_key_id" json:"virtual_key_id"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`
}

// TableName returns the backing table name.
func (TableProviderObject) TableName() string {
	return "provider_objects"
}

// ProviderObjectID builds the stable primary key for a provider object from its
// kind, provider and provider-side id.
func ProviderObjectID(kind, provider, objectID string) string {
	return kind + ":" + provider + ":" + objectID
}
