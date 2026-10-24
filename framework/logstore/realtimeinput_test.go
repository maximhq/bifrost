package logstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestRealtimeInputLookup isolates exact provider item and connection IDs in persisted metadata.
func TestRealtimeInputLookup(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	t.Cleanup(func() { require.NoError(t, hybrid.Close(context.Background())) })
	ctx := context.Background()
	itemID := `item_'"[]`
	for _, test := range []struct{ id, connection, item string }{{"match", "one", itemID}, {"other-connection", "two", itemID}, {"prefix", "one", itemID + "suffix"}} {
		require.NoError(t, inner.Create(ctx, &Log{ID: test.id, Timestamp: time.Now(), Object: "realtime.turn", MetadataParsed: map[string]interface{}{
			"realtime_connection_id": test.connection, "realtime_input_items": []map[string]interface{}{{"id": test.item, "index": 0}},
		}}))
	}
	result, err := inner.SearchLogs(ctx, SearchFilters{RealtimeInputItemID: itemID, MetadataFilters: map[string]string{"realtime_connection_id": "one"}}, PaginationOptions{Limit: 2, SkipCount: true})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "match", result.Logs[0].ID)
}

// TestHybridRealtimeInputCorrectionsPreserveEarlierUpdates exercises stale snapshots queued together.
// Each correction must use the latest object on its ordered upload shard.
func TestHybridRealtimeInputCorrectionsPreserveEarlierUpdates(t *testing.T) {
	for _, together := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate calls", true: "one batch"}[together], func(t *testing.T) {
			hybrid, inner, objects := newTestHybrid(t)
			t.Cleanup(func() { require.NoError(t, hybrid.Close(context.Background())) })
			ctx := context.Background()
			ts := time.Now().UTC()
			message := func(text string) schemas.ChatMessage {
				return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
			}
			require.NoError(t, hybrid.CreateIfNotExists(ctx, &Log{ID: "audio", Timestamp: ts, Object: "realtime.turn", Status: "success", Cost: schemas.Ptr(0.25), InputHistoryParsed: []schemas.ChatMessage{message("missing"), message("missing")}, OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("answer")}}}))
			waitForOffload(t, inner, "audio")
			first := &Log{ID: "audio", InputHistoryParsed: []schemas.ChatMessage{message("first words"), message("missing")}, RealtimeInputCorrection: &RealtimeInputCorrection{Index: 0, ItemID: "first", Transcript: "first words"}}
			// Deliberately stale: this snapshot does not contain the first correction.
			second := &Log{ID: "audio", InputHistoryParsed: []schemas.ChatMessage{message("missing"), message("second words")}, RealtimeInputCorrection: &RealtimeInputCorrection{Index: 1, ItemID: "second", Transcript: "second words"}}
			if together {
				require.NoError(t, hybrid.BatchUpdate(ctx, []*Log{first, second}))
			} else {
				require.NoError(t, hybrid.BatchUpdate(ctx, []*Log{first}))
				require.NoError(t, hybrid.BatchUpdate(ctx, []*Log{second}))
			}
			waitForUploads(t, func() bool {
				data, err := objects.Get(ctx, ObjectKey("test", ts, "audio"))
				if err != nil {
					return false
				}
				var payload map[string]string
				if sonic.Unmarshal(data, &payload) != nil {
					return false
				}
				var inputs []schemas.ChatMessage
				if sonic.UnmarshalString(payload["input_history"], &inputs) != nil || len(inputs) != 2 {
					return false
				}
				return inputs[0].Content != nil && inputs[0].Content.ContentStr != nil && *inputs[0].Content.ContentStr == "first words" && inputs[1].Content != nil && inputs[1].Content.ContentStr != nil && *inputs[1].Content.ContentStr == "second words"
			})
			waitForUploads(t, func() bool {
				row, err := inner.FindByID(ctx, "audio")
				return err == nil && strings.Contains(row.ContentSummary, "first words") && strings.Contains(row.ContentSummary, "second words")
			})
			got, err := hybrid.FindByID(ctx, "audio")
			require.NoError(t, err)
			require.Contains(t, got.InputHistory, "first words")
			require.Contains(t, got.InputHistory, "second words")
			require.Contains(t, got.OutputMessage, "answer")
			require.Equal(t, 0.25, *got.Cost)
			data, err := objects.Get(ctx, ObjectKey("test", ts, "audio"))
			require.NoError(t, err)
			require.NotContains(t, string(data), realtimeInputCorrectionField)
		})
	}
}

// TestRealtimeInputMergePreservesRawItems patches only the targeted event in the latest object.
func TestRealtimeInputMergePreservesRawItems(t *testing.T) {
	first := `{"type":"conversation.item.done","item":{"id":"first"}}`
	second := `{"type":"conversation.item.done","item":{"id":"second"}}`
	firstDone := `{"type":"conversation.item.input_audio_transcription.completed","item_id":"first","transcript":"first words"}`
	secondDone := `{"type":"conversation.item.input_audio_transcription.completed","item_id":"second","transcript":"second words"}`
	base := map[string]string{"raw_request": first + "\n\n" + second}
	// input_history is excluded from offloading in this case and is corrected in the DB.
	for _, correction := range []RealtimeInputCorrection{{Index: 0, ItemID: "first", Transcript: "first words", Raw: firstDone}, {Index: 1, ItemID: "second", Transcript: "second words", Raw: secondDone}} {
		encoded, err := sonic.MarshalString([]RealtimeInputCorrection{correction})
		require.NoError(t, err)
		overlay := map[string]string{realtimeInputCorrectionField: encoded}
		require.NoError(t, mergeRealtimeInputCorrection(base, overlay))
		require.NotContains(t, overlay, realtimeInputCorrectionField)
		for key, value := range overlay {
			base[key] = value
		}
	}
	require.Equal(t, firstDone+"\n\n"+secondDone, base["raw_request"])
}
