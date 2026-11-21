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
func terEditDistance(hyp, ref []string) (int, []byte) {
	rows, cols := len(hyp)+1, len(ref)+1
	dist := make([]terCell, rows*cols)
	for i := range dist {
		dist[i] = terCell{terInfinity, terOpUndefined}
	}
	at := func(i, j int) *terCell { return &dist[i*cols+j] }
	for j := 0; j < cols; j++ {
		*at(0, j) = terCell{j, terOpInsert}
	}

	lengthRatio := 1.0
	if len(hyp) > 0 {
		lengthRatio = float64(len(ref)) / float64(len(hyp))
	}
	beam := terBeamWidth
	if float64(terBeamWidth) < lengthRatio/2 {
		beam = int(math.Ceil(lengthRatio/2 + terBeamWidth))
	}
	for i := 1; i < rows; i++ {
		diagonal := int(math.Floor(float64(i) * lengthRatio))
		minJ := max(0, diagonal-beam)
		maxJ := min(cols, diagonal+beam)
		if i == len(hyp) {
			maxJ = cols
		}
		for j := minJ; j < maxJ; j++ {
			cell := at(i, j)
			if j == 0 {
				*cell = terCell{at(i-1, j).cost + 1, terOpDelete}
				continue
			}
			substitute := terCell{at(i-1, j-1).cost + 1, terOpSubstitute}
			if hyp[i-1] == ref[j-1] {
				substitute = terCell{at(i-1, j-1).cost, terOpMatch}
			}
			// Ties keep the earlier option: match or substitute, then delete, then insert.
			for _, option := range [3]terCell{substitute, {at(i-1, j).cost + 1, terOpDelete}, {at(i, j-1).cost + 1, terOpInsert}} {
				if cell.cost > option.cost {
					*cell = option
				}
			}
		}
	}

	trace := make([]byte, 0, rows+cols)
	for i, j := len(hyp), len(ref); i > 0 || j > 0; {
		op := at(i, j).op
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
			return at(len(hyp), len(ref)).cost, nil
		}
	}
	slices.Reverse(trace)
	return at(len(hyp), len(ref)).cost, trace
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
		for startR := 0; startR < len(ref); startR++ {
			if abs(startR-startH) > terMaxShiftDistance {
				continue
			}
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
		out = append(out, words[:start]...)
		out = append(out, words[start+length:length+target]...)
		out = append(out, words[start:start+length]...)
		out = append(out, words[length+target:]...)
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

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
