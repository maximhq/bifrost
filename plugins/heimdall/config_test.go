package heimdall

import (
	"testing"
	"time"
)

func TestConfig_Defaults(t *testing.T) {
	for _, in := range []*Config{nil, {}} {
		got, err := in.withDefaults()
		if err != nil {
			t.Fatalf("withDefaults(%v): %v", in, err)
		}
		if got.MaxConcurrency != 8 || got.MaxSessions != 200 || got.MaxSteps != 20 || got.RetentionDays != 7 {
			t.Errorf("unexpected defaults: %+v", got)
		}
		d, err := got.callTimeout()
		if err != nil || d != 120*time.Second {
			t.Errorf("callTimeout = %v, %v; want 2m0s", d, err)
		}
		if got.AllowUnauthMutations {
			t.Error("unauthenticated mutations must default to off")
		}
	}
}

func TestConfig_KeepsSetValues(t *testing.T) {
	in := &Config{MaxConcurrency: 1, MaxSessions: 5, MaxSteps: 3, CallTimeout: "30s", RetentionDays: 1}
	got, err := in.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	want := *in
	want.Defaults = DefaultRunParams() // an unset block of run defaults takes the built-in numbers
	if *got != want {
		t.Errorf("withDefaults changed set values: got %+v, want %+v", got, want)
	}

	set := &Config{Defaults: RunParams{Margin: 0.2, MinSessions: 5, Seed: 3}}
	got, err = set.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.Defaults.Margin != 0.2 || got.Defaults.MinSessions != 5 || got.Defaults.Seed != 3 || got.Defaults.BaselineRuns != 3 {
		t.Errorf("run defaults = %+v; want the set numbers kept and the rest filled", got.Defaults)
	}
}

func TestConfig_DoesNotModifyInput(t *testing.T) {
	in := &Config{}
	if _, err := in.withDefaults(); err != nil {
		t.Fatal(err)
	}
	if *in != (Config{}) {
		t.Errorf("input was modified: %+v", in)
	}
}

func TestConfig_Invalid(t *testing.T) {
	cases := map[string]*Config{
		"negative concurrency": {MaxConcurrency: -1},
		"negative sessions":    {MaxSessions: -1},
		"negative steps":       {MaxSteps: -1},
		"negative retention":   {RetentionDays: -1},
		"timeout not a number": {CallTimeout: "soon"},
		"timeout zero":         {CallTimeout: "0s"},
		"timeout negative":     {CallTimeout: "-5s"},
	}
	for name, cfg := range cases {
		if _, err := cfg.withDefaults(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
