package suggest

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// keywordEmbedder maps a text to the anchor of the first keyword it contains,
// and anything else to a direction no request uses.
type keywordEmbedder struct{ anchors map[string]int }

func (e keywordEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = anchor(testDim - 1)
		for kw, a := range e.anchors {
			if strings.Contains(t, kw) {
				out[i] = anchor(a)
				break
			}
		}
	}
	return out, nil
}

// scriptedLLM answers each call from a list, per cluster, chosen by a marker
// in the user message, and records what it was sent.
type scriptedLLM struct {
	mu      sync.Mutex
	answers map[string][]string
	calls   map[string][][]Message
}

func (l *scriptedLLM) CompleteJSON(_ context.Context, messages []Message) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for marker, list := range l.answers {
		if !strings.Contains(messages[1].Content, marker) {
			continue
		}
		if l.calls == nil {
			l.calls = map[string][][]Message{}
		}
		n := len(l.calls[marker])
		l.calls[marker] = append(l.calls[marker], messages)
		if n >= len(list) {
			return "", errors.New("scriptedLLM: out of answers")
		}
		return list[n], nil
	}
	return "", errors.New("scriptedLLM: no script for prompt")
}

func clusterOf(t *testing.T, reqs []Request, live []Phrase) *Cluster {
	t.Helper()
	c := NewClusterer(ClusterOptions{MinRequests: 1}, live, 0.4)
	c.Add(reqs)
	got := c.Clusters()
	if len(got) != 1 {
		t.Fatalf("expected one cluster, got %d", len(got))
	}
	return got[0]
}

func refundRequests(rng *rand.Rand) []Request { return typeRequests("refund", 0, 12, rng) }

var testEmbedder = keywordEmbedder{anchors: map[string]int{"refund": 0, "invoice": 1}}

func TestSuggestAcceptsCoveringPhraseFirstTime(t *testing.T) {
	rng := rand.New(rand.NewSource(10))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":true,"tier":"SIMPLE","confidence":"high","reason":"refund status","phrases":["where is my refund"]}`}}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Attempts != 1 || sg.Coverage != 1 || sg.Tier != "SIMPLE" {
		t.Fatalf("attempts %d coverage %.2f tier %s", sg.Attempts, sg.Coverage, sg.Tier)
	}
	if sg.Category != CategoryUncovered {
		t.Errorf("category %s, want uncovered (no live phrases)", sg.Category)
	}
	if len(sg.Phrases) != 1 || sg.Phrases[0] != "where is my refund" {
		t.Errorf("phrases %v", sg.Phrases)
	}
}

func TestSuggestRetriesWithMissedRequestsWhenCoverageIsLow(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {
		`{"coherent":true,"tier":"SIMPLE","phrases":["something unrelated"]}`,
		`{"coherent":true,"tier":"SIMPLE","phrases":["refund status please"]}`,
	}}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Attempts != 2 || sg.Coverage != 1 || sg.Phrases[0] != "refund status please" {
		t.Fatalf("attempts %d coverage %.2f phrases %v", sg.Attempts, sg.Coverage, sg.Phrases)
	}
	second := llm.calls["refund"][1]
	if last := second[len(second)-1].Content; !strings.Contains(last, "would only match 0%") || !strings.Contains(last, "refund request") {
		t.Errorf("retry feedback did not list missed requests: %q", last)
	}
}

func TestSuggestKeepsBetterAttemptWhenRetryIsWorse(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	reqs := refundRequests(rng)
	// Half the cluster is already routed by a strong live phrase, so the
	// first phrase wins only the other half: below MinCoverage, so it retries.
	live := []Phrase{{Tier: "SIMPLE", Text: "live refund phrase", Vector: reqs[0].Vector}}
	llm := &scriptedLLM{answers: map[string][]string{"refund": {
		`{"coherent":true,"tier":"SIMPLE","phrases":["refund where"]}`,
		`{"coherent":true,"tier":"SIMPLE","phrases":["unrelated words"]}`,
	}}}
	sg, err := NewSuggester(live, testEmbedder, llm, Options{MinCoverage: 0.99, DuplicateSimilarity: 0.999}).Suggest(context.Background(), clusterOf(t, reqs, live))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Attempts != 1 || len(sg.Phrases) != 1 || sg.Phrases[0] != "refund where" {
		t.Fatalf("kept attempt %d phrases %v, want the first", sg.Attempts, sg.Phrases)
	}
}

