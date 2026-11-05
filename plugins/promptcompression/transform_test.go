package promptcompression

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTextTransformTrimAndMinTokens(t *testing.T) {
	s := defaults(t)
	big := bigOutput(200)
	require.Contains(t, textTransform(s)(big), "tokens trimmed by promptcompression")
	s.MinTokens = estimateTokens(big) + 1
	require.Equal(t, big, textTransform(s)(big), "below MinTokens")
}

func TestTextTransformStrictlyShorterGuard(t *testing.T) {
	s := defaults(t)
	s.HeadLines, s.TailLines = 1, 1
	s.Whitespace = false
	text := "0000000000" // padded below
	for len(text) < 2000 {
		text += "0000000000"
	}
	text += "\ny\nend" // cut removes 1 byte, adds a longer marker
	require.Equal(t, text, textTransform(s)(text))
}

func TestTextTransformWhitespace(t *testing.T) {
	s := defaults(t)
	in := "a  \n\n\n\n\nb"
	require.Equal(t, "a\n\nb", textTransform(s)(in), "text is normalised")
	s.Whitespace = false
	require.Equal(t, in, textTransform(s)(in), "normalisation disabled")
}

func TestDedupeStub(t *testing.T) {
	long := bigOutput(10)
	stub, ok := dedupeStub(strp("c9"), long)
	require.True(t, ok)
	require.Equal(t, "[identical to the output of call c9]", stub)

	_, ok = dedupeStub(nil, long)
	require.False(t, ok, "nil id")

	_, ok = dedupeStub(strp("c9"), "short")
	require.False(t, ok, "stub longer than text")
	_, ok = dedupeStub(strp("c9"), stub)
	require.False(t, ok, "stub equal length is not shorter")
}

func TestTextTransformKeepsOriginalWhenEmpty(t *testing.T) {
	s := defaults(t)
	s.Whitespace = true
	in := "   \n\n\n\n  "
	require.Equal(t, in, textTransform(s)(in))
	require.Equal(t, "", textTransform(s)(""), "empty input stays empty")
}
