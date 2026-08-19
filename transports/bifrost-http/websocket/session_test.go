package websocket

import (
	"testing"

	ws "github.com/fasthttp/websocket"
)

func TestSessionManagerCreateAndGet(t *testing.T) {
	manager := NewSessionManager(2)
	conn := newTestConn()

	session, err := manager.Create(conn)
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}
	if session == nil {
		t.Fatal("Create() returned nil session")
	}
	if got := manager.Get(conn); got != session {
		t.Fatal("Get() did not return the created session")
	}
	if got := manager.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1", got)
	}
}

func TestSessionManagerConnectionLimit(t *testing.T) {
	manager := NewSessionManager(1)

	if _, err := manager.Create(newTestConn()); err != nil {
		t.Fatalf("first Create() unexpected error: %v", err)
	}
	if _, err := manager.Create(newTestConn()); err != ErrConnectionLimitReached {
		t.Fatalf("second Create() error = %v, want %v", err, ErrConnectionLimitReached)
	}
}

func TestSessionManagerRemove(t *testing.T) {
	manager := NewSessionManager(2)
	conn := newTestConn()

	session, err := manager.Create(conn)
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}

	manager.Remove(conn)

	if got := manager.Get(conn); got != nil {
		t.Fatal("Get() should return nil after Remove()")
	}
	if got := manager.Count(); got != 0 {
		t.Fatalf("Count() = %d, want 0", got)
	}
	if !session.closed {
		t.Fatal("expected removed session to be closed")
	}
}

func TestSessionLastResponseID(t *testing.T) {
	session := NewSession(newTestConn())
	session.SetLastResponseID("resp-123")

	if got := session.LastResponseID(); got != "resp-123" {
		t.Fatalf("LastResponseID() = %q, want %q", got, "resp-123")
	}
}

func TestSessionManagerCloseAll(t *testing.T) {
	manager := NewSessionManager(4)
	connA := newTestConn()
	connB := newTestConn()

	sessionA, err := manager.Create(connA)
	if err != nil {
		t.Fatalf("Create(connA) unexpected error: %v", err)
	}
	sessionB, err := manager.Create(connB)
	if err != nil {
		t.Fatalf("Create(connB) unexpected error: %v", err)
	}

	manager.CloseAll()

	if got := manager.Count(); got != 0 {
		t.Fatalf("Count() = %d, want 0", got)
	}
	if !sessionA.closed || !sessionB.closed {
		t.Fatal("expected all sessions to be closed")
	}
}

func TestSessionRealtimeState(t *testing.T) {
	session := NewSession(newTestConn())
	if session.ID() == "" {
		t.Fatal("expected session ID to be populated")
	}

	session.SetProviderSessionID("provider-session-1")
	if got := session.ProviderSessionID(); got != "provider-session-1" {
		t.Fatalf("ProviderSessionID() = %q, want %q", got, "provider-session-1")
	}

	session.AppendRealtimeOutputText("hello")
	session.AppendRealtimeOutputText(" world")
	if got := session.ConsumeRealtimeOutputText(); got != "hello world" {
		t.Fatalf("ConsumeRealtimeOutputText() = %q, want %q", got, "hello world")
	}
	if got := session.ConsumeRealtimeOutputText(); got != "" {
		t.Fatalf("ConsumeRealtimeOutputText() after clear = %q, want empty string", got)
	}

	session.AddRealtimeInput("hello", `{"type":"conversation.item.create","item":{"role":"user"}}`)
	session.AddRealtimeToolOutput("tool result", `{"type":"conversation.item.create","item":{"type":"function_call_output"}}`)
	turnInputs := session.ConsumeRealtimeTurnInputs()
	if len(turnInputs) != 2 {
		t.Fatalf("len(ConsumeRealtimeTurnInputs()) = %d, want 2", len(turnInputs))
	}
	if turnInputs[0].Role != "user" || turnInputs[0].Summary != "hello" {
		t.Fatalf("turnInputs[0] = %+v, want user hello", turnInputs[0])
	}
	if turnInputs[1].Role != "tool" || turnInputs[1].Summary != "tool result" {
		t.Fatalf("turnInputs[1] = %+v, want tool result", turnInputs[1])
	}
	if got := session.ConsumeRealtimeTurnInputs(); len(got) != 0 {
		t.Fatalf("len(ConsumeRealtimeTurnInputs()) after clear = %d, want 0", len(got))
	}
}

func TestSessionRecordRealtimeInputUpdatesPendingItemAndIgnoresConsumedLateUpdate(t *testing.T) {
	session := NewSession(newTestConn())

	session.RecordRealtimeInput("item_1", "[Audio transcription unavailable]", `{"type":"conversation.item.done","item":{"id":"item_1"}}`)
	session.RecordRealtimeInput("item_1", "Hello.", `{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"Hello."}`)

	turnInputs := session.ConsumeRealtimeTurnInputs()
	if len(turnInputs) != 1 {
		t.Fatalf("len(ConsumeRealtimeTurnInputs()) = %d, want 1", len(turnInputs))
	}
	if turnInputs[0].ItemID != "item_1" {
		t.Fatalf("turnInputs[0].ItemID = %q, want %q", turnInputs[0].ItemID, "item_1")
	}
	if turnInputs[0].Summary != "Hello." {
		t.Fatalf("turnInputs[0].Summary = %q, want %q", turnInputs[0].Summary, "Hello.")
	}

	session.RecordRealtimeInput("item_1", "Hello.", `{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"Hello."}`)
	if got := session.ConsumeRealtimeTurnInputs(); len(got) != 0 {
		t.Fatalf("len(ConsumeRealtimeTurnInputs()) after late consumed update = %d, want 0", len(got))
	}
}

func newTestConn() *ws.Conn {
	return &ws.Conn{}
}
