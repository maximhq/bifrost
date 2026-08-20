package clis

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// readConfigFromEnv's failure path prints the env slice it was handed. Today's
// only caller passes a literal placeholder key, but the helper is generic: the
// moment a caller passes an env carrying a real provider credential, that
// credential is written verbatim into the CI log, where it is retained for as
// long as the run is.
func TestEnvNamesOmitsValues(t *testing.T) {
	env := []string{
		"ANTHROPIC_API_KEY=sk-ant-not-a-real-key-8f3a",
		"OPENCODE_CONFIG=/tmp/cfg.json",
	}
	got := envNames(env)
	if strings.Contains(got, "sk-ant-not-a-real-key-8f3a") {
		t.Errorf("env rendering leaked a credential value: %s", got)
	}
	if !strings.Contains(got, "ANTHROPIC_API_KEY") || !strings.Contains(got, "OPENCODE_CONFIG") {
		t.Errorf("env rendering dropped the variable names that make the failure diagnosable: %s", got)
	}
}

// The reasoning-replay scenario only reproduces the defect it was written for
// if it actually lands on the Bedrock Responses path. Each gate below is one
// way that silently stops happening, leaving a scenario that runs, passes, and
// verifies nothing.

// Bifrost converts requests separately per wire format, so a wire format with
// no cell behind it ships unexercised - which is how the Bedrock reasoning-replay
// defect reached production while this harness was green. These gates are what
// guarantee all three wires are reachable; if one narrows to nothing, the defect
// class it covers goes untested with no failing test to say so.
func TestOpenCodeVariantsCoverBothEndpoints(t *testing.T) {
	anthropicOnBedrock := ModelInfo{ID: "global.anthropic.claude-opus-5", AdaptiveThinking: true}
	novaOnBedrock := ModelInfo{ID: "us.amazon.nova-2-lite-v1:0"}
	claudeNative := ModelInfo{ID: "claude-sonnet-5", AdaptiveThinking: true}
	claudeOnVertex := ModelInfo{ID: "claude-haiku-4-5@20251001", ExtendedThinking: true}
	gpt := ModelInfo{ID: "gpt-5.5", AdaptiveThinking: true}

	// /openai/v1/responses -- unrestricted, mirroring the chat/completions
	// variant, so the Responses conversion is covered on every provider rather
	// than only the one a defect happened to be reported on.
	for _, provider := range []string{"openai", "azure", "bedrock", "anthropic"} {
		model := gpt
		if provider == "bedrock" {
			model = anthropicOnBedrock
		}
		if !supportsCLIProviderModel("opencode-responses", provider, model) {
			t.Errorf("opencode-responses must reach %s to cover the /openai responses wire there", provider)
		}
	}

	// /anthropic/v1/messages -- gated on the model family, not the provider,
	// because the Anthropic wire can only carry Claude models but every cloud
	// fronting Claude should be covered.
	for _, tc := range []struct {
		provider string
		model    ModelInfo
	}{
		{"anthropic", claudeNative},
		{"bedrock", anthropicOnBedrock},
		{"azure", claudeNative},
		{"vertex", claudeOnVertex},
	} {
		if !supportsCLIProviderModel("opencode-anthropic", tc.provider, tc.model) {
			t.Errorf("opencode-anthropic must reach %s/%s to cover the /anthropic wire there", tc.provider, tc.model.ID)
		}
	}
	// Non-Claude models cannot be served over the Anthropic Messages wire.
	if supportsCLIProviderModel("opencode-anthropic", "bedrock", novaOnBedrock) {
		t.Error("opencode-anthropic must not run against a non-Anthropic model")
	}
	if supportsCLIProviderModel("opencode-anthropic", "openai", gpt) {
		t.Error("opencode-anthropic must not run against a GPT model")
	}

	// /openai/v1/chat/completions -- the default variant is untouched by any of
	// the above scoping.
	if !supportsCLIProviderModel("opencode", "openai", gpt) {
		t.Error("the chat/completions opencode variant must still run against openai")
	}
}

// The two opencode entries differ ONLY in wire format. If they converge on the
// same npm package or base URL, the Responses path stops being exercised while
// the cell keeps passing.
func TestOpenCodeVariantsUseDifferentWireFormats(t *testing.T) {
	const base = "http://localhost:8080/openai"

	chatEnv, chatCleanup, err := opencodePreLaunch(base, "key", "bedrock/global.anthropic.claude-opus-5")
	if err != nil {
		t.Fatalf("opencodePreLaunch: %v", err)
	}
	defer chatCleanup()

	respEnv, respCleanup, err := opencodeResponsesPreLaunch(base, "key", "bedrock/global.anthropic.claude-opus-5")
	if err != nil {
		t.Fatalf("opencodeResponsesPreLaunch: %v", err)
	}
	defer respCleanup()

	chatCfg := readConfigFromEnv(t, chatEnv)
	respCfg := readConfigFromEnv(t, respEnv)

	// @ai-sdk/openai-compatible talks /v1/chat/completions; @ai-sdk/openai
	// talks /v1/responses (https://opencode.ai/docs/providers/).
	assertContains(t, chatCfg, `"npm": "@ai-sdk/openai-compatible"`, "chat variant npm package")
	assertContains(t, respCfg, `"npm": "@ai-sdk/openai"`, "responses variant npm package")
	assertNotContains(t, respCfg, `"npm": "@ai-sdk/openai-compatible"`, "responses variant must not use the compatible package")

	// The SDK appends "/responses" to baseURL verbatim, so the Responses
	// variant must carry the /v1 suffix or it lands on Bifrost's bare alias
	// rather than the canonical route.
	assertContains(t, respCfg, `"baseURL": "`+base+`/v1"`, "responses variant baseURL")
	assertContains(t, chatCfg, `"baseURL": "`+base+`"`, "chat variant baseURL")

	// Both still need the permission grant, or a tool-using cell hangs on a
	// prompt nobody can answer.
	assertContains(t, chatCfg, `"permission": "allow"`, "chat variant permission")
	assertContains(t, respCfg, `"permission": "allow"`, "responses variant permission")
}

