package featureflags

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingDelegate captures OnSet invocations so tests can assert delegate
// fire counts and arguments.
type recordingDelegate struct {
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	id        string
	enabled   bool
	writtenAt int64
}

func (d *recordingDelegate) OnSet(id string, enabled bool, writtenAt int64) {
	d.mu.Lock()
	d.calls = append(d.calls, recordedCall{id, enabled, writtenAt})
	d.mu.Unlock()
}

func (d *recordingDelegate) Calls() []recordedCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]recordedCall, len(d.calls))
	copy(out, d.calls)
	return out
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	resetRegistryForTest()
	s, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func newEnterpriseTestStore(t *testing.T) *Store {
	t.Helper()
	resetRegistryForTest()
	s, err := New(Config{IsEnterprise: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestRegister_RejectsBadIDs(t *testing.T) {
	resetRegistryForTest()
	bad := []string{"", "UPPERCASE", "has space", "_leading", "trailing_", "dou--ble"}
	for _, id := range bad {
		if err := Register(FlagDef{ID: id}); !errors.Is(err, ErrFlagIDInvalid) {
			t.Errorf("Register(%q): want ErrFlagIDInvalid, got %v", id, err)
		}
	}
}

func TestRegister_AcceptsValidIDs(t *testing.T) {
	resetRegistryForTest()
	good := []string{"simple", "dot.namespace", "with-dash", "a1.b2-c3"}
	for _, id := range good {
		if err := Register(FlagDef{ID: id}); err != nil {
			t.Errorf("Register(%q): %v", id, err)
		}
	}
}

func TestRegister_DuplicateFails(t *testing.T) {
	resetRegistryForTest()
	if err := Register(FlagDef{ID: "foo"}); err != nil {
		t.Fatal(err)
	}
	if err := Register(FlagDef{ID: "foo"}); !errors.Is(err, ErrFlagAlreadyRegistered) {
		t.Errorf("duplicate Register: want ErrFlagAlreadyRegistered, got %v", err)
	}
}

func TestIsEnabled_UsesDefault(t *testing.T) {
	s := newTestStore(t)
	if err := Register(FlagDef{ID: "on-by-default", Default: true}); err != nil {
		t.Fatal(err)
	}
	if err := Register(FlagDef{ID: "off-by-default", Default: false}); err != nil {
		t.Fatal(err)
	}
	if !s.IsEnabled("on-by-default") {
		t.Errorf("on-by-default: want true")
	}
	if s.IsEnabled("off-by-default") {
		t.Errorf("off-by-default: want false")
	}
	if s.IsEnabled("unknown") {
		t.Errorf("unknown flag: want false")
	}
}

func TestSet_OverridesDefaultAndFiresDelegate(t *testing.T) {
	s := newTestStore(t)
	if err := Register(FlagDef{ID: "feat.a", DisplayName: "Feature A", Default: false}); err != nil {
		t.Fatal(err)
	}
	d := &recordingDelegate{}
	s.SetDelegate(d)

	status, err := s.Set(context.Background(), "feat.a", true)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !status.Enabled || status.Source != SourceDB || status.DisplayName != "Feature A" {
		t.Errorf("status = %+v, want enabled=true source=db display_name=Feature A", status)
	}
	if !s.IsEnabled("feat.a") {
		t.Error("IsEnabled after Set: want true")
	}

	calls := d.Calls()
	if len(calls) != 1 {
		t.Fatalf("delegate calls = %d, want 1", len(calls))
	}
	if calls[0].id != "feat.a" || !calls[0].enabled {
		t.Errorf("delegate call = %+v, want feat.a/true", calls[0])
	}
}

func TestSet_UnregisteredRejected(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Set(context.Background(), "unknown", true)
	if !errors.Is(err, ErrFlagUnregistered) {
		t.Errorf("Set(unknown): want ErrFlagUnregistered, got %v", err)
	}
}

func TestApplyRemote_DoesNotFireDelegate(t *testing.T) {
	s := newTestStore(t)
	if err := Register(FlagDef{ID: "feat.b"}); err != nil {
		t.Fatal(err)
	}
	d := &recordingDelegate{}
	s.SetDelegate(d)

	s.ApplyRemote("feat.b", true, time.Now().UnixNano())
	if !s.IsEnabled("feat.b") {
		t.Error("after ApplyRemote: want enabled")
	}
	if calls := d.Calls(); len(calls) != 0 {
		t.Errorf("delegate calls = %d, want 0 (echo-loop guard)", len(calls))
	}
}

func TestApplyRemote_LastWriteWins(t *testing.T) {
	s := newTestStore(t)
	_ = Register(FlagDef{ID: "feat.lww"})

	s.ApplyRemote("feat.lww", true, 100)
	s.ApplyRemote("feat.lww", false, 50) // older write, must be ignored
	if !s.IsEnabled("feat.lww") {
		t.Errorf("older write must not override newer (LWW)")
	}

	s.ApplyRemote("feat.lww", false, 200) // newer write, applied
	if s.IsEnabled("feat.lww") {
		t.Errorf("newer write must override older")
	}
}

func TestFileLock_RejectsSetAndIgnoresRemote(t *testing.T) {
	s := newTestStore(t)
	_ = Register(FlagDef{ID: "feat.locked", Default: false})

	s.ApplyFile("feat.locked", true)
	if !s.IsEnabled("feat.locked") {
		t.Error("ApplyFile did not take effect")
	}

	_, err := s.Set(context.Background(), "feat.locked", false)
	if !errors.Is(err, ErrFlagLocked) {
		t.Errorf("Set on file-locked: want ErrFlagLocked, got %v", err)
	}

	// Gossip from a peer must be silently ignored: the node trusts its own
	// config.json. No error returned (gossip is fire-and-forget); value
	// stays at the file-pinned setting.
	s.ApplyRemote("feat.locked", false, time.Now().UnixNano())
	if !s.IsEnabled("feat.locked") {
		t.Error("file-locked flag must ignore remote gossip")
	}
}

func TestHydrate_ThenApplyFile_FileWins(t *testing.T) {
	s := newTestStore(t)
	_ = Register(FlagDef{ID: "feat.pri"})

	s.Hydrate([]HydrationRow{{ID: "feat.pri", Enabled: false, UpdatedAt: 1}})
	s.ApplyFile("feat.pri", true)

	st, err := s.Status("feat.pri")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.Source != SourceFile || !st.Locked {
		t.Errorf("status = %+v, want enabled=true source=file locked=true", st)
	}
}

func TestList_IncludesUnregisteredOverrides(t *testing.T) {
	s := newTestStore(t)
	_ = Register(FlagDef{ID: "feat.known", DisplayName: "Known", Description: "known", Default: false})
	s.Hydrate([]HydrationRow{{ID: "feat.orphan", Enabled: true, UpdatedAt: 42}})

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2 (known + orphan)", len(list))
	}
	byID := map[string]FlagStatus{}
	for _, st := range list {
		byID[st.ID] = st
	}
	if !byID["feat.known"].Registered {
		t.Error("feat.known: want registered=true")
	}
	if byID["feat.known"].DisplayName != "Known" {
		t.Errorf("feat.known: display_name = %q, want Known", byID["feat.known"].DisplayName)
	}
	if byID["feat.orphan"].Registered {
		t.Error("feat.orphan: want registered=false")
	}
	if !byID["feat.orphan"].Enabled {
		t.Error("feat.orphan: want enabled (came from hydrate)")
	}
}

func TestSnapshotRestore_PreservesSource(t *testing.T) {
	// Captures the prior entry verbatim - source, writtenAt, fromFile -
	// so a rollback after a failed downstream write doesn't downgrade a
	// "file" or "db" sourced flag to SourceRemote (the bug that the
	// naive ApplyRemote-based rollback had).
	s := newTestStore(t)
	_ = Register(FlagDef{ID: "feat.file", Default: false})
	s.ApplyFile("feat.file", true) // source=file, fromFile=true

	snap, had := s.Snapshot("feat.file")
	if !had {
		t.Fatal("Snapshot: expected hadEntry=true for file-applied flag")
	}

	// Simulate a successful local mutation that we then need to roll
	// back. Direct entry write since Set would be rejected by the file
	// lock - we want to verify Restore puts the metadata back exactly.
	s.mu.Lock()
	s.entries["feat.file"] = entry{enabled: false, source: SourceRemote, writtenAt: 999}
	s.mu.Unlock()

	s.Restore("feat.file", snap, had)

	st, err := s.Status("feat.file")
	if err != nil {
		t.Fatal(err)
	}
	if st.Source != SourceFile || !st.Locked || !st.Enabled {
		t.Errorf("after Restore: status = %+v, want source=file locked=true enabled=true", st)
	}
}

func TestSnapshotRestore_DeletesWhenNoPriorEntry(t *testing.T) {
	// The subtle bug greptile identified: when the flag had no prior
	// override (default-valued), a naive rollback that inserts a zero-
	// value entry permanently pollutes the in-memory map with a spurious
	// {enabled:false, source:remote} record. Restore must delete instead.
	s := newTestStore(t)
	_ = Register(FlagDef{ID: "feat.def", Default: true})

	snap, had := s.Snapshot("feat.def")
	if had {
		t.Fatal("Snapshot: expected hadEntry=false for default-valued flag")
	}

	// Simulate a local Set that succeeded then needs rollback.
	if _, err := s.Set(context.Background(), "feat.def", false); err != nil {
		t.Fatal(err)
	}

	s.Restore("feat.def", snap, had)

	// After Restore, the flag should report SourceDefault (no entry),
	// and IsEnabled should reflect the code default (true), NOT the
	// rolled-back false that would persist if Restore left an entry.
	st, err := s.Status("feat.def")
	if err != nil {
		t.Fatal(err)
	}
	if st.Source != SourceDefault {
		t.Errorf("after Restore: source = %s, want default (no entry should remain)", st.Source)
	}
	if !s.IsEnabled("feat.def") {
		t.Error("IsEnabled after Restore: want true (code default), got false (spurious entry)")
	}
}

func TestSnapshotRestore_NoDelegateFire(t *testing.T) {
	// Rollback must be local-only - re-broadcasting after a single-node
	// DB failure would create gossip noise.
	s := newTestStore(t)
	_ = Register(FlagDef{ID: "feat.silent"})

	snap, had := s.Snapshot("feat.silent")
	if _, err := s.Set(context.Background(), "feat.silent", true); err != nil {
		t.Fatal(err)
	}

	d := &recordingDelegate{}
	s.SetDelegate(d)
	s.Restore("feat.silent", snap, had)

	if calls := d.Calls(); len(calls) != 0 {
		t.Errorf("Restore must NOT fire delegate, got %d calls", len(calls))
	}
}

func TestEnterpriseOnly_InertInOSS(t *testing.T) {
	s := newTestStore(t)
	_ = Register(FlagDef{ID: "ent.only", DisplayName: "Enterprise Only", EnterpriseOnly: true, Default: true})

	// IsEnabled is forced false in OSS even though Default is true.
	if s.IsEnabled("ent.only") {
		t.Error("EnterpriseOnly flag must be inert in OSS")
	}

	// Set is rejected.
	if _, err := s.Set(context.Background(), "ent.only", true); !errors.Is(err, ErrFlagEnterpriseOnly) {
		t.Errorf("Set on enterprise-only in OSS: want ErrFlagEnterpriseOnly, got %v", err)
	}

	// Status surfaces both enterprise_only and locked so the UI can render
	// a disabled toggle with the right badge.
	st, err := s.Status("ent.only")
	if err != nil {
		t.Fatal(err)
	}
	if !st.EnterpriseOnly || !st.Locked || st.Enabled {
		t.Errorf("status = %+v, want enterprise_only=true locked=true enabled=false", st)
	}
}

func TestEnterpriseOnly_ActiveInEnterprise(t *testing.T) {
	s := newEnterpriseTestStore(t)
	_ = Register(FlagDef{ID: "ent.only", EnterpriseOnly: true, Default: false})

	if _, err := s.Set(context.Background(), "ent.only", true); err != nil {
		t.Fatalf("Set on enterprise-only in enterprise: %v", err)
	}
	if !s.IsEnabled("ent.only") {
		t.Error("enterprise-only flag in enterprise build should toggle normally")
	}
}
