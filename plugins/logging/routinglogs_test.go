package logging

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestFormatRoutingEngineLogs(t *testing.T) {
	t.Run("empty input yields an empty trail", func(t *testing.T) {
		if got := formatRoutingEngineLogs(nil); got != "" {
			t.Fatalf("expected empty string, got %q", got)
		}
	})

	t.Run("writes timestamp, engine, level and message on every line", func(t *testing.T) {
		got := formatRoutingEngineLogs([]schemas.RoutingEngineLogEntry{
			{Engine: schemas.RoutingEngineCore, Level: schemas.LogLevelInfo, Message: "Retry 1/2 for openai/gpt-4o-mini", Timestamp: 1756881000842},
			{Engine: schemas.RoutingEngineLoadbalancing, Level: schemas.LogLevelError, Message: "Weighted selection failed: no eligible providers", Timestamp: 1756881000850},
		})
		want := "[1756881000842] [core] [info] - Retry 1/2 for openai/gpt-4o-mini\n" +
			"[1756881000850] [loadbalancing] [error] - Weighted selection failed: no eligible providers\n"
		if got != want {
			t.Fatalf("unexpected trail\n got: %q\nwant: %q", got, want)
		}
	})

	t.Run("writes a missing level as info so every line keeps the same shape", func(t *testing.T) {
		got := formatRoutingEngineLogs([]schemas.RoutingEngineLogEntry{
			{Engine: schemas.RoutingEngineGovernance, Message: "No weighted configs; skipping load balancing", Timestamp: 1756881000006},
		})
		want := "[1756881000006] [governance] [info] - No weighted configs; skipping load balancing\n"
		if got != want {
			t.Fatalf("unexpected trail\n got: %q\nwant: %q", got, want)
		}
	})
}
