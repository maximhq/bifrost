package metrics

import "strings"

// RougeScores holds the F-measure of each ROUGE variant Maxim reports.
type RougeScores struct {
	Rouge1    float64
	Rouge2    float64
	RougeL    float64
	RougeLsum float64
}

// Rouge scores a candidate against a reference with rouge-score's algorithm, stemming
// on. ROUGE-1 and ROUGE-2 are n-gram overlap, ROUGE-L is the longest common
// subsequence, and ROUGE-Lsum is the union LCS over newline-separated sentences.
func Rouge(candidate, reference string) RougeScores {
	// Maxim passes the output as rouge-score's "target" and the expected output as its
	// "prediction". The F-measure of the first three is symmetric; Lsum is kept in the
	// same order so multi-line texts score as they do in Maxim.
	target, prediction := TokenizeRouge(candidate), TokenizeRouge(reference)
	return RougeScores{
		Rouge1:    rougeN(target, prediction, 1),
		Rouge2:    rougeN(target, prediction, 2),
		RougeL:    rougeLCS(target, prediction),
		RougeLsum: rougeSummaryLCS(rougeSentences(candidate), rougeSentences(reference)),
	}
}

func rougeN(target, prediction []string, order int) float64 {
	targetCounts, predictionCounts := ngramCounts(target, order), ngramCounts(prediction, order)
	shared, targetTotal, predictionTotal := 0, 0, 0
	for ngram, count := range targetCounts {
		targetTotal += count
		shared += min(count, predictionCounts[ngram])
	}
	for _, count := range predictionCounts {
		predictionTotal += count
	}
	precision := float64(shared) / float64(max(predictionTotal, 1))
	recall := float64(shared) / float64(max(targetTotal, 1))
	return fmeasure(precision, recall)
}

func rougeLCS(target, prediction []string) float64 {
	if len(target) == 0 || len(prediction) == 0 {
		return 0
	}
	table := lcsTable(target, prediction)
	length := float64(table[len(target)][len(prediction)])
	return fmeasure(length/float64(len(prediction)), length/float64(len(target)))
}

// lcsTable is the dynamic-programming table of longest common subsequence lengths.
func lcsTable(ref, can []string) [][]int {
	table := make([][]int, len(ref)+1)
	cells := make([]int, (len(ref)+1)*(len(can)+1))
	for i := range table {
		table[i] = cells[i*(len(can)+1) : (i+1)*(len(can)+1)]
	}
	for i := 1; i <= len(ref); i++ {
		for j := 1; j <= len(can); j++ {
			if ref[i-1] == can[j-1] {
				table[i][j] = table[i-1][j-1] + 1
			} else {
				table[i][j] = max(table[i-1][j], table[i][j-1])
			}
		}
	}
	return table
}

// lcsIndices returns the positions in ref of one longest common subsequence with can.
func lcsIndices(ref, can []string) []int {
	table := lcsTable(ref, can)
	var indices []int
	for i, j := len(ref), len(can); i > 0 && j > 0; {
		switch {
		case ref[i-1] == can[j-1]:
			indices = append(indices, i-1)
			i--
			j--
		case table[i][j-1] > table[i-1][j]:
			j--
		default:
			i--
		}
	}
	return indices
}

// rougeSentences splits text into its non-empty lines and tokenizes each.
func rougeSentences(text string) [][]string {
	var sentences [][]string
	for _, line := range strings.Split(text, "\n") {
		if line != "" {
			sentences = append(sentences, TokenizeRouge(line))
		}
	}
	return sentences
}

// rougeSummaryLCS is ROUGE-Lsum: for each reference sentence, the union of its LCS
// with every candidate sentence, with each token credited at most as often as it occurs.
func rougeSummaryLCS(refSentences, canSentences [][]string) float64 {
	if len(refSentences) == 0 || len(canSentences) == 0 {
		return 0
	}
	refCounts, canCounts := map[string]int{}, map[string]int{}
	refTotal, canTotal := 0, 0
	for _, sentence := range refSentences {
		refTotal += len(sentence)
		for _, token := range sentence {
			refCounts[token]++
		}
	}
	for _, sentence := range canSentences {
		canTotal += len(sentence)
		for _, token := range sentence {
			canCounts[token]++
		}
	}
	if refTotal == 0 || canTotal == 0 {
		return 0
	}
	hits := 0
	for _, ref := range refSentences {
		union := make([]bool, len(ref))
		for _, can := range canSentences {
			for _, index := range lcsIndices(ref, can) {
				union[index] = true
			}
		}
		for index, inUnion := range union {
			if !inUnion {
				continue
			}
			token := ref[index]
			if canCounts[token] > 0 && refCounts[token] > 0 {
				hits++
				canCounts[token]--
				refCounts[token]--
			}
		}
	}
	return fmeasure(float64(hits)/float64(canTotal), float64(hits)/float64(refTotal))
}
