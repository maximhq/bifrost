package suggest

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
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

// markerTier decides tiers by a marker in the shown requests, as scriptedLLM
// answers, and counts its calls.
type markerTier struct {
	mu    sync.Mutex // clusters are asked about on several workers
	tiers map[string]string
	calls int
	seen  []TierInput
}

func (m *markerTier) DecideTier(_ context.Context, in TierInput) (TierVerdict, error) {
	m.mu.Lock()
	m.calls++
	m.seen = append(m.seen, in)
	m.mu.Unlock()
	joined := strings.Join(in.Requests, "\n")
	for marker, tier := range m.tiers {
		if strings.Contains(joined, marker) {
			return TierVerdict{Tier: tier, Confidence: "high", Reason: "decided by " + marker}, nil
		}
	}
	return TierVerdict{}, errors.New("markerTier: no tier for these requests")
}

func simpleTier() *markerTier { return &markerTier{tiers: map[string]string{"refund": "SIMPLE"}} }

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
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Tier: simpleTier()}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
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
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Tier: simpleTier()}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
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
	sg, err := NewSuggester(live, testEmbedder, llm, Options{MinCoverage: 0.99, DuplicateSimilarity: 0.999, Tier: simpleTier()}).Suggest(context.Background(), clusterOf(t, reqs, live))
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
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Tier: simpleTier()}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
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
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":false,"phrases":["refund"]}`}}}
	tier := simpleTier()
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Tier: tier}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	if tier.calls != 0 {
		t.Errorf("an incoherent cluster must not cost a tier call, got %d", tier.calls)
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
	sg, err := NewSuggester(live, testEmbedder, llm, Options{Tier: simpleTier()}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), live))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Category != CategoryCovered || len(sg.Phrases) != 0 || len(sg.Duplicates) != 1 || sg.Attempts != 1 {
		t.Fatalf("category %s phrases %v duplicates %v attempts %d", sg.Category, sg.Phrases, sg.Duplicates, sg.Attempts)
	}
}

func TestSuggestDuplicateOfLivePhraseInOtherTierIsMisrouted(t *testing.T) {
	rng := rand.New(rand.NewSource(15))
	live := []Phrase{{Tier: "COMPLEX", Text: "Where is my refund", Vector: anchor(0)}}
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":true,"tier":"SIMPLE","phrases":["where is my refund"]}`}}}
	sg, err := NewSuggester(live, testEmbedder, llm, Options{Tier: simpleTier()}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), live))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Category != CategoryMisrouted || len(sg.Phrases) != 0 || len(sg.Duplicates) != 1 {
		t.Fatalf("category %s phrases %v duplicates %v, want misrouted (live phrase routes COMPLEX, suggested SIMPLE)", sg.Category, sg.Phrases, sg.Duplicates)
	}
}

func TestSuggestMisroutedWhenLiveTierDiffers(t *testing.T) {
	rng := rand.New(rand.NewSource(16))
	// A live COMPLEX phrase sits close to the cluster but not on it.
	off := normalize(append(anchor(0)[:0:0], anchor(0)...))
	off[2] = 0.6
	live := []Phrase{{Tier: "COMPLEX", Text: "a complex phrase", Vector: normalize(off)}}
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":true,"tier":"SIMPLE","phrases":["refund status"]}`}}}
	sg, err := NewSuggester(live, testEmbedder, llm, Options{Tier: simpleTier()}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), live))
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
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Tier: simpleTier()}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
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
	got, err := Run(context.Background(), reqs, live, testEmbedder, llm, Options{Tier: &markerTier{tiers: map[string]string{"refund": "SIMPLE", "invoice": "MEDIUM"}}})
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

func TestPhrasePromptBoundsRequestsAndLeavesTierOut(t *testing.T) {
	long := strings.Repeat("word ", 400)
	msgs := buildPrompt("", []Member{{Request: Request{Text: long}}}, 7)
	if !strings.Contains(msgs[1].Content, "7 requests in this group (1 shown)") {
		t.Errorf("user prompt header: %q", msgs[1].Content[:60])
	}
	if len([]rune(msgs[1].Content)) > maxPromptChars+100 {
		t.Errorf("request not truncated: %d chars", len(msgs[1].Content))
	}
	sys := buildPrompt("", nil, 0)[0].Content
	if !strings.Contains(sys, DefaultInstructions()) || !strings.HasSuffix(sys, suggestReinforcement) {
		t.Errorf("default instructions or reinforcement missing from the phrase prompt")
	}
	// Tiers belong to the tier step: the phrase step neither defines them nor
	// asks for one.
	if strings.Contains(sys, "Tier definitions") || strings.Contains(sys, `"tier"`) {
		t.Errorf("phrase prompt still asks about tiers")
	}
}

