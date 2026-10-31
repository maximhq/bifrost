package schemas

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardrailMetadataContextRoundTrip verifies typed guardrail metadata context storage.
func TestGuardrailMetadataContextRoundTrip(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	call := BifrostGuardrailJudgeCall{
		Phase:         "input",
		RuleName:      "pii",
		JudgeProvider: OpenAI,
		JudgeModel:    "gpt-4o-mini",
		PromptTokens:  12,
		TotalTokens:   12,
	}

	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, call))
	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	require.Len(t, metadata.JudgeCalls, 1)
	assert.Equal(t, call, metadata.JudgeCalls[0])
}

// TestGuardrailMetadataContextReturnsOwnedSnapshot verifies callers cannot mutate context state.
func TestGuardrailMetadataContextReturnsOwnedSnapshot(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{
		JudgeProvider: OpenAI,
		JudgeModel:    "gpt-4o-mini",
		TotalTokens:   10,
	}))

	first, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	first.JudgeCalls[0].TotalTokens = 999

	second, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, 10, second.JudgeCalls[0].TotalTokens)
}

// TestGuardrailMetadataContextClonesUsageDetails verifies nested pricing details cannot alias context state.
func TestGuardrailMetadataContextClonesUsageDetails(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	citationTokens := 3
	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{
		JudgeProvider: OpenAI,
		JudgeModel:    "gpt-4o-mini",
		PromptTokens:  10,
		PromptTokensDetails: &ChatPromptTokensDetails{
			CachedWriteTokenDetails: &ChatCachedWriteTokenDetails{CachedWriteTokens5m: 4},
		},
		CompletionTokens: 5,
		CompletionTokensDetails: &ChatCompletionTokensDetails{
			CitationTokens: &citationTokens,
		},
		TotalTokens: 15,
	}))

	first, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	first.JudgeCalls[0].PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = 999
	*first.JudgeCalls[0].CompletionTokensDetails.CitationTokens = 999

	second, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, 4, second.JudgeCalls[0].PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m)
	assert.Equal(t, 3, *second.JudgeCalls[0].CompletionTokensDetails.CitationTokens)
}

// TestAppendGuardrailJudgeCallRejectsEmptyUsage verifies non-billable calls are omitted.
func TestAppendGuardrailJudgeCallRejectsEmptyUsage(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	assert.False(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{}))
	_, ok := GuardrailMetadataFromContext(ctx)
	assert.False(t, ok)
}

func TestLegacyGuardrailDebugAPIsRemainCompatible(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	metadata := &BifrostGuardrailDebug{JudgeCalls: []BifrostGuardrailJudgeCall{{TotalTokens: 1}}}
	require.True(t, SetGuardrailDebugOnContext(ctx, metadata))

	stored, ok := GuardrailDebugFromContext(ctx)
	require.True(t, ok)
	assert.Len(t, stored.JudgeCalls, 1)
}

// TestGuardrailMetadataKeepsDetectionOnlyMetadata verifies detections persist without any judge calls.
func TestGuardrailMetadataKeepsDetectionOnlyMetadata(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	ruleID := uint(7)
	detection := BifrostGuardrailDetection{
		Phase:             "input",
		RuleID:            &ruleID,
		RuleName:          "pii-audit",
		GuardrailName:     "bedrock-detect",
		GuardrailProvider: "bedrock",
		Reason:            "Guardrail detected a policy match",
		Assessments:       []string{"content_policy VIOLENCE"},
	}

	require.True(t, AppendGuardrailDetectionOnContext(ctx, detection))
	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Empty(t, metadata.JudgeCalls)
	require.Len(t, metadata.Detections, 1)
	assert.Equal(t, detection, metadata.Detections[0])
}

