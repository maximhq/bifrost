package logstore

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routeMatchFixtures returns one log per shape a route can tell apart, keyed
// by name. Every guardrail entry is recorded under rule "pii" and provider
// "bedrock" unless its name says otherwise.
func routeMatchFixtures() map[string]*Log {
	monitorOnly := false
	return map[string]*Log{
		"input-block": {Object: "chat.completion",
			ErrorDetailsParsed: &schemas.BifrostError{StatusCode: new(400), Error: &schemas.ErrorField{Type: new(guardrailInterventionErrorType)}},
			GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Interventions: []schemas.BifrostGuardrailIntervention{
				{Phase: "input", RuleName: "pii", GuardrailProvider: "bedrock"},
			}},
		},
		"legacy-block": {Object: "chat.completion",
			ErrorDetailsParsed: &schemas.BifrostError{StatusCode: new(400), Error: &schemas.ErrorField{Type: new(guardrailInterventionErrorType)}},
		},
		"output-redact": {Object: "chat.completion",
			GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
				{Phase: "output", RuleName: "pii", GuardrailProvider: "bedrock", Action: schemas.GuardrailDetectionActionRedact},
			}},
		},
		"monitor-detect": {Object: "responses",
			GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
				{Phase: "output", RuleName: "toxicity", GuardrailProvider: "azure", Action: schemas.GuardrailDetectionActionBlock, Enforced: &monitorOnly},
			}},
		},
		"judge-allowed": {Object: "chat.completion",
			GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{JudgeCalls: []schemas.BifrostGuardrailJudgeCall{{Phase: "input", RuleName: "pii"}}},
		},
		"rate-limited": {Object: "chat.completion",
			ErrorDetailsParsed: &schemas.BifrostError{Type: new("rate_limit_error"), StatusCode: new(429), Error: &schemas.ErrorField{Message: "slow down"}},
		},
		"clean": {Object: "embedding"},
	}
}

// TestObjectStorageRouteMatchMatches checks each match field, the AND across
// fields, and that guardrail fields must hold for one entry together.
func TestObjectStorageRouteMatchMatches(t *testing.T) {
	cases := []struct {
		name  string
		match ObjectStorageRouteMatch
		want  []string
	}{
		{"blocked includes legacy error", ObjectStorageRouteMatch{GuardrailOutcomes: []string{"blocked"}}, []string{"input-block", "legacy-block"}},
		{"detected is any detection", ObjectStorageRouteMatch{GuardrailOutcomes: []string{"detected"}}, []string{"output-redact", "monitor-detect"}},
		{"redacted only", ObjectStorageRouteMatch{GuardrailOutcomes: []string{"redacted"}}, []string{"output-redact"}},
		{"transformed matches none", ObjectStorageRouteMatch{GuardrailOutcomes: []string{"transformed"}}, nil},
		{"outcomes are OR-ed", ObjectStorageRouteMatch{GuardrailOutcomes: []string{"blocked", "redacted"}}, []string{"input-block", "legacy-block", "output-redact"}},
		{"phase alone means any outcome", ObjectStorageRouteMatch{GuardrailPhases: []string{"output"}}, []string{"output-redact", "monitor-detect"}},
		{"outcome and phase hold for one entry", ObjectStorageRouteMatch{GuardrailOutcomes: []string{"blocked"}, GuardrailPhases: []string{"output"}}, nil},
		{"phase excludes legacy block", ObjectStorageRouteMatch{GuardrailOutcomes: []string{"blocked"}, GuardrailPhases: []string{"input"}}, []string{"input-block"}},
		{"rule", ObjectStorageRouteMatch{GuardrailRules: []string{"toxicity"}}, []string{"monitor-detect"}},
		{"provider", ObjectStorageRouteMatch{GuardrailProviders: []string{"bedrock"}}, []string{"input-block", "output-redact"}},
		{"error type top level", ObjectStorageRouteMatch{ErrorTypes: []string{"rate_limit_error"}}, []string{"rate-limited"}},
		{"error type nested", ObjectStorageRouteMatch{ErrorTypes: []string{guardrailInterventionErrorType}}, []string{"input-block", "legacy-block"}},
		{"status code", ObjectStorageRouteMatch{StatusCodes: []int{429}}, []string{"rate-limited"}},
		{"request type", ObjectStorageRouteMatch{RequestTypes: []string{"embedding"}}, []string{"clean"}},
		{"fields are AND-ed", ObjectStorageRouteMatch{GuardrailOutcomes: []string{"detected"}, RequestTypes: []string{"responses"}}, []string{"monitor-detect"}},
		{"empty match matches nothing", ObjectStorageRouteMatch{}, nil},
	}
	fixtures := routeMatchFixtures()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for name, l := range fixtures {
				if tc.match.Matches(l) {
					got = append(got, name)
				}
			}
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}

