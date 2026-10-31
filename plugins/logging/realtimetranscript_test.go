package logging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// TestRealtimeAttributionPersistsAcrossTurns exercises both hooks and the async writer.
func TestRealtimeAttributionPersistsAcrossTurns(t *testing.T) {
	store := newTestStore(t)
	p, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	g := grant.New()
	g.SetIdentity(grant.NewIdentity(schemas.Credential{}, &schemas.UserRef{ID: "user", Name: "User"}, &schemas.EntityRef{ID: "vk", Name: "Key"}, []schemas.EntityRef{{ID: "team", Name: "Team"}}, []schemas.EntityRef{{ID: "customer", Name: "Customer"}}, []schemas.EntityRef{{ID: "business", Name: "Business"}}, &schemas.EntityRef{ID: "project", Name: "Project"}))
	for i := range 3 {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetGrant(g)
		ctx.SetValue(schemas.BifrostContextKeyRequestID, fmt.Sprintf("turn-%d", i))
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		_, _, err := p.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.RealtimeRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "realtime"}})
		require.NoError(t, err)
		_, _, err = p.PostLLMHook(ctx, &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.RealtimeRequest, Provider: schemas.OpenAI}}}, nil)
		require.NoError(t, err)
	}
	require.NoError(t, p.Cleanup())
	for i := range 3 {
		entry, err := store.FindByID(context.Background(), fmt.Sprintf("turn-%d", i))
		require.NoError(t, err)
		require.NotNil(t, entry.VirtualKeyID)
		require.Equal(t, "vk", *entry.VirtualKeyID)
		require.Equal(t, "team", *entry.TeamID)
		require.Equal(t, "customer", *entry.CustomerID)
		require.Equal(t, schemas.Ptr("user"), entry.UserID)
		require.Equal(t, schemas.Ptr("User"), entry.UserName)
		require.Equal(t, schemas.Ptr("business"), entry.BusinessUnitID)
		require.Equal(t, schemas.Ptr("Business"), entry.BusinessUnitName)
		require.Equal(t, schemas.Ptr("project"), entry.ProjectID)
		require.Equal(t, schemas.Ptr("Project"), entry.ProjectName)
		require.Equal(t, []string{"team"}, entry.TeamIDsParsed)
		require.Equal(t, []string{"Team"}, entry.TeamNamesParsed)
		require.Equal(t, []string{"customer"}, entry.CustomerIDsParsed)
		require.Equal(t, []string{"Customer"}, entry.CustomerNamesParsed)
		require.Equal(t, []string{"business"}, entry.BusinessUnitIDsParsed)
		require.Equal(t, []string{"Business"}, entry.BusinessUnitNamesParsed)
	}
}

