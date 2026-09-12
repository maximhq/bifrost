package logstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- unit: byte-exact splitting and reconstruction ---

func TestSplitJSONArrayByteExact(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"empty array", `[]`, true},
		{"array with space", `[ ]`, true},
		{"numbers", `[1,2,3]`, true},
		{"numbers spaced", `[1, 2, 3]`, true},
		{"floats and scalars", `[1.5e10, true, false, null, -0]`, true},
		{"strings", `["a","b","c"]`, true},
		{"string with comma and bracket", `["a,b]c","d"]`, true},
		{"string with escape", `["a\"b", "c\\d"]`, true},
		{"string with unicode escape", `["\u00e9\\nok"]`, true},
		{"objects", `[{"a":1},{"b":[1,2,{"c":"}"}]}]`, true},
		{"nested arrays", `[[1,2],[3,[4]]]`, true},
		{"newlines and tabs", "[\n\t{\"a\":1},\n\t{\"b\":2}\n]", true},
		{"mixed depth strings", `[{"s":"[not,a,bracket]"}, "x"]`, true},
		{"trailing garbage", `[1,2] x`, false},
		{"truncated", `[1,2`, false},
		{"unterminated string", `["abc`, false},
		{"unbalanced", `[{"a":1}`, false},
		{"not an array", `{"a":1}`, false},
		{"scalar top level", `"hello"`, false},
		{"empty string", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokens, ok := splitJSONArray([]byte(tc.in))
			require.Equal(t, tc.ok, ok, "ok flag for %q", tc.in)
			if !ok {
				return
			}
			var rebuilt strings.Builder
			for _, tok := range tokens {
				rebuilt.Write(tok)
			}
			assert.Equal(t, tc.in, rebuilt.String(), "byte-exact concatenation")
		})
	}
}

func TestManifestRoundTripByteIdentical(t *testing.T) {
	big := strings.Repeat("x", 400)
	payloads := [][]byte{
		[]byte(`[]`),
		[]byte(`[{"role":"user","content":"` + big + `"},{"role":"assistant","content":"short"}, {"role":"user","content":"` + big + `"}]`),
		[]byte(`[123, ` + big + `, true, null]`),
		[]byte(`["` + big + `","` + big + `"]`), // identical elements -> dedup within one field
		{0x5b, 0x22, 0xff, 0xfe, 0x22, 0x5d},   // ["\xff\xfe"] invalid UTF-8 inline -> forced blob
		[]byte(`not json at all ` + big),       // fallback: whole field as one blob
	}
	for i, raw := range payloads {
		_, blobs, manifestBytes, err := buildManifest(raw, 64)
		require.NoError(t, err, "payload %d", i)

		store := make(map[string]casBlob)
		for _, b := range blobs {
			store[b.Hash] = b
		}
		manifestHash := casHash(casManifestDomain, manifestBytes)
		store[manifestHash] = casBlob{Hash: manifestHash, Codec: casCodecZstd, OrigLen: int64(len(manifestBytes)), Data: casEncoder.EncodeAll(manifestBytes, nil)}

		lookup := func(hash string) ([]byte, error) {
			b, ok := store[hash]
			if !ok {
				t.Fatalf("missing blob %s", hash)
			}
			return casDecodeBlob(b)
		}
		out, err := casReconstruct(manifestBytes, lookup)
		require.NoError(t, err, "payload %d", i)
		assert.Equal(t, string(raw), string(out), "byte-identical reconstruction for payload %d", i)
	}
}

// --- integration: CasLogStore over SQLite ---

func newTestCas(t *testing.T) (*CasLogStore, *RDBLogStore) {
	t.Helper()
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "cas.db")}, hybridTestLogger{})
	require.NoError(t, err)
	cfg := &ContentAddressedConfig{Enabled: true, MinFieldBytes: 64, MinChunkBytes: 32}
	cas, err := newCasLogStore(ctx, inner, cfg, hybridTestLogger{})
	require.NoError(t, err)
	return cas, inner
}

