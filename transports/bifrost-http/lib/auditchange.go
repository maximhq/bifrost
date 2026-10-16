package lib

import (
	"encoding/json"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// RecordAuditChange attaches an entity change to the in-flight request so an
// audit layer can record it with a before/after diff. before and after are
// snapshotted to JSON immediately; pass nil for the side that does not exist.
// It costs a JSON marshal per call and is otherwise inert in OSS builds.
func RecordAuditChange(ctx *fasthttp.RequestCtx, eventName, targetType, targetID, targetName string, before, after any) {
	if ctx == nil || eventName == "" {
		return
	}
	change := schemas.AuditChange{
		EventName:  eventName,
		TargetType: targetType,
		TargetID:   targetID,
		TargetName: targetName,
		Before:     AuditSnapshot(before),
		After:      AuditSnapshot(after),
	}
	changes, _ := ctx.UserValue(schemas.AuditChangesUserValueKey).([]schemas.AuditChange)
	ctx.SetUserValue(schemas.AuditChangesUserValueKey, append(changes, change))
}

// AuditSnapshot marshals v for RecordAuditChange, dropping the named top-level
// fields (use it to strip a secret a type's MarshalJSON would otherwise emit).
// Returns nil for nil input or anything that fails to marshal.
func AuditSnapshot(v any, dropFields ...string) json.RawMessage {
	if v == nil {
		return nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" {
		return nil
	}
	if len(dropFields) == 0 {
		return data
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return data
	}
	for _, f := range dropFields {
		delete(fields, f)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return out
}

// startupAuditChanges collects the config.json sections LoadConfig applied over
// the stored config, for an audit layer to record once it is running.
var (
	startupAuditMu      sync.Mutex
	startupAuditChanges []schemas.AuditChange
)

// noteConfigFileApplied records that LoadConfig synced a config.json section
// over the stored copy. section is e.g. "client", "provider", "mcp_client".
func noteConfigFileApplied(section, targetID string) {
	startupAuditMu.Lock()
	defer startupAuditMu.Unlock()
	startupAuditChanges = append(startupAuditChanges, schemas.AuditChange{
		EventName:  "config.file_applied",
		TargetType: section,
		TargetID:   targetID,
		TargetName: targetID,
	})
}

// DrainStartupAuditChanges returns and clears the config.json syncs recorded
// since the last call.
func DrainStartupAuditChanges() []schemas.AuditChange {
	startupAuditMu.Lock()
	defer startupAuditMu.Unlock()
	out := startupAuditChanges
	startupAuditChanges = nil
	return out
}