// TestGrantAttributionAcrossRealtimeTurns covers fresh contexts sharing a resolved grant.
func TestGrantAttributionAcrossRealtimeTurns(t *testing.T) {
	g := grant.New()
	g.SetIdentity(grant.NewIdentity(schemas.Credential{}, &schemas.UserRef{ID: "user", Name: "User"},
		&schemas.EntityRef{ID: "vk", Name: "key"},
		[]schemas.EntityRef{{ID: "team", Name: "Team"}, {ID: "parent-team", Name: "Parent team"}},
		[]schemas.EntityRef{{ID: "direct", Name: "Direct"}, {ID: "inherited", Name: "Inherited"}},
		[]schemas.EntityRef{{ID: "business", Name: "Business"}, {ID: "parent-business", Name: "Parent business"}}, &schemas.EntityRef{ID: "project", Name: "Project"}))
	for range 3 {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetGrant(g)
		entry := &logstore.Log{}
		applyGrantAttribution(ctx, entry)
		require.Equal(t, "vk", *entry.VirtualKeyID)
		require.Equal(t, "team", *entry.TeamID)
		require.Equal(t, "direct", *entry.CustomerID)
		require.Equal(t, "Direct", *entry.CustomerName)
		require.Equal(t, []string{"team", "parent-team"}, entry.TeamIDsParsed)
		require.Equal(t, []string{"Team", "Parent team"}, entry.TeamNamesParsed)
		require.Equal(t, []string{"direct", "inherited"}, entry.CustomerIDsParsed)
		require.Equal(t, []string{"Direct", "Inherited"}, entry.CustomerNamesParsed)
		require.Equal(t, []string{"business", "parent-business"}, entry.BusinessUnitIDsParsed)
		require.Equal(t, []string{"Business", "Parent business"}, entry.BusinessUnitNamesParsed)
		require.Equal(t, schemas.Ptr("user"), entry.UserID)
		require.Equal(t, schemas.Ptr("User"), entry.UserName)
		require.Equal(t, schemas.Ptr("business"), entry.BusinessUnitID)
		require.Equal(t, schemas.Ptr("Business"), entry.BusinessUnitName)
		require.Equal(t, schemas.Ptr("project"), entry.ProjectID)
		require.Equal(t, schemas.Ptr("Project"), entry.ProjectName)
		entry.CustomerID = schemas.Ptr("scoped")
		entry.UserID, entry.UserName = schemas.Ptr("scoped-user"), schemas.Ptr("Scoped user")
		entry.BusinessUnitID, entry.BusinessUnitName = schemas.Ptr("scoped-business"), schemas.Ptr("Scoped business")
		entry.ProjectID, entry.ProjectName = schemas.Ptr("scoped-project"), schemas.Ptr("Scoped project")
		entry.TeamIDsParsed, entry.TeamNamesParsed = []string{"scoped-team"}, []string{"Scoped team"}
		entry.CustomerIDsParsed, entry.CustomerNamesParsed = []string{"scoped-customer"}, []string{"Scoped customer"}
		entry.BusinessUnitIDsParsed, entry.BusinessUnitNamesParsed = []string{"scoped-business"}, []string{"Scoped business"}
		applyGrantAttribution(ctx, entry)
		require.Equal(t, "scoped", *entry.CustomerID)
		require.Equal(t, "scoped-user", *entry.UserID)
		require.Equal(t, "Scoped user", *entry.UserName)
		require.Equal(t, "scoped-business", *entry.BusinessUnitID)
		require.Equal(t, "Scoped business", *entry.BusinessUnitName)
		require.Equal(t, "scoped-project", *entry.ProjectID)
		require.Equal(t, "Scoped project", *entry.ProjectName)
		require.Equal(t, []string{"scoped-team"}, entry.TeamIDsParsed)
		require.Equal(t, []string{"Scoped team"}, entry.TeamNamesParsed)
		require.Equal(t, []string{"scoped-customer"}, entry.CustomerIDsParsed)
		require.Equal(t, []string{"Scoped customer"}, entry.CustomerNamesParsed)
		require.Equal(t, []string{"scoped-business"}, entry.BusinessUnitIDsParsed)
		require.Equal(t, []string{"Scoped business"}, entry.BusinessUnitNamesParsed)
	}
}

// TestRealtimeInputAssociationsPreserveMergedHistory rejects unsafe positional associations.
func TestRealtimeInputAssociationsPreserveMergedHistory(t *testing.T) {
	raw := `{"type":"conversation.item.done","item":{"id":"audio","type":"message","role":"user","content":[{"type":"input_audio","transcript":null}]}}`
	for _, mode := range []string{"additional content", "duplicate events", "reordered", "aligned"} {
		t.Run(mode, func(t *testing.T) {
			entry := &logstore.Log{MetadataParsed: map[string]interface{}{
				realtimeInputItemsMetadata: []realtimeInputItem{{ID: "stale", Index: 0}},
				realtimeConnectionMetadata: "stale", "realtime_input_count": 1, "keep": "metadata",
			}}
			events := raw
			switch mode {
			case "additional content":
				entry.InputHistoryParsed = []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("existing input")}}}
			case "duplicate events":
				events += "\n\n" + raw
			case "reordered":
				events = `{"type":"conversation.item.create","item":{"id":"tool","type":"function_call_output","call_id":"call","output":"tool output"}}` + "\n\n" + raw
			case "aligned":
				entry.InputHistoryParsed = extractRealtimeInputHistoryFromRawRequest(raw)
				entry.InputHistoryParsed[0].Name = schemas.Ptr("preserve this field")
			}
			backfill := extractRealtimeInputHistoryFromRawRequest(events)
			want := mergeRealtimeInputHistory(entry.InputHistoryParsed, backfill)
			applyRealtimeRawRequestBackfill(entry, events, true, false)
			require.Equal(t, want, entry.InputHistoryParsed)
			require.Equal(t, "metadata", entry.MetadataParsed["keep"])
			if mode == "aligned" {
				require.Contains(t, entry.MetadataParsed, realtimeInputItemsMetadata)
			} else {
				require.NotContains(t, entry.MetadataParsed, realtimeInputItemsMetadata)
				require.NotContains(t, entry.MetadataParsed, "realtime_input_count")
				require.NotContains(t, entry.MetadataParsed, realtimeConnectionMetadata)
			}
		})
	}
}

