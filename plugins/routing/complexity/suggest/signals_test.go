package suggest

import (
	"context"
	"strings"
	"testing"
)

func signalGroups(n int) []SignalGroup {
	groups := make([]SignalGroup, n)
	for i := range groups {
		groups[i] = SignalGroup{
			Examples: []string{"Diagnose a failing build from its error"},
			Requests: []string{"why does my build fail", "fix this build error", "build broke after upgrade", "a fourth request"},
			Weight:   10 * (i + 1),
		}
	}
	return groups
}

func TestSuggestSignalsKeepsTraitsSeveralGroupsShare(t *testing.T) {
	llm := &scriptedLLM{answers: map[string][]string{"Tier: MEDIUM": {`{"signals": [
		{"text": "Diagnose a fault from the evidence the request provides", "groups": [1, 2]},
		{"text": "Explain one group's own task", "groups": [2]},
		{"text": "Fix order 48213 for the customer", "groups": [1, 2]},
		{"text": "Weigh a few dependent steps before answering", "groups": [1, 3, 3, 9]},
		{"text": "Repeat an existing trait", "groups": [1, 2]},
		{"text": "Something the user declined", "groups": [1, 2]}
	]}`}}}
	emb := keywordEmbedder{anchors: map[string]int{"Diagnose": 1, "Weigh": 2, "existing": 3, "declined": 4, "Repeat": 3, "Something": 4}}
	got, err := SuggestSignals(context.Background(), llm, emb, SignalInput{
		Tier: "MEDIUM", Definition: "Moderate work.",
		Signals:  []string{"An existing trait of the tier"},
		Declined: []string{"A trait the user declined"},
		Groups:   signalGroups(3),
		Max:      10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Asked {
		t.Fatal("a tier with three groups is asked about")
	}
	want := []Signal{
		{Text: "Diagnose a fault from the evidence the request provides", Groups: []int{0, 1}},
		{Text: "Weigh a few dependent steps before answering", Groups: []int{0, 2}},
	}
	if len(got.Signals) != len(want) {
		t.Fatalf("signals = %+v, want %+v", got.Signals, want)
	}
	for i := range want {
		if got.Signals[i].Text != want[i].Text || len(got.Signals[i].Groups) != len(want[i].Groups) {
			t.Fatalf("signal %d = %+v, want %+v", i, got.Signals[i], want[i])
		}
		for j := range want[i].Groups {
			if got.Signals[i].Groups[j] != want[i].Groups[j] {
				t.Fatalf("signal %d groups = %v, want %v (in range, distinct)", i, got.Signals[i].Groups, want[i].Groups)
			}
		}
	}
	if len(got.Narrow) != 1 || len(got.Leaked) != 1 || len(got.Duplicates) != 2 {
		t.Fatalf("narrow %v, leaked %v, duplicates %v: want one, one, and two", got.Narrow, got.Leaked, got.Duplicates)
	}

	prompt := llm.calls["Tier: MEDIUM"][0]
	for _, part := range []string{"Tier definition: Moderate work.", "- An existing trait of the tier", "- A trait the user declined", "Group 3 (30 requests).", "- Diagnose a failing build from its error"} {
		if !strings.Contains(prompt[1].Content, part) {
			t.Errorf("prompt is missing %q:\n%s", part, prompt[1].Content)
		}
	}
	if strings.Contains(prompt[1].Content, "a fourth request") {
		t.Errorf("a group shows at most %d requests", maxSignalGroupRequests)
	}
	if !strings.Contains(prompt[0].Content, `{"signals": [{"text": "...", "groups": [1, 2], "restates": ""}]}`) {
		t.Error("the system prompt ends with the fixed answer shape")
	}
}

func TestSuggestSignalsBoundsAndSkips(t *testing.T) {
	ctx := context.Background()
	emb := keywordEmbedder{anchors: map[string]int{"one": 1, "two": 2, "three": 3, "four": 4}}
	answer := `{"signals": [{"text": "Trait one of the work", "groups": [1, 2]}, {"text": "Trait two of the work", "groups": [1, 2]}, {"text": "Trait three of the work", "groups": [1, 2]}, {"text": "Trait four of the work", "groups": [1, 2]}]}`

	// No call for a tier with fewer groups than a signal must describe, or
	// with no room.
	for name, in := range map[string]SignalInput{
		"one group": {Tier: "SIMPLE", Groups: signalGroups(1), Max: 3},
		"no room":   {Tier: "SIMPLE", Groups: signalGroups(4), Max: 0},
	} {
		llm := &scriptedLLM{}
		got, err := SuggestSignals(ctx, llm, emb, in)
		if err != nil || got.Asked || len(llm.calls) != 0 {
			t.Fatalf("%s: asked %v, calls %d, err %v; want no call", name, got.Asked, len(llm.calls), err)
		}
	}

	// Room below the run's bound keeps the first that fit; the run's bound
	// caps a larger room.
	for _, tc := range []struct{ room, want int }{{2, 2}, {30, MaxSignalsPerRun}} {
		llm := &scriptedLLM{answers: map[string][]string{"Tier: COMPLEX": {answer}}}
		got, err := SuggestSignals(ctx, llm, emb, SignalInput{Tier: "COMPLEX", Groups: signalGroups(2), Max: tc.room})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Signals) != tc.want {
			t.Fatalf("room %d kept %d signals, want %d", tc.room, len(got.Signals), tc.want)
		}
	}

	// Groups past the bound are not shown.
	llm := &scriptedLLM{answers: map[string][]string{"Tier: COMPLEX": {`{"signals": []}`}}}
	if _, err := SuggestSignals(ctx, llm, emb, SignalInput{Tier: "COMPLEX", Groups: signalGroups(maxSignalGroups + 2), Max: 3}); err != nil {
		t.Fatal(err)
	}
	if content := llm.calls["Tier: COMPLEX"][0][1].Content; strings.Contains(content, "Group 13 ") {
		t.Errorf("at most %d groups are shown", maxSignalGroups)
	}

	// An answer that is not JSON fails the tier.
	bad := &scriptedLLM{answers: map[string][]string{"Tier: COMPLEX": {"not json"}}}
	if _, err := SuggestSignals(ctx, bad, emb, SignalInput{Tier: "COMPLEX", Groups: signalGroups(2), Max: 3}); err == nil {
		t.Fatal("an unreadable answer is an error")
	}
}

// A signal the model says restates an existing one is dropped whatever its
// wording, and once a tier has enough groups, one that describes most of
// them is dropped as too broad; one describing half is kept.
func TestSuggestSignalsDropsRestatedAndBroadSignals(t *testing.T) {
	llm := &scriptedLLM{answers: map[string][]string{"Tier: SIMPLE": {`{"signals": [
		{"text": "Recall only simple common facts", "groups": [1, 2], "restates": "The information is common knowledge"},
		{"text": "Broad trait of nearly every group", "groups": [1, 2, 3, 4, 5]},
		{"text": "Half trait of the groups", "groups": [1, 2, 3], "restates": " "},
		{"text": "Narrow trait of two groups", "groups": [4, 5], "restates": ""}
	]}`}}}
	emb := keywordEmbedder{anchors: map[string]int{"Recall": 1, "Broad": 2, "Half": 3, "Narrow": 4, "common knowledge": 5}}
	got, err := SuggestSignals(context.Background(), llm, emb, SignalInput{
		Tier: "SIMPLE", Signals: []string{"The information is common knowledge"}, Groups: signalGroups(6), Max: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, s := range got.Signals {
		kept = append(kept, s.Text)
	}
	if strings.Join(kept, "|") != "Half trait of the groups|Narrow trait of two groups" {
		t.Fatalf("kept %v", kept)
	}
	if len(got.Duplicates) != 1 || got.Duplicates[0] != "Recall only simple common facts" {
		t.Fatalf("duplicates %v: the restated signal", got.Duplicates)
	}
	if len(got.Broad) != 1 || got.Broad[0] != "Broad trait of nearly every group" {
		t.Fatalf("broad %v", got.Broad)
	}

	for _, tc := range []struct {
		covered, total int
		want           bool
	}{{2, 2, false}, {3, 3, false}, {3, 4, true}, {2, 4, false}, {8, 10, true}, {5, 10, false}} {
		if got := tooBroad(tc.covered, tc.total); got != tc.want {
			t.Errorf("tooBroad(%d, %d) = %v, want %v", tc.covered, tc.total, got, tc.want)
		}
	}
}
