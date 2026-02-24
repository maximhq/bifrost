package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ws "github.com/fasthttp/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dialTestWS(t *testing.T, server *httptest.Server) *ws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := ws.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	return conn
}

func startEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := ws.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			conn.WriteMessage(mt, msg)
		}
	}))
}

func TestSessionManager_CreateAndGet(t *testing.T) {
	server := startEchoServer(t)
	defer server.Close()

	sm := NewSessionManager(10)

	conn := dialTestWS(t, server)
	defer conn.Close()

	session, err := sm.Create(conn)
	require.NoError(t, err)
	require.NotNil(t, session)

	got := sm.Get(conn)
	assert.Equal(t, session, got)
	assert.Equal(t, 1, sm.Count())
}

func TestSessionManager_ConnectionLimit(t *testing.T) {
	server := startEchoServer(t)
	defer server.Close()

	sm := NewSessionManager(2)

	conn1 := dialTestWS(t, server)
	defer conn1.Close()
	conn2 := dialTestWS(t, server)
	defer conn2.Close()
	conn3 := dialTestWS(t, server)
	defer conn3.Close()

	_, err := sm.Create(conn1)
	require.NoError(t, err)
	_, err = sm.Create(conn2)
	require.NoError(t, err)

	// Third should fail
	_, err = sm.Create(conn3)
	assert.ErrorIs(t, err, ErrConnectionLimitReached)
	assert.Equal(t, 2, sm.Count())
}

func TestSessionManager_Remove(t *testing.T) {
	server := startEchoServer(t)
	defer server.Close()

	sm := NewSessionManager(10)

	conn := dialTestWS(t, server)
	defer conn.Close()

	_, err := sm.Create(conn)
	require.NoError(t, err)
	assert.Equal(t, 1, sm.Count())

	sm.Remove(conn)
	assert.Equal(t, 0, sm.Count())
	assert.Nil(t, sm.Get(conn))
}

func TestSession_LastResponseID(t *testing.T) {
	server := startEchoServer(t)
	defer server.Close()

	conn := dialTestWS(t, server)
	defer conn.Close()

	session := NewSession(conn)
	assert.Equal(t, "", session.LastResponseID())

	session.SetLastResponseID("resp_123")
	assert.Equal(t, "resp_123", session.LastResponseID())
}

func TestSessionManager_CloseAll(t *testing.T) {
	server := startEchoServer(t)
	defer server.Close()

	sm := NewSessionManager(10)

	conn1 := dialTestWS(t, server)
	defer conn1.Close()
	conn2 := dialTestWS(t, server)
	defer conn2.Close()

	sm.Create(conn1)
	sm.Create(conn2)
	assert.Equal(t, 2, sm.Count())

	sm.CloseAll()
	assert.Equal(t, 0, sm.Count())
}
