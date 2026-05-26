// Package featureflags provides a process-wide boolean toggle registry with
// layered configuration sources (code default, configstore DB, config.json
// file) and an optional SyncDelegate hook for cluster gossip.
//
// Precedence (highest wins):
//
//	config.json file  >  configstore DB  >  code default
//
// File-locked flags reject Set() with ErrLocked and silently ignore
// ApplyRemote, preserving the GitOps invariant that operators' config.json /
// Helm values cannot be overridden at runtime by either UI toggles or peer
// gossip. The DB row is still kept around inert so it re-emerges if the file
// override is later removed.
package featureflags

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Source records which configuration layer produced the effective value.
// Surfaced in the API/UI so operators can see why a flag is on or off
// without grepping logs.
type Source string

const (
	SourceDefault Source = "default"
	SourceDB      Source = "db"
	SourceRemote  Source = "remote"
	SourceFile    Source = "file"
)

var (
	// ErrFlagNotFound means the id is neither registered in code nor
	// present in the store's override map.
	ErrFlagNotFound = errors.New("feature flag not found")
	// ErrFlagLocked means the flag's value is pinned by config.json or
	// Helm. The operator must edit the file (or redeploy) to change it.
	ErrFlagLocked = errors.New("feature flag is locked by config.json")
	// ErrFlagUnregistered means the flag exists in the override store but
	// no code registered it, so toggling has no effect. The caller should
	// DELETE the stale row instead.
	ErrFlagUnregistered = errors.New("feature flag has no code registration")
	// ErrFlagEnterpriseOnly means the flag is marked EnterpriseOnly in
	// its registration and the current process is running in OSS mode.
	// Such flags are inert: IsEnabled always returns false and toggling
	// is rejected.
	ErrFlagEnterpriseOnly = errors.New("feature flag is enterprise-only")
)

// FlagStatus is the API/UI-facing snapshot of a single flag. ID is the
// stable identifier; DisplayName is what the UI renders. Keeping both on
// the wire lets the UI show the friendly label as the primary text and the
// id as muted secondary text for debugging.
type FlagStatus struct {
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	Default        bool   `json:"default"`
	Enabled        bool   `json:"enabled"`
	Source         Source `json:"source"`
	Locked         bool   `json:"locked"`
	Registered     bool   `json:"registered"`
	EnterpriseOnly bool   `json:"enterprise_only"`
	UpdatedAt      int64  `json:"updated_at,omitempty"`
}

// HydrationRow is the shape produced by configstore.ListFeatureFlags. We
// avoid importing configstore here to keep the package free of DB types.
type HydrationRow struct {
	ID        string
	Enabled   bool
	UpdatedAt int64
}

// SyncDelegate is invoked synchronously after a local Set() succeeds.
// Enterprise's gossip layer implements this and broadcasts each change to
// peers. ApplyRemote is the inbound path; it deliberately does NOT call the
// delegate to prevent echo loops.
type SyncDelegate interface {
	OnSet(id string, enabled bool, writtenAt int64)
}

// Config controls Store behavior.
type Config struct {
	// IsEnterprise should be true when the binary is the enterprise build.
	// Flags registered with EnterpriseOnly=true are inert when this is
	// false: IsEnabled returns false, Set rejects with ErrFlagEnterpriseOnly,
	// and the UI renders them disabled. Wired from initFeatureFlags by
	// checking schemas.BifrostContextKeyIsEnterprise on the bootstrap ctx.
	IsEnterprise bool
}

type entry struct {
	enabled   bool
	source    Source
	writtenAt int64
	fromFile  bool
}

// Store holds the per-process effective state for every flag.
type Store struct {
	mu           sync.RWMutex
	entries      map[string]entry
	delegate     SyncDelegate
	delegateM    sync.RWMutex
	isEnterprise bool
}

// New creates a Store. Call Hydrate and ApplyFile during bootstrap to load
// DB and file overrides respectively.
func New(cfg Config) (*Store, error) {
	return &Store{
		entries:      make(map[string]entry),
		isEnterprise: cfg.IsEnterprise,
	}, nil
}

