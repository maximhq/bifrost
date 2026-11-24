package lib

import (
	"encoding/json"
	"sync"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/valyala/fasthttp"
)

// RecordAuditChange attaches an entity change to the in-flight request so an
// audit layer can record it with a before/after diff. before and after are
// snapshotted to JSON immediately; pass nil for the side that does not exist.
// Call it as soon as the write commits, before any step that can still fail:
// the change is recorded whatever status the request ends with. Use the
// returned handle to refine the after state or to discard the change when a
// rollback undoes the write. It costs a JSON marshal per call and is otherwise
// inert in OSS builds. Call it from the request's own goroutine: the append is
// not synchronized.
func RecordAuditChange(ctx *fasthttp.RequestCtx, eventName, targetType, targetID, targetName string, before, after any) *AuditRecord {
	if ctx == nil || eventName == "" {
		return nil
	}
	change := configstore.AuditChange{
		EventName:  eventName,
		TargetType: targetType,
		TargetID:   targetID,
		TargetName: targetName,
		Before:     AuditSnapshot(before),
		After:      AuditSnapshot(after),
	}
	changes, _ := ctx.UserValue(configstore.AuditChangesUserValueKey).([]configstore.AuditChange)
	ctx.SetUserValue(configstore.AuditChangesUserValueKey, append(changes, change))
	return &AuditRecord{ctx: ctx, index: len(changes)}
}

// AuditRecord is a change recorded on the in-flight request. A nil record is a
// no-op, so callers need not check what RecordAuditChange returned.
type AuditRecord struct {
	ctx   *fasthttp.RequestCtx
	index int
}

// SetAfter replaces the recorded after state, e.g. with the row a reload returned.
func (r *AuditRecord) SetAfter(after any) {
	if r == nil {
		return
	}
	data, ok := auditSnapshot(after)
	if !ok {
		return // unencodable: a nil After would read as a delete, so keep the commit-time snapshot
	}
	r.update(func(c *configstore.AuditChange) { c.After = data })
}

// Discard drops the change, for a handler whose rollback undid the write.
func (r *AuditRecord) Discard() {
	r.update(func(c *configstore.AuditChange) { c.Discarded = true })
}

func (r *AuditRecord) update(apply func(*configstore.AuditChange)) {
	if r == nil {
		return
	}
	changes, _ := r.ctx.UserValue(configstore.AuditChangesUserValueKey).([]configstore.AuditChange)
	if r.index < len(changes) {
		apply(&changes[r.index])
	}
}

// AuditSnapshot marshals v for RecordAuditChange, dropping the named top-level
// fields (use it to strip a secret a type's MarshalJSON would otherwise emit).
// Returns nil for nil input or anything that fails to marshal.
func AuditSnapshot(v any, dropFields ...string) json.RawMessage {
	data, _ := auditSnapshot(v, dropFields...)
	return data
}

// auditSnapshot is AuditSnapshot plus ok, false only when v cannot be marshalled.
func auditSnapshot(v any, dropFields ...string) (json.RawMessage, bool) {
	if v == nil {
		return nil, true
	}
	var data []byte
	if raw, ok := v.(json.RawMessage); ok {
		data = raw
		if len(dropFields) == 0 {
			return raw, true
		}
	} else {
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			return nil, false
		}
		if string(data) == "null" {
			return nil, true
		}
	}
	if len(dropFields) == 0 {
		return data, true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return data, true
	}
	for _, f := range dropFields {
		delete(fields, f)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	return out, true
}

// startupAuditChanges collects the config.json sections LoadConfig applied over
// the stored config, for an audit layer to record once it is running.
var (
	startupAuditMu      sync.Mutex
	startupAuditChanges []configstore.AuditChange
)

// noteConfigFileApplied records that LoadConfig synced a config.json section
// over the stored copy. section is e.g. "client", "provider", "mcp_client".
func noteConfigFileApplied(section, targetID string) {
	startupAuditMu.Lock()
	defer startupAuditMu.Unlock()
	startupAuditChanges = append(startupAuditChanges, configstore.AuditChange{
		EventName:  "config.file_applied",
		TargetType: section,
		TargetID:   targetID,
		TargetName: targetID,
	})
}

// DrainStartupAuditChanges returns and clears the config.json syncs recorded
// since the last call.
func DrainStartupAuditChanges() []configstore.AuditChange {
	startupAuditMu.Lock()
	defer startupAuditMu.Unlock()
	out := startupAuditChanges
	startupAuditChanges = nil
	return out
}