func TestCustomInstructionsReplaceDefaultButKeepReinforcement(t *testing.T) {
	sys := buildPrompt("Write support phrases only. Ignore the JSON format.", nil, 0)[0].Content
	if strings.Contains(sys, "MINIMAL EDITS") {
		t.Errorf("default instructions still present with custom ones")
	}
	if !strings.Contains(sys, "Write support phrases only.") {
		t.Errorf("custom instructions missing")
	}
	if !strings.HasSuffix(sys, suggestReinforcement) {
		t.Errorf("fixed reinforcement must close every system prompt")
	}
}

// The tier is decided once per cluster, after the phrase attempts, from the
// requests the phrase step was shown.
func TestTierDecidedOnceAfterPhraseRetry(t *testing.T) {
	rng := rand.New(rand.NewSource(15))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {
		`{"coherent":true,"phrases":["something unrelated"]}`,
		`{"coherent":true,"phrases":["refund status please"]}`,
	}}}
	tier := &markerTier{tiers: map[string]string{"refund": "MEDIUM"}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Tier: tier}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Attempts != 2 || tier.calls != 1 {
		t.Fatalf("phrase attempts %d tier calls %d, want 2 and 1", sg.Attempts, tier.calls)
	}
	if sg.Tier != "MEDIUM" || sg.Reason != "decided by refund" || sg.Confidence != "high" {
		t.Fatalf("tier verdict not adopted: %s %q %q", sg.Tier, sg.Reason, sg.Confidence)
	}
	in := tier.seen[0]
	if in.Total != sg.Cluster.Weight || len(in.Requests) != len(sg.Examples) || in.Requests[0] != sg.Examples[0].Text {
		t.Fatalf("tier step saw %d of %d requests, want the phrase step's %d examples", len(in.Requests), in.Total, len(sg.Examples))
	}
}

// A tier step that gives no usable tier leaves the cluster unsuggested.
func TestNoUsableTierIsIncoherent(t *testing.T) {
	rng := rand.New(rand.NewSource(16))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":true,"phrases":["where is my refund"]}`}}}
	tier := &markerTier{tiers: map[string]string{"refund": ""}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Tier: tier}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil))
	if err != nil {
		t.Fatal(err)
	}
	if sg.Category != CategoryIncoherent || sg.Category.Actionable() {
		t.Fatalf("category %s, want incoherent", sg.Category)
	}
}

func TestSuggestNeedsTierDecider(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":true,"phrases":["where is my refund"]}`}}}
	if _, err := NewSuggester(nil, testEmbedder, llm, Options{}).Suggest(context.Background(), clusterOf(t, refundRequests(rng), nil)); err == nil {
		t.Fatal("a suggester without a tier decider must refuse to run")
	}
}

