package logstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCas_PhaseCCorpusRoundTripByteExact(t *testing.T) {
	cases := []struct {
		name  string
		field string
		raw   string
	}{
		{"dynamic-instruction", "input_history", `[{"role":"system","content":"v1"},{"role":"system","content":"v2 changed"},{"role":"user","content":"run"}]   `},
		{"tool-definition-modified", "tools", `[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"old":{"type":"string"}}}}},{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"new":{"type":"integer"}},"x-unknown":true}}}]`},
		{"history-rollback-fork", "input_history", `[{"role":"user","content":"root"},{"role":"assistant","content":"branch-a"},{"role":"user","content":"rollback then branch-b"}]`},
		{"message-reorder", "input_history", `[{"role":"user","content":"third"},{"role":"assistant","content":"first"},{"role":"user","content":"second"}]`},
		{"null-missing-unknown", "params", `{"present_null":null,"unknown":{"nested":true},"missing_is_not_serialized":"marker"}`},
		{"large-numbers", "params", `{"beyond_js":9007199254740993,"beyond_int64":184467440737095516160,"tiny":0.000000000000000000123}`},
		{"unicode-escapes", "tool_calls", `[{"id":"调用-1","function":{"name":"x","arguments":"{\"line\":\"a\\nb\",\"emoji\":\"\\ud83d\\ude80\"}"}}]`},
		{"malformed-fallback", "tools", `not-json:\xff\xfe::{"partial":"this deliberately malformed opaque payload is longer than the CAS field threshold"`},
		{"multimodal-reference", "input_history", `[{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"input_audio","input_audio":{"data":"AQID","format":"wav"}}]}]`},
		{"retry-failed-incomplete", "error_details", `{"retry":2,"status":"failed","stream_state":"cancelled","finish_reason":null,"partial":true}`},
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cas, _ := newTestCas(t)
			defer cas.Close(context.Background())
			ctx := context.Background()
			entry := bigChatEntry(fmt.Sprintf("phase-c-%d", index), "seed")
			require.NoError(t, entry.SerializeFields())
			updates := map[string]interface{}{tc.field: tc.raw}
			require.NoError(t, cas.CreateIfNotExists(ctx, entry))
			require.NoError(t, cas.Update(ctx, entry.ID, updates))

			found, err := cas.FindByID(ctx, entry.ID)
			require.NoError(t, err)
			payload := ExtractPayload(found)
			require.Equal(t, []byte(tc.raw), []byte(payload[tc.field]), "serialized bytes must remain identical")

			var pointers int64
			require.NoError(t, cas.db.Model(&casPayload{}).Where("log_id = ? AND field = ?", entry.ID, tc.field).Count(&pointers).Error)
			require.Equal(t, int64(1), pointers, "large corpus field must exercise CAS storage")
			requireCasGraphReachable(t, cas)
		})
	}
}