func bigChatEntry(id string, msgs ...string) *Log {
	history := make([]schemas.ChatMessage, 0, len(msgs))
	for i, m := range msgs {
		role := schemas.ChatMessageRoleUser
		if i%2 == 1 {
			role = schemas.ChatMessageRoleAssistant
		}
		s := m
		history = append(history, schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &s}})
	}
	return &Log{
		ID:                  id,
		Timestamp:           time.Now().UTC(),
		Provider:            "openai",
		Model:               "gpt-test",
		Status:              "success",
		Object:              "chat.completion",
		InputHistoryParsed:  history,
		OutputMessageParsed: &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: strPtr("ok")}},
	}
}

func casBlobCount(t *testing.T, cas *CasLogStore) int64 {
	t.Helper()
	var n int64
	require.NoError(t, cas.db.Model(&casBlob{}).Count(&n).Error)
	return n
}

func TestCas_CreateAndHydrateByteIdentical(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("cas-1",
		strings.Repeat("system prompt fixed instructions ", 8),
		strings.Repeat("tool definitions ", 20),
		"hello",
	)
	require.NoError(t, entry.SerializeFields())
	originalInput := entry.InputHistory
	require.NotEmpty(t, originalInput)

	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	// Row must not contain the full payload.
	row, err := inner.FindByID(ctx, "cas-1")
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	assert.Less(t, len(row.InputHistory), len(originalInput), "row keeps only the preview")
	assert.Equal(t, entry.OutputMessage, row.OutputMessage, "small output stays in the row")

	// Hydrated read must be byte-identical.
	found, err := cas.FindByID(ctx, "cas-1")
	require.NoError(t, err)
	assert.Equal(t, originalInput, found.InputHistory, "byte-identical input reconstruction")
	assert.Contains(t, found.ContentSummary, "hello")
}

func TestCas_SmallFieldsStayInRow(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("cas-small", "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	row, err := inner.FindByID(ctx, "cas-small")
	require.NoError(t, err)
	assert.False(t, row.HasObject, "nothing eligible for CAS")
	assert.Equal(t, entry.InputHistory, row.InputHistory)
	var n int64
	require.NoError(t, cas.db.Model(&casPayload{}).Count(&n).Error)
	assert.Zero(t, n, "no CAS pointers expected")
}

func TestCas_DedupAcrossLogs(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	sharedMsgs := []string{
		strings.Repeat("shared system prompt ", 10),
		strings.Repeat("shared tool definitions ", 10),
	}
	e1 := bigChatEntry("dedup-1", append(append([]string{}, sharedMsgs...), "turn 1")...)
	require.NoError(t, e1.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e1))
	blobsAfterFirst := casBlobCount(t, cas)

	// Second log re-sends the same history plus one new message.
	e2 := bigChatEntry("dedup-2", append(append([]string{}, sharedMsgs...), "turn 2")...)
	require.NoError(t, e2.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e2))
	blobsAfterSecond := casBlobCount(t, cas)

	// A full second copy would at least double the blob count; reuse means
	// growth is only the new element's segment + the new manifest.
	assert.Less(t, blobsAfterSecond-blobsAfterFirst, blobsAfterFirst,
		"second log must reuse shared segments (grew by %d of %d)", blobsAfterSecond-blobsAfterFirst, blobsAfterFirst)

	for _, id := range []string{"dedup-1", "dedup-2"} {
		found, err := cas.FindByID(ctx, id)
		require.NoError(t, err)
		e := e1
		if id == "dedup-2" {
			e = e2
		}
		assert.Equal(t, e.InputHistory, found.InputHistory, "byte-identical for %s", id)
	}
}

