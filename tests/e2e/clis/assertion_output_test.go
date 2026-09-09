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

	// reasoning turns 2 and 3.
	if err := validateReasoningDistanceFromA("The meeting point is 192 km from station A."); err != nil {
		t.Fatalf("distance validator rejected valid answer: %v", err)
	}
	if err := validateReasoningDistanceFromA("About 200 km from station A."); err == nil {
		t.Fatal("distance validator accepted a wrong answer")
	}
	if err := validateReasoningSimultaneousDeparture("They would meet at 11:48 AM."); err != nil {
		t.Fatalf("simultaneous-departure validator rejected valid answer: %v", err)
	}
	if err := validateReasoningSimultaneousDeparture("They would meet at 12:12 PM."); err == nil {
		t.Fatal("simultaneous-departure validator accepted turn 1's answer")
	}

	// reasoning-replay: the chain is 56 boxes -> 672 items -> 2 trips / 172
	// left -> $120. Each validator must accept prose and thousands separators
	// but reject a neighbouring-but-wrong number.
	if err := validateNumber(56, "total boxes")("There are 56 boxes in total."); err != nil {
		t.Errorf("boxes validator rejected a correct answer: %v", err)
	}
	if err := validateNumber(672, "total items")("That gives 672 items."); err != nil {
		t.Errorf("items validator rejected a correct answer: %v", err)
	}
	if err := validateNumber(1234, "grouped")("A total of 1,234 units."); err != nil {
		t.Errorf("validator should accept thousands separators: %v", err)
	}
	// 560 contains "56" but is not 56 -- the word boundaries are what stop a
	// substring match from passing a wrong answer.
	if err := validateNumber(56, "total boxes")("There are 560 boxes."); err == nil {
		t.Error("boxes validator accepted 560 as 56")
	}
	if err := validateTripSplit("2 full trips with 172 items left over"); err != nil {
		t.Errorf("trip validator rejected a correct answer: %v", err)
	}
	// Half an answer is not an answer: the leftover count is the part that
	// proves the division was actually carried out.
	if err := validateTripSplit("It can make 2 full trips."); err == nil {
		t.Error("trip validator accepted a reply missing the leftover count")
	}
	if err := validateReplayRecall("56 boxes, 672 items, 172 left over, $120"); err != nil {
		t.Errorf("recall validator rejected a correct answer: %v", err)
	}
	if err := validateReplayRecall("56 boxes, 672 items, 172 left over"); err == nil {
		t.Error("recall validator accepted a reply missing the cost")
	}

	// web-search turn 3 accepts every reasonable Fahrenheit phrasing, but a
	// bare mention of the unit with no reading is not an answer.
	// "38F" with no separator is common model output. It is the case a
	// zero-width \s* plus \bF\b cannot match, because the position between a
	// digit and an F is not a word boundary.
	for _, ok := range []string{"38 F", "38F", "38.5°F", "It is 38 degrees Fahrenheit.", "-4 F right now", "38 º F", "-4.5F"} {
		if err := validateFahrenheitReading(ok); err != nil {
			t.Errorf("Fahrenheit validator rejected %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"I could not convert it to Fahrenheit.", "Fahrenheit", "38 degrees Celsius"} {
		if err := validateFahrenheitReading(bad); err == nil {
			t.Errorf("Fahrenheit validator accepted %q, which reports no reading", bad)
		}
	}
}

// A small integer on its own proves nothing: CLI output is full of them, in
// timings, token counts, turn indices and JSON fields. Each numeric assertion
// has to be bound to the quantity it claims to be checking, or the turn passes
// on output that never answered the question.
func TestNumericAssertionsAreBoundToTheirQuantity(t *testing.T) {
	// file-read turn 2 asks for the square root of 144. The verification token
	// the model is quoting from turn 1 is FILE_FIXTURE_73129 -- and "12" is a
	// substring of "73129". A raw substring assertion is therefore satisfied by
	// the model echoing the token it already had, having answered nothing.
	turn := fileReadScenario().Turns[1]
	if err := assertTurn(turn, "The file said FILE_FIXTURE_73129."); err == nil {
		t.Error("file-read turn 2 accepted a reply that only echoes the fixture token")
	}
	if err := assertTurn(turn, "12"); err != nil {
		t.Errorf("file-read turn 2 rejected the correct answer: %v", err)
	}
	if err := assertTurn(turn, "The square root of 144 is 12."); err != nil {
		t.Errorf("file-read turn 2 rejected the correct answer in prose: %v", err)
	}

	// validateTripSplit's 172 check is strict enough that only the trip count
	// is loose, so a run where 172 is answered and the 2 comes from unrelated
	// output slips through.
	if err := validateTripSplit("172 items remain after 2 seconds of processing"); err == nil {
		t.Error("trip validator accepted a 2 that names no trips")
	}
	for _, ok := range []string{
		"2 full trips with 172 items left over",
		"It needs 172 items split across 2 trips",
	} {
		if err := validateTripSplit(ok); err != nil {
			t.Errorf("trip validator rejected %q: %v", ok, err)
		}
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

// A throttle has to be recognised whichever side of the phrase the failure word
// lands on. "API Error: rate limit" is standard CLI wording and puts the error
// marker first, where the reverse-order branch was only looking for
// exceeded/hit/reached/throttled - so the cell failed outright instead of
// retrying a transient throttle.
//
// The negative cases are the reason the pattern demands a failure word at all:
// a model that has read this repo will happily quote "rate-limit" out of the
// architecture docs, and that must not be mistaken for being rate limited.
func TestRateLimitDelayDetectsErrorFirstWording(t *testing.T) {
	for _, throttled := range []string{
		"API Error: rate limit",
		"API Error: rate limit reached for this model",
		"Error: rate_limit_error",
		"Rate limit exceeded. Please wait 12 seconds before retrying.",
		"429 Too Many Requests",
	} {
		if _, ok := rateLimitDelay(throttled, nil); !ok {
			t.Errorf("genuine throttle not detected: %q", throttled)
		}
	}

	for _, prose := range []string{
		"PreLLMHook Pipeline (auth, rate-limit, cache check - registration order)",
		"The gateway supports rate limiting per virtual key.",
	} {
		if _, ok := rateLimitDelay(prose, nil); ok {
			t.Errorf("model prose about rate limits mistaken for a throttle: %q", prose)
		}
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

// The regression: a model that READ Bifrost's own docs and quoted them made a
// cell wait 3 x 60s and then fail. Discussing rate limits is not being rate
// limited.
func TestRateLimitSignalRequiresFailureContext(t *testing.T) {
	quoted := `{"type":"assistant","message":{"content":[{"type":"text","text":` +
		`"The pipeline is: TransportPreHook -> PreLLMHook Pipeline (auth, rate-limit, cache check - registration order) -> Provider Queue"}]}}`

	if _, ok := rateLimitDelay(quoted, nil); ok {
		t.Error("assistant prose merely mentioning rate-limit must not trigger a retry")
	}
	if pattern, _, ok := detectError(quoted, nil); ok {
		t.Errorf("assistant prose merely mentioning rate-limit must not be an error marker (matched %q)", pattern)
	}

	// Genuine throttling must still be caught, on both the plain-stdout channel
	// (CLIs print this without a non-zero exit) and the structured one.
	for _, genuine := range []string{
		"API Error: 429 rate limit exceeded. Please wait 30 seconds before retrying.",
		`{"type":"error","message":"rate_limit_error: too many requests, try again in 5 seconds"}`,
		"Rate limit reached for this model. Please wait 12 seconds before retrying.",
	} {
		if _, ok := rateLimitDelay(genuine, nil); !ok {
			t.Errorf("genuine rate limit not detected: %q", genuine)
		}
	}

	// And the wait is still parsed out of it.
	wait, ok := rateLimitDelay("API Error: 429 rate limit exceeded. Please wait 30 seconds before retrying.", nil)
	if !ok || wait.Seconds() != 31 {
		t.Errorf("expected a 31s wait (30 + buffer), got %s ok=%v", wait, ok)
	}
}
