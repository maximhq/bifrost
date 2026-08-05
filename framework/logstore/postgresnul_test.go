package logstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const postgresNULTestSchema = "logstore_nul_test"

func setupPostgresNULTestStore(t *testing.T) *RDBLogStore {
	t.Helper()

	adminDB := trySetupPostgresDB(t)
	if adminDB == nil {
		t.Skip("Postgres not available, skipping NUL persistence test")
	}
	recreateSchema := func() {
		require.NoError(t, adminDB.Exec("DROP SCHEMA IF EXISTS "+postgresNULTestSchema+" CASCADE").Error)
		require.NoError(t, adminDB.Exec("CREATE SCHEMA "+postgresNULTestSchema).Error)
	}
	recreateSchema()

	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+postgresNULTestSchema, 1)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))

	t.Cleanup(func() {
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			require.NoError(t, sqlDB.Close())
		}
		require.NoError(t, adminDB.Exec("DROP SCHEMA IF EXISTS "+postgresNULTestSchema+" CASCADE").Error)
	})

	return &RDBLogStore{db: db, logger: testLogger{}}
}

func decodedAnthropicOutput(t *testing.T) *schemas.ChatMessage {
	t.Helper()

	var response struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"content":[{"type":"text","text":"before\u0000after"}]}`), &response))
	require.Len(t, response.Content, 1)
	require.Contains(t, response.Content[0].Text, "\x00")

	return &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{
			ContentStr: &response.Content[0].Text,
		},
	}
}

func basePostgresNULTestLog(id string) *Log {
	return &Log{
		ID:        id,
		Timestamp: time.Now().UTC(),
		Object:    "chat.completion",
		Provider:  "anthropic",
		Model:     "claude-test",
		Status:    "success",
	}
}

func TestPostgresCreateSanitizesNULContentSummary(t *testing.T) {
	store := setupPostgresNULTestStore(t)
	entry := basePostgresNULTestLog("nul-create")
	entry.OutputMessageParsed = decodedAnthropicOutput(t)

	require.NoError(t, store.Create(context.Background(), entry))
	require.Contains(t, *entry.OutputMessageParsed.Content.ContentStr, "\x00", "persistence must not mutate the decoded response")

	persisted, err := store.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Equal(t, "beforeafter", persisted.ContentSummary)
	require.Contains(t, *persisted.OutputMessageParsed.Content.ContentStr, "\x00", "JSON payload must retain the decoded response")
}

func TestPostgresUpdateSanitizesNULContentSummary(t *testing.T) {
	store := setupPostgresNULTestStore(t)
	entry := basePostgresNULTestLog("nul-update")
	require.NoError(t, store.Create(context.Background(), entry))

	output := decodedAnthropicOutput(t)
	serialized := &Log{OutputMessageParsed: output}
	require.NoError(t, serialized.SerializeFields())
	require.Contains(t, serialized.ContentSummary, "\x00")
	updates := map[string]interface{}{
		"output_message":  serialized.OutputMessage,
		"content_summary": serialized.ContentSummary,
	}
	require.NoError(t, store.Update(context.Background(), entry.ID, updates))
	require.Contains(t, updates["content_summary"].(string), "\x00", "persistence must not mutate caller-owned update data")
	require.Contains(t, *output.Content.ContentStr, "\x00", "persistence must not mutate the decoded response")

	persisted, err := store.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Equal(t, "beforeafter", persisted.ContentSummary)
	require.Contains(t, *persisted.OutputMessageParsed.Content.ContentStr, "\x00", "JSON payload must retain the decoded response")

	pointerValue := "pointer\x00value"
	pointerUpdate := &Log{VirtualKeyName: &pointerValue}
	require.NoError(t, store.Update(context.Background(), entry.ID, pointerUpdate))
	require.Equal(t, "pointer\x00value", pointerValue, "persistence must not mutate caller-owned pointed-to strings")
	require.Equal(t, "pointervalue", *pointerUpdate.VirtualKeyName)

	persisted, err = store.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Equal(t, "pointervalue", *persisted.VirtualKeyName)
}
