package live

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
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