// TestRealtimeTranscriptSessionTimeBound excludes older rows without imposing an arbitrary session duration.
func TestRealtimeTranscriptSessionTimeBound(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	p := &LoggerPlugin{ctx: context.Background(), logger: testLogger{}, store: store, writeQueue: make(chan *writeQueueEntry, 4)}
	started := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Millisecond)
	current := realtimeTranscriptFixture(t, p, "connection", "current", false)
	current.Timestamp = started
	old := realtimeTranscriptFixture(t, p, "connection", "older", false)
	old.Timestamp = started.Add(-time.Minute)
	require.NoError(t, store.BatchCreateIfNotExists(context.Background(), []*logstore.Log{current, old}))
	p.EnqueueRealtimeTranscript("connection", started, "first", "late words", "")
	require.Empty(t, p.processBatch(context.Background(), []*writeQueueEntry{<-p.writeQueue}))
	got, err := store.FindByID(context.Background(), current.ID)
	require.NoError(t, err)
	require.Contains(t, got.InputHistory, "late words", "sessions older than 24 hours remain eligible")
	untouched, err := store.FindByID(context.Background(), old.ID)
	require.NoError(t, err)
	require.NotContains(t, untouched.InputHistory, "late words")
	p.EnqueueRealtimeTranscript("connection", time.Time{}, "first", "no bound", "")
	require.Empty(t, p.writeQueue, "a missing start must not trigger an unbounded lookup")
}

// realtimeTranscriptFixture builds a turn with two distinct audio inputs and one text input.
func realtimeTranscriptFixture(t *testing.T, p *LoggerPlugin, connectionID, requestID string, storeRaw bool) *logstore.Log {
	t.Helper()
	raw := `{"type":"conversation.item.done","item":{"id":"first","type":"message","role":"user","content":[{"type":"input_audio","transcript":null}]}}` + "\n\n" +
		`{"type":"conversation.item.done","item":{"id":"text","type":"message","role":"user","content":[{"type":"input_text","text":"existing text"}]}}` + "\n\n" +
		`{"type":"conversation.item.done","item":{"id":"second","type":"message","role":"user","content":[{"type":"input_audio","transcript":null}]}}`
	cost := 0.123
	entry := &logstore.Log{ID: requestID, Timestamp: time.Now(), Object: "realtime.turn", Status: "success", Cost: &cost,
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("answer")}}}
	// Model input already captured by the pre-hook, including distinct audio items.
	entry.InputHistoryParsed = extractRealtimeInputHistoryFromRawRequest(raw)
	applyRealtimeRawRequestBackfill(entry, raw, true, storeRaw)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRealtimeConnectionID, connectionID)
	p.prepareRealtimeTranscriptLog(ctx, entry)
	return entry
}

// TestRealtimeTranscriptCorrection looks up persisted associations after a fresh plugin instance.
func TestRealtimeTranscriptCorrection(t *testing.T) {
	for _, storeRaw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw=%t", storeRaw), func(t *testing.T) {
			store := newTestStore(t)
			t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
			p := &LoggerPlugin{ctx: context.Background(), logger: testLogger{}, store: store, writeQueue: make(chan *writeQueueEntry, 4)}
			entry := realtimeTranscriptFixture(t, p, "connection-a", "original", storeRaw)
			other := realtimeTranscriptFixture(t, p, "connection-b", "other", storeRaw)
			require.NoError(t, store.BatchCreateIfNotExists(context.Background(), []*logstore.Log{entry, other}))
			// No session or prior plugin state participates in the correction.
			p = &LoggerPlugin{ctx: context.Background(), logger: testLogger{}, store: store, writeQueue: make(chan *writeQueueEntry, 4)}
			for _, item := range []struct{ id, text string }{{"second", "second words"}, {"first", "first words"}, {"first", "first words"}} {
				raw := fmt.Sprintf(`{"type":"conversation.item.input_audio_transcription.completed","item_id":%q,"transcript":%q}`, item.id, item.text)
				p.EnqueueRealtimeTranscript("connection-a", time.Now().Add(-time.Hour), item.id, item.text, raw)
				require.Empty(t, p.processBatch(context.Background(), []*writeQueueEntry{<-p.writeQueue}))
			}
			got, err := store.FindByID(context.Background(), entry.ID)
			require.NoError(t, err)
			require.Len(t, got.InputHistoryParsed, 3)
			require.Equal(t, "first words", realtimeInputHistoryMessageContent(got.InputHistoryParsed[0]))
			require.Equal(t, "existing text", realtimeInputHistoryMessageContent(got.InputHistoryParsed[1]))
			require.Equal(t, "second words", realtimeInputHistoryMessageContent(got.InputHistoryParsed[2]))
			require.Contains(t, got.ContentSummary, "first words")
			require.Contains(t, got.ContentSummary, "second words")
			require.Contains(t, got.OutputMessage, "answer")
			require.Equal(t, 0.123, *got.Cost)
			require.Equal(t, "success", got.Status)
			if storeRaw {
				require.Contains(t, got.RawRequest, "first words")
				require.Contains(t, got.RawRequest, "second words")
			} else {
				require.Empty(t, got.RawRequest)
			}
			untouched, err := store.FindByID(context.Background(), other.ID)
			require.NoError(t, err)
			require.NotContains(t, untouched.InputHistory, "first words")
			require.NotContains(t, untouched.InputHistory, "second words")
		})
	}
}

