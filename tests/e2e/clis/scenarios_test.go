package clis

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// scenario is one feature exercise expressed as N turns of conversation.
// len(Turns) == 1 → single-turn; >1 → multi-turn (driven by cli.MultiTurnDriver).
type scenario struct {
	ID          string
	ModelKind   string                                               // "chat" or "reasoning"
	Supports    func(cliID, providerID string, model ModelInfo) bool // matrix gate (per cell)
	ErrorIgnore []string                                             // sentinels to whitelist before error scan
	Turns       []Turn

	// Efforts, if non-empty, runs this scenario once per effort level as a
	// nested t.Run subtest (TestCLIs/<cli>/<provider>/<model>/<scenario>/<effort>),
	// for cells with model.AdaptiveThinking and cli.EffortArgs != nil. Cells
	// without an effort surface still run the scenario once at the CLI's
	// default effort -- this sweep multiplies runs, it never gates them.
	// Only wired for single-turn scenarios (len(Turns) == 1) -- runCell
	// t.Fatalf's if a multi-turn scenario sets this, since effort isn't
	// threaded through the multi-turn drivers.
	Efforts []string
}

func supportsCLIProviderModel(cliID, providerID string, model ModelInfo) bool {
	if cliID == "claude" && providerID == "bedrock" {
		return isBedrockAnthropicModel(model.ID)
	}
	if cliID == "codex" && providerID != "openai" {
		return false
	}
	// opencode-responses exists solely to exercise Bifrost's /v1/responses
	// request conversion for Anthropic-on-Bedrock -- the path a reasoning-replay
	// conversion defect was reported on, and the one no other cell reaches
	// (opencode is chat/completions, codex is openai-only). Scoping it to those
	// cells rather than the whole matrix keeps it from duplicating coverage the
	// chat/completions variant already provides, at five models rather than
	// every provider's catalogue.
	// The Anthropic Messages wire can only carry Anthropic-family models, whoever
	// serves them -- native, Bedrock, Azure or Vertex. Gating on the model family
	// rather than on the provider is what gives /anthropic coverage across every
	// cloud that fronts Claude, instead of only the one a defect was reported on.
	if cliID == "opencode-anthropic" {
		return isAnthropicFamilyModel(model.ID)
	}
	return true
}

func supportsAnyChat(cliID, providerID string, model ModelInfo) bool {
	return supportsCLIProviderModel(cliID, providerID, model)
}

func supportsStableConversation(cliID, providerID string, model ModelInfo) bool {
	if !supportsCLIProviderModel(cliID, providerID, model) {
		return false
	}
	if providerID == "bedrock" && isBedrockNovaModel(model.ID) {
		return false
	}
	return true
}

func allScenarios() []scenario {
	return []scenario{
		simpleChatScenario(),
		toolCallScenario(),
		fileReadScenario(),
		webSearchScenario(),
		reasoningScenario(),
		conversationMemoryScenario(),
		conversationRefinementScenario(),
		conversationRoleStabilityScenario(),
		reasoningReplayScenario(),
		reasoningToolReplayScenario(),
		subagentDelegationScenario(),
		imageQAScenario(),
		pdfQAScenario(),
	}
}

// 01 simple-chat — three-turn smoke test. Turn 1 proves the round trip; turns
// 2 and 3 prove the CLI's session actually carries state, by asking for
// transformations of a token that is never repeated in the later prompts. A
// model that lost the conversation cannot answer them at all, so this catches
// session-continuity breakage (a dropped resume, a reset context) that a
// single-turn ping would sail straight through.
func simpleChatScenario() scenario {
	return scenario{
		ID:          "simple-chat",
		ModelKind:   "chat",
		Supports:    supportsAnyChat,
		ErrorIgnore: []string{"OKBIFROST", "okbifrost"},
		Turns: []Turn{
			{
				Send:       "This is a harmless connectivity test. Reply with exactly the single token OKBIFROST and nothing else.",
				AssertText: []string{"OKBIFROST"},
				Timeout:    90 * time.Second,
			},
			{
				// Case-sensitive on purpose: a fold-insensitive match would be
				// satisfied by the model simply echoing the uppercase token from
				// turn 1, which is exactly the non-answer this turn rules out.
				Send:       "Reply with that same token in all lowercase, and nothing else.",
				AssertText: []string{"okbifrost"},
				Timeout:    90 * time.Second,
			},
			{
				Send:       "Reply with the original uppercase token again, followed by a space and the word DONE.",
				AssertText: []string{"OKBIFROST", "DONE"},
				Timeout:    90 * time.Second,
			},
		},
	}
}

