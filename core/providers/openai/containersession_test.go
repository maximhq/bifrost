package openai

import (
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// withFreshContainerRegistry swaps in an empty registry so one test's claims cannot
// suppress another's, and restores the process-wide one afterwards.
func withFreshContainerRegistry(t *testing.T) {
	t.Helper()
	original := providerUtils.ContainerSessions
	providerUtils.ContainerSessions = providerUtils.NewContainerSessionRegistry(time.Hour, 100)
	t.Cleanup(func() { providerUtils.ContainerSessions = original })
}

// responseWithContainers builds a responses payload whose output names the given
// containers on code_interpreter_call items.
func responseWithContainers(containerIDs ...string) *schemas.BifrostResponsesResponse {
	resp := &schemas.BifrostResponsesResponse{}
	for _, id := range containerIDs {
		callType := schemas.ResponsesMessageTypeCodeInterpreterCall
		resp.Output = append(resp.Output, schemas.ResponsesMessage{
			Type: &callType,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
					ContainerID: id,
				},
			},
		})
	}
	return resp
}

func stampedSessions(resp *schemas.BifrostResponsesResponse) *int {
	if resp == nil || resp.Usage == nil || resp.Usage.OutputTokensDetails == nil {
		return nil
	}
	return resp.Usage.OutputTokensDetails.NumContainerSessions
}

func TestStampContainerSessions_BillsFirstSightingOnly(t *testing.T) {
	withFreshContainerRegistry(t)

	first := responseWithContainers("cntr_abc")
	stampContainerSessions(first, schemas.OpenAI, nil)
	if got := stampedSessions(first); got == nil || *got != 1 {
		t.Fatalf("first response sessions = %v, want 1", got)
	}

	// A follow-up turn reuses the container. OpenAI charges per session, not per
	// response, so the second turn owes nothing.
	second := responseWithContainers("cntr_abc")
	stampContainerSessions(second, schemas.OpenAI, nil)
	if got := stampedSessions(second); got != nil {
		t.Fatalf("second response sessions = %v, want nil", *got)
	}
}

func TestStampContainerSessions_DeduplicatesWithinOneResponse(t *testing.T) {
	withFreshContainerRegistry(t)

	// Several interpreter calls in one response run inside one container.
	resp := responseWithContainers("cntr_abc", "cntr_abc", "cntr_abc")
	stampContainerSessions(resp, schemas.OpenAI, nil)

	if got := stampedSessions(resp); got == nil || *got != 1 {
		t.Fatalf("sessions = %v, want 1", got)
	}
}

func TestStampContainerSessions_CountsDistinctContainers(t *testing.T) {
	withFreshContainerRegistry(t)

	resp := responseWithContainers("cntr_abc", "cntr_def")
	stampContainerSessions(resp, schemas.OpenAI, nil)

	if got := stampedSessions(resp); got == nil || *got != 2 {
		t.Fatalf("sessions = %v, want 2", got)
	}
}

func TestStampContainerSessions_IgnoresProvidersWithoutHostedCodeInterpreter(t *testing.T) {
	withFreshContainerRegistry(t)

	// Bedrock populates ContainerID with a tool-use id rather than a real container,
	// which would otherwise look like a fresh sandbox on every single turn.
	resp := responseWithContainers("toolu_01xyz")
	stampContainerSessions(resp, schemas.Bedrock, nil)

	if got := stampedSessions(resp); got != nil {
		t.Fatalf("sessions = %v, want nil for a provider with no hosted code interpreter", *got)
	}
}

func TestStampContainerSessions_IgnoresResponsesWithoutContainers(t *testing.T) {
	withFreshContainerRegistry(t)

	resp := &schemas.BifrostResponsesResponse{
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 5},
	}
	stampContainerSessions(resp, schemas.OpenAI, nil)

	if got := stampedSessions(resp); got != nil {
		t.Fatalf("sessions = %v, want nil when no container ran", *got)
	}
}

func TestStampContainerSessions_PreservesExistingUsage(t *testing.T) {
	withFreshContainerRegistry(t)

	resp := responseWithContainers("cntr_abc")
	resp.Usage = &schemas.ResponsesResponseUsage{
		InputTokens:         120,
		OutputTokens:        45,
		OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{ReasoningTokens: 30},
	}
	stampContainerSessions(resp, schemas.OpenAI, nil)

	if resp.Usage.InputTokens != 120 || resp.Usage.OutputTokens != 45 {
		t.Fatalf("token counts were disturbed: %+v", resp.Usage)
	}
	if resp.Usage.OutputTokensDetails.ReasoningTokens != 30 {
		t.Fatalf("reasoning tokens = %d, want 30", resp.Usage.OutputTokensDetails.ReasoningTokens)
	}
	if got := stampedSessions(resp); got == nil || *got != 1 {
		t.Fatalf("sessions = %v, want 1", got)
	}
}
