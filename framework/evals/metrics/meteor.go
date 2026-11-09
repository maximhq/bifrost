package metrics

import (
	"math"
	"slices"
	"strings"
)

// METEOR parameters, the defaults Hugging Face's metric passes to NLTK.
const (
	meteorAlpha = 0.9
	meteorBeta  = 3.0
	meteorGamma = 0.5
)

type meteorWord struct {
	index int
	text  string
}

// METEOR scores a candidate against a reference by aligning their words, first on
// exact matches and then on stems, and combining precision and recall with a penalty
// for matches that are scattered. NLTK also aligns WordNet synonyms; that stage is
// left out here because it needs the WordNet database, so a candidate that only
// paraphrases with synonyms scores lower than it does in Maxim.
func METEOR(candidate, reference string) float64 {
	hyp := meteorWords(TokenizeWords(candidate))
	ref := meteorWords(TokenizeWords(reference))
	hypLen, refLen := len(hyp), len(ref)
	if hypLen == 0 || refLen == 0 {
		return 0
	}

	matches, hyp, ref := meteorMatch(hyp, ref)
	for i := range hyp {
		hyp[i].text = PorterStem(hyp[i].text)
	}
	for i := range ref {
		ref[i].text = PorterStem(ref[i].text)
	}
	stemMatches, _, _ := meteorMatch(hyp, ref)
	matches = append(matches, stemMatches...)
	if len(matches) == 0 {
		return 0
	}
	slices.SortStableFunc(matches, func(a, b [2]int) int { return a[0] - b[0] })

	count := float64(len(matches))
	precision := count / float64(hypLen)
	recall := count / float64(refLen)
	fmean := precision * recall / (meteorAlpha*precision + (1-meteorAlpha)*recall)

	chunks := 1
	for i := 0; i < len(matches)-1; i++ {
		if matches[i+1][0] != matches[i][0]+1 || matches[i+1][1] != matches[i][1]+1 {
			chunks++
		}
	}
	penalty := meteorGamma * math.Pow(float64(chunks)/count, meteorBeta)
	return (1 - penalty) * fmean
}

func meteorWords(tokens []string) []meteorWord {
	words := make([]meteorWord, len(tokens))
	for i, token := range tokens {
		words[i] = meteorWord{index: i, text: strings.ToLower(token)}
	}
	return words
}

// meteorMatch pairs equal words, scanning both lists from the end as NLTK does, and
// returns the pairs as (candidate index, reference index) with the unmatched words.
func meteorMatch(hyp, ref []meteorWord) (matches [][2]int, restHyp, restRef []meteorWord) {
	hyp, ref = slices.Clone(hyp), slices.Clone(ref)
	for i := len(hyp) - 1; i >= 0; i-- {
		for j := len(ref) - 1; j >= 0; j-- {
			if hyp[i].text == ref[j].text {
				matches = append(matches, [2]int{hyp[i].index, ref[j].index})
				hyp = slices.Delete(hyp, i, i+1)
				ref = slices.Delete(ref, j, j+1)
				break
			}
		}
	}
	return matches, hyp, ref
}
