package logstore

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCas_HiddenSmallPayloadStillPersisted(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	entry := bigChatEntry("hidden-small", strings.Repeat("large hidden context ", 20))
	entry.ContentHidden = true
	entry.Tools = `[{"name":"tiny"}]`
	require.Less(t, len(entry.Tools), cas.minFieldBytes)
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(context.Background(), entry))

	row, err := inner.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Empty(t, row.Tools, "hidden row must not expose content inline")
	var pointers int64
	require.NoError(t, cas.db.Model(&casPayload{}).Where("log_id = ? AND field = ?", entry.ID, "tools").Count(&pointers).Error)
	require.Equal(t, int64(1), pointers, "hidden nonempty payload must have a durable CAS pointer even below the normal threshold")
	served, err := cas.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Empty(t, served.Tools, "hidden payload must remain inaccessible through serving reads")
}

func TestCas_HiddenUpdatesAndTransitions(t *testing.T) {
	for _, kind := range []string{"map", "pointer", "value"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			cas, _ := newTestCas(t)
			cas.excluded["raw_request"] = struct{}{}
			defer cas.Close(ctx)
			entry := bigChatEntry("transition", strings.Repeat("original ", 40))
			entry.Tools = `[{"name":"initial"}]`
			require.NoError(t, cas.CreateIfNotExists(ctx, entry))
			var hide any = map[string]interface{}{"ContentHidden": true}
			if kind == "pointer" {
				hide = &Log{ContentHidden: true}
			}
			if kind == "value" {
				hide = Log{ContentHidden: true}
			}
			require.NoError(t, cas.Update(ctx, entry.ID, hide))
			var row Log
			require.NoError(t, cas.db.First(&row, "id = ?", entry.ID).Error)
			require.Empty(t, row.Tools, "visible to hidden must evacuate inline content")
			require.Empty(t, row.InputHistory, "visible to hidden must clear previews")
			require.Empty(t, row.ContentSummary)
			for _, tools := range []string{`[{"name":"small"}]`, `[{"name":"` + strings.Repeat("large", 100) + `"}]`} {
				var update any = map[string]interface{}{"tools": tools, "raw_request": "tiny raw"}
				if kind == "pointer" {
					update = &Log{Tools: tools, RawRequest: "tiny raw"}
				}
				if kind == "value" {
					update = Log{Tools: tools, RawRequest: "tiny raw"}
				}
				require.NoError(t, cas.Update(ctx, entry.ID, update))
				row = Log{}
				require.NoError(t, cas.db.First(&row, "id = ?", entry.ID).Error)
				require.Empty(t, row.Tools, "late hidden update must not inline payload")
				require.Empty(t, row.RawRequest)
				require.NoError(t, cas.hydrateFieldsTx(cas.db, &row, true))
				require.Equal(t, tools, row.Tools)
				require.Equal(t, "tiny raw", row.RawRequest)
				require.NoError(t, cas.Update(ctx, entry.ID, map[string]interface{}{"content_hidden": false}))
				served, err := cas.FindByID(ctx, entry.ID)
				require.NoError(t, err)
				require.Equal(t, tools, served.Tools)
				require.Equal(t, entry.InputHistory, served.InputHistory)
				require.NoError(t, cas.Update(ctx, entry.ID, map[string]interface{}{"raw_request": "visible replacement"}))
				served, err = cas.FindByID(ctx, entry.ID)
				require.NoError(t, err)
				require.Equal(t, "visible replacement", served.RawRequest, "excluded field must drop its hidden-era pointer")
				require.NoError(t, cas.Update(ctx, entry.ID, map[string]interface{}{"content_hidden": true}))
			}
		})
	}
}

func TestCas_HiddenLateSmallUpdate(t *testing.T) {
	ctx := context.Background()
	cas, _ := newTestCas(t)
	defer cas.Close(ctx)
	entry := bigChatEntry("late-hidden", strings.Repeat("context ", 40))
	entry.ContentHidden = true
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.NoError(t, cas.Update(ctx, entry.ID, map[string]interface{}{"tools": `[]`}))
	var row Log
	require.NoError(t, cas.db.First(&row, "id = ?", entry.ID).Error)
	require.Empty(t, row.Tools)
	require.NoError(t, cas.hydrateFieldsTx(cas.db, &row, true))
	require.Equal(t, `[]`, row.Tools)
}

func TestCas_HiddenUpdateClearAndRejectExpression(t *testing.T) {
	ctx := context.Background()
	cas, _ := newTestCas(t)
	defer cas.Close(ctx)
	entry := bigChatEntry("hidden-clear", strings.Repeat("context ", 40))
	entry.ContentHidden = true
	entry.Tools = `[]`
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Error(t, cas.Update(ctx, entry.ID, map[string]interface{}{"ContentHidden": "false", "tools": "invalid"}))
	require.NoError(t, cas.Update(ctx, entry.ID, map[string]interface{}{"tools": nil}))
	require.NoError(t, cas.Update(ctx, entry.ID, map[string]interface{}{"content_hidden": false}))
	row, err := cas.FindByID(ctx, entry.ID)
	require.NoError(t, err)
	require.Empty(t, row.Tools)
	require.Equal(t, entry.InputHistory, row.InputHistory)
}
