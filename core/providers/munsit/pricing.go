package munsit

import "unicode/utf8"

// Munsit TTS billing: 1 character = 2 credits, 3,000,000 credits = $100.
// => USD per character = 2 * (100 / 3_000_000) = 1/15_000
const (
	munsitCreditsPerChar       = 2
	munsitCreditsPerHundredUSD = 3_000_000
	munsitUSDPerHundred        = 100.0
)

// usdPerCharacter is the Pay-As-You-Go TTS rate derived from credit pricing.
const usdPerCharacter = (munsitCreditsPerChar * munsitUSDPerHundred) / float64(munsitCreditsPerHundredUSD)

// countBillableChars counts characters the same way SpeechUsage.InputChars is
// backfilled (Unicode code points / runes, not UTF-8 bytes).
func countBillableChars(text string) int {
	return utf8.RuneCountInString(text)
}

// speechCostUSD returns the USD cost for synthesizing the given character count.
func speechCostUSD(chars int) float64 {
	if chars <= 0 {
		return 0
	}
	return float64(chars) * usdPerCharacter
}