// TestGuardrailMetadataDetectionSnapshotIsOwned verifies nested detection fields cannot alias context state.
func TestGuardrailMetadataDetectionSnapshotIsOwned(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	ruleID := uint(3)
	require.True(t, AppendGuardrailDetectionOnContext(ctx, BifrostGuardrailDetection{
		RuleID:      &ruleID,
		Assessments: []string{"topic_policy weapons"},
	}))

	first, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	*first.Detections[0].RuleID = 999
	first.Detections[0].Assessments[0] = "mutated"

	second, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, uint(3), *second.Detections[0].RuleID)
	assert.Equal(t, "topic_policy weapons", second.Detections[0].Assessments[0])
}

// TestGuardrailMetadataKeepsJudgeCallsAndDetectionsTogether verifies each append preserves the other list.
func TestGuardrailMetadataKeepsJudgeCallsAndDetectionsTogether(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	require.True(t, AppendGuardrailDetectionOnContext(ctx, BifrostGuardrailDetection{RuleName: "first"}))
	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{TotalTokens: 5}))
	require.True(t, AppendGuardrailDetectionOnContext(ctx, BifrostGuardrailDetection{RuleName: "second"}))

	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	require.Len(t, metadata.JudgeCalls, 1)
	require.Len(t, metadata.Detections, 2)
	assert.Equal(t, "first", metadata.Detections[0].RuleName)
	assert.Equal(t, "second", metadata.Detections[1].RuleName)
}

// TestGuardrailMetadataEmptyCloneIsNil verifies metadata with neither list stays omitted.
func TestGuardrailMetadataEmptyCloneIsNil(t *testing.T) {
	assert.Nil(t, (&BifrostGuardrailMetadata{}).Clone())
	assert.False(t, SetGuardrailMetadataOnContext(NewBifrostContext(nil, NoDeadline), &BifrostGuardrailMetadata{}))
}

// TestDeferredGuardrailResultDeliversFirstResultToEveryCallback verifies the log
// writer sees the result, plugin log lines included, whether it registers before
// or after completion, and that a second Complete cannot replace it.
func TestDeferredGuardrailResultDeliversFirstResultToEveryCallback(t *testing.T) {
	result := NewDeferredGuardrailResult()
	var early, late *BifrostGuardrailMetadata
	var earlyLogs, lateLogs []PluginLogEntry
	result.OnComplete(func(metadata *BifrostGuardrailMetadata, _ RedactionData, logs []PluginLogEntry) {
		early, earlyLogs = metadata, logs
	})
	require.Nil(t, early, "callback must wait for completion")

	firstLogs := []PluginLogEntry{{PluginName: "guardrails", Message: "first"}}
	result.Complete(&BifrostGuardrailMetadata{Detections: []BifrostGuardrailDetection{{RuleName: "first"}}}, RedactionData{}, firstLogs)
	result.Complete(&BifrostGuardrailMetadata{Detections: []BifrostGuardrailDetection{{RuleName: "second"}}}, RedactionData{},
		[]PluginLogEntry{{PluginName: "guardrails", Message: "second"}})
	result.OnComplete(func(metadata *BifrostGuardrailMetadata, _ RedactionData, logs []PluginLogEntry) {
		late, lateLogs = metadata, logs
	})

	require.NotNil(t, early)
	require.NotNil(t, late)
	assert.Equal(t, "first", early.Detections[0].RuleName)
	assert.Equal(t, "first", late.Detections[0].RuleName)
	assert.Equal(t, firstLogs, earlyLogs)
	assert.Equal(t, firstLogs, lateLogs)
}