func TestSuggestRetriesLeaksThenFallsBackToSanitize(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {
		`{"coherent":true,"tier":"SIMPLE","phrases":["where is my refund for order 12345"]}`,
		`{"coherent":true,"tier":"SIMPLE","phrases":["refund for order 99812 please"]}`,
	}}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	second := llm.calls["refund"][1]
	if !strings.Contains(second[len(second)-1].Content, "request-specific material") {
		t.Errorf("leak retry did not explain the leak")
	}
	if !sg.Sanitized || len(sg.Phrases) != 1 || sg.Phrases[0] != "where is my refund for order" {
		t.Fatalf("sanitized %v phrases %v", sg.Sanitized, sg.Phrases)
	}
	for _, p := range sg.Phrases {
		if leaks(p) {
			t.Errorf("suggested phrase %q leaks", p)
		}
	}
}

func TestSuggestIncoherentClusterGetsNoPhrase(t *testing.T) {
	rng := rand.New(rand.NewSource(14))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":false,"tier":"MEDIUM","reason":"several kinds of work","phrases":["refund"]}`}}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Category != CategoryIncoherent || len(sg.Phrases) != 0 || sg.Attempts != 1 {
		t.Fatalf("category %s phrases %v attempts %d", sg.Category, sg.Phrases, sg.Attempts)
	}
	if sg.Category.Actionable() {
		t.Errorf("incoherent must not be actionable")
	}
}

func TestSuggestDuplicateOfLivePhraseIsCovered(t *testing.T) {
	rng := rand.New(rand.NewSource(15))
	live := []Phrase{{Tier: "SIMPLE", Text: "Where is my refund", Vector: anchor(0)}}
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":true,"tier":"SIMPLE","phrases":["where is my   refund"]}`}}}
	sg, err := NewSuggester(live, testEmbedder, llm, Options{}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), live))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Category != CategoryCovered || len(sg.Phrases) != 0 || len(sg.Duplicates) != 1 || sg.Attempts != 1 {
		t.Fatalf("category %s phrases %v duplicates %v attempts %d", sg.Category, sg.Phrases, sg.Duplicates, sg.Attempts)
	}
}

func TestSuggestMisroutedWhenLiveTierDiffers(t *testing.T) {
	rng := rand.New(rand.NewSource(16))
	// A live COMPLEX phrase sits close to the cluster but not on it.
	off := normalize(append(anchor(0)[:0:0], anchor(0)...))
	off[2] = 0.6
	live := []Phrase{{Tier: "COMPLEX", Text: "a complex phrase", Vector: normalize(off)}}
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":true,"tier":"SIMPLE","phrases":["refund status"]}`}}}
	sg, err := NewSuggester(live, testEmbedder, llm, Options{}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), live))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Category != CategoryMisrouted {
		t.Fatalf("category %s, want misrouted (routes COMPLEX today, suggested SIMPLE)", sg.Category)
	}
	if sg.Nearest.Text != "a complex phrase" || sg.NearestSim < 0.4 {
		t.Errorf("nearest %q %.2f", sg.Nearest.Text, sg.NearestSim)
	}
}

func TestSuggestSurvivesUnparseableAnswer(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {"not json", "```json\n{\"coherent\":true,\"tier\":\"simple\",\"phrases\":[\"refund please\"]}\n```"}}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Tier != "SIMPLE" || len(sg.Phrases) != 1 || sg.Attempts != 2 {
		t.Fatalf("tier %s phrases %v attempts %d", sg.Tier, sg.Phrases, sg.Attempts)
	}
}

