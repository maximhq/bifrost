package handlers

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendKeepAliveIfIdle covers the keepalive decision used by handleStreamingResponse:
// it must fire on client-visible silence (not on chunk receipt), so a stream that is
// busy-but-non-emitting still gets keepalives.
func TestSendKeepAliveIfIdle(t *testing.T) {
	// Client silent longer than the interval: a keepalive is written, last-write advances.
	r := lib.NewSSEStreamReader()
	before := time.Now().Add(-2 * time.Second)
	after, alive := sendKeepAliveIfIdle(r, before, time.Second)
	require.True(t, alive)
	assert.True(t, after.After(before), "last-write should advance after emitting a keepalive")
	buf := make([]byte, 64)
	n, _ := r.Read(buf)
	assert.Equal(t, ": keep-alive\n", string(buf[:n]))
	r.Done()

	// Within the interval: no keepalive, last-write unchanged.
	r2 := lib.NewSSEStreamReader()
	lw := time.Now()
	after2, alive2 := sendKeepAliveIfIdle(r2, lw, time.Hour)
	require.True(t, alive2)
	assert.Equal(t, lw, after2)
	r2.Done()

	// Disabled (interval <= 0): never emits.
	r3 := lib.NewSSEStreamReader()
	lw3 := time.Now().Add(-time.Hour)
	after3, alive3 := sendKeepAliveIfIdle(r3, lw3, 0)
	require.True(t, alive3)
	assert.Equal(t, lw3, after3)
	r3.Done()
}
