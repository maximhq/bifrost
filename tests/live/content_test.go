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
	})
}

func TestContent_AnonymousDownloadIsRefused(t *testing.T) {
	requireFake(t)
	t.Parallel()
	status, body, _ := downloadContent(t, "live_any", nil)
	assert.Equal(t, http.StatusUnauthorized, status, "%s", body)
}
