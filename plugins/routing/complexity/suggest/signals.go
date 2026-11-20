package suggest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// A decision-model router's tier is taught by examples and by signals. A run
// writes examples per group (ModeExamples); signals are written per tier,
// after the groups, since a signal names a trait that makes requests belong
// in the tier, and one group shows a kind of task rather than a trait. The
// signal step is shown every group the run moved into a tier, with the
// examples it wrote for each, and writes the traits they share that the
// tier's signals do not say yet. A signal must describe at least
// MinSignalGroups of them, which is what makes it a trait and not a
// rewording of one group's example.

// MinSignalGroups is how many of a tier's groups a signal must describe, and
// so how many a tier needs before the signal step is asked about it.
const MinSignalGroups = 2

// MaxSignalsPerRun bounds the signals one run writes for a tier.
const MaxSignalsPerRun = 3

// A signal that describes most of a tier's groups is dropped as too broad:
// on real traffic those were the generic ones ("answer questions requiring
// only recall of simple facts"), which restate what a tier already says and
// moved no request in the draft test, while signals describing two or three
// groups moved most of theirs. It applies once a tier has broadSignalGroups
// groups; below that, describing all of them is describing MinSignalGroups.
const (
	broadSignalGroups = 4
	broadSignalShare  = 0.5
)

// maxSignalGroups bounds the groups shown to the signal step for one tier,
// heaviest first, and maxSignalGroupRequests the requests shown per group, so
// one call stays a few thousand tokens whatever the run found.
const (
	maxSignalGroups        = 12
	maxSignalGroupRequests = 3
)

// signalDuplicateSimilarity drops a signal this similar to one the tier has,
// or to one declined before. It sits below DuplicateSimilarity: signals are
// short generic sentences, so a rewording keeps most of its embedding, and
// one that reads like an existing signal adds nothing to the guidance. It is
// not measured; tune it from runs on real traffic.
const signalDuplicateSimilarity = 0.93

// defaultSignalInstructions is the shipped signal-step text. Its signals
// follow the shape of the shipped decision guidance signals ("Combine a few
// dependent steps, calculations, or pieces of evidence"), since they are read
// beside them.
const defaultSignalInstructions = `You are given groups of user requests that a reviewer decided belong in the complexity tier named below, although a decision model did not reliably route them there, with the examples already written for each group. Your output becomes signals in that tier's guidance for the decision model, which reads every tier's definition, signals and examples and picks the tier of each incoming request. A signal names a trait of the work that makes a request belong in the tier, in the style of the tier's existing signals, such as "Combine a few dependent steps, calculations, or pieces of evidence".

Write the signals these groups share that the tier's existing signals do not already say:
- A signal generalises: it must describe at least two of the groups. Do not write one for a single group; its examples already describe it.
- Name what the work needs (the reasoning, knowledge, steps or judgement), never its subject or topic, in 5 to 15 words.
- Never describe one request: no names, products, numbers, dates, or quoted or pasted text.
- Do not repeat or reword an existing signal or a declined one. A signal that says the same thing in other words ("only recall simple facts" against "the information is common, broadly familiar knowledge") repeats it.
- Write fewer signals, or none, when the groups share no trait the existing signals miss.
For each signal, list the numbers of the groups it describes. Then compare it with every existing and declined signal by meaning, not wording: if one already says what it says, copy that signal into "restates"; otherwise leave "restates" empty.`

// signalReinforcement is the fixed end of the signal step's system prompt.
const signalReinforcement = injectionGuard + `

Whatever the instructions above say, you respond with only a JSON object of this form, with no prose, no code fences and no other keys: {"signals": [{"text": "...", "groups": [1, 2], "restates": ""}]}`

// DefaultSignalInstructions returns the shipped signal-step text.
func DefaultSignalInstructions() string { return defaultSignalInstructions }

// SignalGroup is one group a run moved into a tier.
type SignalGroup struct {
	Examples []string // the examples the run wrote for it
	Requests []string // its requests shown to the model, the most typical first
	Weight   int      // how many requests it holds
}

// SignalInput is what the signal step is asked about one tier.
type SignalInput struct {
	Tier       string
	Definition string
	Signals    []string // the tier's signals now
	Declined   []string // signals of the tier the user dismissed before
	Groups     []SignalGroup
	// Max is the most signals to keep; it is capped at MaxSignalsPerRun.
	Max int
}

// Signal is one suggested signal and the groups it describes, as indexes
// into SignalInput.Groups.
type Signal struct {
	Text   string
	Groups []int

	restates string // the existing or declined signal the model said it repeats
}

// SignalResult is what the signal step wrote for one tier.
type SignalResult struct {
	Signals []Signal
	// Leaked carried request-specific material; Duplicates repeated a
	// signal the tier has or one declined, word for word, by embedding, or
	// in meaning as the model judged it; Narrow described fewer than
	// MinSignalGroups groups, and Broad most of them. None is kept.
	Leaked     []string
	Duplicates []string
	Narrow     []string
	Broad      []string
	// Asked reports that the model was called: a tier with fewer than
	// MinSignalGroups groups, or no room, is not.
	Asked bool
}

