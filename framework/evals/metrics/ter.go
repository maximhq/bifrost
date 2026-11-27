package metrics

import (
	"math"
	"slices"
	"strings"
)

// TER search limits, as in tercom and sacrebleu.
const (
	terMaxShiftSize       = 10
	terMaxShiftDistance   = 50
	terBeamWidth          = 25
	terMaxShiftCandidates = 1000
	terInfinity           = int(1e16)
)

// Edit operations in a TER alignment trace.
const (
	terOpInsert     = 'i'
	terOpDelete     = 'd'
	terOpMatch      = ' '
	terOpSubstitute = 's'
	terOpUndefined  = 'x'
)

// TER is the translation edit rate: the fewest edits that turn the candidate into the
// reference, as a percentage of the reference length. An edit is an insertion, a
// deletion, a substitution or a shift of a whole phrase, so reordered text costs one
// edit per moved phrase instead of one per word. Words are lowercased and split on
// whitespace, which is sacrebleu's default. The score can exceed 100.
func TER(candidate, reference string) float64 {
	hyp := strings.Fields(strings.ToLower(candidate))
	ref := strings.Fields(strings.ToLower(reference))
	if len(ref) == 0 {
		if len(hyp) > 0 {
			return 100
		}
		return 0
	}
	shifts := 0
	checked := 0
	for {
		delta, shifted, nowChecked := terBestShift(hyp, ref, checked)
		checked = nowChecked
		if checked >= terMaxShiftCandidates || delta <= 0 {
			break
		}
		shifts++
		hyp = shifted
	}
	distance, _ := terEditDistance(hyp, ref)
	return 100 * float64(shifts+distance) / float64(len(ref))
}

type terCell struct {
	cost int
	op   byte
}

// terEditDistance is a beam-limited edit distance from hyp to ref. It returns the
// distance and the trace of operations, one byte per step.
//
// Only the cells the beam can write are stored: row 0 in full, each middle row's beam
// window in a fixed stride of min(2*beam, len(ref)+1) cells, and the last row from its
// window start to the end. Memory grows with len(hyp) times the beam instead of
// len(hyp) times len(ref), in one allocation besides the trace. Every cell outside a
// row's window reads as {terInfinity, terOpUndefined}, which is what an unwritten cell
// of a full table holds, so the distance and the trace are the same as the full table's.
func terEditDistance(hyp, ref []string) (int, []byte) {
	rows, cols := len(hyp)+1, len(ref)+1
	lengthRatio := 1.0
	if len(hyp) > 0 {
		lengthRatio = float64(len(ref)) / float64(len(hyp))
	}
	beam := terBeamWidth
	if float64(terBeamWidth) < lengthRatio/2 {
		beam = int(math.Ceil(lengthRatio/2 + terBeamWidth))
	}
	// window is the column range [lo, hi) stored for row i.
	window := func(i int) (lo, hi int) {
		if i == 0 {
			return 0, cols
		}
		diagonal := int(math.Floor(float64(i) * lengthRatio))
		lo = max(0, diagonal-beam)
		hi = min(cols, diagonal+beam)
		if i == len(hyp) {
			hi = cols
		}
		return lo, max(lo, hi)
	}
	stride := min(2*beam, cols)
	base := func(i int) int {
		if i == 0 {
			return 0
		}
		return cols + (i-1)*stride
	}
	total := cols
	if len(hyp) > 0 {
		lastLo, _ := window(len(hyp))
		total = base(len(hyp)) + cols - lastLo
	}
	cells := make([]terCell, total)
	at := func(i, j, lo, hi int) terCell {
		if j < lo || j >= hi {
			return terCell{terInfinity, terOpUndefined}
		}
		return cells[base(i)+j-lo]
	}
	for j := 0; j < cols; j++ {
		cells[j] = terCell{j, terOpInsert}
	}

	prevLo, prevHi := window(0)
	for i := 1; i < rows; i++ {
		lo, hi := window(i)
		row := cells[base(i) : base(i)+hi-lo]
		for j := lo; j < hi; j++ {
			if j == 0 {
				row[0] = terCell{at(i-1, j, prevLo, prevHi).cost + 1, terOpDelete}
				continue
			}
			cell := terCell{terInfinity, terOpUndefined}
			diagonal := at(i-1, j-1, prevLo, prevHi)
			substitute := terCell{diagonal.cost + 1, terOpSubstitute}
			if hyp[i-1] == ref[j-1] {
				substitute = terCell{diagonal.cost, terOpMatch}
			}
			// Ties keep the earlier option: match or substitute, then delete, then insert.
			for _, option := range [3]terCell{substitute, {at(i-1, j, prevLo, prevHi).cost + 1, terOpDelete}, {at(i, j-1, lo, hi).cost + 1, terOpInsert}} {
				if cell.cost > option.cost {
					cell = option
				}
			}
			row[j-lo] = cell
		}
		prevLo, prevHi = lo, hi
	}

	lastLo, lastHi := window(len(hyp))
	final := at(len(hyp), len(ref), lastLo, lastHi).cost
	trace := make([]byte, 0, rows+cols)
	for i, j := len(hyp), len(ref); i > 0 || j > 0; {
		lo, hi := window(i)
		op := at(i, j, lo, hi).op
		trace = append(trace, op)
		switch op {
		case terOpSubstitute, terOpMatch:
			i--
			j--
		case terOpInsert:
			j--
		case terOpDelete:
			i--
		default:
			// The beam cut off every path here; no alignment can be read back.
			return final, nil
		}
	}
	slices.Reverse(trace)
	return final, trace
}