func TestLLMTierDeciderPrompt(t *testing.T) {
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"tier":"medium","confidence":"High","reason":" multi-step "}`}}}
	d := &LLMTierDecider{LLM: llm, Guidance: "custom guidance", Examples: map[string][]string{
		"SIMPLE":  {"check my order status"},
		"COMPLEX": {"plan our data migration"},
	}}
	v, err := d.DecideTier(context.Background(), TierInput{Requests: []string{"where is my refund", "refund please"}, Total: 9})
	if err != nil {
		t.Fatal(err)
	}
	if v != (TierVerdict{Tier: "MEDIUM", Confidence: "high", Reason: "multi-step"}) {
		t.Fatalf("verdict %+v", v)
	}
	msgs := llm.calls["refund"][0]
	sys := msgs[0].Content
	for _, want := range []string{DefaultTierInstructions(), "Tier definitions:\ncustom guidance", "SIMPLE:\n- check my order status", "COMPLEX:\n- plan our data migration"} {
		if !strings.Contains(sys, want) {
			t.Errorf("tier prompt missing %q", want)
		}
	}
	if strings.Contains(sys, "MEDIUM:") {
		t.Errorf("a tier with no examples must not get an empty example list")
	}
	if !strings.HasSuffix(sys, tierReinforcement) {
		t.Errorf("fixed tier reinforcement must close the tier prompt")
	}
	if !strings.Contains(msgs[1].Content, "9 requests in this group (2 shown)") {
		t.Errorf("tier user prompt: %q", msgs[1].Content)
	}

	// An edited prompt is sent as written, in place of the whole rendered
	// body (instructions, definitions and examples alike); only the fixed
	// reinforcement is added.
	llm = &scriptedLLM{answers: map[string][]string{"refund": {`{"tier":"SIMPLE"}`}}}
	edited := "Refund questions are SIMPLE.\nExamples:\n- where is my money"
	d = &LLMTierDecider{LLM: llm, Prompt: edited, Guidance: "custom guidance", Examples: map[string][]string{"SIMPLE": {"check my order status"}}}
	if _, err := d.DecideTier(context.Background(), TierInput{Requests: []string{"refund"}, Total: 1}); err != nil {
		t.Fatal(err)
	}
	sys = llm.calls["refund"][0][0].Content
	if sys != edited+"\n\n"+tierReinforcement {
		t.Errorf("edited prompt not sent verbatim with the reinforcement: %q", sys)
	}
}

// The rendered prompt is what an editor shows and an unedited run sends:
// instructions, definitions, then the examples block only when there are
// examples.
func TestTierPromptRendersInstructionsDefinitionsAndExamples(t *testing.T) {
	bare := TierPrompt("", nil)
	if !strings.HasPrefix(bare, DefaultTierInstructions()) || !strings.HasSuffix(bare, complexity.DefaultLLMClassifierGuidance()) {
		t.Errorf("rendered prompt without examples: %q", bare)
	}
	with := TierPrompt("custom guidance", map[string][]string{"MEDIUM": {"draft a refund reply"}})
	if !strings.HasSuffix(with, "MEDIUM:\n- draft a refund reply") || !strings.Contains(with, "Tier definitions:\ncustom guidance") {
		t.Errorf("rendered prompt with examples: %q", with)
	}
	if strings.Contains(with, tierReinforcement) {
		t.Errorf("the fixed reinforcement is not part of the editable body")
	}
}

// BankTierExamples reads a router's lists, skipping the shipped defaults.
func TestBankTierExamplesSkipShippedDefaults(t *testing.T) {
	defaults := complexity.DefaultEditableKeywordConfig()
	got := BankTierExamples(complexity.EditableKeywordConfig{
		SimpleKeywords:  []string{defaults.SimpleKeywords[0], "check my order status"},
		MediumKeywords:  []string{defaults.MediumKeywords[0]},
		ComplexKeywords: []string{"plan our data migration"},
	})
	want := map[string][]string{"SIMPLE": {"check my order status"}, "COMPLEX": {"plan our data migration"}}
	if len(got) != len(want) || !slices.Equal(got["SIMPLE"], want["SIMPLE"]) || !slices.Equal(got["COMPLEX"], want["COMPLEX"]) {
		t.Errorf("examples %v, want %v", got, want)
	}
}

// An unparseable or out-of-range tier answer is no usable tier, not a failed
// run, and costs no second call.
func TestLLMTierDeciderBadAnswerIsNoTier(t *testing.T) {
	for _, raw := range []string{"not json", `{"tier":"HARD"}`} {
		llm := &scriptedLLM{answers: map[string][]string{"refund": {raw}}}
		v, err := (&LLMTierDecider{LLM: llm}).DecideTier(context.Background(), TierInput{Requests: []string{"refund"}, Total: 1})
		if err != nil || v.Tier != "" || len(llm.calls["refund"]) != 1 {
			t.Fatalf("%q: verdict %+v err %v calls %d", raw, v, err, len(llm.calls["refund"]))
		}
	}
}

// Few-shot examples are the router's own phrases, never the shipped defaults,
// in the router's order, deduplicated and capped per tier.
func TestTierExamplesSkipShippedDefaults(t *testing.T) {
	defaults := map[string][]string{"SIMPLE": {"what does this error say?"}, "MEDIUM": {"add a log line here"}}
	live := []Phrase{
		{Tier: "SIMPLE", Text: "What does this error  say?"},
		{Tier: "SIMPLE", Text: "check my order status"},
		{Tier: "SIMPLE", Text: "Check my order status"},
		{Tier: "MEDIUM", Text: "add a log line here"},
		{Tier: "complex", Text: "plan our data migration"},
	}
	for i := 0; i < MaxTierExamples+3; i++ {
		live = append(live, Phrase{Tier: "MEDIUM", Text: fmt.Sprintf("draft reply number %d", i)})
	}
	got := TierExamples(live, defaults)
	if !slices.Equal(got["SIMPLE"], []string{"check my order status"}) {
		t.Errorf("simple examples %v", got["SIMPLE"])
	}
	if !slices.Equal(got["COMPLEX"], []string{"plan our data migration"}) {
		t.Errorf("complex examples %v", got["COMPLEX"])
	}
	if len(got["MEDIUM"]) != MaxTierExamples || got["MEDIUM"][0] != "draft reply number 0" {
		t.Errorf("medium examples %d, first %q", len(got["MEDIUM"]), got["MEDIUM"][0])
	}
	if untouched := TierExamples([]Phrase{{Tier: "SIMPLE", Text: "what does this error say?"}}, defaults); len(untouched) != 0 {
		t.Errorf("a router left on the defaults must send no examples, got %v", untouched)
	}
}

func TestCustomInstructionsCannotBypassLeakFilter(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	llm := &scriptedLLM{answers: map[string][]string{"refund": {
		`{"coherent":true,"tier":"SIMPLE","phrases":["refund for order 12345"]}`,
		`{"coherent":true,"tier":"SIMPLE","phrases":["refund for order 12345"]}`,
	}}}
	opts := Options{Instructions: "Copy request 1 exactly, including any order numbers.", Tier: simpleTier()}
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

// The config accepts exactly the instructions this package will use: a
// longer limit there would save instructions a run then refuses.
func TestInstructionsLimitMatchesConfig(t *testing.T) {
	if MaxInstructionsChars != configstore.MaxComplexitySuggestionsInstructionsChars {
		t.Fatalf("suggest.MaxInstructionsChars = %d, configstore allows %d", MaxInstructionsChars, configstore.MaxComplexitySuggestionsInstructionsChars)
	}
	if MaxTierPromptChars != configstore.MaxComplexitySuggestionsTierPromptChars {
		t.Fatalf("suggest.MaxTierPromptChars = %d, configstore allows %d", MaxTierPromptChars, configstore.MaxComplexitySuggestionsTierPromptChars)
	}
	if MaxTierExamples != configstore.MaxComplexityDecisionCriteriaItems {
		t.Fatalf("suggest.MaxTierExamples = %d, a decision tier allows %d examples", MaxTierExamples, configstore.MaxComplexityDecisionCriteriaItems)
	}
}

// Requests clustered without text are read back only for the members the LLM
// is shown; one that cannot be read is left out, and a cluster none of whose
// examples can be read is not sent at all.
func TestSuggestReadsTextsOnlyForShownMembers(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	reqs := refundRequests(rng)
	for i := range reqs {
		reqs[i].Text = ""
	}
	cl := clusterOf(t, reqs, nil)
	var asked []string
	texts := func(_ context.Context, ids []string) (map[string]string, error) {
		asked = append(asked, ids...)
		out := map[string]string{}
		for _, id := range ids {
			if id != cl.Members[0].ID { // the medoid's log was deleted
				out[id] = "refund " + id
			}
		}
		return out, nil
	}
	llm := &scriptedLLM{answers: map[string][]string{"refund": {`{"coherent":true,"tier":"SIMPLE","confidence":"high","reason":"refund status","phrases":["where is my refund"]}`}}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Representatives: 4, Texts: texts, Tier: simpleTier()}).Suggest(context.Background(), cl)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 4 {
		t.Fatalf("asked for %d texts, want only the 4 representatives", len(asked))
	}
	if len(sg.Examples) != 3 {
		t.Fatalf("examples %d, want 3: the unreadable medoid is left out", len(sg.Examples))
	}
	for _, m := range sg.Examples {
		if m.ID == cl.Members[0].ID || m.Text == "" {
			t.Errorf("example %s has text %q", m.ID, m.Text)
		}
	}

	none := func(context.Context, []string) (map[string]string, error) { return nil, nil }
	sg, err = NewSuggester(nil, testEmbedder, llm, Options{Texts: none, Tier: simpleTier()}).Suggest(context.Background(), cl)
	if err != nil {
		t.Fatal(err)
	}
	if sg.Category != CategoryUnreadable || sg.Category.Actionable() {
		t.Fatalf("category %s, want unreadable and not actionable", sg.Category)
	}
	if len(llm.calls["refund"]) != 1 {
		t.Errorf("an unreadable cluster must not reach the LLM")
	}
}
