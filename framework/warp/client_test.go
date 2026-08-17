package warp

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// bifrost.Init stores a context derived from the one it is handed, so passing
// the request context ties the cached instance's lifetime to whichever request
// happened to build it. That request ending - a user closing the tab mid-answer
// - then poisons the shared instance for everyone after them.
func TestWarpClientInstanceOutlivesTheRequestThatBuiltIt(t *testing.T) {
	client := NewClient(bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(client.Shutdown)
	config := &schemas.WarpConfig{Enabled: true, Provider: schemas.OpenAI, Model: "gpt-4o"}

	first, cancelFirst := context.WithCancel(context.Background())
	instance, err := client.instanceFor(first, config)
	require.NoError(t, err)
	require.NotNil(t, instance)

	// The request that built the instance goes away. Cancellation propagates
	// through a watcher goroutine, so give it time to land rather than racing it.
	cancelFirst()
	require.Eventually(t, func() bool { return first.Err() != nil }, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	second, err := client.instanceFor(context.Background(), config)
	require.NoError(t, err)
	require.Same(t, instance, second, "the cached instance should be reused")

	// UpdateProvider refuses once the instance context is done, which is the
	// observable form of "this cached client is dead".
	require.NoError(t, second.UpdateProvider(schemas.OpenAI),
		"the cached instance must not be torn down with the request that built it")
}

// A replaced instance has to outlive any turn already running against it.
// Shutdown cancels the instance context, and a turn makes up to maxIterations
// sequential model calls - so a grace of one call's timeout aborted a turn
// mid-flight whenever settings were saved while somebody was waiting.
func TestWarpClientRetirementGraceCoversAWholeTurn(t *testing.T) {
	config := &schemas.WarpConfig{
		Enabled: true, Provider: schemas.OpenAI, Model: "gpt-4o",
		MaxIterations: 6, RequestTimeoutSeconds: 30,
	}
	require.Equal(t, 180*time.Second, retirementGrace(config),
		"the grace must cover every iteration a turn may take, not one call")

	// Defaults resolve the same way Turn.Budget does.
	defaults := &schemas.WarpConfig{Enabled: true, Provider: schemas.OpenAI, Model: "gpt-4o"}
	expected := time.Duration(schemas.WarpDefaultMaxIterations*schemas.WarpDefaultRequestTimeoutSeconds) * time.Second
	require.Equal(t, expected, retirementGrace(defaults))
	require.Greater(t, retirementGrace(defaults),
		time.Duration(defaults.EffectiveRequestTimeoutSeconds())*time.Second)
}

// A replaced instance has to be retired on its own grace, not its successor's.
//
// The grace is a whole turn's worth of budget, computed from the config the
// instance was built with. Scheduling the old instance's shutdown from the new
// config meant that saving a shorter timeout cancelled a turn already running
// under the longer one - the setting change reached back and killed work that
// started before it.
func TestWarpReplacedInstanceRetiresOnItsOwnGrace(t *testing.T) {
	var scheduled []time.Duration
	original := scheduleRetirement
	scheduleRetirement = func(d time.Duration, fn func()) *time.Timer {
		scheduled = append(scheduled, d)
		return time.AfterFunc(time.Hour, func() {}) // never fires during the test
	}
	t.Cleanup(func() { scheduleRetirement = original })

	client := NewClient(nil)
	slow := &schemas.WarpConfig{
		Provider: "openai", Model: "gpt-4o",
		MaxIterations: 8, RequestTimeoutSeconds: 600,
	}
	fast := &schemas.WarpConfig{
		Provider: "openai", Model: "gpt-4o-mini",
		MaxIterations: 2, RequestTimeoutSeconds: 5,
	}

	if _, err := client.instanceFor(context.Background(), slow); err != nil {
		t.Skipf("cannot build a Warp client in this environment: %v", err)
	}
	require.Empty(t, scheduled, "the first instance replaces nothing")

	if _, err := client.instanceFor(context.Background(), fast); err != nil {
		t.Skipf("cannot build a Warp client in this environment: %v", err)
	}
	require.Len(t, scheduled, 1)
	require.Equal(t, retirementGrace(slow), scheduled[0],
		"the replaced instance must be retired on the grace it was built with, not the replacement's")
	require.NotEqual(t, retirementGrace(fast), scheduled[0])
}
