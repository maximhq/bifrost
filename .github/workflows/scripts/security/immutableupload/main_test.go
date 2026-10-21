package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestConditionalCreation refuses substitutions and never retries as an overwrite.
func TestConditionalCreation(t *testing.T) {
	for _, status := range []string{"new", "identical", "substituted", "denied"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "binary"), []byte("approved"), 0600); err != nil {
				t.Fatal(err)
			}
			puts, gets := 0, 0
			run := func(args []string) (string, error) {
				if args[1] == "put-object" {
					puts++
					i := slices.Index(args, "--if-none-match")
					if i < 0 || i+1 >= len(args) || args[i+1] != "*" {
						t.Fatal("nonconditional upload", args)
					}
					if status == "new" {
						return "", nil
					}
					if status == "denied" {
						return "AccessDenied", errors.New("denied")
					}
					return "PreconditionFailed", errors.New("exists")
				}
				gets++
				body := "injected"
				if status == "identical" {
					body = "approved"
				}
				return "", os.WriteFile(args[len(args)-1], []byte(body), 0600)
			}
			err := upload(root, "bifrost/v1.2.3", map[string]string{"R2_BUCKET": "fixture", "R2_ENDPOINT": "https://fixture.invalid"}, run)
			if (status == "new" || status == "identical") != (err == nil) {
				t.Fatalf("unexpected result: %v", err)
			}
			if puts != 1 {
				t.Fatalf("repeated put: %d", puts)
			}
			if (status == "new" || status == "denied") && gets != 0 {
				t.Fatal("unexpected read after non-conflict")
			}
		})
	}
}

// TestInvalidUploadsNeverContactStorage checks mutable prefixes, symlinks, and empty roots.
func TestInvalidUploadsNeverContactStorage(t *testing.T) {
	run := func([]string) (string, error) { t.Fatal("unexpected storage access"); return "", nil }
	env := map[string]string{"R2_BUCKET": "fixture", "R2_ENDPOINT": "https://fixture.invalid"}
	root := t.TempDir()
	if err := upload(root, "bifrost/latest", env, run); err == nil {
		t.Fatal("mutable prefix accepted")
	}
	if err := upload(root, "bifrost/v1.2.3", env, run); err == nil {
		t.Fatal("empty upload accepted")
	}
	if err := os.Symlink("outside", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := upload(root, "bifrost/v1.2.3", env, run); err == nil {
		t.Fatal("symlink accepted")
	}
}
