package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bytedance/sonic"
)

const (
	defaultDirName  = ".bifrost"
	defaultFileName = "config.json"
	stateFileName   = "state.json"
)

// FileConfig represents the on-disk bifrost configuration file (~/.bifrost/config.json).
type FileConfig struct {
	BaseURL            string `json:"base_url"`
	VirtualKey         string `json:"virtual_key"`
	DefaultHarness     string `json:"default_harness"`
	DefaultModel       string `json:"default_model"`
	AutoInstallHarness *bool  `json:"auto_install_harness"`
	AutoAttachMCP      *bool  `json:"auto_attach_mcp"`
}

// Profile represents a named Bifrost connection profile with a base URL.
type Profile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
}

// Selection stores the user's last chosen harness and model for a profile.
type Selection struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
}

// State holds the persistent runtime state including profiles and per-profile selections.
type State struct {
	Profiles         []Profile            `json:"profiles"`
	LastProfileID    string               `json:"last_profile_id"`
	Selections       map[string]Selection `json:"selections"`
	LastVersionCheck int64                `json:"last_version_check,omitempty"`
	LastKnownVersion string               `json:"last_known_version,omitempty"`
}

// DefaultConfigPath returns the default path to the bifrost config file (~/.bifrost/config.json).
func DefaultConfigPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(h, defaultDirName, defaultFileName), nil
}

// DefaultStatePath returns the default path to the bifrost state file (~/.bifrost/state.json).
func DefaultStatePath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(h, defaultDirName, stateFileName), nil
}

// LoadFile reads and parses a config file from disk.
// Returns nil with no error if the file does not exist.
func LoadFile(path string) (*FileConfig, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read config file: %w", err)
	}

	var c FileConfig
	if err := sonic.Unmarshal(b, &c); err != nil {
		return nil, "", fmt.Errorf("parse config file: %w", err)
	}

	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	return &c, abs, nil
}

// LoadState reads and parses the state file from disk.
// Returns a fresh State if the file does not exist.
func LoadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{Selections: map[string]Selection{}}, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}

	var s State
	if err := sonic.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if s.Selections == nil {
		s.Selections = map[string]Selection{}
	}
	return &s, nil
}

// WriteAtomic writes data to a temp file in the same directory and atomically
// replaces the target path via rename, preventing partial/corrupt files on crash.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// SaveState writes the state to disk, creating parent directories as needed.
func SaveState(path string, s *State) error {
	d := filepath.Dir(path)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	b, err := sonic.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := WriteAtomic(path, b, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// SaveConfig writes the config to disk at the given path, creating parent
// directories as needed. File permissions are set to 0o600.
func SaveConfig(path string, c *FileConfig) error {
	d := filepath.Dir(path)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	persisted := *c
	persisted.VirtualKey = ""
	b, err := sonic.MarshalIndent(&persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := WriteAtomic(path, b, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// ProfileByID returns a pointer to the profile with the given ID, or nil if not found.
func (s *State) ProfileByID(id string) *Profile {
	for i := range s.Profiles {
		if s.Profiles[i].ID == id {
			return &s.Profiles[i]
		}
	}
	return nil
}
