// Command immutableupload atomically creates versioned R2 objects without overwriting.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// runner separates AWS invocation from deterministic offline tests.
type runner func(args []string) (string, error)

// digest hashes regular files in bounded memory.
func digest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular object: %s", path)
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

// upload permits only conditional creates and byte-identical retries.
func upload(root, prefix string, environment map[string]string, run runner) error {
	if !regexp.MustCompile(`^(bifrost|bifrost-cli|bifrost-migration-cli)/v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`).MatchString(prefix) {
		return fmt.Errorf("expected a versioned release prefix")
	}
	if environment["R2_BUCKET"] == "" || environment["R2_ENDPOINT"] == "" {
		return fmt.Errorf("R2 bucket and endpoint are required")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("release root must be a real directory")
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release upload cannot follow symlinks")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("nonregular upload object")
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("empty release upload")
	}
	profile := environment["R2_AWS_PROFILE"]
	if profile == "" {
		profile = "R2"
	}
	options := []string{"--bucket", environment["R2_BUCKET"], "--endpoint-url", environment["R2_ENDPOINT"], "--profile", profile}
	for _, path := range files {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := prefix + "/" + filepath.ToSlash(relative)
		args := append([]string{"s3api", "put-object"}, options...)
		args = append(args, "--key", key, "--body", path, "--if-none-match", "*", "--cache-control", "public,max-age=31536000,immutable")
		output, err := run(args)
		if err == nil {
			continue
		}
		if !strings.Contains(output, "PreconditionFailed") && !strings.Contains(output, "(412)") {
			return fmt.Errorf("conditional upload failed for %s: %s", key, output)
		}
		if err := compareExisting(path, key, options, run); err != nil {
			return err
		}
		fmt.Printf("Identical release object already exists: %s\n", key)
	}
	return nil
}

// compareExisting never retries a failed conditional create as an overwrite.
func compareExisting(source, key string, options []string, run runner) error {
	directory, err := os.MkdirTemp("", "bifrost-existing-object-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	existing := filepath.Join(directory, "existing")
	args := append([]string{"s3api", "get-object"}, options...)
	args = append(args, "--key", key, existing)
	if _, err = run(args); err != nil {
		return fmt.Errorf("could not verify existing release object: %s", key)
	}
	actual, err := digest(existing)
	if err != nil {
		return err
	}
	expected, err := digest(source)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("refusing to replace existing release object: %s", key)
	}
	return nil
}

// aws captures command output without printing credentials or configuration.
func aws(args []string) (string, error) {
	data, err := exec.Command("aws", args...).CombinedOutput()
	return string(data), err
}

// main obtains only the required storage configuration from the environment.
func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: immutableupload ROOT VERSIONED_PREFIX")
		os.Exit(2)
	}
	environment := map[string]string{}
	for _, key := range []string{"R2_BUCKET", "R2_ENDPOINT", "R2_AWS_PROFILE"} {
		environment[key] = os.Getenv(key)
	}
	if err := upload(os.Args[1], os.Args[2], environment, aws); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
