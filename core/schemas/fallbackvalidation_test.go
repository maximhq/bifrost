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
func (r *recordingLogger) Error(msg string, args ...any)          {}
func (r *recordingLogger) Fatal(msg string, args ...any)          {}
func (r *recordingLogger) SetLevel(schemas.LogLevel)              {}
func (r *recordingLogger) SetOutputType(schemas.LoggerOutputType) {}
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
		{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},                   // valid
		{Provider: schemas.Bedrock},                                        // invalid: no model
		{Provider: "", Model: "gpt-4.1"},                                   // invalid: no provider
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
		"openai/gpt-4o-mini", // valid
		"openai/",            // invalid: empty model
		"",                   // invalid: empty string
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
			{Provider: schemas.Bedrock},                      // invalid second
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

// TestNormalizeAndValidateFallback_CrossProviderModelID guards the fix for the
// case where the model string itself contains a slash that belongs to the model
// ID rather than a "provider/model" qualifier. The canonical example is
// OpenRouter where upstream model IDs like "openai/gpt-4o" are opaque strings
// passed verbatim to the provider.
//
// Before the fix, NormalizeAndValidateFallback would re-parse "openai/gpt-4o"
// via ParseModelString, derive provider="openai", and then reject the entry
// because "openai" != "openrouter". The fix treats the explicit provider field
// as authoritative and only strips a matching "<provider>/" prefix.
func TestNormalizeAndValidateFallback_CrossProviderModelID(t *testing.T) {
	tests := []struct {
		name         string
		input        schemas.Fallback
		wantProvider schemas.ModelProvider
		wantModel    string
		wantValid    bool
	}{
		{
			name:         "openrouter with cross-provider model id is accepted",
			input:        schemas.Fallback{Provider: "openrouter", Model: "openai/gpt-4o"},
			wantProvider: "openrouter",
			wantModel:    "openai/gpt-4o",
			wantValid:    true,
		},
		{
			name:         "matching prefix is stripped",
			input:        schemas.Fallback{Provider: schemas.OpenAI, Model: "openai/gpt-4o-mini"},
			wantProvider: schemas.OpenAI,
			wantModel:    "gpt-4o-mini",
			wantValid:    true,
		},
		{
			name:         "bedrock colon model id is accepted",
			input:        schemas.Fallback{Provider: schemas.Bedrock, Model: "us.anthropic.claude-3-5-sonnet-20241022-v2:0"},
			wantProvider: schemas.Bedrock,
			wantModel:    "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
			wantValid:    true,
		},
		{
			name:      "missing model is rejected",
			input:     schemas.Fallback{Provider: schemas.OpenAI, Model: ""},
			wantValid: false,
		},
		{
			name:      "matching prefix that empties model is rejected",
			input:     schemas.Fallback{Provider: schemas.OpenAI, Model: "openai/"},
			wantValid: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := schemas.NormalizeAndValidateFallback(tc.input, 0)
			if ok != tc.wantValid {
				t.Fatalf("wantValid=%v got ok=%v err=%v", tc.wantValid, ok, err)
			}
			if !tc.wantValid {
				return
			}
			if got.Provider != tc.wantProvider {
				t.Errorf("provider: want %q got %q", tc.wantProvider, got.Provider)
			}
			if got.Model != tc.wantModel {
				t.Errorf("model: want %q got %q", tc.wantModel, got.Model)
			}
		})
	}
}

// TestFallbackStringsToFallbacks_TrimsWhitespace verifies that legacy string
// fallbacks with surrounding whitespace are accepted rather than silently
// dropped. Object-form fallbacks trim in NormalizeAndValidateFallback; the
// string path must apply the same normalisation via strings.TrimSpace before
// calling ParseModelString.
func TestFallbackStringsToFallbacks_TrimsWhitespace(t *testing.T) {
	inputs := []string{
		"  openai/gpt-4o-mini  ",
		"\tanthropic/claude-3-5-haiku-20241022\t",
		" openrouter/openai/gpt-4o ",
	}
	got, err := schemas.FallbackStringsToFallbacks(inputs, schemas.FallbackValidationStrict)
	if err != nil {
		t.Fatalf("unexpected error for whitespace-padded strings: %v", err)
	}
	if len(got) != len(inputs) {
		t.Fatalf("want %d fallbacks, got %d", len(inputs), len(got))
	}
	// Spot-check first entry: provider and model must be trimmed/parsed correctly.
	if got[0].Provider != schemas.OpenAI {
		t.Errorf("entry 0 provider: want %q got %q", schemas.OpenAI, got[0].Provider)
	}
	if got[0].Model != "gpt-4o-mini" {
		t.Errorf("entry 0 model: want %q got %q", "gpt-4o-mini", got[0].Model)
	}
}
