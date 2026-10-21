// Package workflows reads GitHub workflow definitions without executing them.
package workflows

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Workflow preserves fields needed for policy and the full job bodies for secret checks.
type Workflow struct {
	On          any            `yaml:"on"`
	Permissions any            `yaml:"permissions"`
	Jobs        map[string]Job `yaml:"jobs"`
}

// Job includes both executable steps and reusable workflow references.
type Job struct {
	Uses        string         `yaml:"uses"`
	Steps       []Step         `yaml:"steps"`
	Environment any            `yaml:"environment"`
	Other       map[string]any `yaml:",inline"`
}

// Step captures action references, commands, and runner policy inputs.
type Step struct {
	Uses  string         `yaml:"uses"`
	Run   string         `yaml:"run"`
	If    string         `yaml:"if"`
	With  map[string]any `yaml:"with"`
	Other map[string]any `yaml:",inline"`
}

// Triggers normalizes all three event syntaxes accepted by GitHub.
func Triggers(on any) map[string]any {
	switch v := on.(type) {
	case string:
		return map[string]any{v: nil}
	case map[string]any:
		return v
	case []any:
		result := map[string]any{}
		for _, event := range v {
			result[fmt.Sprint(event)] = nil
		}
		return result
	default:
		return map[string]any{}
	}
}

// Read rejects malformed YAML, duplicate keys, and multiple documents.
func Read(filename string) (Workflow, error) {
	var result Workflow
	f, err := os.Open(filename)
	if err != nil {
		return result, err
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return result, fmt.Errorf("expected one YAML document: %s", filename)
	}
	if len(result.Jobs) == 0 {
		return result, fmt.Errorf("no workflow jobs: %s", filename)
	}
	return result, nil
}

// Paths enumerates YAML definitions and fails closed on an empty inventory.
func Paths(root string) ([]string, error) {
	var paths []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no workflows in %s", root)
	}
	return paths, nil
}

// String reads a scalar input while leaving absent inputs empty.
func String(values map[string]any, key string) string {
	if value, ok := values[key]; ok {
		return fmt.Sprint(value)
	}
	return ""
}
