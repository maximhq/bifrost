package heimdall

import (
	"context"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

var _ schemas.LLMPlugin = (*Plugin)(nil)

func testLogger() schemas.Logger { return bifrost.NewDefaultLogger(schemas.LogLevelError) }

func baseDeps(t *testing.T) Deps {
	t.Helper()
	return Deps{Account: newTestAccount(), LogStore: newFakeLogs(), ConfigStore: newTestConfigStore(t)}
}

func TestInit_Defaults(t *testing.T) {
	p, err := Init(context.Background(), nil, testLogger(), baseDeps(t))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = p.Cleanup() })
	if p.GetName() != PluginName || PluginName != "heimdall" {
		t.Errorf("name = %q, want heimdall", p.GetName())
	}
	if p.config.MaxConcurrency != 8 || p.config.Defaults.Margin != 0.10 || p.config.Defaults.MinSessions != 30 {
		t.Errorf("config defaults were not applied: %+v", p.config)
	}
}

func TestInit_NeedsWhatItRunsOn(t *testing.T) {
	deps := baseDeps(t)
	for name, tc := range map[string]struct {
		logger  schemas.Logger
		mutate  func(*Deps)
		message string
	}{
		"no logger":       {nil, func(*Deps) {}, "logger"},
		"no log store":    {testLogger(), func(d *Deps) { d.LogStore = nil }, "log store"},
		"no config store": {testLogger(), func(d *Deps) { d.ConfigStore = nil }, "config store"},
		"no account":      {testLogger(), func(d *Deps) { d.Account = nil }, "account"},
	} {
		d := deps
		tc.mutate(&d)
		p, err := Init(context.Background(), nil, tc.logger, d)
		if err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%s: err = %v, want one that mentions %q", name, err, tc.message)
			if p != nil {
				_ = p.Cleanup()
			}
		}
	}
}

func TestInit_RejectsInvalidConfig(t *testing.T) {
	for name, cfg := range map[string]*Config{
		"call timeout":      {CallTimeout: "never"},
		"default margin":    {Defaults: RunParams{Margin: 5}},
		"default run count": {Defaults: RunParams{BaselineRuns: 9}},
		"negative sessions": {MaxSessions: -1},
	} {
		if p, err := Init(context.Background(), cfg, testLogger(), baseDeps(t)); err == nil {
			_ = p.Cleanup()
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestInit_EndsRunsLeftByAPreviousProcess(t *testing.T) {
	deps := baseDeps(t)
	s, ctx := &store{cs: deps.ConfigStore}, context.Background()
	mustCreateRun(t, s, testRun("done", StatusComplete, 2*time.Hour))
	mustCreateRun(t, s, testRun("died", StatusRunning, 0))

	p, err := Init(ctx, nil, testLogger(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Cleanup() })
	if died, _ := s.getRun(ctx, "died"); died.Status != StatusInterrupted || died.FinishedAt == nil || died.Verdict != nil {
		t.Errorf("a run left running = %+v; want it interrupted, ended, with no verdict", died)
	}
	if done, _ := s.getRun(ctx, "done"); done.Status != StatusComplete {
		t.Errorf("a finished run = %s", done.Status)
	}
	// With the stale run ended, a new run can start.
	if active, _ := s.activeRun(ctx); active != nil {
		t.Errorf("active run = %+v", active)
	}
}

func TestInit_ClearsContentPastRetention(t *testing.T) {
	deps := baseDeps(t)
	s, ctx := &store{cs: deps.ConfigStore}, context.Background()
	old := testRun("old", StatusComplete, 40*24*time.Hour)
	old.CreatedAt = time.Now().Add(-40 * 24 * time.Hour)
	fresh := testRun("fresh", StatusComplete, 0)
	fresh.CreatedAt = time.Now()
	mustCreateRun(t, s, old)
	mustCreateRun(t, s, fresh)
	for _, id := range []string{"old", "fresh"} {
		if err := s.createRunSessions(ctx, []tables.TableHeimdallRunSession{sessionRow(id, "a", CodeEligible, 1, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Init(ctx, &Config{RetentionDays: 7}, testLogger(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Cleanup() })
	if got, _ := s.getRunSession(ctx, "old", "a"); got.SnapshotJSON != nil || got.CriticalCount != 1 {
		t.Errorf("old session = %+v; want the snapshot gone and the score kept", got)
	}
	if got, _ := s.getRunSession(ctx, "fresh", "a"); got.SnapshotJSON == nil {
		t.Error("a recent run lost its snapshot")
	}
}

func TestInit_HooksDoNothing(t *testing.T) {
	p, err := Init(context.Background(), nil, testLogger(), baseDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	req := &schemas.BifrostRequest{}
	if err := p.PreRequestHook(nil, req); err != nil {
		t.Errorf("PreRequestHook: %v", err)
	}
	gotReq, short, err := p.PreLLMHook(nil, req)
	if gotReq != req || short != nil || err != nil {
		t.Errorf("PreLLMHook = %v, %v, %v; want the same request, no short circuit, no error", gotReq, short, err)
	}
	resp := &schemas.BifrostResponse{}
	bErr := &schemas.BifrostError{}
	gotResp, gotErr, err := p.PostLLMHook(nil, resp, bErr)
	if gotResp != resp || gotErr != bErr || err != nil {
		t.Errorf("PostLLMHook changed its inputs")
	}
	if err := p.Cleanup(); err != nil {
		t.Errorf("Cleanup: %v", err)
	}
	// Cleanup is safe to call twice.
	if err := p.Cleanup(); err != nil {
		t.Errorf("second Cleanup: %v", err)
	}
}
