package promptcompression

// estimateTokens approximates the token count of s as ceil(len(s)/4), counting
// bytes. It is deliberately cheap and tokenizer-free: the plugin only needs a
// consistent size signal to compare against thresholds, not exact billing.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}
