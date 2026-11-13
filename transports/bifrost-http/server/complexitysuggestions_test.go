package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Request text comes back as the router embedded it, and never for a log
// that is gone or hides its content.
func TestComplexitySuggestionTextsReadsWhatTheRouterEmbedded(t *testing.T) {
	ctx := context.Background()
	ls, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Close(ctx) })

	text := func(s string) *schemas.ChatMessageContent { return &schemas.ChatMessageContent{ContentStr: &s} }
	claude := schemas.ClaudeCLI.String()
	at := time.Now().UTC()
	for _, entry := range []*logstore.Log{
		{ID: "plain", Timestamp: at, CreatedAt: at, Object: "chat.completion", Status: "success",
			InputHistoryParsed: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: text("Explain vector clocks")},
				{Role: schemas.ChatMessageRoleUser, Content: text("Compare them to Lamport clocks")},
			}},
		{ID: "harness", Timestamp: at, CreatedAt: at, Object: "chat.completion", Status: "success", UserAgent: &claude,
			InputHistoryParsed: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: text("Fix the flaky test")},
				{Role: schemas.ChatMessageRoleUser, Content: text("<system-reminder>Use the repository instructions</system-reminder>")},
			}},
		{ID: "hidden", Timestamp: at, CreatedAt: at, Object: "chat.completion", Status: "success", ContentHidden: true,
			InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: text("secret")}}},
	} {
		require.NoError(t, ls.Create(ctx, entry))
	}

	texts, err := complexitySuggestionTexts(ls)(ctx, []string{"plain", "harness", "hidden", "deleted"}, 2)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"plain":   "Explain vector clocks\nCompare them to Lamport clocks",
		"harness": "Fix the flaky test",
	}, texts)
}

// The server hands the config store to the routing plugin as its schedule
// store through the ConfigStore interface, so a store that wraps the RDB one
// (as enterprise's does, by embedding the interface) still shares the
// schedule instead of each node running every slot.
var _ routing.ComplexitySuggestionScheduleStore = configstore.ConfigStore(nil)