// 02 tool-call — model uses its built-in shell tool across three turns. A
// single shell call only proves the tool round-trips once; chaining three
// proves tool results stay in context, which is the shape that actually breaks
// (a tool_result block dropped between turns leaves turn 3 unanswerable).
func toolCallScenario() scenario {
	const token = "BIFROST_TOOL_EXEC_73129"
	// len(token) == 23; `printf` writes no trailing newline, so `wc -c`
	// reports exactly the token length.
	const tokenLen = "23"
	return scenario{
		ID:          "tool-call",
		ModelKind:   "chat",
		Supports:    supportsAnyChat,
		ErrorIgnore: []string{token},
		Turns: []Turn{
			{
				Send: "Use your shell tool to run `printf " + token + "` (no newline) and report the exact " +
					"output. Do not simulate it. Do not just type the expected token - you must run the shell command. " +
					"If your shell tool accepts structured input, provide both a command string and a description string.",
				AssertText: []string{token},
				AssertNotText: []string{
					"don't have access to a shell",
					"cannot run shell",
					"unable to execute",
					"can't run commands",
				},
				Timeout: 120 * time.Second,
			},
			{
				Send: "Now use your shell tool to run `printf " + token + " | wc -c` and reply with just " +
					"the number it prints. Actually run it - do not calculate the answer yourself.",
				AssertText: []string{tokenLen},
				Timeout:    120 * time.Second,
			},
			{
				Send: "Without running any further commands, reply on one line with the token from the first " +
					"command, a space, and the number from the second command.",
				AssertText: []string{token, tokenLen},
				Timeout:    120 * time.Second,
			},
		},
	}
}

// 03 file-read — model uses its file tool to read a fixture, then answers a
// follow-up from context alone, then reads again. The middle turn is the
// interesting one: it forbids re-reading, so it can only be answered from the
// turn-1 tool result still being in context.
func fileReadScenario() scenario {
	fixture, _ := filepath.Abs("fixtures/sample.txt")
	return scenario{
		ID:          "file-read",
		ModelKind:   "chat",
		Supports:    supportsAnyChat,
		ErrorIgnore: []string{"FILEOK", "FILEDONE"},
		Turns: []Turn{
			{
				Send: "Read the file at " + fixture + " using your file tool. After reading, " +
					"reply with the single token FILEOK followed by the capital city of France and the hidden verification token from the file.",
				AssertText:     []string{"FILE_FIXTURE_73129"},
				AssertTextFold: []string{"FILEOK", "Paris"},
				AssertNotText: []string{
					"don't have access to file",
					"cannot read files",
					"unable to read",
				},
				Timeout: 120 * time.Second,
			},
			{
				Send: "Without reading the file again, tell me what that file said the square root of 144 is. " +
					"Reply with just the number.",
				// Not AssertText: "12" -- that is a raw substring, and "12"
				// sits inside FILE_FIXTURE_73129, the token the model quoted
				// in turn 1. The substring form passes on a reply that merely
				// echoes that token and never answers. validateNumber anchors
				// on word boundaries, so the digits have to stand alone.
				Validate: validateNumber(12, "square root of 144"),
				Timeout:  120 * time.Second,
			},
			{
				Send: "Now read that same file once more with your file tool, and reply with the hidden " +
					"verification token followed by the token FILEDONE on the same line.",
				AssertText:     []string{"FILE_FIXTURE_73129"},
				AssertTextFold: []string{"FILEDONE"},
				Timeout:        120 * time.Second,
			},
		},
	}
}