// terAlignment reads a trace into, for each reference position, the hypothesis
// position aligned to it, plus per-word error flags for both sides. The trace is read
// with insert and delete swapped, which is how tercom views it from the hypothesis.
func terAlignment(trace []byte) (align map[int]int, refErr, hypErr []int) {
	align = make(map[int]int)
	posHyp, posRef := -1, -1
	for _, op := range trace {
		switch op {
		case terOpMatch:
			posHyp++
			posRef++
			align[posRef] = posHyp
			hypErr = append(hypErr, 0)
			refErr = append(refErr, 0)
		case terOpSubstitute:
			posHyp++
			posRef++
			align[posRef] = posHyp
			hypErr = append(hypErr, 1)
			refErr = append(refErr, 1)
		case terOpDelete: // flipped: an insertion on the hypothesis side
			posHyp++
			hypErr = append(hypErr, 1)
		case terOpInsert: // flipped: a deletion on the hypothesis side
			posRef++
			align[posRef] = posHyp
			refErr = append(refErr, 1)
		}
	}
	return align, refErr, hypErr
}

type terCandidate struct {
	gain    int
	length  int
	startH  int
	target  int
	shifted []string
}

// better reports whether c beats other: highest gain, then longest phrase, then
// earliest source, then earliest target, then the shifted words themselves.
func (c *terCandidate) better(other *terCandidate) bool {
	switch {
	case c.gain != other.gain:
		return c.gain > other.gain
	case c.length != other.length:
		return c.length > other.length
	case c.startH != other.startH:
		return c.startH < other.startH
	case c.target != other.target:
		return c.target < other.target
	}
	return slices.Compare(c.shifted, other.shifted) > 0
}

// terBestShift finds the phrase shift that lowers the edit distance the most. It
// returns the gain, the shifted hypothesis and the running count of candidates tried.
func terBestShift(hyp, ref []string, checked int) (int, []string, int) {
	preScore, trace := terEditDistance(hyp, ref)
	if trace == nil {
		return 0, hyp, checked
	}
	align, refErr, hypErr := terAlignment(trace)
	var best *terCandidate

search:
	for startH := 0; startH < len(hyp); startH++ {
		// Only reference positions within the shift distance are visited, in the same
		// order as before, instead of every position with the far ones skipped.
		for startR := max(0, startH-terMaxShiftDistance); startR < min(len(ref), startH+terMaxShiftDistance+1); startR++ {
			for length := 1; length <= terMaxShiftSize && hyp[startH+length-1] == ref[startR+length-1]; length++ {
				atEnd := startH+length == len(hyp) || startR+length == len(ref)
				if terConsiderShift(hyp, ref, align, refErr, hypErr, startH, startR, length, preScore, &best, &checked) {
					break search
				}
				if atEnd {
					break
				}
			}
		}
	}
	if best == nil {
		return 0, hyp, checked
	}
	return best.gain, best.shifted, checked
}

// terConsiderShift tries moving one matching phrase to each position it could align
// to, and reports whether the candidate budget is spent.
func terConsiderShift(hyp, ref []string, align map[int]int, refErr, hypErr []int, startH, startR, length, preScore int, best **terCandidate, checked *int) bool {
	// A phrase already in place on either side, or already aligned to itself, gains nothing.
	if sum(hypErr[startH:min(startH+length, len(hypErr))]) == 0 {
		return *checked >= terMaxShiftCandidates
	}
	if sum(refErr[startR:min(startR+length, len(refErr))]) == 0 {
		return *checked >= terMaxShiftCandidates
	}
	if aligned := align[startR]; startH <= aligned && aligned < startH+length {
		return *checked >= terMaxShiftCandidates
	}
	previous := -1
	for offset := -1; offset < length; offset++ {
		var target int
		if startR+offset == -1 {
			target = 0
		} else if aligned, ok := align[startR+offset]; ok {
			target = aligned + 1
		} else {
			break
		}
		if target == previous {
			continue
		}
		previous = target
		shifted := terShift(hyp, startH, length, target)
		distance, _ := terEditDistance(shifted, ref)
		candidate := &terCandidate{gain: preScore - distance, length: length, startH: startH, target: target, shifted: shifted}
		*checked++
		if *best == nil || candidate.better(*best) {
			*best = candidate
		}
	}
	return *checked >= terMaxShiftCandidates
}

// terShift moves words[start:start+length] so that it begins at target.
func terShift(words []string, start, length, target int) []string {
	out := make([]string, 0, len(words))
	switch {
	case target < start:
		out = append(out, words[:target]...)
		out = append(out, words[start:start+length]...)
		out = append(out, words[target:start]...)
		out = append(out, words[start+length:]...)
	case target > start+length:
		out = append(out, words[:start]...)
		out = append(out, words[start+length:target]...)
		out = append(out, words[start:start+length]...)
		out = append(out, words[target:]...)
	default:
		// The overlap case can name an end past the last word. sacrebleu's Python
		// slices clip there, so this clips too.
		end := min(len(words), length+target)
		out = append(out, words[:start]...)
		out = append(out, words[start+length:end]...)
		out = append(out, words[start:start+length]...)
		out = append(out, words[end:]...)
	}
	return out
}

func sum(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}