// SetDelegate installs (or replaces) the gossip hook. Safe to call before or
// after the store has any entries; only future Set() calls are observed.
func (s *Store) SetDelegate(d SyncDelegate) {
	s.delegateM.Lock()
	s.delegate = d
	s.delegateM.Unlock()
}

// IsEnabled is the hot-path read. Unregistered/unknown flags return false so
// guarding code can treat "I don't recognize this id" as "off." Enterprise-
// only flags always return false in OSS mode regardless of any override,
// which lets guarding code use the flag uniformly across builds without
// extra plumbing.
func (s *Store) IsEnabled(id string) bool {
	if def, ok := LookupDef(id); ok && def.EnterpriseOnly && !s.isEnterprise {
		return false
	}
	s.mu.RLock()
	if e, ok := s.entries[id]; ok {
		s.mu.RUnlock()
		return e.enabled
	}
	s.mu.RUnlock()
	if def, ok := LookupDef(id); ok {
		return def.Default
	}
	return false
}

// Set is the LOCAL toggle path. Used by the HTTP handler when an operator
// flips a switch in the UI. Returns ErrFlagLocked if the flag is currently
// pinned by config.json, ErrFlagUnregistered if no code registered it.
func (s *Store) Set(_ context.Context, id string, enabled bool) (FlagStatus, error) {
	def, registered := LookupDef(id)
	if !registered {
		return FlagStatus{}, fmt.Errorf("%w: %q", ErrFlagUnregistered, id)
	}
	if def.EnterpriseOnly && !s.isEnterprise {
		return FlagStatus{}, fmt.Errorf("%w: %q", ErrFlagEnterpriseOnly, id)
	}

	now := time.Now().UnixNano()

	s.mu.Lock()
	if cur, ok := s.entries[id]; ok && cur.fromFile {
		s.mu.Unlock()
		return FlagStatus{}, fmt.Errorf("%w: %q", ErrFlagLocked, id)
	}
	s.entries[id] = entry{
		enabled:   enabled,
		source:    SourceDB,
		writtenAt: now,
	}
	s.mu.Unlock()

	s.delegateM.RLock()
	d := s.delegate
	s.delegateM.RUnlock()
	if d != nil {
		d.OnSet(id, enabled, now)
	}

	return s.statusFor(def, registered), nil
}

// ApplyRemote is the inbound gossip path. Last-write-wins by writtenAt.
// File-locked flags are silently ignored: each node trusts its own config.
// The delegate is NOT invoked, preventing echo loops.
func (s *Store) ApplyRemote(id string, enabled bool, writtenAt int64) {
	s.mu.Lock()
	cur, exists := s.entries[id]
	if exists && cur.fromFile {
		s.mu.Unlock()
		return
	}
	if exists && cur.writtenAt > writtenAt {
		s.mu.Unlock()
		return
	}
	s.entries[id] = entry{
		enabled:   enabled,
		source:    SourceRemote,
		writtenAt: writtenAt,
	}
	s.mu.Unlock()
}

// ApplyFile installs a value from config.json. Called LAST during bootstrap
// (after Hydrate) so file values win. fromFile=true means subsequent Set
// calls reject and gossip is ignored. The delegate is NOT invoked since
// file values are node-local by design.
func (s *Store) ApplyFile(id string, enabled bool) {
	now := time.Now().UnixNano()
	s.mu.Lock()
	s.entries[id] = entry{
		enabled:   enabled,
		source:    SourceFile,
		writtenAt: now,
		fromFile:  true,
	}
	s.mu.Unlock()
}

// EntrySnapshot opaquely captures the full prior state of a feature flag
// override (enabled / source / writtenAt / fromFile) so a caller can roll
// back a subsequent failed mutation without corrupting metadata. The
// fields are unexported - the only valid use is passing the snapshot back
// to Restore.
type EntrySnapshot struct {
	e entry
}

