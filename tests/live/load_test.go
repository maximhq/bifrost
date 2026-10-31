package live

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Load: many sessions at once, each billed and logged exactly once. WebRTC sessions each hold a
// peer connection with ICE and DTLS on both ends, so fewer of them run together.

func TestLoad_ConcurrentSessionsEachLogOnce(t *testing.T) {
	requireFake(t)
	for _, tc := range []struct {
		tr       transport
		sessions int
	}{{wsTransport, 50}, {webrtcTransport, 10}} {
		t.Run(string(tc.tr), func(t *testing.T) { runConcurrentSessions(t, tc.tr, tc.sessions) })
	}
}

func runConcurrentSessions(t *testing.T, tr transport, sessions int) {
	vk := createVirtualKey(t, virtualKeySpec{})

	var wg sync.WaitGroup
	ids := make([]string, sessions)
	errs := make([]error, sessions)
	for i := range errs {
		// A helper's Fatalf ends its goroutine without reaching the end; the sentinel reports it.
		errs[i] = fmt.Errorf("session %d did not finish", i)
	}
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("session %d: %v", i, r)
				}
			}()
			session := sessionFor(t, voiceModel, backendModel, nil)
			session["instructions"] = fmt.Sprintf("%s #%d", session["instructions"], i)
			c := openLive(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
			s := fake.WaitSession(t, session["instructions"].(string))
			s.EmitUsage(30)
			c.WaitFor("session.usage.updated")
			s.Play(delegation{ID: "item_1", ResponseID: fmt.Sprintf("resp_%d", i), Model: backendModel, Created: true, Input: 10, Output: 5, Items: []string{messageItem("msg_1", "ok")}})
			c.WaitFor("response.event")
			s.EmitUsage(35)
			c.WaitFor("session.usage.updated")
			c.CloseSession()
			ids[i] = s.ID()
			errs[i] = nil
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	for i, id := range ids {
		row := findLiveLog(t, id)
		assert.Equal(t, 35.0, row.Get("token_usage.audio_seconds").Float(), "session %d", i)
		assert.Len(t, row.Get("live_session.delegations").Array(), 1, "session %d", i)
		assert.Len(t, liveLogRows(t, id), 1, "session %d logged once", i)
	}
}
