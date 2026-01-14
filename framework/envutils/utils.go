package envutils

import (
	"fmt"
	"os"
	"strings"
)

// ProcessEnvValue processes a value that might be an environment variable reference
func ProcessEnvValue(value string) (string, error) {
	v := strings.TrimSpace(value)
	if !strings.HasPrefix(v, "env.") {
		return value, nil
	}
	envKey := strings.TrimSpace(strings.TrimPrefix(v, "env."))
	if envKey == "" {
		return "", fmt.Errorf("environment variable name missing in %q", value)
	}
	if envValue, ok := os.LookupEnv(envKey); ok {
		return envValue, nil
	}
	return "", fmt.Errorf("environment variable %s not found", envKey)
}
