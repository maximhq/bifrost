package githubcopilot_test

import "testing"

// TestHasCopilotCredentials pins that a partial GitHub App bundle does not look like a
// runnable configuration. Accepting one would start the live suite with empty installation,
// repository or private-key values, and the resulting failure surfaces deep inside the
// provider rather than as "you forgot a secret".
func TestHasCopilotCredentials(t *testing.T) {
	const (
		appID    = "GITHUB_COPILOT_APP_ID"
		instID   = "GITHUB_COPILOT_INSTALLATION_ID"
		repoID   = "GITHUB_COPILOT_REPOSITORY_ID"
		privKey  = "GITHUB_COPILOT_PRIVATE_KEY"
		apiToken = "GITHUB_COPILOT_API_KEY"
	)
	full := map[string]string{appID: "1", instID: "2", repoID: "3", privKey: "pem"}

	without := func(missing string) map[string]string {
		m := map[string]string{}
		for k, v := range full {
			if k != missing {
				m[k] = v
			}
		}
		return m
	}

	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nothing set", map[string]string{}, false},
		{"direct api token alone", map[string]string{apiToken: "tid=abc"}, true},
		{"complete app bundle", full, true},
		{"app id only", map[string]string{appID: "1"}, false},
		{"missing installation id", without(instID), false},
		{"missing repository id", without(repoID), false},
		{"missing private key", without(privKey), false},
		{"whitespace-only private key", map[string]string{appID: "1", instID: "2", repoID: "3", privKey: "   "}, false},
		// The trim on the direct token is load-bearing the same way: a secret that resolved
		// to whitespace would otherwise start the live suite with an unusable token.
		{"whitespace-only direct api token", map[string]string{apiToken: "   "}, false},
		{"whitespace-only token with a complete app bundle still runs", func() map[string]string {
			m := map[string]string{apiToken: "  "}
			for k, v := range full {
				m[k] = v
			}
			return m
		}(), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCopilotCredentials(func(k string) string { return tt.env[k] })
			if got != tt.want {
				t.Fatalf("hasCopilotCredentials = %v, want %v", got, tt.want)
			}
		})
	}
}
