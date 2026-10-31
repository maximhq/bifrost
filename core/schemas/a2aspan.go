package schemas

import "strings"

// A2ASpanData retains captured bodies separately from attributes so connectors
// must explicitly opt into exporting them. Values are immutable after attachment.
type A2ASpanData struct {
	RawRequest  string
	RawResponse string
}

// RawPayloads returns the operation's raw bodies without exposing them through
// ordinary trace serialization. Callers must enforce export and content policy.
func (s *Span) RawPayloads() (request, response string) {
	if s == nil {
		return "", ""
	}
	if s.A2A != nil {
		return s.A2A.RawRequest, s.A2A.RawResponse
	}
	if s.LLM != nil {
		return s.LLM.RawRequest, s.LLM.RawResponse
	}
	return "", ""
}

// SetA2ARawPayloads attaches an immutable capture under the span lock only while
// id still matches, preventing a late completion from writing to a recycled span.
func (s *Span) SetA2ARawPayloads(id string, data *A2ASpanData) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SpanID != id {
		return
	}
	s.A2A = data
}

// A2AStreamOutput renders the retained, validated event JSON under the span lock
// only while id still matches. The gateway bounds count and content size.
func (s *Span) A2AStreamOutput(id string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SpanID != id {
		return ""
	}
	var output strings.Builder
	output.WriteByte('[')
	count := 0
	for _, event := range s.Events {
		if body, ok := event.Attributes[AttrOutputMessages].(string); ok {
			if count > 0 {
				output.WriteByte(',')
			}
			output.WriteString(body)
			count++
		}
	}
	if count == 0 {
		return ""
	}
	output.WriteByte(']')
	return output.String()
}