// 04 web-search — model-gated. Forces the model to use its web-search tool
// and prove it did by including a number-with-unit (a real weather data
// point). Negative assertions catch the failure mode where the model refuses
// or hallucinates without searching.
func webSearchScenario() scenario {
	return scenario{
		ID:        "web-search",
		ModelKind: "chat",
		Supports: func(cliID, providerID string, m ModelInfo) bool {
			return supportsCLIProviderModel(cliID, providerID, m) && m.WebSearch
		},
		ErrorIgnore: []string{"SEARCHOK"},
		Turns: []Turn{
			{
				Send: "Use your web search tool to find the current temperature in Reykjavik right now. " +
					"You MUST actually search the web - do not say you can't or refuse. Reply with the temperature " +
					"in Celsius (a number followed by °C or 'degrees Celsius') and a source URL beginning with http. " +
					"After your answer, append the single token SEARCHOK on its own line.",
				AssertText: []string{"SEARCHOK", "http"},
				// One of: a number followed by °C / C / degrees / Celsius. Loose
				// because models phrase temperature differently across providers.
				AssertTextAny: []string{"°C", "Celsius", "celsius", " C ", " C\n", " C."},
				AssertNotText: []string{
					"don't have access",
					"do not have access",
					"can't access",
					"cannot access",
					"unable to access",
					"no web search",
					"no access to web",
					"can't browse",
					"cannot browse",
					"only Bash",
					"only Read",
				},
				Timeout: 180 * time.Second,
			},
			// Turns 2 and 3 deliberately require no further searching: they work
			// off the turn-1 search result already in context. That keeps the
			// scenario multi-turn without tripling its search-tool cost, and it
			// isolates conversation carry-over from search capability -- a
			// failure here is a context problem, not a search problem.
			{
				Send:       "Repeat the exact source URL you cited, and nothing else.",
				AssertText: []string{"http"},
				Timeout:    120 * time.Second,
			},
			{
				Send: "Convert the Reykjavik temperature you found into degrees Fahrenheit. Reply with the " +
					"number followed by F, then the token SEARCHOK on its own line.",
				AssertText: []string{"SEARCHOK"},
				// A bare substring check can't express this: matching "F"
				// case-insensitively hits every "f" in the reply, and matching
				// "Fahrenheit" rejects the equally correct "38 F". Require a
				// number actually adjacent to an F/°F/Fahrenheit unit instead.
				Validate: validateFahrenheitReading,
				Timeout:  120 * time.Second,
			},
		},
	}
}

// 05 reasoning — gate on either thinking surface. Our prompt doesn't pass
// --effort, so any model that supports extended OR adaptive thinking can
// run; we only skip cells where neither surface is available.
func reasoningScenario() scenario {
	return scenario{
		ID:        "reasoning",
		ModelKind: "reasoning",
		Supports: func(cliID, providerID string, m ModelInfo) bool {
			return supportsCLIProviderModel(cliID, providerID, m) && (m.ExtendedThinking || m.AdaptiveThinking)
		},
		ErrorIgnore: []string{"REASONOK", "144"},
		// Three turns over one shared setup. Turns 2 and 3 never restate the
		// premises, so they only work if the problem statement survived in
		// context -- and each needs a fresh derivation from it rather than a
		// recall of turn 1's answer.
		Turns: []Turn{
			{
				Send: "A train leaves station A at 9:00 AM going 60 km/h east. Another leaves station B at " +
					"10:00 AM going 40 km/h west. Stations A and B are 280 km apart. At what time do they meet? " +
					"Answer the meeting time.",
				Validate: validateReasoningMeetingTime,
				Timeout:  240 * time.Second,
			},
			{
				Send: "How many kilometres from station A is that meeting point? Reply with just the number " +
					"of kilometres.",
				Validate: validateReasoningDistanceFromA,
				Timeout:  240 * time.Second,
			},
			{
				Send: "Now suppose the second train had also left at 9:00 AM instead of 10:00 AM, with " +
					"everything else unchanged. At what time would they meet?",
				Validate: validateReasoningSimultaneousDeparture,
				Timeout:  240 * time.Second,
			},
		},
	}
}

// 06 conversation-memory — multi-turn: the model must remember a fact
// across three turns.
func conversationMemoryScenario() scenario {
	return scenario{
		ID:          "conversation-memory",
		ModelKind:   "chat",
		Supports:    supportsStableConversation,
		ErrorIgnore: []string{"pangolin"},
		Turns: []Turn{
			{
				Send:       "Remember the secret word: pangolin. Reply with just the word REMEMBERED.",
				AssertText: []string{"REMEMBERED"},
				Timeout:    60 * time.Second,
			},
			{
				Send:       "What was the secret word I just told you?",
				AssertText: []string{"pangolin"},
				Timeout:    60 * time.Second,
			},
			{
				Send:       "Use that secret word in a one-sentence description of an animal.",
				AssertText: []string{"pangolin"},
				Timeout:    60 * time.Second,
			},
		},
	}
}

