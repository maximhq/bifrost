package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/maximhq/bifrost/core/schemas"
)

// BenchmarkA2AEventObservation measures metadata extraction and the duplicate
// JSON encoding performed before stream events reach observational consumers.
func BenchmarkA2AEventObservation(b *testing.B) {
	for _, size := range []int{1024, 64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			event := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(strings.Repeat("x", size)))
			envelope := &schemas.BifrostA2ARequest{RequestType: schemas.A2ARequestTypeSendStreamingMessage, AgentName: "fixture"}
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for b.Loop() {
				observed := describeA2AEvent(envelope, 1, event)
				if observed.Body == nil || len(*observed.Body) < size {
					b.Fatal("missing observed event body")
				}
			}
		})
	}
}