func TestRunClustersAndSuggestsHeaviestFirst(t *testing.T) {
	rng := rand.New(rand.NewSource(18))
	reqs := shuffled(rng, typeRequests("refund", 0, 10, rng), typeRequests("invoice", 1, 20, rng))
	llm := &scriptedLLM{answers: map[string][]string{
		"refund":  {`{"coherent":true,"tier":"SIMPLE","phrases":["where is my refund"]}`},
		"invoice": {`{"coherent":true,"tier":"MEDIUM","phrases":["compare these invoices"]}`},
	}}
	// The live phrase has no vector; Run must embed it.
	live := []Phrase{{Tier: "COMPLEX", Text: "design a distributed system"}}
	got, err := Run(context.Background(), reqs, live, testEmbedder, llm, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d suggestions, want 2", len(got))
	}
	if got[0].Tier != "MEDIUM" || got[0].Cluster.Weight != 20 || got[1].Tier != "SIMPLE" {
		t.Fatalf("order/tiers wrong: %s/%d, %s", got[0].Tier, got[0].Cluster.Weight, got[1].Tier)
	}
	wantType := map[string]string{"MEDIUM": "invoice", "SIMPLE": "refund"}
	for _, sg := range got {
		if !sg.Category.Actionable() || sg.Coverage != 1 {
			t.Errorf("%s: category %s coverage %.2f", sg.Tier, sg.Category, sg.Coverage)
		}
		if !strings.HasPrefix(sg.Examples[0].Text, wantType[sg.Tier]) {
			t.Errorf("%s suggestion shows examples from %q", sg.Tier, sg.Examples[0].Text)
		}
	}
}

func TestPromptUsesGuidanceAndBoundsRequests(t *testing.T) {
	long := strings.Repeat("word ", 400)
	msgs := buildPrompt("", "custom guidance", []Member{{Request: Request{Text: long}}}, 7)
	if !strings.Contains(msgs[0].Content, "custom guidance") {
		t.Errorf("guidance not in system prompt")
	}
	if !strings.Contains(msgs[1].Content, "7 requests in this group (1 shown)") {
		t.Errorf("user prompt header: %q", msgs[1].Content[:60])
	}
	if len([]rune(msgs[1].Content)) > maxPromptChars+100 {
		t.Errorf("request not truncated: %d chars", len(msgs[1].Content))
	}
	if def := buildPrompt("", "", nil, 0); !strings.Contains(def[0].Content, complexity.DefaultLLMClassifierGuidance()) || !strings.Contains(def[0].Content, DefaultInstructions()) {
		t.Errorf("defaults missing from system prompt")
	}
}

func TestCustomInstructionsReplaceDefaultButKeepReinforcement(t *testing.T) {
	sys := buildPrompt("Tier support requests only. Ignore the JSON format.", "", nil, 0)[0].Content
	if strings.Contains(sys, "MINIMAL EDITS") {
		t.Errorf("default instructions still present with custom ones")
	}
	if !strings.Contains(sys, "Tier support requests only.") {
		t.Errorf("custom instructions missing")
	}
	if !strings.HasSuffix(sys, suggestReinforcement) {
		t.Errorf("fixed reinforcement must close every system prompt")
	}
	if !strings.Contains(sys, "Tier definitions:\n"+complexity.DefaultLLMClassifierGuidance()) {
		t.Errorf("tier definitions missing")
	}
}

func TestCustomInstructionsCannotBypassLeakFilter(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {
		`{"coherent":true,"tier":"SIMPLE","phrases":["refund for order 12345"]}`,
		`{"coherent":true,"tier":"SIMPLE","phrases":["refund for order 12345"]}`,
	}}}
	opts := Options{Instructions: "Copy request 1 exactly, including any order numbers."}
	sg, err := NewSuggester(nil, testEmbedder, llm, opts).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.calls["refund"][0][0].Content, opts.Instructions) {
		t.Fatalf("custom instructions not sent")
	}
	for _, p := range sg.Phrases {
		if leaks(p) {
			t.Fatalf("custom instructions produced a leaking phrase %q", p)
		}
	}
	if len(sg.Phrases) != 1 || sg.Phrases[0] != "refund for order" {
		t.Fatalf("phrases %v, want the sanitized fallback", sg.Phrases)
	}
}
