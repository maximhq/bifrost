package lib

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/valyala/fasthttp"
)

func TestAuditSnapshotAppliesDropFieldsToRawMessage(t *testing.T) {
	raw := json.RawMessage(`{"name":"a","secret":"s3cret"}`)
	got := AuditSnapshot(raw, "secret")
	if string(got) != `{"name":"a"}` {
		t.Fatalf("got %s, want secret dropped", got)
	}
}

func TestAuditSnapshotRawMessagePassthroughWithoutDropFields(t *testing.T) {
	raw := json.RawMessage(`{"name":"a"}`)
	if got := AuditSnapshot(raw); string(got) != `{"name":"a"}` {
		t.Fatalf("got %s", got)
	}
}

func TestAuditRecordSetAfterAndDiscard(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	first := RecordAuditChange(ctx, "team.created", "team", "t1", "Team", nil, map[string]any{"name": "as written"})
	second := RecordAuditChange(ctx, "team.updated", "team", "t2", "Team 2", nil, map[string]any{"name": "b"})
	first.SetAfter(map[string]any{"name": "reloaded"})
	second.Discard()
	changes := ctx.UserValue(configstore.AuditChangesUserValueKey).([]configstore.AuditChange)
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2", len(changes))
	}
	if string(changes[0].After) != `{"name":"reloaded"}` || changes[0].Discarded {
		t.Fatalf("first change = %+v", changes[0])
	}
	if !changes[1].Discarded {
		t.Fatal("second change should be discarded")
	}
}

func TestAuditRecordNilIsNoop(t *testing.T) {
	rec := RecordAuditChange(nil, "team.created", "team", "t1", "Team", nil, nil)
	encoded := false
	rec.SetAfter(marshalSpy{&encoded})
	rec.Discard()
	if encoded {
		t.Fatal("a nil record must not encode the after state")
	}
}

// marshalSpy reports whether it was encoded.
type marshalSpy struct{ encoded *bool }

func (s marshalSpy) MarshalJSON() ([]byte, error) {
	*s.encoded = true
	return []byte("{}"), nil
}

// BenchmarkRecordAuditChange measures a management write's audit record, for callers that ignore the handle.
func BenchmarkRecordAuditChange(b *testing.B) {
	before := map[string]any{"name": "before", "description": "d"}
	after := map[string]any{"name": "after", "description": "d"}
	ctx := &fasthttp.RequestCtx{}
	b.ReportAllocs()
	for b.Loop() {
		ctx.ResetUserValues()
		RecordAuditChange(ctx, "team.updated", "team", "t1", "Team", before, after)
	}
}
