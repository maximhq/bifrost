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
		subagentDelegationScenario(),
		imageQAScenario(),
		pdfQAScenario(),
	}
}

// 01 simple-chat — single-turn smoke test.
func simpleChatScenario() scenario {
	return scenario{
		ID:          "simple-chat",
		ModelKind:   "chat",
		Supports:    supportsAnyChat,
		ErrorIgnore: []string{"OKBIFROST"},
		Turns: []Turn{{
			Send:       "This is a harmless connectivity test. Reply with exactly the single token OKBIFROST and nothing else.",
			AssertText: []string{"OKBIFROST"},
			Timeout:    90 * time.Second,
		}},
	}
}

// 02 tool-call — model uses its built-in shell tool.
func toolCallScenario() scenario {
	const token = "BIFROST_TOOL_EXEC_73129"
	return scenario{
		ID:          "tool-call",
		ModelKind:   "chat",
		Supports:    supportsAnyChat,
		ErrorIgnore: []string{token},
		Turns: []Turn{{
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
		}},
	}
}

// 03 file-read — model uses file tool to read a fixture.
func fileReadScenario() scenario {
	fixture, _ := filepath.Abs("fixtures/sample.txt")
	return scenario{
		ID:          "file-read",
		ModelKind:   "chat",
		Supports:    supportsAnyChat,
		ErrorIgnore: []string{"FILEOK"},
		Turns: []Turn{{
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
		}},
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
		Turns: []Turn{{
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
		}},
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
		Turns: []Turn{{
			Send: "A train leaves station A at 9:00 AM going 60 km/h east. Another leaves station B at " +
				"10:00 AM going 40 km/h west. Stations A and B are 280 km apart. At what time do they meet? " +
				"Answer the meeting time.",
			Validate: validateReasoningMeetingTime,
			Timeout:  240 * time.Second,
		}},
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

// 09 subagent-delegation — the CLI's own subagent-delegation tool (Claude
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

// 10 image-qa — the model receives a genuine image attachment (not just a
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

// 11 pdf-qa — the model reads a real PDF document (not a .txt file) and
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
