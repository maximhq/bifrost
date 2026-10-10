package live

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Recordings: the stored session's audio, served through the gateway.

func TestContent_StoredRecordingIsRelayed(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, map[string]any{"store": true})
		c.CloseSession()

		status, body, contentType := downloadContent(t, s.ID(), vkHeaders(vk))
		assert.Equal(t, http.StatusOK, status, "%s", body)
		assert.Equal(t, "audio/wav", contentType)
		assert.Equal(t, fakeRecording(), body, "the recording is relayed byte for byte")

		// The download is a request of its own: logged like a file download, under the key that
		// made it, naming the session it belongs to, and never folded into the session's row.
		row := findContentLog(t, s.ID())
		assert.Equal(t, "success", row.Get("status").Str)
		assert.Equal(t, "openai", row.Get("provider").Str)
		assert.Equal(t, vk.ID, row.Get("virtual_key_id").Str)
		assert.Equal(t, 0.0, row.Get("cost").Float(), "nothing is billed for a download")
		assert.False(t, row.Get("live_session").Exists(), "a download is not a session")
		assert.Len(t, liveLogRows(t, s.ID()), 1, "the session still logs exactly one row")
	})
}

func TestContent_UnstoredSessionHasNoRecording(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)
		c.CloseSession()

		status, body, _ := downloadContent(t, s.ID(), vkHeaders(vk))
		assert.Equal(t, http.StatusNotFound, status, "%s", body)

		row := findContentLog(t, s.ID())
		assert.Equal(t, "error", row.Get("status").Str, "the refused download is logged too")
		assert.Equal(t, 404.0, row.Get("error_details.status_code").Float())
	})
}

func TestContent_AnonymousDownloadIsRefused(t *testing.T) {
	requireFake(t)
	t.Parallel()
	status, body, _ := downloadContent(t, "live_any", nil)
	assert.Equal(t, http.StatusUnauthorized, status, "%s", body)
}

// A long call's recording is relayed as it arrives: the first byte reaches the client while the
// provider is still sending, and the gateway holds chunks, not the recording.
func TestContent_LargeRecordingStreamsThroughTheGateway(t *testing.T) {
	requireFake(t)
	t.Parallel()
	const recordingBytes = 48 << 20
	vk := createVirtualKey(t, virtualKeySpec{})
	c, s := openFakeSession(t, wsTransport, vk, backendModel, map[string]any{"store": true})
	c.CloseSession()
	// 48 chunks at 20 ms apart: the provider takes about a second to send the whole recording.
	s.SetRecording(recordingBytes, 20*time.Millisecond)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, gatewayURL+"/v1/live/sessions/"+s.ID()+"/content", nil)
	require.NoError(t, err)
	for k, v := range vkHeaders(vk) {
		req.Header.Set(k, v)
	}
	started := time.Now()
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	firstByte := time.Since(started)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, strconv.Itoa(recordingBytes), resp.Header.Get("Content-Length"), "the provider's length is passed through")

	head := make([]byte, 4)
	_, err = io.ReadFull(resp.Body, head)
	require.NoError(t, err)
	assert.Equal(t, "RIFF", string(head))
	rest, err := io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	total := time.Since(started)

	assert.Equal(t, int64(recordingBytes), rest+4, "the whole recording arrives")
	assert.Less(t, firstByte, total/2, "the first byte arrived while the provider was still sending (first byte %s, whole recording %s)", firstByte, total)
}

func TestContent_AbandonedDownloadStopsTheUpstreamRead(t *testing.T) {
	requireFake(t)
	t.Parallel()
	const recordingBytes = 64 << 20
	vk := createVirtualKey(t, virtualKeySpec{})
	c, s := openFakeSession(t, wsTransport, vk, backendModel, map[string]any{"store": true})
	c.CloseSession()
	// 64 chunks at 50 ms apart: the provider needs about three seconds to send the whole recording.
	s.SetRecording(recordingBytes, 50*time.Millisecond)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, gatewayURL+"/v1/live/sessions/"+s.ID()+"/content", nil)
	require.NoError(t, err)
	for k, v := range vkHeaders(vk) {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, err = io.ReadFull(resp.Body, make([]byte, 1<<20))
	require.NoError(t, err)
	// The client hangs up after the first MiB.
	require.NoError(t, resp.Body.Close())

	// The gateway must stop reading from the provider too, instead of pulling the rest of the
	// recording for nobody: once the provider's writes fail, its sent count stops moving.
	time.Sleep(time.Second)
	sent := s.RecordingSent()
	time.Sleep(time.Second)
	assert.Equal(t, sent, s.RecordingSent(), "the provider is still sending a recording nobody is reading")
	assert.Less(t, sent, recordingBytes/2, "the gateway pulled most of the abandoned recording")
}
