package configstore

import "encoding/json"

// AuditChangesUserValueKey is the fasthttp user value under which OSS handlers
// stash the AuditChange records for the in-flight request. OSS never reads it;
// an enterprise audit layer drains it after the handler returns and turns each
// record into a typed audit event with a before/after diff.
const AuditChangesUserValueKey = "bifrost-audit-changes"

// AuditChange describes one entity change a management handler made. Before
// and After are JSON snapshots taken when the handler records the change, so a
// later in-place mutation cannot alter them. Snapshots must not carry secret
// values: callers pass redacted forms.
type AuditChange struct {
	// EventName is the typed event name, "<resource>.<verb>", e.g. "virtual_key.updated".
	EventName string
	// TargetType is the resource type, e.g. "virtual_key", "provider".
	TargetType string
	// TargetID identifies the changed entity.
	TargetID string
	// TargetName is a human-readable name for the entity.
	TargetName string
	// Before is the entity's state before the change. Nil on create.
	Before json.RawMessage
	// After is the entity's state after the change. Nil on delete.
	After json.RawMessage
}