// The scenario's whole premise is that reasoning accumulates across turns. One
// turn replays nothing, and a chat-only model emits no reasoning to replay.
func TestReasoningReplayScenarioShape(t *testing.T) {
	sc := reasoningReplayScenario()

	if got := len(sc.Turns); got < 3 {
		t.Fatalf("reasoning-replay has %d turns; depth IS the variable under test", got)
	}
	if len(sc.Efforts) != 0 {
		t.Error("reasoning-replay must not declare Efforts - runCell rejects that on a multi-turn scenario")
	}
	for i, turn := range sc.Turns {
		if turn.Validate == nil {
			t.Errorf("turn %d has no validator, so it asserts nothing", i+1)
		}
		if turn.Timeout <= 0 {
			t.Errorf("turn %d has no timeout", i+1)
		}
	}

	thinking := ModelInfo{ID: "global.anthropic.claude-opus-5", AdaptiveThinking: true}
	chatOnly := ModelInfo{ID: "us.amazon.nova-2-lite-v1:0"}
	if !sc.Supports("opencode-responses", "bedrock", thinking) {
		t.Error("reasoning-replay must run on the Bedrock Responses path - that is the defect's path")
	}
	if sc.Supports("opencode-responses", "bedrock", chatOnly) {
		t.Error("reasoning-replay should skip models that emit no reasoning")
	}
	// The case above is rejected by the cell gate before the reasoning flags
	// are ever read - supportsCLIProviderModel turns nova away because it is
	// not Anthropic-on-Bedrock - so on its own it would still pass with the
	// reasoning check deleted entirely. This pair is the one that isolates it:
	// the cell gate admits both models (claude/anthropic is unrestricted), so
	// only ExtendedThinking/AdaptiveThinking can decide between them.
	noThinking := ModelInfo{ID: "claude-haiku-4-5"}
	if !supportsCLIProviderModel("claude", "anthropic", noThinking) {
		t.Fatal("precondition: the cell gate must admit claude/anthropic, or this pair proves nothing")
	}
	if sc.Supports("claude", "anthropic", noThinking) {
		t.Error("reasoning-replay ran on a model with neither thinking surface - the reasoning gate is not being applied")
	}
	if !sc.Supports("claude", "anthropic", ModelInfo{ID: "claude-haiku-4-5", ExtendedThinking: true}) {
		t.Error("reasoning-replay skipped an extended-thinking model the cell gate admits")
	}
	// The Anthropic Messages path was reported unaffected; running there is
	// what makes that a checked claim rather than a footnote.
	if !sc.Supports("claude", "anthropic", ModelInfo{ID: "claude-opus-5", AdaptiveThinking: true}) {
		t.Error("reasoning-replay should also run on the Anthropic Messages path as a control")
	}
}

// A cell budget shorter than the turns it contains reports a cell deadline
// instead of the turn that actually stalled, turning a diagnosable timeout into
// an opaque one.
func TestCellBudgetExceedsSummedTurnTimeouts(t *testing.T) {
	sc := reasoningReplayScenario()
	var summed time.Duration
	for _, turn := range sc.Turns {
		summed += turn.Timeout
	}
	budget := cellBudget(sc)
	if budget <= summed {
		t.Errorf("cell budget %s does not exceed summed turn timeouts %s", budget, summed)
	}

	// Short scenarios keep the floor they have always had.
	if got := cellBudget(scenario{Turns: []Turn{{Timeout: time.Second}}}); got != 15*time.Minute {
		t.Errorf("cellBudget floor = %s, want 15m", got)
	}
	// A turn with no explicit timeout still has to be accounted for; counting
	// it as zero is how a budget silently ends up shorter than its scenario.
	zero := cellBudget(scenario{Turns: make([]Turn, 12)})
	if zero <= 15*time.Minute {
		t.Errorf("cellBudget = %s for 12 default-timeout turns, want more than the floor", zero)
	}
}

func readConfigFromEnv(t *testing.T, env []string) string {
	t.Helper()
	path, ok := envValue(env, "OPENCODE_CONFIG")
	if !ok {
		t.Fatalf("OPENCODE_CONFIG not set in %s", envNames(env))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// envNames renders an env slice for a failure message as variable names only.
//
// The names are what make a "not set" failure diagnosable; the values are what
// make it a credential disclosure. A CI log is long-lived and widely readable,
// so the split is not a close call. An entry with no "=" is printed whole,
// since it has no value to withhold.
func envNames(env []string) string {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			name = kv
		}
		names = append(names, name)
	}
	return fmt.Sprintf("%v", names)
}

func assertContains(t *testing.T, haystack, needle, label string) {
	t.Helper()
	if !containsFold(haystack, needle) {
		t.Errorf("%s: expected %q in config:\n%s", label, needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle, label string) {
	t.Helper()
	if containsFold(haystack, needle) {
		t.Errorf("%s: did not expect %q in config:\n%s", label, needle, haystack)
	}
}