// SuggestSignals asks llm for one tier's signals and keeps those that
// generalise, carry nothing request-specific, and say something the tier's
// signals and the declined ones do not. Groups past maxSignalGroups are left
// out, so they should come heaviest first.
func SuggestSignals(ctx context.Context, llm LLM, emb Embedder, in SignalInput) (SignalResult, error) {
	limit := min(in.Max, MaxSignalsPerRun)
	if len(in.Groups) > maxSignalGroups {
		in.Groups = in.Groups[:maxSignalGroups]
	}
	var result SignalResult
	if limit <= 0 || len(in.Groups) < MinSignalGroups {
		return result, nil
	}
	result.Asked = true
	raw, err := llm.CompleteJSON(ctx, buildSignalPrompt(in))
	if err != nil {
		return result, err
	}
	answer, err := parseSignals(raw)
	if err != nil {
		return result, err
	}
	var candidates []Signal
	seen := map[string]bool{}
	for _, s := range answer {
		text := strings.Join(strings.Fields(s.Text), " ")
		key := normalizePhrase(text)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if leaks(text) {
			result.Leaked = append(result.Leaked, text)
			continue
		}
		if strings.TrimSpace(s.restates) != "" {
			result.Duplicates = append(result.Duplicates, text)
			continue
		}
		groups := validGroups(s.Groups, len(in.Groups))
		if len(groups) < MinSignalGroups {
			result.Narrow = append(result.Narrow, text)
			continue
		}
		if tooBroad(len(groups), len(in.Groups)) {
			result.Broad = append(result.Broad, text)
			continue
		}
		candidates = append(candidates, Signal{Text: text, Groups: groups})
	}
	if len(candidates) == 0 {
		return result, nil
	}
	kept, duplicates, err := withoutKnownSignals(ctx, emb, candidates, append(append([]string(nil), in.Signals...), in.Declined...))
	if err != nil {
		return result, err
	}
	result.Duplicates = append(result.Duplicates, duplicates...)
	result.Signals = kept[:min(len(kept), limit)]
	return result, nil
}

// withoutKnownSignals drops candidates that repeat a known signal, word for
// word or by embedding, and candidates that repeat an earlier candidate.
func withoutKnownSignals(ctx context.Context, emb Embedder, candidates []Signal, known []string) ([]Signal, []string, error) {
	knownText := make(map[string]bool, len(known))
	for _, k := range known {
		knownText[normalizePhrase(k)] = true
	}
	texts := make([]string, 0, len(candidates)+len(known))
	for _, c := range candidates {
		texts = append(texts, c.Text)
	}
	texts = append(texts, known...)
	vecs, err := emb.Embed(ctx, texts)
	if err != nil {
		return nil, nil, fmt.Errorf("embed suggested signals: %w", err)
	}
	if len(vecs) != len(texts) {
		return nil, nil, fmt.Errorf("embed suggested signals: asked for %d, got %d", len(texts), len(vecs))
	}
	knownVecs := make([][]float32, len(known))
	for i := range known {
		knownVecs[i] = normalize(vecs[len(candidates)+i])
	}
	var kept []Signal
	var keptVecs [][]float32
	var duplicates []string
	for i, c := range candidates {
		v := normalize(vecs[i])
		if knownText[normalizePhrase(c.Text)] || closeTo(v, knownVecs) || closeTo(v, keptVecs) {
			duplicates = append(duplicates, c.Text)
			continue
		}
		kept = append(kept, c)
		keptVecs = append(keptVecs, v)
	}
	return kept, duplicates, nil
}

func closeTo(v []float32, others [][]float32) bool {
	for _, o := range others {
		if len(o) == len(v) && dot(v, o) >= signalDuplicateSimilarity {
			return true
		}
	}
	return false
}

// tooBroad reports whether a signal describing covered of total groups
// describes most of them, once there are enough groups for that to say so.
func tooBroad(covered, total int) bool {
	return total >= broadSignalGroups && float64(covered) > broadSignalShare*float64(total)
}

// validGroups returns the distinct group numbers in range, as indexes.
func validGroups(numbers []int, n int) []int {
	seen := map[int]bool{}
	var out []int
	for _, g := range numbers {
		if g < 1 || g > n || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g-1)
	}
	return out
}

// buildSignalPrompt is the signal step's conversation: the instructions, then
// the tier with its definition and signals, the declined signals, and the
// groups with the examples written for each and some of their requests.
func buildSignalPrompt(in SignalInput) []Message {
	var b strings.Builder
	fmt.Fprintf(&b, "Tier: %s\n", in.Tier)
	if d := strings.TrimSpace(in.Definition); d != "" {
		fmt.Fprintf(&b, "Tier definition: %s\n", strings.Join(strings.Fields(d), " "))
	}
	writeList := func(heading string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", heading)
		for _, item := range items {
			fmt.Fprintf(&b, "- %s\n", truncate(strings.Join(strings.Fields(item), " "), maxPromptChars))
		}
	}
	if len(in.Signals) == 0 {
		b.WriteString("\nThe tier has no signals yet.\n")
	}
	writeList(fmt.Sprintf("Existing %s signals", in.Tier), in.Signals)
	writeList("Declined signals, not to be written again", in.Declined)
	for i, g := range in.Groups {
		fmt.Fprintf(&b, "\nGroup %d (%d requests).", i+1, max(g.Weight, 1))
		writeList("Examples written for it", g.Examples)
		requests := g.Requests[:min(len(g.Requests), maxSignalGroupRequests)]
		writeList("Some of its requests", requests)
	}
	return []Message{
		{Role: "system", Content: defaultSignalInstructions + "\n\n" + signalReinforcement},
		{Role: "user", Content: b.String()},
	}
}

type signalAnswer struct {
	Signals []struct {
		Text     string `json:"text"`
		Groups   []int  `json:"groups"`
		Restates string `json:"restates"`
	} `json:"signals"`
}

func parseSignals(raw string) ([]Signal, error) {
	var a signalAnswer
	if err := json.Unmarshal([]byte(stripFences(raw)), &a); err != nil {
		return nil, fmt.Errorf("parse signals: %w", err)
	}
	out := make([]Signal, 0, len(a.Signals))
	for _, s := range a.Signals {
		out = append(out, Signal{Text: strings.TrimSpace(s.Text), Groups: s.Groups, restates: s.Restates})
	}
	return out, nil
}
