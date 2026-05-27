package telemetry

import (
	"reflect"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// recordingLogger captures Info messages for assertions; all other levels noop.
type recordingLogger struct {
	infoMsgs []string
}

func (l *recordingLogger) Debug(string, ...any)            {}
func (l *recordingLogger) Info(format string, args ...any) { l.infoMsgs = append(l.infoMsgs, format) }
func (l *recordingLogger) Warn(string, ...any)             {}
func (l *recordingLogger) Error(string, ...any)            {}
func (l *recordingLogger) Fatal(string, ...any)            {}
func (l *recordingLogger) SetLevel(schemas.LogLevel)       {}
func (l *recordingLogger) SetOutputType(schemas.LoggerOutputType) {
}

func (l *recordingLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestFilterDisabledLabels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		labels   []string
		disabled []string
		want     []string
		wantLogs int
	}{
		{
			name:     "empty disabled list is a no-op",
			labels:   []string{"provider", "model", "virtual_key_id"},
			disabled: nil,
			want:     []string{"provider", "model", "virtual_key_id"},
			wantLogs: 0,
		},
		{
			name:     "drops exact matches",
			labels:   []string{"provider", "model", "virtual_key_id", "virtual_key_name"},
			disabled: []string{"virtual_key_id", "virtual_key_name"},
			want:     []string{"provider", "model"},
			wantLogs: 2,
		},
		{
			name: "matches across hyphen/underscore variants",
			// Operators write hyphenated names ("virtual-key-id") in helm
			// values; the registered labels use underscores. containsLabel
			// already normalises both directions; this asserts the filter
			// inherits that.
			labels:   []string{"virtual_key_id", "team_id"},
			disabled: []string{"virtual-key-id"},
			want:     []string{"team_id"},
			wantLogs: 1,
		},
		{
			name:     "preserves order of remaining labels",
			labels:   []string{"a", "b", "c", "d", "e"},
			disabled: []string{"b", "d"},
			want:     []string{"a", "c", "e"},
			wantLogs: 2,
		},
		{
			name:     "unknown disabled label is silently ignored",
			labels:   []string{"provider", "model"},
			disabled: []string{"not_a_real_label"},
			want:     []string{"provider", "model"},
			wantLogs: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger := &recordingLogger{}
			got := filterDisabledLabels(tc.labels, tc.disabled, "test", logger)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if len(logger.infoMsgs) != tc.wantLogs {
				t.Fatalf("got %d info logs, want %d (%v)", len(logger.infoMsgs), tc.wantLogs, logger.infoMsgs)
			}
		})
	}
}

// Asserts the filter never aliases the caller's slice — important because the
// returned slice is later passed to promauto.NewHistogramVec etc. via
// `append(defaultBifrostLabels, filteredCustomLabels...)` and we do not want a
// later `append` to corrupt the original `defaultBifrostLabels` backing array.
func TestFilterDisabledLabels_DoesNotAliasInput(t *testing.T) {
	t.Parallel()
	input := []string{"provider", "model", "virtual_key_id"}
	logger := &recordingLogger{}

	got := filterDisabledLabels(input, []string{"virtual_key_id"}, "test", logger)

	// Mutating the result's backing array must not bleed into input.
	if cap(got) > 0 {
		got = append(got, "mutated")
	}
	if input[0] != "provider" || input[1] != "model" || input[2] != "virtual_key_id" {
		t.Fatalf("input was mutated: %v", input)
	}
}
