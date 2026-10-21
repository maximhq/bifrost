// Package ghapi makes read-only GitHub API calls through the authenticated gh CLI.
package ghapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// ErrNotFound lets callers treat an absent resource as a verified state rather than an outage.
var ErrNotFound = errors.New("not found")

// Read decodes one endpoint and never prints tokens or response bodies.
func Read(endpoint string, target any, headers ...string) error {
	args := []string{"api", endpoint}
	for _, header := range headers {
		args = append(args, "-H", header)
	}
	data, err := exec.Command("gh", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && bytes.Contains(exit.Stderr, []byte("HTTP 404")) {
		return fmt.Errorf("%s: %w", endpoint, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("could not read %s: %w", endpoint, err)
	}
	return json.Unmarshal(data, target)
}
