package clis

import (
	"fmt"
	"strings"
	"testing"
)

func TestAssertionOutputCodexExtractsAgentMessageOnly(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"user_message","content":[{"type":"input_text","text":"Reply with OKBIFROST"}]}}`,
		`{"type":"item.completed","item":{"type":"agent_message","content":[{"type":"output_text","text":"actual assistant text"}]}}`,
	}, "\n")

	got := assertionOutput("codex", raw)
	if strings.Contains(got, "OKBIFROST") {
		t.Fatalf("assertion output included user prompt echo: %q", got)
	}
	if !strings.Contains(got, "actual assistant text") {
		t.Fatalf("assertion output missed assistant text: %q", got)
	}
}

func TestAssertionOutputOpenCodeExtractsAssistantRoleOnly(t *testing.T) {
	raw := strings.Join([]string{
		`{"role":"user","content":[{"type":"text","text":"Use token FILEOK"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"FILEOK FILE_FIXTURE_73129"}]}`,
	}, "\n")

	got := assertionOutput("opencode", raw)
	if strings.Contains(got, "Use token") {
		t.Fatalf("assertion output included user prompt echo: %q", got)
	}
	if !strings.Contains(got, "FILE_FIXTURE_73129") {
		t.Fatalf("assertion output missed assistant text: %q", got)
	}
}

func TestAssertionOutputOpenCodeExtractsTextPartEvents(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"step_start","part":{"type":"step-start"}}`,
		`{"type":"text","part":{"type":"text","text":"OKBIFROST"}}`,
		`{"type":"step_finish","part":{"type":"step-finish","reason":"stop"}}`,
	}, "\n")

	got := assertionOutput("opencode", raw)
	if !strings.Contains(got, "OKBIFROST") {
		t.Fatalf("assertion output missed opencode text event: %q", got)
	}
}

func TestDetectErrorCatchesClientDisconnectTrace(t *testing.T) {
	raw := strings.Join([]string{
		`The bash tool was called with invalid arguments`,
		`Error`,
		`Request cancelled: client disconnected`,
	}, "\n")

	pattern, snippet, ok := detectError(raw, nil)
	if !ok {
		t.Fatal("expected client disconnect trace to be detected")
	}
	if pattern == "" || !strings.Contains(snippet, "client disconnected") {
		t.Fatalf("unexpected error detection result: pattern=%q snippet=%q", pattern, snippet)
	}
}

func TestSemanticValidatorsAcceptMeaningfulAnswers(t *testing.T) {
	if err := validateReasoningMeetingTime("They meet at 12:12 PM."); err != nil {
		t.Fatalf("reasoning validator rejected valid answer: %v", err)
	}

	poem := strings.Join([]string{
		"Waves crash where the salt wind sings,",
		"Dunes stretch beneath a burning sky,",
		"The tide pulls secrets from the deep,",
		"While desert stars watch silently.",
	}, "\n")
	if err := validateOceanDesertPoem(poem); err != nil {
		t.Fatalf("poem validator rejected valid answer: %v", err)
	}
}

func TestCaseInsensitiveAssertions(t *testing.T) {
	turn := Turn{
		AssertTextFold:    []string{"FILEOK"},
		AssertTextAnyFold: []string{"paris"},
	}
	if err := assertTurn(turn, "fileok PARIS FILE_FIXTURE_73129"); err != nil {
		t.Fatalf("case-insensitive assertion failed: %v", err)
	}
}

func TestRateLimitDelayParsesAzureWaitSeconds(t *testing.T) {
	msg := "Rate limit of 50000 per 60s exceeded for UserByModelByMinuteUncachedInputTokens. Please wait 56 seconds before retrying."
	wait, ok := rateLimitDelay(msg, nil)
	if !ok {
		t.Fatal("expected rate limit to be detected")
	}
	if wait.Seconds() != 57 {
		t.Fatalf("expected 57s wait including buffer, got %s", wait)
	}
}

func TestRateLimitDelayParsesEscapedCLIError(t *testing.T) {
	msg := `{"type":"error","message":"Rate limit exceeded. Please wait 12 seconds before retrying."}`
	wait, ok := rateLimitDelay("", fmt.Errorf("%s", msg))
	if !ok {
		t.Fatal("expected escaped rate limit to be detected")
	}
	if wait.Seconds() != 13 {
		t.Fatalf("expected 13s wait including buffer, got %s", wait)
	}
}

func TestSoftPassCandidateRequiresNonErrorAssistantText(t *testing.T) {
	okTranscript := `{"type":"text","part":{"type":"text","text":"Waves crash on grey stone, tide pulls secrets."}}`
	if !isSoftPassCandidate(okTranscript, "opencode") {
		t.Fatal("expected non-empty assistant text without errors to be soft-pass eligible")
	}

	errorTranscript := `API Error: 400 provider API error`
	if isSoftPassCandidate(errorTranscript, "claude") {
		t.Fatal("expected API error transcript to be ineligible for soft pass")
	}

	emptyTranscript := `{"type":"step_finish","part":{"type":"step-finish","reason":"stop"}}`
	if isSoftPassCandidate(emptyTranscript, "opencode") {
		t.Fatal("expected empty assistant text to be ineligible for soft pass")
	}
}
