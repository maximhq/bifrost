package agentcapabilityrouter

import "testing"

func TestCapabilityClassification(t *testing.T) {
	cfg := defaultConfig()
	tests := []struct {
		name   string
		events []SignalEvent
		want   string
	}{
		{"architecture becomes orchestrate", []SignalEvent{{Kind: "user", Text: "Design a migration architecture and explain the trade-offs."}}, CapabilityOrchestrate},
		{"edit becomes implement", []SignalEvent{{Kind: "edit", Text: "apply_patch authentication middleware"}}, CapabilityImplement},
		{"successful command becomes tool loop", []SignalEvent{{Kind: "tool-result", Text: "go test ./... ok"}}, CapabilityToolLoop},
		{"failure becomes debug", []SignalEvent{{Kind: "tool-result", Text: "FAIL TestAuthentication expected 200 got 401 exit status 1", Failed: true}}, CapabilityDebug},
		{"exploration becomes explore", []SignalEvent{{Kind: "search", Text: "Find where authentication middleware is registered"}}, CapabilityExplore},
		{"final information becomes summarize", []SignalEvent{{Kind: "user", Text: "Summarize what changed and list the files modified."}}, CapabilitySummarize},
		{"summary plus fix stays debug", []SignalEvent{{Kind: "user", Text: "Summarize why these tests are failing and fix them."}}, CapabilityDebug},
		{"ambiguous becomes general", []SignalEvent{{Kind: "user", Text: "continue"}}, CapabilityGeneral},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classify(SignalSnapshot{Events: test.events}, cfg)
			if got.Capability != test.want {
				t.Fatalf("capability=%q confidence=%.2f signals=%v, want %q", got.Capability, got.Confidence, got.Signals, test.want)
			}
		})
	}
}

func TestAgentLifecycleTransitions(t *testing.T) {
	cfg := defaultConfig()
	steps := []struct {
		event SignalEvent
		want  string
	}{
		{SignalEvent{Kind: "user", Text: "Design the solution architecture"}, CapabilityOrchestrate},
		{SignalEvent{Kind: "edit", Text: "Implement it with apply_patch"}, CapabilityImplement},
		{SignalEvent{Kind: "tool-result", Text: "go test ./... ok"}, CapabilityToolLoop},
		{SignalEvent{Kind: "tool-result", Text: "FAIL TestLogin exit status 1", Failed: true}, CapabilityDebug},
		{SignalEvent{Kind: "edit", Text: "Patch the diagnosed condition"}, CapabilityImplement},
		{SignalEvent{Kind: "user", Text: "Summarize what changed"}, CapabilitySummarize},
	}
	for index, step := range steps {
		got := classify(SignalSnapshot{Events: []SignalEvent{step.event}}, cfg)
		if got.Capability != step.want {
			t.Fatalf("step %d capability=%q, want %q", index+1, got.Capability, step.want)
		}
	}
}

func TestLaterActionMovesPastHistoricalFailure(t *testing.T) {
	cfg := defaultConfig()
	got := classify(SignalSnapshot{Events: []SignalEvent{
		{Kind: "tool-result", Text: "FAIL TestLogin exit status 1", Failed: true},
		{Kind: "edit", Text: "Patch the diagnosed condition"},
	}}, cfg)
	if got.Capability != CapabilityImplement {
		t.Fatalf("capability=%q, want %q", got.Capability, CapabilityImplement)
	}
}

func TestUnknownKeywordGroupIsIgnoredByClassifier(t *testing.T) {
	cfg := defaultConfig()
	cfg.Keywords["unknown"] = []string{"anything"}
	got := classify(SignalSnapshot{Events: []SignalEvent{{Kind: "user", Text: "anything"}}}, cfg)
	if got.Capability != CapabilityGeneral {
		t.Fatalf("capability=%q, want %q", got.Capability, CapabilityGeneral)
	}
}
