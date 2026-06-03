package gigachat_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
)

func TestGigachat(t *testing.T) {
	t.Parallel()

	validateGigaChatComprehensiveEnv(t)
	if !hasGigaChatComprehensiveAuthEnv() {
		t.Skip("Skipping GigaChat comprehensive tests because GIGACHAT_ACCESS_TOKEN, GIGACHAT_CREDENTIALS, or GIGACHAT_USER+GIGACHAT_PASSWORD+GIGACHAT_BASE_URL is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	t.Run("GigachatTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, llmtests.GigaChatComprehensiveTestConfig())
	})
}

func hasGigaChatComprehensiveAuthEnv() bool {
	if strings.TrimSpace(os.Getenv("GIGACHAT_ACCESS_TOKEN")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("GIGACHAT_CREDENTIALS")) != "" {
		return true
	}
	return strings.TrimSpace(os.Getenv("GIGACHAT_USER")) != "" &&
		strings.TrimSpace(os.Getenv("GIGACHAT_PASSWORD")) != "" &&
		strings.TrimSpace(os.Getenv("GIGACHAT_BASE_URL")) != ""
}

func validateGigaChatComprehensiveEnv(t *testing.T) {
	t.Helper()

	hasCertFile := strings.TrimSpace(os.Getenv("GIGACHAT_CERT_FILE")) != ""
	hasKeyFile := strings.TrimSpace(os.Getenv("GIGACHAT_KEY_FILE")) != ""
	if hasCertFile != hasKeyFile {
		t.Fatal("GIGACHAT_CERT_FILE and GIGACHAT_KEY_FILE must be set together")
	}

	hasUser := strings.TrimSpace(os.Getenv("GIGACHAT_USER")) != ""
	hasPassword := strings.TrimSpace(os.Getenv("GIGACHAT_PASSWORD")) != ""
	if hasUser != hasPassword {
		t.Fatal("GIGACHAT_USER and GIGACHAT_PASSWORD must be set together")
	}
	if hasUser && strings.TrimSpace(os.Getenv("GIGACHAT_BASE_URL")) == "" {
		t.Fatal("GIGACHAT_BASE_URL must be set when using GIGACHAT_USER and GIGACHAT_PASSWORD")
	}
}
