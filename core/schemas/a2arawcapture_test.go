package schemas

import "testing"

// TestRawCaptureBoundsAndReset verifies oversized bodies are omitted and retries start fresh.
func TestRawCaptureBoundsAndReset(t *testing.T) {
	c := &A2ARawCapture{}
	c.Append([]byte("first"), true)
	c.Append(make([]byte, 1<<20), false)
	c.Append([]byte("overflow"), false)
	req, resp := c.Snapshot()
	if req == nil || resp != nil {
		t.Fatal("oversized response was not dropped independently")
	}
	c.Reset()
	req, resp = c.Snapshot()
	if req != nil || resp != nil {
		t.Fatal("retry retained previous attempt bytes")
	}
	c.Append([]byte{0xff, 0x00}, false)
	_, resp = c.Snapshot()
	if resp == nil || *resp != "/wA=" {
		t.Fatal("binary bytes were not preserved")
	}
}
