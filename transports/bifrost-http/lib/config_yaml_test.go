package lib

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_YAML(t *testing.T) {
	dir := createTempDir(t)

	yamlContent := `
client:
  initial_pool_size: 50
providers:
  openai:
    keys:
      - &common
        name: common-key
        value: sk-test-123
        models:
          - gpt-4
      - <<: *common
        name: specific-key
`
	err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlContent), 0644)
	if err != nil {
		t.Fatalf("failed to write yaml file: %v", err)
	}

	config, err := LoadConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if config.ClientConfig.InitialPoolSize != 50 {
		t.Errorf("expected InitialPoolSize 50, got %d", config.ClientConfig.InitialPoolSize)
	}

	openai, ok := config.Providers["openai"]
	if !ok {
		t.Fatal("openai provider not found")
	}

	if len(openai.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(openai.Keys))
	}

	// Order might vary depending on how YAML unmarshal handles keys map if it were a map, but keys is a list here
	// The order in list should be preserved.

	// First key
	if openai.Keys[0].Name != "common-key" {
		t.Errorf("expected key 0 name common-key, got %s", openai.Keys[0].Name)
	}
	if openai.Keys[0].Value != "sk-test-123" {
		t.Errorf("expected key 0 value sk-test-123, got %s", openai.Keys[0].Value)
	}

	// Second key (merged)
	if openai.Keys[1].Name != "specific-key" {
		t.Errorf("expected key 1 name specific-key, got %s", openai.Keys[1].Name)
	}
	if openai.Keys[1].Value != "sk-test-123" {
		// Should inherit from anchor
		t.Errorf("expected key 1 value sk-test-123, got %s", openai.Keys[1].Value)
	}
}

func TestLoadConfig_YAML_Precedence(t *testing.T) {
	dir := createTempDir(t)

	// Create config.json
	err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"client":{"initial_pool_size": 10}}`), 0644)
	if err != nil {
		t.Fatalf("failed to write json file: %v", err)
	}

	// Create config.yaml
	err = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
client:
  initial_pool_size: 20
`), 0644)
	if err != nil {
		t.Fatalf("failed to write yaml file: %v", err)
	}

	config, err := LoadConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Should prefer config.json
	if config.ClientConfig.InitialPoolSize != 10 {
		t.Errorf("expected InitialPoolSize 10 (from json), got %d", config.ClientConfig.InitialPoolSize)
	}

	// Remove json and retry
	os.Remove(filepath.Join(dir, "config.json"))

	config, err = LoadConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Should fallback to config.yaml
	if config.ClientConfig.InitialPoolSize != 20 {
		t.Errorf("expected InitialPoolSize 20 (from yaml), got %d", config.ClientConfig.InitialPoolSize)
	}
}
