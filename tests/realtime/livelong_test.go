package realtime

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireLong skips unless the long sessions are asked for with REALTIME_LONG=1. They run against the
// real providers and take a quarter of an hour, because the limit they exercise is Google's own.
func requireLong(t *testing.T) {
	t.Helper()
	requireUpstream(t)
	if fake != nil {
		t.Skip("the long sessions run against the real providers")
	}
	if os.Getenv("REALTIME_LONG") == "" {
		t.Skip("set REALTIME_LONG=1 to run the long sessions")
	}
}

// Google ends every Live connection after about ten minutes: a goAway at nine, then a close. A native client
// that reconnects with its resumption handle keeps its conversation, and the gateway relays all of it: the
// goAway, the provider's close with its own code, and the resumed session on a new connection.
func TestLiveLong_NativeSessionContinuesAcrossTheProvidersLimit(t *testing.T) {
	for _, target := range []nativeTarget{nativeTargets[0], nativeTargets[2]} {
		t.Run(target.name, func(t *testing.T) {
			requireLong(t)
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			first := connectLive(t, nativeDial(target.route, "bearer", vk))
			first.open(target.model, map[string]any{"sessionResumption": map[string]any{}})
			first.say("Remember the code 7342.")
			stop := make(chan struct{})
			t.Cleanup(func() { close(stop) })
			go first.keepAlive(stop)

			handle := first.handle
			var goAway, ended time.Time
			deadline := time.After(13 * time.Minute)
		wait:
			for {
				select {
				case frame, open := <-first.frames:
					if !open {
						ended = time.Now()
						break wait
					}
					if frame.Get("goAway").Exists() && goAway.IsZero() {
						goAway = time.Now()
					}
					if update := frame.Get("sessionResumptionUpdate"); update.Get("resumable").Bool() && update.Get("newHandle").Str != "" {
						handle = update.Get("newHandle").Str
					}
				case <-deadline:
					t.Fatalf("the provider had not ended the connection after 13 minutes (goAway seen: %v)", !goAway.IsZero())
				}
			}

			require.False(t, goAway.IsZero(), "the provider's goAway reaches the client before the connection ends")
			assert.True(t, goAway.Before(ended), "the goAway comes before the close")
			t.Logf("goAway %s before the close; close code %d %q", ended.Sub(goAway).Round(time.Second), first.closeCode.Load(), first.closeReason())
			require.NotEmpty(t, handle, "the provider handed out a resumption handle")

			second := connectLive(t, nativeDial(target.route, "bearer", vk))
			second.open(target.model, map[string]any{"sessionResumption": map[string]any{"handle": handle}})
			reply := second.say("What code did I ask you to remember?")

			assert.Contains(t, reply.transcript(), "7342", "the resumed session remembers the first")
			waitTurnRows(t, vk, 2)
		})
	}
}

// keepAlive streams silence at real-time pace until stop closes, as an open microphone does.
func (c *liveClient) keepAlive(stop <-chan struct{}) {
	ticker := time.NewTicker(32 * time.Millisecond)
	defer ticker.Stop()
	silence := audioFrame(make([]byte, 16000*2*32/1000))
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if c.sendLiveQuiet(silence) != nil {
				return
			}
		}
	}
}
