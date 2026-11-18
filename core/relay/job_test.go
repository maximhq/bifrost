package relay

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The job id comes from the upstream's reply and lands in the poll path, so an id that could leave
// the configured path, or overflow the job row, is not recorded as a job.
func TestJobHandleRefusesUnsafeIDs(t *testing.T) {
	route := &compiledRoute{job: &schemas.RelayJobConfig{IDPath: "JobId"}, jobParams: "{}"}
	handle := func(id string) (*schemas.RelayJobHandle, error) {
		body, _ := schemas.MarshalSorted(map[string]string{"JobId": id})
		return route.jobHandle("application/json", body)
	}
	for _, id := range []string{
		"8f14e45fceea167a5a36dedd4bea2543",
		"arn:aws:bedrock:us-east-1:123456789012:async-invoke/abc123",
		"job_01-ABC.v2",
	} {
		h, err := handle(id)
		require.NoError(t, err, id)
		require.NotNil(t, h, id)
		assert.Equal(t, id, h.ID)
	}
	for name, id := range map[string]string{
		"parent segment":    "../../admin/keys",
		"current segment":   "a/./b",
		"trailing parent":   "a/..",
		"backslash":         `a\..\b`,
		"control character": "a\r\nX-Injected: 1",
		"space":             "a b",
		"query":             "a?b=1",
		"fragment":          "a#b",
		"non-ASCII":         "jöb",
		"too long":          strings.Repeat("a", maxRelayJobIDLength+1),
	} {
		h, err := handle(id)
		assert.Error(t, err, name)
		assert.Nil(t, h, name)
	}
	h, err := handle("")
	assert.NoError(t, err, "a reply naming no id is an ordinary call")
	assert.Nil(t, h)
}