// Snapshot captures the current override for id, if any. The second
// return is false when no override exists; pass it back to Restore to
// preserve "no override -> fall back to default/code" semantics, rather
// than mis-recording the flag as a forever-pinned remote entry.
func (s *Store) Snapshot(id string) (EntrySnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[id]
	return EntrySnapshot{e: e}, ok
}

// Restore reverts the in-memory entry for id to a snapshot captured
// earlier. When hadEntry is false (no override existed when the snapshot
// was taken), the current entry is deleted instead so the flag returns
// to its code default. Restore does NOT fire the gossip delegate;
// rollback is a local concern and re-broadcasting would create cluster
// noise after a single-node failure.
func (s *Store) Restore(id string, snap EntrySnapshot, hadEntry bool) {
	s.mu.Lock()
	if hadEntry {
		s.entries[id] = snap.e
	} else {
		delete(s.entries, id)
	}
	s.mu.Unlock()
}

// Hydrate loads DB overrides during bootstrap. Call BEFORE ApplyFile so
// file values overwrite any DB conflicts in-memory while leaving the DB row
// intact (so removing the file override later re-exposes the DB value).
func (s *Store) Hydrate(rows []HydrationRow) {
	s.mu.Lock()
	for _, row := range rows {
		s.entries[row.ID] = entry{
			enabled:   row.Enabled,
			source:    SourceDB,
			writtenAt: row.UpdatedAt,
		}
	}
	s.mu.Unlock()
}

// List returns a status row for every flag known to the process: registered
// flags (always included, even when at default) plus any orphan rows in the
// override map (e.g. a config.json or DB entry whose code registration was
// removed). Sorted by id for deterministic output.
func (s *Store) List() []FlagStatus {
	defs := RegisteredDefs()
	defByID := make(map[string]FlagDef, len(defs))
	for _, d := range defs {
		defByID[d.ID] = d
	}

	s.mu.RLock()
	ids := make(map[string]struct{}, len(defs)+len(s.entries))
	for _, d := range defs {
		ids[d.ID] = struct{}{}
	}
	for id := range s.entries {
		ids[id] = struct{}{}
	}
	out := make([]FlagStatus, 0, len(ids))
	for id := range ids {
		def, registered := defByID[id]
		if !registered {
			def = FlagDef{ID: id}
		}
		out = append(out, s.statusForLocked(def, registered))
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Status returns the status of a single flag. Unregistered flags with no
// override return ErrFlagNotFound.
func (s *Store) Status(id string) (FlagStatus, error) {
	def, registered := LookupDef(id)
	s.mu.RLock()
	_, hasOverride := s.entries[id]
	s.mu.RUnlock()
	if !registered && !hasOverride {
		return FlagStatus{}, fmt.Errorf("%w: %q", ErrFlagNotFound, id)
	}
	if !registered {
		def = FlagDef{ID: id}
	}
	return s.statusFor(def, registered), nil
}

// statusFor takes the read lock; safe to call from public methods.
func (s *Store) statusFor(def FlagDef, registered bool) FlagStatus {
	s.mu.RLock()
	out := s.statusForLocked(def, registered)
	s.mu.RUnlock()
	return out
}

// statusForLocked must be called with s.mu held (read or write).
func (s *Store) statusForLocked(def FlagDef, registered bool) FlagStatus {
	status := FlagStatus{
		ID:             def.ID,
		DisplayName:    def.DisplayName,
		Description:    def.Description,
		Default:        def.Default,
		Enabled:        def.Default,
		Source:         SourceDefault,
		Registered:     registered,
		EnterpriseOnly: def.EnterpriseOnly,
	}
	if e, ok := s.entries[def.ID]; ok {
		status.Enabled = e.enabled
		status.Source = e.source
		status.Locked = e.fromFile
		status.UpdatedAt = e.writtenAt
	}
	// In OSS mode, enterprise-only flags are forced off and reported as
	// locked so the UI can render them disabled without needing to look
	// up the build mode separately.
	if def.EnterpriseOnly && !s.isEnterprise {
		status.Enabled = false
		status.Locked = true
	}
	return status
}