// TestRealtimeTranscriptBeforeInsert retries using the existing queue until insertion is visible.
func TestRealtimeTranscriptBeforeInsert(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	p := &LoggerPlugin{ctx: context.Background(), logger: testLogger{}, store: store, writeQueue: make(chan *writeQueueEntry, 4)}
	entry := realtimeTranscriptFixture(t, p, "connection", "original", false)
	p.EnqueueRealtimeTranscript("connection", time.Now().Add(-time.Hour), "first", "late words", `{"type":"conversation.item.input_audio_transcription.completed","item_id":"first","transcript":"late words"}`)
	require.Empty(t, p.processBatch(context.Background(), []*writeQueueEntry{<-p.writeQueue}))
	require.Len(t, p.writeQueue, 1)
	// Same-batch inserts are persisted before transcript correction, independent of queue order.
	require.Empty(t, p.processBatch(context.Background(), []*writeQueueEntry{<-p.writeQueue, {log: entry}}))
	require.Empty(t, p.writeQueue)
	got, err := store.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Contains(t, got.InputHistory, "late words")
}

// TestRealtimeTranscriptMissingRowAndBackpressure releases unmatched events within normal retry bounds.
func TestRealtimeTranscriptMissingRowAndBackpressure(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	p := &LoggerPlugin{ctx: context.Background(), logger: testLogger{}, store: store, writeQueue: make(chan *writeQueueEntry, 1)}
	p.EnqueueRealtimeTranscript("connection", time.Now().Add(-time.Hour), "missing", "text", "")
	p.EnqueueRealtimeTranscript("connection", time.Now().Add(-time.Hour), "overflow", "text", "")
	require.Len(t, p.writeQueue, 1)
	for range batchWriteAttempts {
		require.Empty(t, p.processBatch(context.Background(), []*writeQueueEntry{<-p.writeQueue}))
	}
	require.Empty(t, p.writeQueue)
	require.EqualValues(t, 2, p.droppedRequests.Load())
	p.closed.Store(true)
	p.EnqueueRealtimeTranscript("connection", time.Now().Add(-time.Hour), "closed", "text", "")
	require.Empty(t, p.writeQueue)
}

// TestRealtimeTranscriptDisabledContent persists no association usable for correction.
func TestRealtimeTranscriptDisabledContent(t *testing.T) {
	for _, redacted := range []bool{false, true} {
		p := &LoggerPlugin{disableContentLogging: schemas.Ptr(!redacted)}
		entry := &logstore.Log{Object: "realtime.turn", MetadataParsed: map[string]interface{}{realtimeInputItemsMetadata: []realtimeInputItem{{ID: "first", Index: 0}}}}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyRealtimeConnectionID, "connection")
		if redacted {
			schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{LiteralReplacements: schemas.RedactionMapsByPhase{Input: map[string]string{"secret": "hidden"}}})
		}
		p.prepareRealtimeTranscriptLog(ctx, entry)
		require.NotContains(t, entry.MetadataParsed, realtimeInputItemsMetadata)
		require.NotContains(t, entry.MetadataParsed, realtimeConnectionMetadata)
	}
}

// BenchmarkGrantAttribution measures the added fallback for a fresh turn context.
func BenchmarkGrantAttribution(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	g := grant.New()
	g.SetIdentity(grant.NewIdentity(schemas.Credential{}, nil, &schemas.EntityRef{ID: "vk", Name: "Key"}, []schemas.EntityRef{{ID: "team"}}, []schemas.EntityRef{{ID: "customer"}}, nil, nil))
	ctx.SetGrant(g)
	b.ReportAllocs()
	for b.Loop() {
		applyGrantAttribution(ctx, &logstore.Log{})
	}
}

// BenchmarkEnqueueRealtimeTranscript measures transport handoff into the existing queue.
func BenchmarkEnqueueRealtimeTranscript(b *testing.B) {
	p := &LoggerPlugin{writeQueue: make(chan *writeQueueEntry, 1)}
	started := time.Now()
	b.ReportAllocs()
	for b.Loop() {
		p.EnqueueRealtimeTranscript("connection", started, "item", "late words", `{"type":"conversation.item.input_audio_transcription.completed","item_id":"item","transcript":"late words"}`)
		<-p.writeQueue
	}
}
