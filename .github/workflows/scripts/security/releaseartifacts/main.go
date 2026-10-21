// Command releaseartifacts binds the full native release inventory to its source.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

var platforms = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"}
var binaries = map[string]string{"cli": "bifrost", "bifrost-migration-cli": "bifrost-migration-cli"}

// manifest binds all publishable bytes to canonical source and version identifiers.
type manifest struct {
	Schema       int               `json:"schema"`
	Repository   string            `json:"repository"`
	RepositoryID int64             `json:"repository_id"`
	Component    string            `json:"component"`
	Version      string            `json:"version"`
	SourceSHA    string            `json:"source_sha"`
	Files        map[string]string `json:"files"`
}

// digest streams regular files without accepting devices or symlinks.
func digest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular artifact: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// inventory requires every platform binary and checksum and rejects added payloads.
func inventory(root, component string) (map[string]string, error) {
	expected := map[string]bool{"version.txt": true}
	for _, platform := range platforms {
		name := platform + "/" + binaries[component]
		if strings.HasPrefix(platform, "windows/") {
			name += ".exe"
		}
		expected[name] = true
		expected[name+".sha256"] = true
	}
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact symlink: %s", path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("nonregular artifact: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if name != "release.json" {
			found[name] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(expected, found) {
		return nil, fmt.Errorf("release inventory does not match the supported platform set")
	}
	hashes := map[string]string{}
	for name := range expected {
		sum, err := digest(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		hashes[name] = sum
		if expected[name+".sha256"] {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name+".sha256")))
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(string(data)) != sum+"  "+filepath.Base(name) {
				return nil, fmt.Errorf("binary checksum mismatch: %s", name)
			}
		}
	}
	return hashes, nil
}

// writeRegular refuses existing symlinks before replacing generated metadata.
func writeRegular(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular metadata file: %s", path)
	}
	return os.WriteFile(path, data, 0644)
}

// process validates identity and inventory before creating or comparing the manifest.
func process(mode, root, component, version, commit string) error {
	if mode != "create" && mode != "verify" {
		return fmt.Errorf("expected create or verify")
	}
	if binaries[component] == "" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		return fmt.Errorf("invalid component or source commit")
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`).MatchString(version) {
		return fmt.Errorf("invalid release version")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("artifact root must be a real directory")
	}
	if mode == "create" {
		if err := writeRegular(filepath.Join(root, "version.txt"), []byte("v"+version)); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "version.txt"))
	if err != nil {
		return err
	}
	if string(data) != "v"+version {
		return fmt.Errorf("artifact version does not match release")
	}
	hashes, err := inventory(root, component)
	if err != nil {
		return err
	}
	expected := manifest{1, "maximhq/bifrost", 951115072, component, version, commit, hashes}
	target := filepath.Join(root, "release.json")
	if mode == "create" {
		data, err = json.MarshalIndent(expected, "", "  ")
		if err != nil {
			return err
		}
		return writeRegular(target, append(data, '\n'))
	}
	f, err := os.Open(target)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	var actual manifest
	if err := decoder.Decode(&actual); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing manifest data")
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("release manifest does not match source or artifact bytes")
	}
	return nil
}

// main requires explicit immutable release coordinates.
func main() {
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: releaseartifacts create|verify ROOT COMPONENT VERSION COMMIT")
		os.Exit(2)
	}
	if err := process(os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