// 07 conversation-refinement — multi-turn: model produces an answer, then
// refines it based on follow-up constraints. Tests that constraints from
// turn N-1 carry forward.
func conversationRefinementScenario() scenario {
	return scenario{
		ID:          "conversation-refinement",
		ModelKind:   "chat",
		Supports:    supportsStableConversation,
		ErrorIgnore: []string{"haiku", "Haiku"},
		Turns: []Turn{
			{
				Send:       "Write a haiku about the ocean.",
				AssertText: []string{},
				Timeout:    60 * time.Second,
			},
			{
				Send:              "Now rewrite it but make it about a desert instead. Keep haiku form.",
				AssertTextAnyFold: []string{"desert", "sand", "dune", "dry"},
				Timeout:           60 * time.Second,
			},
			{
				Send:     "Now combine both into a four-line poem with one ocean image and one desert image.",
				Validate: validateOceanDesertPoem,
				Timeout:  60 * time.Second,
			},
		},
	}
}

func validateReasoningMeetingTime(output string) error {
	if regexp.MustCompile(`(?i)\b12[:.]12\b`).MatchString(output) {
		return nil
	}
	return fmt.Errorf("expected answer to identify 12:12 PM meeting time, got tail:\n%s", tailStr(output, 600))
}

// Train A runs 60 km/h from 9:00 to the 12:12 meeting: 3.2 h × 60 = 192 km.
// Accept a bare 192 as well as "192 km"/"192km" phrasings.
func validateReasoningDistanceFromA(output string) error {
	if regexp.MustCompile(`\b192\b`).MatchString(output) {
		return nil
	}
	return fmt.Errorf("expected answer to identify 192 km from station A, got tail:\n%s", tailStr(output, 600))
}

// Departing together, the trains close 280 km at a combined 100 km/h: 2.8 h
// after 9:00, i.e. 11:48.
func validateReasoningSimultaneousDeparture(output string) error {
	if regexp.MustCompile(`(?i)\b11[:.]48\b`).MatchString(output) {
		return nil
	}
	return fmt.Errorf("expected answer to identify an 11:48 AM meeting time, got tail:\n%s", tailStr(output, 600))
}

// validateFahrenheitReading requires a number immediately followed by a
// Fahrenheit unit, so the model has to actually report a converted reading
// rather than merely mention the word.
func validateFahrenheitReading(output string) error {
	// The bare-F branch is `F\b`, not `\bF\b`: with `\s*` matching zero
	// characters there is no word boundary between the digit and the F, so a
	// leading \b would reject the unspaced "38F" that models commonly emit.
	// The trailing \b still does the work that matters, keeping "38 files" and
	// "1080p FHD" from counting as readings.
	if regexp.MustCompile(`(?i)-?\d+(?:\.\d+)?\s*(?:°\s*F\b|º\s*F\b|F\b|degrees?\s+F(?:ahrenheit)?\b|\bFahrenheit\b)`).MatchString(output) {
		return nil
	}
	return fmt.Errorf("expected a Fahrenheit temperature reading (a number followed by F/°F/Fahrenheit), got tail:\n%s",
		tailStr(output, 600))
}

func validateOceanDesertPoem(output string) error {
	var nonEmpty int
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
	}
	if nonEmpty < 4 {
		return fmt.Errorf("expected at least 4 non-empty lines for a four-line poem, got %d:\n%s", nonEmpty, tailStr(output, 600))
	}
	lower := strings.ToLower(output)
	oceanTerms := []string{"ocean", "sea", "wave", "tide", "shore", "salt"}
	desertTerms := []string{"desert", "sand", "dune", "dry", "sun", "cracked earth"}
	if !containsAny(lower, oceanTerms) {
		return fmt.Errorf("expected an ocean image in output, got tail:\n%s", tailStr(output, 600))
	}
	if !containsAny(lower, desertTerms) {
		return fmt.Errorf("expected a desert image in output, got tail:\n%s", tailStr(output, 600))
	}
	return nil
}

func containsAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// 08 conversation-role-stability — multi-turn: the model is given a role
// in turn 1 and we check it sticks across turns even when distracted.
func conversationRoleStabilityScenario() scenario {
	return scenario{
		ID:          "conversation-role-stability",
		ModelKind:   "chat",
		Supports:    supportsStableConversation,
		ErrorIgnore: []string{"PIRATE"},
		Turns: []Turn{
			{
				Send:          "From now on, end every reply with the literal token PIRATE on its own line. Acknowledge with: ARRR",
				AssertTextAny: []string{"ARRR", "PIRATE"},
				Timeout:       60 * time.Second,
			},
			{
				Send:       "What is 2 plus 2?",
				AssertText: []string{"4", "PIRATE"},
				Timeout:    60 * time.Second,
			},
			{
				Send:       "Name a primary color.",
				AssertText: []string{"PIRATE"},
				Timeout:    60 * time.Second,
			},
		},
	}
}

// 09 reasoning-replay — five chained derivations on a thinking-capable model.
//
// This is the one scenario whose *depth* is the point. Every turn requires real
// multi-step arithmetic, so the model emits reasoning; every turn depends on the
// previous answer and never restates it, so the client must replay the whole
// accumulated history - including prior reasoning blocks - on each request.
//
// That replay is what a request-conversion defect lives in. A Bedrock Responses
// defect of exactly this shape was reported against a build this harness passed:
// reasoning items replayed to Anthropic-on-Bedrock produced upstream 400s, one
// validation error per prior assistant turn. A single-turn reasoning probe
// cannot see it - there is no history to replay on turn 1 - and the existing
// multi-turn scenarios (conversation-memory and friends) run on chat models that
// emit no reasoning to replay. Five turns is chosen for the same reason the
// report's reproduction swept 1/3/10: the failure scales 1:1 with turn count, so
// depth is the variable, and turn 5 additionally forces a recall of every prior
// result.
//
// The numbers below are fully determined, so a wrong answer is a wrong answer
// rather than a judgement call:
//
//	A=24, B=A/2=12, C=B+8=20            -> 56 boxes
//	56 boxes x 12 items                 -> 672 items
//	672 items / 250 per trip            -> 2 full trips, 172 left over
//	3 trips (2 full + 1 remainder) x $40 -> $120
func reasoningReplayScenario() scenario {
	return scenario{
		ID:        "reasoning-replay",
		ModelKind: "reasoning",
		Supports:  supportsReasoningReplay,
		// The intermediate results recur throughout the transcript; none of them
		// are error markers, but 400/500-looking numbers would be, so the
		// sentinels are whitelisted for the same reason the reasoning scenario
		// whitelists 144.
		ErrorIgnore: []string{"56", "672", "172", "120", "250"},
		Turns: []Turn{
			{
				Send: "A warehouse has three shelves. Shelf A holds 24 boxes. Shelf B holds half as many " +
					"as shelf A. Shelf C holds 8 more than shelf B. How many boxes are there in total? " +
					"Reply with just the number.",
				Validate: validateNumber(56, "total boxes"),
				Timeout:  180 * time.Second,
			},
			{
				Send: "Each of those boxes holds 12 items. How many items are in the warehouse? " +
					"Reply with just the number.",
				Validate: validateNumber(672, "total items"),
				Timeout:  180 * time.Second,
			},
			{
				Send: "A truck carries 250 items per trip. How many completely full trips can it make, " +
					"and how many items are left over? Reply with both numbers.",
				Validate: validateTripSplit,
				Timeout:  180 * time.Second,
			},
			{
				Send: "Each trip costs $40, and the leftover items need one more trip. What is the total " +
					"cost in dollars? Reply with just the number.",
				Validate: validateNumber(120, "total cost"),
				Timeout:  180 * time.Second,
			},
			{
				// The recall turn. Answering it requires the full chain to still
				// be in context, which is exactly the state that makes the
				// request carry every prior reasoning block.
				Send: "Without recalculating anything, list in order the four results you worked out: " +
					"total boxes, total items, leftover items, and total cost in dollars.",
				Validate: validateReplayRecall,
				Timeout:  180 * time.Second,
			},
		},
	}
}

