package suggest

import (
	"context"
	"math/rand"
	"strings"
	"testing"
)

func members(texts ...string) []Member {
	out := make([]Member, len(texts))
	for i, t := range texts {
		out[i] = Member{Request: Request{ID: t, Text: t, Count: 1}}
	}
	return out
}

func TestSharedTemplate(t *testing.T) {
	scaffold := "You will act as a prompt generator for an image model. Describe the scene:"
	got := sharedTemplate(members(scaffold+" a cat on a roof", scaffold+" a busy market at dawn", scaffold+" two ships in a storm"))
	if got != strings.ToLower(strings.TrimSuffix(scaffold, "")) {
		t.Fatalf("template %q", got)
	}
	if got := sharedTemplate(members("summarise this: one text", "summarise this: another text", "summarise this: a third")); got != "" {
		t.Errorf("a short shared instruction is not a template, got %q", got)
	}
	if got := sharedTemplate(members(scaffold+" a cat", scaffold+" a dog")); got != "" {
		t.Errorf("two requests are not enough to call a template, got %q", got)
	}
	same := scaffold + " a cat on a roof"
	if got := sharedTemplate(members(same, same, same, same)); got != "" {
		t.Errorf("one request repeated is not a template, got %q", got)
	}
}

func TestPastedFrom(t *testing.T) {
	template := "write a long description of this film for a festival booklet"
	reps := members(
		template+" the lady had issues is a spy thriller set in vienna about a stolen formula",
		template+" a quiet harbour town hides a smuggling ring run by the mayor and his sons",
		template+" two rival chefs compete for a single michelin star in a small paris bistro",
	)
	if !pastedFrom("describe the lady had issues is a spy thriller set in vienna about a stolen formula", template, reps) {
		t.Error("a run of one request's fill is that request's own material")
	}
	if pastedFrom("write a long description of this film for a festival booklet", template, reps) {
		t.Error("the template's own wording is the type, not pasted")
	}
	if pastedFrom("describe the lady had issues is a spy thriller set in vienna about a stolen formula", "", reps) {
		t.Error("outside a template a minimal edit of one request is the design, not a leak")
	}
	if pastedFrom("describe a spy thriller set in vienna", template, reps) {
		t.Error("a short phrase cannot carry a long copied run")
	}
}

// A template cluster is told which wording is fixed, a phrase copying one
// request's fill is refused, and the suggestion is categorised as a template.
func TestSuggestTemplateCluster(t *testing.T) {
	rng := rand.New(rand.NewSource(41))
	scaffold := "You will act as a prompt generator for an image model. Describe the scene in rich detail:"
	// Every fill is long enough that a phrase can copy more than
	// pastedRunWords of it, whichever request the suggestion is written from.
	fills := []string{
		"a refund desk at closing time with a tired clerk counting coins by lamplight",
		"a refund queue in the rain where umbrellas drip on a long patient line",
		"a refund office full of plants and humming printers beside a sleepy cat",
		"a refund kiosk in an airport glowing under departure boards at midnight",
		"a refund counter run by robots with blinking eyes and polite metal voices",
		"a refund line at a stadium after the final whistle under bright floodlights",
		"a refund booth on a ferry rocking gently between grey harbour walls at dusk",
		"a refund window in a castle guarded by sleepy knights and faded old banners",
	}
	reqs := typeRequests("refund", 0, len(fills), rng)
	for i := range reqs {
		reqs[i].Text = scaffold + " " + fills[i]
	}
	cl := clusterOf(t, reqs, nil)
	// The pasted phrase copies the fill of the request the suggestion is
	// written from, in plain words, so the generic leak filter keeps it and
	// only pastedFrom can see it is that request's own material.
	reps := representatives(cl, 8)
	fill := strings.Join(words(reps[0].Text)[len(words(sharedTemplate(reps))):], " ")
	pastedPhrase := "picture " + fill
	if kept, _ := filterLeaks([]string{pastedPhrase}); len(kept) != 1 {
		t.Fatalf("%q must pass filterLeaks so the test reaches pastedFrom", pastedPhrase)
	}
	pasted := `{"coherent":true,"tier":"MEDIUM","confidence":"high","reason":"image prompt","phrases":["` + pastedPhrase + `"]}`
	clean := `{"coherent":true,"tier":"MEDIUM","confidence":"high","reason":"image prompt","phrases":["act as a prompt generator for an image model, refund scene"]}`
	llm := &scriptedLLM{answers: map[string][]string{"refund": {pasted, clean}}}
	sg, err := NewSuggester(nil, testEmbedder, llm, Options{Representatives: 8}).Suggest(context.Background(), cl)
	if err != nil {
		t.Fatal(err)
	}
	if sg.Template == "" || !strings.Contains(llm.calls["refund"][0][1].Content, "fixed template") {
		t.Fatalf("template %q not detected or not told to the LLM", sg.Template)
	}
	calls := llm.calls["refund"]
	if sg.Attempts != 2 || len(calls) != 2 || !strings.Contains(calls[1][len(calls[1])-1].Content, "request-specific material") {
		t.Fatalf("the pasted fill should be refused and sent back with leak feedback: attempts %d", sg.Attempts)
	}
	if len(sg.Phrases) != 1 || sg.Phrases[0] == pastedPhrase {
		t.Fatalf("phrases %v should be the clean retry", sg.Phrases)
	}
	if sg.Category != CategoryTemplate || !sg.Category.Actionable() {
		t.Fatalf("category %s, want template", sg.Category)
	}
}