// TestGuardrailDetectionActionAndEnforcedSerialize verifies a detection names
// the action and keeps an explicit enforced=false, so a monitored block reads
// differently from an enforced one, while an older detection without them
// serializes as before.
func TestGuardrailDetectionActionAndEnforcedSerialize(t *testing.T) {
	monitored := BifrostGuardrailDetection{Phase: "output", RuleName: "r", Action: GuardrailDetectionActionBlock, Enforced: Ptr(false)}
	data, err := json.Marshal(monitored)
	require.NoError(t, err)
	assert.JSONEq(t, `{"phase":"output","rule_name":"r","action":"block","enforced":false}`, string(data))

	data, err = json.Marshal(BifrostGuardrailDetection{Phase: "input"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"phase":"input"}`, string(data))

	cloned := (&BifrostGuardrailMetadata{Detections: []BifrostGuardrailDetection{monitored}}).Clone()
	require.NotNil(t, cloned.Detections[0].Enforced)
	assert.False(t, *cloned.Detections[0].Enforced)
	*cloned.Detections[0].Enforced = true
	assert.False(t, *monitored.Enforced, "clone must not share enforced with the original")
}

// TestGuardrailMetadataKeepsInterventionOnlyMetadata verifies a block with no judge usage is still stored.
func TestGuardrailMetadataKeepsInterventionOnlyMetadata(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	ruleID := uint(11)
	intervention := BifrostGuardrailIntervention{
		Phase:             "output",
		RuleID:            &ruleID,
		RuleName:          "block-secrets",
		GuardrailName:     "azure-content",
		GuardrailProvider: "azure",
		Reason:            "Secret detected",
		Assessments:       []string{"secrets AWS_KEY"},
	}

	require.True(t, AppendGuardrailInterventionOnContext(ctx, intervention))
	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Empty(t, metadata.JudgeCalls)
	assert.Empty(t, metadata.Detections)
	require.Len(t, metadata.Interventions, 1)
	assert.Equal(t, intervention, metadata.Interventions[0])
}

// TestGuardrailMetadataInterventionSnapshotIsOwned verifies nested intervention fields cannot alias context state.
func TestGuardrailMetadataInterventionSnapshotIsOwned(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	ruleID := uint(4)
	require.True(t, AppendGuardrailInterventionOnContext(ctx, BifrostGuardrailIntervention{
		RuleID:      &ruleID,
		Assessments: []string{"pii EMAIL"},
	}))

	first, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	*first.Interventions[0].RuleID = 999
	first.Interventions[0].Assessments[0] = "mutated"

	second, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, uint(4), *second.Interventions[0].RuleID)
	assert.Equal(t, "pii EMAIL", second.Interventions[0].Assessments[0])
}

// TestGuardrailMetadataInterventionsPreserveOtherLists verifies appending a block keeps detections and judge calls.
func TestGuardrailMetadataInterventionsPreserveOtherLists(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	require.True(t, AppendGuardrailDetectionOnContext(ctx, BifrostGuardrailDetection{RuleName: "detect"}))
	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{TotalTokens: 5}))
	require.True(t, AppendGuardrailInterventionOnContext(ctx, BifrostGuardrailIntervention{RuleName: "block"}))
	assert.False(t, AppendGuardrailInterventionOnContext(nil, BifrostGuardrailIntervention{RuleName: "ignored"}))

	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	require.Len(t, metadata.Detections, 1)
	require.Len(t, metadata.JudgeCalls, 1)
	require.Len(t, metadata.Interventions, 1)
	assert.Equal(t, "block", metadata.Interventions[0].RuleName)
}

// TestGuardrailMetadataConcurrentAppendsAreNotLost verifies concurrent appends of every kind keep every entry.
func TestGuardrailMetadataConcurrentAppendsAreNotLost(t *testing.T) {
	const writers = 64
	ctx := NewBifrostContext(nil, NoDeadline)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			AppendGuardrailInterventionOnContext(ctx, BifrostGuardrailIntervention{RuleName: "block"})
		}()
		go func() {
			defer wg.Done()
			<-start
			AppendGuardrailDetectionOnContext(ctx, BifrostGuardrailDetection{RuleName: "detect"})
		}()
		go func() {
			defer wg.Done()
			<-start
			AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{TotalTokens: 1})
		}()
	}
	close(start)
	wg.Wait()

	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Len(t, metadata.Interventions, writers)
	assert.Len(t, metadata.Detections, writers)
	assert.Len(t, metadata.JudgeCalls, writers)
}
