package clis

import (
	"path/filepath"
	"time"
)

// scenario is one feature exercise expressed as N turns of conversation.
// len(Turns) == 1 → single-turn; >1 → multi-turn (driven by cli.MultiTurnDriver).
type scenario struct {
	ID          string
	ModelKind   string                                                   // "chat" or "reasoning"
	Supports    func(cliID, providerID string, model ModelInfo) bool     // matrix gate (per cell)
	ErrorIgnore []string                                                 // sentinels to whitelist before error scan
	Turns       []Turn
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
	}
}

// 01 simple-chat — single-turn smoke test.
func simpleChatScenario() scenario {
	return scenario{
		ID:          "simple-chat",
		ModelKind:   "chat",
		Supports:    func(string, string, ModelInfo) bool { return true },
		ErrorIgnore: []string{"OKBIFROST"},
		Turns: []Turn{{
			Send:       "Reply with exactly the single token OKBIFROST and nothing else.",
			AssertText: []string{"OKBIFROST"},
			Timeout:    90 * time.Second,
		}},
	}
}

// 02 tool-call — model uses its built-in shell tool.
func toolCallScenario() scenario {
	return scenario{
		ID:          "tool-call",
		ModelKind:   "chat",
		Supports:    func(string, string, ModelInfo) bool { return true },
		ErrorIgnore: []string{"TOOLOK"},
		Turns: []Turn{{
			Send: "Use your shell tool to run `printf TOOLOK` (no newline) and report the exact " +
				"output. Do not simulate it. Do not just type TOOLOK — you must run the shell command.",
			AssertText: []string{"TOOLOK"},
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
		Supports:    func(string, string, ModelInfo) bool { return true },
		ErrorIgnore: []string{"FILEOK"},
		Turns: []Turn{{
			Send: "Read the file at " + fixture + " using your file tool. After reading, " +
				"reply with the single token FILEOK followed by the capital city of France from the file.",
			AssertText: []string{"FILEOK", "Paris"},
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
		Supports: func(_, _ string, m ModelInfo) bool {
			return m.WebSearch
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
		Supports: func(_, _ string, m ModelInfo) bool {
			return m.ExtendedThinking || m.AdaptiveThinking
		},
		ErrorIgnore: []string{"REASONOK", "144"},
		Turns: []Turn{{
			Send: "A train leaves station A at 9:00 AM going 60 km/h east. Another leaves station B at " +
				"10:00 AM going 40 km/h west. Stations A and B are 280 km apart. At what time do they meet? " +
				"After answering, append the single token REASONOK on its own line.",
			AssertText:    []string{"REASONOK"},
			AssertTextAny: []string{"12:12", "12.12"},
			Timeout:       240 * time.Second,
		}},
	}
}

// 06 conversation-memory — multi-turn: the model must remember a fact
// across three turns.
func conversationMemoryScenario() scenario {
	return scenario{
		ID:          "conversation-memory",
		ModelKind:   "chat",
		Supports:    func(string, string, ModelInfo) bool { return true },
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
		Supports:    func(string, string, ModelInfo) bool { return true },
		ErrorIgnore: []string{"haiku", "Haiku"},
		Turns: []Turn{
			{
				Send:       "Write a haiku about the ocean.",
				AssertText: []string{},
				Timeout:    60 * time.Second,
			},
			{
				Send:          "Now rewrite it but make it about a desert instead. Keep haiku form.",
				AssertTextAny: []string{"desert", "sand", "dune", "dry"},
				Timeout:       60 * time.Second,
			},
			{
				Send:          "Now combine both into a four-line poem with one ocean image and one desert image.",
				AssertTextAny: []string{"ocean", "sea", "wave"},
				Timeout:       60 * time.Second,
			},
		},
	}
}

// 08 conversation-role-stability — multi-turn: the model is given a role
// in turn 1 and we check it sticks across turns even when distracted.
func conversationRoleStabilityScenario() scenario {
	return scenario{
		ID:          "conversation-role-stability",
		ModelKind:   "chat",
		Supports:    func(string, string, ModelInfo) bool { return true },
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
