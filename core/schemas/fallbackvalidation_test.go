package schemas_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// recordingLogger captures Warn-level messages so tests can assert on drop
// notifications without relying on log output parsing.
type recordingLogger struct {
	warns []string
}

func (r *recordingLogger) Debug(msg string, args ...any) {}
func (r *recordingLogger) Info(msg string, args ...any)  {}
func (r *recordingLogger) Warn(msg string, args ...any) {
	r.warns = append(r.warns, fmt.Sprintf(msg, args...))
}
func (r *recordingLogger) Error(msg string, args ...any)                     {}
func (r *recordingLogger) Fatal(msg string, args ...any)                     {}
func (r *recordingLogger) SetLevel(schemas.LogLevel)                         {}
func (r *recordingLogger) SetOutputType(schemas.LoggerOutputType)            {}
func (r *recordingLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// TestNormalizeFallbacks_DropsAreLoggedInLenientMode verifies that when a
// logger is supplied, every skipped-over invalid entry produces a Warn log.
// This is the observability fix that closes the "silent failure" gap: operators
// will now see misconfigured entries in their log stream even without enabling
// strict mode.
func TestNormalizeFallbacks_DropsAreLoggedInLenientMode(t *testing.T) {
	l := &recordingLogger{}

	input := []schemas.Fallback{
		{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},    // valid
		{Provider: schemas.Bedrock},                          // invalid: no model
		{Provider: "", Model: "gpt-4.1"},                     // invalid: no provider
		{Provider: schemas.Anthropic, Model: "claude-3-5-sonnet-20241022"}, // valid
	}

	out, err := schemas.NormalizeFallbacks(input, schemas.FallbackValidationLenient, l)
	if err != nil {
		t.Fatalf("lenient mode must not return error; got: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 valid entries, got %d: %#v", len(out), out)
	}
	if len(l.warns) != 2 {
		t.Fatalf("expected 2 Warn logs for 2 dropped entries, got %d: %v", len(l.warns), l.warns)
	}
	for _, w := range l.warns {
		if !strings.Contains(w, "dropping") {
			t.Errorf("expected warn to mention 'dropping', got: %q", w)
		}
	}
}

// TestNormalizeFallbacks_NoLogWithoutLogger verifies backward compatibility:
// when no logger is passed, drops are silent (old behaviour). This guards
// against accidental panics in the nil-logger path.
func TestNormalizeFallbacks_NoLogWithoutLogger(t *testing.T) {
	input := []schemas.Fallback{
		{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		{Provider: schemas.Bedrock}, // invalid: no model
	}

	out, err := schemas.NormalizeFallbacks(input, schemas.FallbackValidationLenient)
	if err != nil {
		t.Fatalf("lenient mode without logger must not error; got: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 valid entry, got %d", len(out))
	}
}

// TestFallbackStringsToFallbacks_DropsAreLoggedInLenientMode mirrors the
// object-form test but for the legacy string-array path.
func TestFallbackStringsToFallbacks_DropsAreLoggedInLenientMode(t *testing.T) {
	l := &recordingLogger{}

	input := []string{
		"openai/gpt-4o-mini",  // valid
		"openai/",              // invalid: empty model
		"",                     // invalid: empty string
		"bedrock/us.anthropic.claude-3-5-sonnet-20241022-v2:0", // valid
	}

	out, err := schemas.FallbackStringsToFallbacks(input, schemas.FallbackValidationLenient, l)
	if err != nil {
		t.Fatalf("lenient mode must not return error; got: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 valid entries, got %d: %#v", len(out), out)
	}
	if len(l.warns) != 2 {
		t.Fatalf("expected 2 Warn logs for 2 dropped strings, got %d: %v", len(l.warns), l.warns)
	}
}

// TestNormalizeFallbacks_StrictModeRejectsFirstInvalid verifies that strict
// mode is not dead code: a real call with the strict constant must return an
// error on the first bad entry and not silently drop it.
func TestNormalizeFallbacks_StrictModeRejectsFirstInvalid(t *testing.T) {
	_, err := schemas.NormalizeFallbacks(
		[]schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}, // valid first
			{Provider: schemas.Bedrock},                       // invalid second
		},
		schemas.FallbackValidationStrict,
	)
	if err == nil {
		t.Fatal("strict mode must return an error for invalid entry")
	}
	if !strings.Contains(err.Error(), schemas.InvalidFallbackEntryError) {
		t.Errorf("expected error to contain canonical error string, got: %v", err)
	}
}
