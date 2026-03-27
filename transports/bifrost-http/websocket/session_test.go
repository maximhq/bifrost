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

	session.SetRealtimeInputText("hello")
	if got := session.ConsumeRealtimeInputText(); got != "hello" {
		t.Fatalf("ConsumeRealtimeInputText() = %q, want %q", got, "hello")
	}
	session.SetRealtimeInputRaw(`{"type":"conversation.item.create"}`)
	if got := session.ConsumeRealtimeInputRaw(); got != `{"type":"conversation.item.create"}` {
		t.Fatalf("ConsumeRealtimeInputRaw() = %q, want raw input", got)
	}

	session.AddRealtimeToolOutput("tool result", `{"type":"conversation.item.create","item":{"type":"function_call_output"}}`)
	toolOutputs := session.ConsumeRealtimeToolOutputs()
	if len(toolOutputs) != 1 {
		t.Fatalf("len(ConsumeRealtimeToolOutputs()) = %d, want 1", len(toolOutputs))
	}
	if toolOutputs[0].Summary != "tool result" {
		t.Fatalf("tool summary = %q, want %q", toolOutputs[0].Summary, "tool result")
	}
	if got := session.ConsumeRealtimeToolOutputs(); len(got) != 0 {
		t.Fatalf("len(ConsumeRealtimeToolOutputs()) after clear = %d, want 0", len(got))
	}
}

func newTestConn() *ws.Conn {
	return &ws.Conn{}
}