// TestObjectStorageRouteMatchMonitorOnlyContentChange checks that a
// monitor-only redact or transform, which left the content untouched, counts
// as detected but not as redacted or transformed, while an enforced one does.
func TestObjectStorageRouteMatchMonitorOnlyContentChange(t *testing.T) {
	detection := func(action string, enforced *bool) *Log {
		return &Log{Object: "chat.completion", GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
			{Phase: "output", RuleName: "pii", GuardrailProvider: "bedrock", Action: action, Enforced: enforced},
		}}}
	}
	for _, tc := range []struct {
		action  string
		outcome string
	}{
		{schemas.GuardrailDetectionActionRedact, GuardrailOutcomeRedacted},
		{schemas.GuardrailDetectionActionTransform, GuardrailOutcomeTransformed},
	} {
		t.Run(tc.action, func(t *testing.T) {
			changed := ObjectStorageRouteMatch{GuardrailOutcomes: []string{tc.outcome}}
			detected := ObjectStorageRouteMatch{GuardrailOutcomes: []string{GuardrailOutcomeDetected}}
			monitorOnly := detection(tc.action, new(false))
			assert.False(t, changed.Matches(monitorOnly), "a monitor-only %s left the content untouched", tc.action)
			assert.True(t, detected.Matches(monitorOnly))
			assert.True(t, changed.Matches(detection(tc.action, new(true))))
			assert.True(t, changed.Matches(detection(tc.action, nil)), "an entry without enforced was acted on")
		})
	}
}

// TestObjectStorageRouteMatchPhaseAsRecorded checks that phases are compared
// exactly as recorded, with no case folding.
func TestObjectStorageRouteMatchPhaseAsRecorded(t *testing.T) {
	l := routeMatchFixtures()["output-redact"]
	assert.True(t, ObjectStorageRouteMatch{GuardrailPhases: []string{"output"}}.Matches(l))
	assert.False(t, ObjectStorageRouteMatch{GuardrailPhases: []string{"Output"}}.Matches(l))
}

// TestObjectStorageRouteMatchSerializedColumns checks that a serialized entry,
// with only the text columns set, matches the same way as a parsed one.
func TestObjectStorageRouteMatchSerializedColumns(t *testing.T) {
	src := routeMatchFixtures()["input-block"]
	errJSON, err := sonic.MarshalString(src.ErrorDetailsParsed)
	require.NoError(t, err)
	debugJSON, err := sonic.MarshalString(src.GuardrailDebugParsed)
	require.NoError(t, err)
	l := &Log{Object: "chat.completion", ErrorDetails: errJSON, GuardrailDebug: debugJSON}

	assert.True(t, ObjectStorageRouteMatch{GuardrailOutcomes: []string{"blocked"}, GuardrailPhases: []string{"input"}}.Matches(l))
	assert.True(t, ObjectStorageRouteMatch{StatusCodes: []int{400}}.Matches(l))
	assert.Nil(t, l.ErrorDetailsParsed, "matching must not modify the log")
	assert.Nil(t, l.GuardrailDebugParsed, "matching must not modify the log")
}

// TestObjectStorageRouteMatchPassthroughRequestType checks that a passthrough
// row matches the passthrough request types, as exclusions do.
func TestObjectStorageRouteMatchPassthroughRequestType(t *testing.T) {
	l := &Log{Object: "chat.completion", IsPassthrough: true, Stream: true}
	assert.True(t, ObjectStorageRouteMatch{RequestTypes: []string{"passthrough_stream"}}.Matches(l))
	assert.True(t, ObjectStorageRouteMatch{RequestTypes: []string{"chat.completion"}}.Matches(l))
	assert.False(t, ObjectStorageRouteMatch{RequestTypes: []string{"passthrough"}}.Matches(l))
}

// TestObjectStorageRouteMatchValidate checks the startup errors.
func TestObjectStorageRouteMatchValidate(t *testing.T) {
	assert.Error(t, ObjectStorageRouteMatch{}.Validate())
	assert.Error(t, ObjectStorageRouteMatch{GuardrailOutcomes: []string{"block"}}.Validate())
	assert.Error(t, ObjectStorageRouteMatch{StatusCodes: []int{42}}.Validate())
	assert.NoError(t, ObjectStorageRouteMatch{GuardrailOutcomes: []string{"blocked", "detected", "redacted", "transformed"}}.Validate())
	assert.NoError(t, ObjectStorageRouteMatch{GuardrailPhases: []string{"some-future-phase"}}.Validate())
}
