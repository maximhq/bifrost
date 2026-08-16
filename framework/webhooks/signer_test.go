package webhooks

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSignReferenceVector pins the canonical example from the Standard
// Webhooks specification, so any drift from the reference implementation
// fails loudly.
func TestSignReferenceVector(t *testing.T) {
	signature, err := Sign(
		"whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw",
		"msg_p5jXN8AQM9LWM0D4loKWxJek",
		time.Unix(1614265330, 0),
		[]byte(`{"test": 2432232314}`),
	)
	require.NoError(t, err)
	assert.Equal(t, "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=", signature)
}

func TestSignSecretHandling(t *testing.T) {
	body := []byte(`{}`)
	ts := time.Unix(1700000000, 0)

	_, err := Sign("", "wh-1", ts, body)
	assert.Error(t, err, "empty secret must be rejected")

	_, err = Sign("whsec_not-base64!!!", "wh-1", ts, body)
	assert.Error(t, err, "non-base64 secret must be rejected")

	// The decoded key must be 24–64 bytes per the Standard Webhooks spec.
	_, err = Sign("whsec_", "wh-1", ts, body)
	assert.Error(t, err, "bare whsec_ (empty key) must be rejected")
	_, err = Sign("whsec_"+base64.StdEncoding.EncodeToString(make([]byte, 23)), "wh-1", ts, body)
	assert.Error(t, err, "sub-24-byte key must be rejected")
	_, err = Sign("whsec_"+base64.StdEncoding.EncodeToString(make([]byte, 65)), "wh-1", ts, body)
	assert.Error(t, err, "over-64-byte key must be rejected")
	_, err = Sign("whsec_"+base64.StdEncoding.EncodeToString(make([]byte, 24)), "wh-1", ts, body)
	assert.NoError(t, err, "24-byte key is the minimum valid length")

	// The prefix is optional on input; the same key must produce the same
	// signature either way.
	withPrefix, err := Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "wh-1", ts, body)
	require.NoError(t, err)
	withoutPrefix, err := Sign("MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "wh-1", ts, body)
	require.NoError(t, err)
	assert.Equal(t, withPrefix, withoutPrefix)
}

func TestSignVariesWithInputs(t *testing.T) {
	ts := time.Unix(1700000000, 0)
	base, err := Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "wh-1", ts, []byte(`{"a":1}`))
	require.NoError(t, err)

	differentID, err := Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "wh-2", ts, []byte(`{"a":1}`))
	require.NoError(t, err)
	assert.NotEqual(t, base, differentID)

	differentBody, err := Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "wh-1", ts, []byte(`{"a":2}`))
	require.NoError(t, err)
	assert.NotEqual(t, base, differentBody)

	differentTime, err := Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "wh-1", ts.Add(time.Second), []byte(`{"a":1}`))
	require.NoError(t, err)
	assert.NotEqual(t, base, differentTime)
}
