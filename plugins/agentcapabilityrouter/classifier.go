package agentcapabilityrouter

import (
	"math"
	"sort"
	"strings"
)

const (
	CapabilityOrchestrate = "orchestrate"
	CapabilityImplement   = "implement"
	CapabilityDebug       = "debug"
	CapabilityToolLoop    = "tool-loop"
	CapabilityExplore     = "explore"
	CapabilitySummarize   = "summarize"
	CapabilityGeneral     = "general"
)

var capabilityPriority = []string{
	CapabilityDebug,
	CapabilityImplement,
	CapabilityOrchestrate,
	CapabilityToolLoop,
	CapabilityExplore,
	CapabilitySummarize,
	CapabilityGeneral,
}

type SignalEvent struct {
	Kind   string
	Text   string
	Failed bool
}

type SignalSnapshot struct {
	Events []SignalEvent
}

type Classification struct {
	Capability string
	Confidence float64
	Signals    []string
}

func defaultKeywords() map[string][]string {
	return map[string][]string{
		CapabilityOrchestrate: {
			"architect", "architecture", "design ", "plan ", "strategy", "trade-off", "tradeoff",
			"decompose", "coordinate", "orchestrate", "migration plan", "rollout plan",
		},
		CapabilityImplement: {
			"implement", "write code", "add code", "create file", "edit file", "apply_patch", "patch ",
			"refactor", "rename", "update the code", "modify", "build the feature",
		},
		CapabilityDebug: {
			"debug", "fix ", "broken", "failing", "failed", "failure", "error", "panic", "exception",
			"traceback", "root cause", "regression", "doesn't work", "does not work", "why is",
			"test failed", "compile error", "type error",
		},
		CapabilityToolLoop: {
			"run ", "execute", "command", "terminal", "shell", "build", "test ", "lint", "format",
			"deploy", "install", "curl ", "docker ", "kubectl ", "terraform ", "npm ", "go test",
		},
		CapabilityExplore: {
			"explore", "inspect", "search", "find ", "locate", "read ", "review", "investigate",
			"understand the codebase", "where is", "which file", "look for", "trace the flow",
		},
		CapabilitySummarize: {
			"summarize", "summary", "recap", "report", "explain what changed", "list the changes",
			"document", "release notes", "final answer", "status update",
		},
	}
}

func isKnownCapability(capability string) bool {
	switch capability {
	case CapabilityOrchestrate, CapabilityImplement, CapabilityDebug, CapabilityToolLoop,
		CapabilityExplore, CapabilitySummarize, CapabilityGeneral:
		return true
	default:
		return false
	}
}

func classify(snapshot SignalSnapshot, cfg resolvedConfig) Classification {
	if len(snapshot.Events) == 0 {
		return Classification{Capability: CapabilityGeneral}
	}

	scores := map[string]float64{}
	signals := map[string][]string{}
	lastIndex := len(snapshot.Events) - 1

	for index, event := range snapshot.Events {
		text := strings.ToLower(event.Text)
		if text == "" && event.Kind == "" {
			continue
		}

		weight := 0.55 + (0.45 * float64(index+1) / float64(len(snapshot.Events)))
		if index == lastIndex {
			weight += 0.25
		}

		switch event.Kind {
		case "edit":
			addScore(scores, signals, CapabilityImplement, 5.0*weight, "edit-action")
		case "tool-result":
			if event.Failed {
				addScore(scores, signals, CapabilityDebug, 6.0*weight, "failed-tool-result")
			} else {
				addScore(scores, signals, CapabilityToolLoop, 4.0*weight, "tool-result")
			}
		case "tool-call":
			addScore(scores, signals, CapabilityToolLoop, 4.0*weight, "tool-call")
		case "search":
			addScore(scores, signals, CapabilityExplore, 4.0*weight, "search-action")
		}

		for capability, keywords := range cfg.Keywords {
			for _, keyword := range keywords {
				if keyword != "" && strings.Contains(text, strings.ToLower(keyword)) {
					addScore(scores, signals, capability, 1.4*weight, keyword)
				}
			}
		}
	}

	latest := snapshot.Events[lastIndex]
	latestText := strings.ToLower(latest.Text)
	if latest.Failed || (latest.Kind == "tool-result" && looksLikeFailure(latestText)) {
		addScore(scores, signals, CapabilityDebug, 8, "latest-failure")
	}
	if latest.Kind == "edit" {
		addScore(scores, signals, CapabilityImplement, 8, "latest-edit")
	}
	if latest.Kind == "search" {
		addScore(scores, signals, CapabilityExplore, 6, "latest-search")
	}
	if latest.Kind == "tool-result" && !latest.Failed {
		addScore(scores, signals, CapabilityToolLoop, 6, "latest-tool-result")
	}

	if containsAny(latestText, "summarize", "summary", "recap", "final report", "status update") &&
		!containsAny(latestText, "fix", "debug", "failing", "failed", "error", "implement", "patch") {
		addScore(scores, signals, CapabilitySummarize, 7, "explicit-summary")
	}

	bestCapability := CapabilityGeneral
	bestScore := 0.0
	secondScore := 0.0
	for _, capability := range capabilityPriority {
		score := scores[capability]
		if score > bestScore {
			secondScore = bestScore
			bestScore = score
			bestCapability = capability
		} else if score > secondScore {
			secondScore = score
		}
	}

	if bestScore < 1.5 {
		return Classification{Capability: CapabilityGeneral, Confidence: math.Min(bestScore/2, 0.49)}
	}

	margin := bestScore - secondScore
	confidence := 0.55 + math.Min(bestScore/14, 0.30) + math.Min(margin/12, 0.14)
	confidence = math.Min(confidence, 0.99)
	selectedSignals := uniqueSorted(signals[bestCapability])
	return Classification{Capability: bestCapability, Confidence: confidence, Signals: selectedSignals}
}

func addScore(scores map[string]float64, signals map[string][]string, capability string, score float64, signal string) {
	if !isKnownCapability(capability) {
		return
	}
	scores[capability] += score
	signals[capability] = append(signals[capability], signal)
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
