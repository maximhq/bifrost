package lib

import (
	"encoding/json"
	"testing"
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
