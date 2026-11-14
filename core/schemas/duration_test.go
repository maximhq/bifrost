package schemas

import (
	"encoding/json"
	"testing"
	"time"
)

// TestParseFlexibleDuration_EmptyStringIsZero pins that an empty duration
// string means "unset", the same as an absent or null field, rather than a
// parse error. Clearing a duration field in a UI form or a config file sends
// "", and rejecting it would leave callers with no way to express "disabled"
// other than the integer 0.
func TestParseFlexibleDuration_EmptyStringIsZero(t *testing.T) {
	for _, raw := range []string{`""`, `"   "`} {
		dur, err := ParseFlexibleDuration(json.RawMessage(raw), "vk_rotation_cooldown")
		if err != nil {
			t.Fatalf("ParseFlexibleDuration(%s) returned error: %v", raw, err)
		}
		if dur != 0 {
			t.Fatalf("ParseFlexibleDuration(%s) = %v, want 0", raw, dur)
		}
	}
}

func TestParseFlexibleDuration_AcceptedForms(t *testing.T) {
	cases := map[string]time.Duration{
		`"5m"`:         5 * time.Minute,
		`"1m30s"`:      90 * time.Second,
		`300000000000`: 5 * time.Minute,
		`0`:            0,
		`null`:         0,
	}
	for raw, want := range cases {
		got, err := ParseFlexibleDuration(json.RawMessage(raw), "duration")
		if err != nil {
			t.Fatalf("ParseFlexibleDuration(%s) returned error: %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseFlexibleDuration(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestParseFlexibleDuration_InvalidStringStillRejected(t *testing.T) {
	if _, err := ParseFlexibleDuration(json.RawMessage(`"xyz"`), "vk_rotation_cooldown"); err == nil {
		t.Fatal("expected an error for a non-duration string, got nil")
	}
}

// TestDurationUnmarshalJSON_EmptyString covers the same contract through the
// Duration type used by config fields.
func TestDurationUnmarshalJSON_EmptyString(t *testing.T) {
	var cfg struct {
		Cooldown Duration `json:"vk_rotation_cooldown"`
	}
	if err := json.Unmarshal([]byte(`{"vk_rotation_cooldown": ""}`), &cfg); err != nil {
		t.Fatalf("unmarshalling an empty cooldown returned error: %v", err)
	}
	if cfg.Cooldown.D() != 0 {
		t.Fatalf("empty cooldown = %v, want 0", cfg.Cooldown.D())
	}
}
