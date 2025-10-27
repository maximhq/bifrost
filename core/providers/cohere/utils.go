package cohere

var (
	// Maps provider-specific finish reasons to Bifrost format
	cohereFinishReasonToBifrost = map[CohereFinishReason]string{
		FinishReasonComplete:     "stop",
		FinishReasonStopSequence: "stop",
		FinishReasonMaxTokens:    "length",
		FinishReasonToolCall:     "tool_calls",
	}
)

// ConvertCohereFinishReasonToBifrost converts provider finish reasons to Bifrost format
func ConvertCohereFinishReasonToBifrost(providerReason CohereFinishReason) string {
	if bifrostReason, ok := cohereFinishReasonToBifrost[providerReason]; ok {
		return bifrostReason
	}
	return string(providerReason)
}
