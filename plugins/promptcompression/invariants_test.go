package promptcompression

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// assertChatInvariants checks the guarantees every compression must keep: no
// message added, removed or reordered, roles and tool-call IDs unchanged, and
// the first protected messages byte-identical (cache prefix).
func assertChatInvariants(t *testing.T, before, after []schemas.ChatMessage, protected int) {
	t.Helper()
	require.Len(t, after, len(before))
	for i := range before {
		require.Equal(t, before[i].Role, after[i].Role, "role at %d", i)

		var bid, aid *string
		if before[i].ChatToolMessage != nil {
			bid = before[i].ChatToolMessage.ToolCallID
		}
		if after[i].ChatToolMessage != nil {
			aid = after[i].ChatToolMessage.ToolCallID
		}
		require.Equal(t, bid, aid, "tool_call_id at %d", i)

		require.Equal(t, toolCallIDs(before[i]), toolCallIDs(after[i]), "assistant tool calls at %d", i)

		if i < protected {
			bj, err := json.Marshal(before[i])
			require.NoError(t, err)
			aj, err := json.Marshal(after[i])
			require.NoError(t, err)
			require.Equal(t, string(bj), string(aj), "protected message %d changed", i)
		}
	}
}

func toolCallIDs(m schemas.ChatMessage) []string {
	if m.ChatAssistantMessage == nil {
		return nil
	}
	var ids []string
	for _, tc := range m.ChatAssistantMessage.ToolCalls {
		if tc.ID != nil {
			ids = append(ids, *tc.ID)
		}
	}
	return ids
}

// assertResponsesInvariants is the Responses counterpart of
// assertChatInvariants: same item count, and Type, Role and CallID unchanged at
// every index (nil versus non-nil counts as a difference), with the protected
// prefix byte-identical.
func assertResponsesInvariants(t *testing.T, before, after []schemas.ResponsesMessage, protected int) {
	t.Helper()
	require.Len(t, after, len(before))
	for i := range before {
		require.Equal(t, before[i].Type, after[i].Type, "type at %d", i)
		require.Equal(t, before[i].Role, after[i].Role, "role at %d", i)
		require.Equal(t, respCallID(before[i]), respCallID(after[i]), "call_id at %d", i)

		if i < protected {
			bj, err := json.Marshal(before[i])
			require.NoError(t, err)
			aj, err := json.Marshal(after[i])
			require.NoError(t, err)
			require.Equal(t, string(bj), string(aj), "protected item %d changed", i)
		}
	}
}

func respCallID(m schemas.ResponsesMessage) *string {
	if m.ResponsesToolMessage == nil {
		return nil
	}
	return m.ResponsesToolMessage.CallID
}