// supportsReasoningReplay gates to models that actually emit reasoning - there
// is nothing to replay otherwise - on top of the usual cell gate. It is
// deliberately NOT restricted to opencode-responses: the Anthropic Messages path
// was reported unaffected by the Bedrock defect, and running the same scenario
// there is what turns that claim into a checked assertion rather than a
// footnote.
func supportsReasoningReplay(cliID, providerID string, m ModelInfo) bool {
	if !supportsCLIProviderModel(cliID, providerID, m) {
		return false
	}
	return m.ExtendedThinking || m.AdaptiveThinking
}

// validateNumber builds a validator for a single expected integer, tolerating
// thousands separators and surrounding prose.
func validateNumber(want int, label string) func(string) error {
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(withCommas(want)) + `\b|\b` + fmt.Sprint(want) + `\b`)
	return func(output string) error {
		if pattern.MatchString(output) {
			return nil
		}
		return fmt.Errorf("expected %s = %d, got tail:\n%s", label, want, tailStr(output, 600))
	}
}

// withCommas renders 1234 as "1,234" so a validator matches either grouping.
func withCommas(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// 672 items at 250 per trip is 2 full trips with 172 left over. Both numbers
// must appear -- "2" alone would also match a model that answered only half the
// question.
func validateTripSplit(output string) error {
	// The 2 has to sit next to the word it quantifies. A bare \b2\b is
	// satisfied by any standalone 2 in the transcript - an elapsed second, a
	// token count, a turn index - so the check could pass while the model
	// answered something else entirely. Matching either order keeps "2 trips"
	// and "trips: 2" both valid; staying inside [^.\n] keeps the pairing
	// within one sentence rather than spanning the whole reply.
	if !regexp.MustCompile(`(?i)\b2\b[^.\n]{0,40}\btrips?\b|\btrips?\b[^.\n]{0,40}\b2\b`).MatchString(output) {
		return fmt.Errorf("expected 2 full trips, got tail:\n%s", tailStr(output, 600))
	}
	if !regexp.MustCompile(`\b172\b`).MatchString(output) {
		return fmt.Errorf("expected 172 leftover items, got tail:\n%s", tailStr(output, 600))
	}
	return nil
}

// validateReplayRecall requires every result from the chain, which is only
// answerable if the whole conversation survived the round trip.
func validateReplayRecall(output string) error {
	for _, want := range []struct {
		n     int
		label string
	}{
		{56, "total boxes"},
		{672, "total items"},
		{172, "leftover items"},
		{120, "total cost"},
	} {
		if err := validateNumber(want.n, want.label)(output); err != nil {
			return err
		}
	}
	return nil
}

// 10 reasoning-tool-replay — a thinking model that also calls tools, then has
// to answer from the resulting history.
//
// This is the shape reported from the field, and it is deliberately distinct
// from reasoning-replay above. The difference is tool calls, and it turns out to
// be the whole difference:
//
//   - reasoning-replay is five turns of pure arithmetic. It passes.
//   - This is the same models over the same wire path, but each assistant turn
//     carries reasoning AND a tool call. Reported failing at `messages.2` — the
//     FIRST assistant turn, from a one-word prompt.
//
// Why tools change it: a client can paraphrase a plain assistant answer, but a
// turn containing a tool call has to be replayed verbatim to keep the
// tool_use/tool_result pairing intact. That verbatim replay is what puts the
// reasoning item back on the wire, and on the Bedrock Responses path a reasoning
// item that arrives with an empty summary and only encrypted_content converts to
// a block with no `text` — which Bedrock rejects.
//
// It also explains the reported error's index pattern (14, 16, then 24, then 30,
// 32, ...): the gaps are the assistant turns that made no tool call, so carried
// no replayable reasoning. A scenario without tools reproduces nothing, which is
// exactly what reasoning-replay demonstrated.
//
// Turn 2 is the one under test: it forbids re-reading, so the model can only
// answer from a history that includes turn 1's reasoning + tool call.
func reasoningToolReplayScenario() scenario {
	fixture, _ := filepath.Abs("fixtures/sample.txt")
	return scenario{
		ID:          "reasoning-tool-replay",
		ModelKind:   "reasoning",
		Supports:    supportsReasoningReplay,
		ErrorIgnore: []string{"FILE_FIXTURE_73129", "TOOLREPLAY"},
		Turns: []Turn{
			{
				Send: "Think carefully about this before answering, then use your file tool to read " +
					fixture + ". Report the hidden verification token it contains.",
				AssertText: []string{"FILE_FIXTURE_73129"},
				AssertNotText: []string{
					"don't have access to file", "cannot read files", "unable to read",
				},
				Timeout: 180 * time.Second,
			},
			{
				// The replay turn. No re-read allowed, so the answer must come from
				// the turn-1 history - which the client can only send back by
				// replaying the assistant turn (reasoning + tool call) verbatim.
				Send: "Without reading the file again, which capital city did that file name, and what " +
					"did it say the square root of 144 is?",
				AssertTextFold: []string{"Paris"},
				AssertText:     []string{"12"},
				Timeout:        180 * time.Second,
			},
			{
				// A second tool call on top of an already-replayed history, so the
				// reasoning items accumulate the way they do in a real session.
				Send: "Now use your shell tool to run `printf TOOLREPLAY` and reply with its exact " +
					"output followed by the verification token from the first step.",
				AssertText: []string{"TOOLREPLAY", "FILE_FIXTURE_73129"},
				Timeout:    180 * time.Second,
			},
		},
	}
}

// 11 subagent-delegation — the CLI's own subagent-delegation tool (Claude
// Code's Agent/Task tool, or Codex's spawn_agent collab-tool) spawns a
// nested subagent that makes its own separate LLM call. Proves that traffic
// shape (a completion whose tool call triggers an additional completion with
// a different system prompt/tool schema, multiplexed into one streamed
// response) round-trips correctly through Bifrost. claude + codex only: see
// README "Known limitations" for why OpenCode is deferred.
func subagentDelegationScenario() scenario {
	const subagentToken = "OKSUBAGENT_73129"
	return scenario{
		ID:          "subagent-delegation",
		ModelKind:   "chat",
		Supports:    supportsSubagentDelegation,
		ErrorIgnore: []string{subagentToken, "SUBAGENT_RELAY", agentToolUseMarker},
		Turns: []Turn{
			// Turn 1 asserts only that delegation was actually attempted (via
			// the tool-use marker), NOT that the result came back. The parent
			// agent does not reliably block on the subagent -- observed in
			// practice: "The subagent is still running. I'll relay its exact
			// response when it completes." Asserting the relay here would make
			// the scenario flaky for a behavior that isn't the thing under test.
			{
				Send: "Delegate this task to a subagent right now: spawn a subagent and give it exactly " +
					"this task - \"Reply with exactly the token " + subagentToken + " and nothing else.\" " +
					"Do not answer this yourself, do not simulate the subagent, and do not just type the " +
					"expected output - you must actually delegate this to a real subagent.",
				AssertText: []string{agentToolUseMarker},
				AssertNotText: []string{
					"don't have subagents", "no subagent", "cannot spawn", "can't spawn",
					"can't delegate", "unable to delegate",
				},
				Timeout: 180 * time.Second,
			},
			// Turn 2 is where the round trip is actually proven: the subagent's
			// own separate completion has to have come back through Bifrost and
			// landed in the parent's context for the token to be reportable.
			{
				Send: "What exact token did that subagent reply with? Wait for it to finish if it is " +
					"still running. Do not spawn another subagent. Reply with the token on its own line " +
					"prefixed with 'SUBAGENT_RELAY: '.",
				AssertText: []string{"SUBAGENT_RELAY:", subagentToken},
				Timeout:    180 * time.Second,
			},
		},
	}
}

// supportsSubagentDelegation gates the subagent-delegation scenario to
// claude and codex (both have a confirmed, cited subagent-delegation tool;
// OpenCode's `task` tool's event visibility in `opencode run --format json`
// is unconfirmed, see README), and to a single ("chat" default) model per
// provider -- this scenario is inherently >=2 LLM calls per turn (top-level
// + subagent), so the full model sweep isn't worth the extra cost.
func supportsSubagentDelegation(cliID, providerID string, model ModelInfo) bool {
	if cliID != "claude" && cliID != "codex" {
		return false
	}
	if !supportsCLIProviderModel(cliID, providerID, model) {
		return false
	}
	prov, ok := providers[providerID]
	return ok && model.ID == prov.chatModel().ID
}

// 12 image-qa — the model receives a genuine image attachment (not just a
// file path) and must answer questions requiring real visual/OCR
// understanding: a token rendered as text, a fact written in the image, and
// the color of a drawn shape. Claude has no CLI-level image-attach flag --
// its multimodal path is the Read tool, triggered by a path in the prompt
// (confirmed via https://code.claude.com/docs/en/tools-reference: "Images:
// PNG, JPG, and other image formats are returned as visual content that
// Claude can see"). Codex attaches via -i/--image (confirmed via
// `codex exec --help` and
// https://learn.chatgpt.com/docs/developer-commands?surface=cli). Both
// mechanisms are exercised: AttachImage drives Codex's flag, and the Send
// prompt separately mentions the path so Claude's Read tool can open it.
// Runs at low and high effort (see Efforts) for models that support it.
func imageQAScenario() scenario {
	fixture, _ := filepath.Abs("fixtures/sample.png")
	return scenario{
		ID:          "image-qa",
		ModelKind:   "chat",
		Supports:    supportsImageQA,
		ErrorIgnore: []string{"IMAGE_FIXTURE_58204"},
		Efforts:     []string{"low", "high"},
		Turns: []Turn{{
			Send: "Look at the attached image and answer three things: (1) the hidden verification " +
				"token rendered as text in the image, (2) the name of the metal mentioned in the " +
				"image's text, (3) the color of the square shown in the image. If the image was not " +
				"attached directly and you need to open it yourself, its absolute path is " + fixture +
				" - use your image/file viewing tool to open it. Do not guess - actually look at the image.",
			AttachImage:    fixture,
			AssertText:     []string{"IMAGE_FIXTURE_58204"},
			AssertTextFold: []string{"gold", "red"},
			AssertNotText: []string{
				"don't have access to images", "cannot view images", "unable to view",
				"can't see images", "no image was attached", "don't have the ability to view",
			},
			Timeout: 120 * time.Second,
		}},
	}
}

func supportsImageQA(cliID, providerID string, m ModelInfo) bool {
	if cliID != "claude" && cliID != "codex" {
		return false
	}
	return supportsCLIProviderModel(cliID, providerID, m)
}

// 13 pdf-qa — the model reads a real PDF document (not a .txt file) and
// must answer questions from its content, exercising genuine document
// parsing rather than plain-text file reading. Claude only: PDF attachment
// has no CLI flag on either CLI, and Claude's Read tool natively handles
// PDFs (whole-file for short documents, paged for >10 pages, per
// https://code.claude.com/docs/en/tools-reference), while Codex's only
// attachment mechanism (-i/--image) is confirmed image-only -- the
// UserInput wire-protocol enum (codex-rs/protocol/src/user_input.rs,
// rust-v0.145.0) has no document/PDF variant, and no PDF-related flag
// exists anywhere in `codex exec --help`. Runs at low and high effort (see
// Efforts) for models that support it.
func pdfQAScenario() scenario {
	fixture, _ := filepath.Abs("fixtures/sample.pdf")
	return scenario{
		ID:          "pdf-qa",
		ModelKind:   "chat",
		Supports:    supportsPDFQA,
		ErrorIgnore: []string{"PDF_FIXTURE_29104"},
		Efforts:     []string{"low", "high"},
		Turns: []Turn{{
			Send: "Read the PDF at " + fixture + " using your file tool. After reading it, reply with: " +
				"(1) the hidden verification token in the document, (2) the name of the river mentioned " +
				"in the document.",
			AssertText:     []string{"PDF_FIXTURE_29104"},
			AssertTextFold: []string{"Nile"},
			AssertNotText: []string{
				"don't have access to file", "cannot read files", "unable to read",
				"can't open pdf", "cannot open pdf", "unable to open pdf",
			},
			Timeout: 120 * time.Second,
		}},
	}
}

func supportsPDFQA(cliID, providerID string, m ModelInfo) bool {
	if cliID != "claude" {
		return false
	}
	return supportsCLIProviderModel(cliID, providerID, m)
}
