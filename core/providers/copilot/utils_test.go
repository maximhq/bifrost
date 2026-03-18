package copilot

import "testing"

func TestIsValidCopilotAPIBase(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// Valid
		{"valid individual domain", "https://api.individual.githubcopilot.com", true},
		{"valid business domain", "https://api.business.githubcopilot.com", true},
		{"valid github.com subdomain", "https://api.github.com", true},
		{"valid deep github.com subdomain", "https://copilot.api.github.com", true},
		{"valid enterprise githubcopilot subdomain", "https://api.enterprise.githubcopilot.com", true},

		// Invalid — wrong scheme
		{"http not https", "http://api.individual.githubcopilot.com", false},
		{"ftp scheme", "ftp://api.individual.githubcopilot.com", false},
		{"no scheme", "api.individual.githubcopilot.com", false},

		// Invalid — wrong domain
		{"unrelated domain", "https://evil.com", false},
		{"similar but not suffix", "https://notgithubcopilot.com", false},
		{"githubcopilot.com in path not host", "https://evil.com/githubcopilot.com", false},

		// SSRF vectors
		{"look-alike suffix attack", "https://notreally.githubcopilot.com.evil.co", false},
		{"github.com in subdomain of attacker", "https://github.com.attacker.io", false},
		{"empty string", "", false},
		{"invalid URL", "://bad-url", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidCopilotAPIBase(tc.input)
			if got != tc.want {
				t.Errorf("isValidCopilotAPIBase(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
