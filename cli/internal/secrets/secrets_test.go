package secrets

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func restoreKeyringFunctions(t *testing.T) {
	t.Helper()
	get, set, remove := keyringGet, keyringSet, keyringDelete
	t.Cleanup(func() {
		keyringGet, keyringSet, keyringDelete = get, set, remove
	})
}

func TestKeyForProfileSeparatesKindsAndProfiles(t *testing.T) {
	if got := keyForProfile("engineering", VirtualKey); got != "profile:engineering:virtual-key" {
		t.Fatalf("key = %q", got)
	}
	if keyForProfile("engineering", VirtualKey) == keyForProfile("production", VirtualKey) ||
		keyForProfile("engineering", VirtualKey) == keyForProfile("engineering", ManagementKey) {
		t.Fatal("profile key does not isolate profile and credential kind")
	}
}

func TestGetTreatsMissingCredentialAsEmpty(t *testing.T) {
	restoreKeyringFunctions(t)
	keyringGet = func(string, string) (string, error) { return "", keyring.ErrNotFound }
	value, err := Get("engineering", AgentToken)
	if err != nil || value != "" {
		t.Fatalf("Get() = %q, %v", value, err)
	}
}

func TestGetWrapsBackendFailure(t *testing.T) {
	restoreKeyringFunctions(t)
	keyringGet = func(string, string) (string, error) { return "", errors.New("backend unavailable") }
	_, err := Get("engineering", AgentToken)
	if err == nil || !strings.Contains(err.Error(), "read agent-token") || !strings.Contains(err.Error(), "backend unavailable") {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestSetTrimsCredentialAndEmptyDeletes(t *testing.T) {
	restoreKeyringFunctions(t)
	var stored, deleted string
	keyringSet = func(_, account, value string) error {
		stored = account + "=" + value
		return nil
	}
	keyringDelete = func(_, account string) error {
		deleted = account
		return keyring.ErrNotFound
	}
	if err := Set("engineering", VirtualKey, "  sk-bf-test  "); err != nil {
		t.Fatal(err)
	}
	if stored != "profile:engineering:virtual-key=sk-bf-test" {
		t.Fatalf("stored = %q", stored)
	}
	if err := Set("engineering", VirtualKey, "  "); err != nil {
		t.Fatal(err)
	}
	if deleted != "profile:engineering:virtual-key" {
		t.Fatalf("deleted = %q", deleted)
	}
}
