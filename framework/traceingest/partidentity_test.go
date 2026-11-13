package traceingest

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// maskOwnerHook is a part hook that replaces an address in metadata and output with replacement, the
// way a masking rule would.
func maskOwnerHook(replacement *string, seen *[]*SpanPart) PartHook {
	return func(_ context.Context, _ *tables.TableTraceProject, part *SpanPart) {
		copied := *part
		*seen = append(*seen, &copied)
		if owner, ok := part.Metadata["owner"].(string); ok {
			part.Metadata = map[string]any{"owner": strings.ReplaceAll(owner, "a@b.co", *replacement)}
		}
		if text, ok := part.Output.(string); ok {
			part.Output = strings.ReplaceAll(text, "a@b.co", *replacement)
		}
	}
}

// TestPartHooksRewriteMetadataOfHiddenContent pins that a request whose content is not stored still
// goes through the part hooks for what is kept: its input, output and expected value are cleared
// first, and its metadata is rewritten before the kept part is serialized.
func TestPartHooksRewriteMetadataOfHiddenContent(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	replacement := "[email]"
	var seen []*SpanPart
	env.service.AddPartHook(maskOwnerHook(&replacement, &seen))
	quiet := *env.key
	quiet.LogContent = false
	require.NoError(t, env.service.Ingest(ctx, &quiet, env.project, logstore.TraceSourceOTel, []*SpanPart{
		{TraceID: "t1", SpanID: "s", Name: "x", StartTime: at(0), Input: "question", Output: "mail a@b.co", Metadata: map[string]any{"owner": "a@b.co"}},
	}))
	require.Len(t, seen, 1, "the hook ran on the hidden request")
	assert.Nil(t, seen[0].Input, "content is cleared before the hook runs")
	assert.Nil(t, seen[0].Output)
	parts, err := env.store.ListTraceSpanParts(ctx, "p1", "t1", "s")
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.NotContains(t, parts[0].Payload, "a@b.co", "kept metadata is rewritten")
	assert.Contains(t, parts[0].Payload, "[email]")
}

// TestKeyedPartIdentitySurvivesHookChanges pins the keyed part id: a part retried after the hooks'
// output changed (a masking replacement was edited) keeps its id, so the raw part store holds it once.
func TestKeyedPartIdentitySurvivesHookChanges(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{PartIdentityKey: []byte("0123456789abcdef0123456789abcdef")})
	replacement := "[email]"
	var seen []*SpanPart
	env.service.AddPartHook(maskOwnerHook(&replacement, &seen))
	part := func() []*SpanPart {
		return []*SpanPart{{TraceID: "t1", SpanID: "s", Name: "x", StartTime: at(0), Output: "mail a@b.co"}}
	}
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, part()))
	replacement = "[redacted]"
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, part()))
	parts, err := env.store.ListTraceSpanParts(ctx, "p1", "t1", "s")
	require.NoError(t, err)
	assert.Len(t, parts, 1, "the retry maps onto the part already stored")
}

// TestKeyedPartIdentityIsNoContentHash pins that the keyed id is not the plain content hash of the
// part as received, which would let anyone who reads the id confirm a guess at text the hooks removed;
// and that without a key the id stays the content hash of the stored part, as before.
func TestKeyedPartIdentityIsNoContentHash(t *testing.T) {
	ctx := context.Background()
	idOf := func(cfg Config, hooked bool) string {
		env := newTestEnv(t, cfg)
		replacement := "[email]"
		var seen []*SpanPart
		if hooked {
			env.service.AddPartHook(maskOwnerHook(&replacement, &seen))
		}
		require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{
			{TraceID: "t1", SpanID: "s", Name: "x", StartTime: at(0), Output: "mail a@b.co"},
		}))
		parts, err := env.store.ListTraceSpanParts(ctx, "p1", "t1", "s")
		require.NoError(t, err)
		require.Len(t, parts, 1)
		if !hooked || len(cfg.PartIdentityKey) == 0 {
			assert.Equal(t, parts[0].ContentID(), parts[0].ID, "without a key the id is the stored part's content hash")
		}
		return parts[0].ID
	}
	unmasked := idOf(Config{}, false)
	keyed := idOf(Config{PartIdentityKey: []byte("0123456789abcdef0123456789abcdef")}, true)
	assert.NotEqual(t, unmasked, keyed, "the keyed id is not the content hash of the unmasked part")
	idOf(Config{}, true)
}
