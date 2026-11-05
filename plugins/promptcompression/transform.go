package promptcompression

// stats reports what a compressor did to the eligible region (old tool
// outputs outside the protected prefix). EligibleBefore and EligibleAfter are
// estimated tokens summed over every eligible output, edited or not, so the
// caller can judge savings against the part of the request that may change.
// Changed is true when at least one message differs from the input.
type stats struct {
	EligibleBefore, EligibleAfter int
	Changed                       bool
}

// textTransform returns the per-text rewrite shared by the Chat and Responses
// compressors, so both APIs trim and normalise identically. Callers pass only
// the text of an old tool output. Text of at least MinTokens is cut by
// trimMiddle, kept only if strictly shorter in bytes (the marker can outweigh
// a tiny cut). Whitespace normalisation then runs if enabled; if that leaves
// nothing of a non-empty text, the original is kept. It holds no API types on
// purpose.
func textTransform(s settings) func(string) string {
	return func(text string) string {
		orig := text
		if estimateTokens(text) >= s.MinTokens {
			if r, ok := trimMiddle(text, s.HeadLines, s.TailLines); ok && len(r) < len(text) {
				text = r
			}
		}
		if s.Whitespace {
			text = normalizeWhitespace(text)
		}
		// A whitespace-only output would normalise to "", and an empty tool
		// result can be rejected by providers or mislead the model; keep it.
		if text == "" && orig != "" {
			return orig
		}
		return text
	}
}

// dedupeStub builds the pointer that replaces an older duplicate tool output,
// naming the call of the immediately next newer identical copy, so adding a
// later duplicate never rewrites an existing stub. It reports false when there
// is no id to point at or the stub would not be strictly shorter than text,
// in which case the original must stay.
func dedupeStub(id *string, text string) (string, bool) {
	if id == nil {
		return "", false
	}
	stub := "[identical to the output of call " + *id + "]"
	if len(stub) >= len(text) {
		return "", false
	}
	return stub, true
}