func TestCas_UpdateMapInterception(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("upd-1", strings.Repeat("history ", 20))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	bigOutput, err := sonic.Marshal([]schemas.ChatMessage{
		{Role: "assistant", Content: &schemas.ChatMessageContent{ContentStr: strPtr(strings.Repeat("answer ", 60))}},
	})
	require.NoError(t, err)

	require.NoError(t, cas.Update(ctx, "upd-1", map[string]interface{}{
		"output_message": string(bigOutput),
		"status":         "success",
	}))

	row, err := inner.FindByID(ctx, "upd-1")
	require.NoError(t, err)
	assert.Empty(t, row.OutputMessage, "output must not live in the row")

	found, err := cas.FindByID(ctx, "upd-1")
	require.NoError(t, err)
	assert.Equal(t, string(bigOutput), found.OutputMessage, "byte-identical output after update-map")
	assert.Equal(t, entry.InputHistory, found.InputHistory, "input unchanged by update")
}

func TestCas_DeleteReclaimsBlobs(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	e1 := bigChatEntry("del-1", strings.Repeat("unique one ", 30))
	require.NoError(t, e1.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e1))
	require.Positive(t, casBlobCount(t, cas))

	require.NoError(t, cas.DeleteLog(ctx, "del-1"))

	var blobs, payloads, refs int64
	require.NoError(t, cas.db.Model(&casBlob{}).Count(&blobs).Error)
	require.NoError(t, cas.db.Model(&casPayload{}).Count(&payloads).Error)
	require.NoError(t, cas.db.Model(&casRef{}).Count(&refs).Error)
	assert.Zero(t, payloads, "payload pointers reclaimed")
	assert.Zero(t, refs, "refs reclaimed")
	assert.Zero(t, blobs, "unreferenced blobs reclaimed")

	_, err := cas.FindByID(ctx, "del-1")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCas_DeleteKeepsSharedBlobs(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	shared := strings.Repeat("shared content ", 20)
	e1 := bigChatEntry("keep-1", shared)
	e2 := bigChatEntry("keep-2", shared)
	require.NoError(t, e1.SerializeFields())
	require.NoError(t, e2.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e1))
	require.NoError(t, cas.CreateIfNotExists(ctx, e2))

	require.NoError(t, cas.DeleteLog(ctx, "keep-1"))

	found, err := cas.FindByID(ctx, "keep-2")
	require.NoError(t, err)
	assert.Equal(t, e2.InputHistory, found.InputHistory, "surviving log still hydrates from shared blobs")
}

func TestCas_HiddenContentNotServed(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("hidden-1", strings.Repeat("secret ", 40))
	entry.ContentHidden = true
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	row, err := inner.FindByID(ctx, "hidden-1")
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	assert.Empty(t, row.InputHistory, "hidden row keeps no content")
	assert.Empty(t, row.ContentSummary, "hidden row keeps no summary")

	found, err := cas.FindByID(ctx, "hidden-1")
	require.NoError(t, err)
	assert.Empty(t, found.InputHistory, "hidden content must never be served back")
}

func TestCas_PricingMetadataStaysInRow(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("price-1", strings.Repeat("context ", 40))
	entry.TokenUsage = `{"prompt_tokens":100,"completion_tokens":20}`
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	row, err := inner.FindByID(ctx, "price-1")
	require.NoError(t, err)
	assert.Equal(t, `{"prompt_tokens":100,"completion_tokens":20}`, row.TokenUsage, "token_usage must stay DB-resident")

	var n int64
	require.NoError(t, cas.db.Model(&casPayload{}).Where("field = ?", "token_usage").Count(&n).Error)
	assert.Zero(t, n, "token_usage must never be CAS'd")
}

func TestCas_FindAllProjection(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("proj-1", strings.Repeat("context ", 40))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	logs, err := cas.FindAll(ctx, map[string]any{"id": "proj-1"})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, entry.InputHistory, logs[0].InputHistory, "no projection -> full hydration")

	logs, err = cas.FindAll(ctx, map[string]any{"id": "proj-1"}, "id", "status")
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Empty(t, logs[0].InputHistory, "projection without payload fields -> no hydration")
}
