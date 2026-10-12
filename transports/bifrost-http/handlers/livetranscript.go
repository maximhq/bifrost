package handlers

import (
	"sort"
	"strings"
	"unicode"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// liveTranscriptMaxBytes bounds what a session keeps of its transcript; beyond it, later
	// fragments are dropped rather than the session.
	liveTranscriptMaxBytes = 1 << 20
	// liveTranscriptMergeGapMs is how far a fragment may trail its speaker's last turn once the
	// other speaker has cut in. Overlapping speech interleaves on the wire, so a fragment joins
	// its speaker's turn by timing, not by arrival order.
	liveTranscriptMergeGapMs = 1500
)

// liveTranscript is a session's conversation as the provider transcribed it, one line per
// speaker turn on the session's timeline. It holds text only; audio never reaches it.
type liveTranscript struct {
	lines []*liveTranscriptLine
	bytes int
}

type liveTranscriptLine struct {
	role    string
	text    strings.Builder
	startMs int64
	endMs   int64
}

// append adds a transcript fragment to its speaker's current turn, or starts a new turn.
func (t *liveTranscript) append(role, delta string, startMs, endMs int64) {
	if delta == "" || t.bytes+len(delta) > liveTranscriptMaxBytes {
		return
	}
	line := t.turnFor(role, startMs)
	if line == nil {
		line = &liveTranscriptLine{role: role, startMs: startMs}
		t.lines = append(t.lines, line)
	} else if startMs-line.endMs > liveTranscriptMergeGapMs && !endsWithSpace(line.text.String()) && !startsWithSpace(delta) {
		// A fragment that resumes after a pause starts a new response, which carries no leading space.
		line.text.WriteByte(' ')
	}
	line.text.WriteString(delta)
	line.endMs = max(line.endMs, endMs)
	t.bytes += len(delta)
}

// turnFor is the speaker's last turn when the fragment continues it, else nil. A pause alone
// never ends a turn; only the other speaker does.
func (t *liveTranscript) turnFor(role string, startMs int64) *liveTranscriptLine {
	if len(t.lines) == 0 {
		return nil
	}
	if last := t.lines[len(t.lines)-1]; last.role == role {
		return last
	}
	for i := len(t.lines) - 2; i >= 0; i-- {
		if t.lines[i].role != role {
			continue
		}
		if startMs-t.lines[i].endMs <= liveTranscriptMergeGapMs {
			return t.lines[i]
		}
		return nil
	}
	return nil
}

// snapshot returns the transcript in timeline order, as the session's log records it.
func (t *liveTranscript) snapshot() []schemas.LiveTranscriptLine {
	if len(t.lines) == 0 {
		return nil
	}
	lines := make([]schemas.LiveTranscriptLine, 0, len(t.lines))
	for _, line := range t.lines {
		lines = append(lines, schemas.LiveTranscriptLine{Role: line.role, Text: line.text.String(), StartMs: line.startMs, EndMs: line.endMs})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].StartMs < lines[j].StartMs })
	return lines
}

func startsWithSpace(s string) bool { return s != "" && unicode.IsSpace(rune(s[0])) }
func endsWithSpace(s string) bool   { return s != "" && unicode.IsSpace(rune(s[len(s)-1])) }
