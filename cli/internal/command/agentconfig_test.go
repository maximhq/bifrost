package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/cli/internal/config"
)

// erroringWriter fails every write, simulating a broken output pipe.
type erroringWriter struct{ err error }

func (w erroringWriter) Write([]byte) (int, error) { return 0, w.err }

// TestRunAPIUsageWriteFailureReturnsError verifies a broken output pipe while
// printing the "bifrost api" usage text is reported.
func TestRunAPIUsageWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.runAPI(context.Background(), nil, nil); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

func TestAPIListRejectsUnexpectedArguments(t *testing.T) {
	runner := &Runner{Out: &bytes.Buffer{}, ErrOut: io.Discard}
	err := runner.listOperations(&environment{}, []string{"typo", "--include-deprecated"})
	if err == nil || !strings.Contains(err.Error(), "unexpected api list arguments") {
		t.Fatalf("error = %v, want unexpected api list arguments", err)
	}
}

// TestAPICallDeprecationWarningWriteFailureReturnsError verifies a broken
// stderr pipe while printing the deprecated-operation warning is reported,
// even though the underlying request itself succeeded.
func TestAPICallDeprecationWarningWriteFailureReturnsError(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(request, `{"ok":true}`), nil
	}))
	writeErr := errors.New("broken pipe")
	runner.ErrOut = erroringWriter{err: writeErr}
	err := runner.Run(context.Background(), []string{
		"api", "call", "activateAccessProfileLegacy", "--param", "profile_id=p1",
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("error = %v, want deprecated-warning write error", err)
	}
}

func TestResolveOperationPathSortsUnknownParameters(t *testing.T) {
	const want = "unknown path parameter(s): alpha, middle, zeta"
	for range 100 {
		_, err := resolveOperationPath("/v1/models", map[string]string{
			"zeta":   "1",
			"alpha":  "2",
			"middle": "3",
		})
		if err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
	}
}

// TestReadBodyFileEnforcesMaxInputBytes verifies the --file path rejects a
// body over maxInputBytes without fully buffering it via os.ReadFile, using
// the same bounded-read pattern already established for stdin.
func TestReadBodyFileEnforcesMaxInputBytes(t *testing.T) {
	runner := &Runner{}
	dir := t.TempDir()

	atLimit := filepath.Join(dir, "at-limit.json")
	if err := os.WriteFile(atLimit, make([]byte, maxInputBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := runner.readBody("", atLimit)
	if err != nil {
		t.Fatalf("expected a body at exactly maxInputBytes to be accepted: %v", err)
	}
	if len(body) != maxInputBytes {
		t.Fatalf("body length = %d, want %d", len(body), maxInputBytes)
	}

	overLimit := filepath.Join(dir, "over-limit.json")
	if err := os.WriteFile(overLimit, make([]byte, maxInputBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.readBody("", overLimit); err == nil {
		t.Fatal("expected a body over maxInputBytes to be rejected")
	}
}

// TestInferHelpWriteFailureReturnsError verifies a broken output pipe while
// printing "bifrost infer" usage is reported.
func TestInferHelpWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.Run(context.Background(), []string{"infer", "--help"}); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestRunHarnessHelpWriteFailureReturnsError verifies a broken output pipe
// while printing "bifrost run" usage is reported.
func TestRunHarnessHelpWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.Run(context.Background(), []string{"run", "--help"}); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestUsageHelpWriteFailureReturnsError verifies a broken output pipe while
// printing "bifrost usage" usage is reported.
func TestUsageHelpWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.Run(context.Background(), []string{"usage", "--help"}); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestCompletionWriteFailureReturnsError verifies a broken output pipe
// while generating a shell completion script is reported, for each shell.
func TestCompletionWriteFailureReturnsError(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
			if err := runner.Run(context.Background(), []string{"completion", shell}); err == nil {
				t.Fatal("expected a write failure while generating the completion script to be reported")
			}
		})
	}
}

// TestAuthHelpWriteFailureReturnsError verifies a broken output pipe while
// printing "bifrost auth" usage is reported.
func TestAuthHelpWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.Run(context.Background(), []string{"auth", "--help"}); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestSelectVirtualKeyWriteFailureReturnsError verifies a broken output pipe
// while printing the selection confirmation is reported.
func TestSelectVirtualKeyWriteFailureReturnsError(t *testing.T) {
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(request, `{"virtual_keys":[{"id":"vk-1","name":"Engineering","is_active":true}]}`), nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"auth", "select-virtual-key", "vk-1"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestClearVirtualKeySelectionWriteFailureReturnsError verifies a broken
// output pipe while printing the clear-selection confirmation is reported.
func TestClearVirtualKeySelectionWriteFailureReturnsError(t *testing.T) {
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("clear-selection unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"auth", "clear-selection"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestAuthLogoutWriteFailureReturnsError verifies a broken output pipe while
// printing the logout confirmation is reported.
func TestAuthLogoutWriteFailureReturnsError(t *testing.T) {
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("logout unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:agent-token"] = ""
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"auth", "logout"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestSetCredentialWriteFailureReturnsError verifies a broken output pipe
// while printing the "Stored" confirmation is reported.
func TestSetCredentialWriteFailureReturnsError(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("set-management-key unexpectedly called %s", request.URL)
		return nil, nil
	}))
	runner.In = strings.NewReader("bfst-test\n")
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"auth", "set-management-key", "--stdin"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestClearCredentialWriteFailureReturnsError verifies a broken output pipe
// while printing the "Removed" confirmation is reported.
func TestClearCredentialWriteFailureReturnsError(t *testing.T) {
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("clear-management-key unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:management-key"] = "bfst-test"
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"auth", "clear-management-key"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestAuthPasswordLoginWriteFailureReturnsError verifies a broken output
// pipe while printing the password-login confirmation is reported.
func TestAuthPasswordLoginWriteFailureReturnsError(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"token=session-test; Path=/; HttpOnly"}},
			Body: io.NopCloser(strings.NewReader(`{"message":"Login successful"}`)), Request: request,
		}, nil
	}))
	runner.In = strings.NewReader("password123\n")
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"auth", "login", "--username", "admin", "--password-stdin"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestLocalConfigUsageWriteFailureReturnsError verifies a broken output pipe
// while printing "bifrost config" usage is reported.
func TestLocalConfigUsageWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.Run(context.Background(), []string{"config", "--help"}); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestLocalConfigGetOneValueWriteFailureReturnsError verifies a broken
// output pipe while printing a single "config get <key>" value is reported.
func TestLocalConfigGetOneValueWriteFailureReturnsError(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("config get unexpectedly called %s", request.URL)
		return nil, nil
	}))
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"config", "get", "base-url"}); err == nil {
		t.Fatal("expected a write failure while printing the value to be reported")
	}
}

// TestLocalConfigSetWriteFailureReturnsError verifies a broken output pipe
// while printing the "config set" confirmation is reported.
func TestLocalConfigSetWriteFailureReturnsError(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("config set unexpectedly called %s", request.URL)
		return nil, nil
	}))
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"config", "set", "default-model", "gpt-test"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestLocalConfigUnsetWriteFailureReturnsError verifies a broken output pipe
// while printing the "config unset" confirmation is reported.
func TestLocalConfigUnsetWriteFailureReturnsError(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("config unset unexpectedly called %s", request.URL)
		return nil, nil
	}))
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"config", "unset", "default-model"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestAddContextWriteFailureReturnsError verifies a broken output pipe while
// printing the "Added context" confirmation is reported.
func TestAddContextWriteFailureReturnsError(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := &config.State{Selections: map[string]config.Selection{}}
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}, ErrOut: &bytes.Buffer{}}
	if err := runner.addContext(statePath, state, []string{"prod", "--base-url", "https://gateway.example"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestUseContextWriteFailureReturnsError verifies a broken output pipe while
// printing the "Using context" confirmation is reported.
func TestUseContextWriteFailureReturnsError(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := &config.State{
		Profiles:   []config.Profile{{ID: "prod", Name: "prod", BaseURL: "https://gateway.example"}},
		Selections: map[string]config.Selection{},
	}
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}, ErrOut: &bytes.Buffer{}}
	if err := runner.useContext(statePath, state, []string{"prod"}); err == nil {
		t.Fatal("expected a write failure while printing the confirmation to be reported")
	}
}

// TestReconfigureFailurePreservesReceiptAndDoesNotRevertToOriginal verifies
// that when an already-configured agent is reconfigured (e.g. changing the
// model) and the write fails, rollback does not revert all the way back to
// the very first pre-Bifrost snapshot, and the canonical receipt is kept
// intact rather than deleted, since the prior configuration this rolls back
// to is still valid and restorable via unconfigure. (The precise claim that
// rollback restores exactly the pre-attempt state, rather than merely "not
// pre-Bifrost," is covered directly by
// TestRollbackReconfigureFailureRestoresAttemptSnapshot below, since setting
// up a WriteNativeConfig failure here without also corrupting the file the
// pre-attempt snapshot itself reads is not controllable from outside.)
func TestReconfigureFailurePreservesReceiptAndDoesNotRevertToOriginal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	preBifrost := []byte(`{"theme":"original"}`)
	if err := os.WriteFile(settingsPath, preBifrost, 0o600); err != nil {
		t.Fatal(err)
	}

	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("configure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-first"}); err != nil {
		t.Fatal(err)
	}
	lastWorking, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(lastWorking, preBifrost) {
		t.Fatal("first configure did not change the settings file")
	}

	receiptPath := filepath.Join(filepath.Dir(runner.StatePath), "receipts", "test-claude.json")
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("expected a canonical receipt after the first successful configure: %v", err)
	}

	// Make the second configure attempt's WriteNativeConfig fail cleanly
	// (at the existing-settings parse step, before any write) by
	// corrupting the settings file, and use --force so verifyConfiguredFiles
	// doesn't reject the corrupted content before WriteNativeConfig is even
	// reached.
	if err := os.WriteFile(settingsPath, []byte("not valid json{{{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-second", "--force"}); err == nil {
		t.Fatal("expected the second configure attempt to fail")
	}

	afterRollback, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(afterRollback, preBifrost) {
		t.Fatalf("rollback reverted all the way to the pre-Bifrost state, losing the last working configuration: %s", afterRollback)
	}
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("expected the canonical receipt to be preserved after a failed reconfigure, stat err = %v", err)
	}
}

// TestRollbackReconfigureFailureRestoresAttemptSnapshot verifies the
// precise rollback target: rollbackReconfigureFailure restores exactly the
// snapshot captured immediately before the failed attempt, and does not
// touch (let alone delete) any canonical receipt.
func TestRollbackReconfigureFailureRestoresAttemptSnapshot(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	lastWorking := []byte(`{"model":"gpt-first"}`)
	if err := os.WriteFile(settingsPath, lastWorking, 0o600); err != nil {
		t.Fatal(err)
	}
	attemptSnapshot := &configReceipt{
		Version: 1, ContextID: "test", Agent: "claude",
		Files: []configSnapshot{{Path: settingsPath, Existed: true, Mode: 0o600, Original: lastWorking}},
	}
	// Simulate WriteNativeConfig having already modified the file to a
	// broken intermediate state before failing.
	if err := os.WriteFile(settingsPath, []byte("corrupted-mid-write"), 0o600); err != nil {
		t.Fatal(err)
	}

	writeErr := errors.New("write native config failed")
	err := rollbackReconfigureFailure(attemptSnapshot, writeErr)
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected the original write error to be returned, got %v", err)
	}
	restored, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(restored, lastWorking) {
		t.Fatalf("expected rollback to restore the pre-attempt snapshot, got %s", restored)
	}
}

// TestSnapshotAndRestorePreserveSymlink verifies a symlink-backed config
// file (e.g. one managed by a dotfiles tool) is captured as a symlink and
// recreated as one on restore, instead of being replaced by a plain file
// containing the target's bytes.
func TestSnapshotAndRestorePreserveSymlink(t *testing.T) {
	dir := t.TempDir()
	realTarget := filepath.Join(dir, "dotfiles", "claude-settings.json")
	if err := os.MkdirAll(filepath.Dir(realTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realTarget, []byte(`{"managed":"by-dotfiles"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "settings.json")
	if err := os.Symlink(realTarget, linkPath); err != nil {
		t.Fatal(err)
	}

	receipt, err := snapshotConfigFiles("test", "claude", []string{linkPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Files) != 1 || !receipt.Files[0].Existed {
		t.Fatalf("expected one existing snapshot, got %#v", receipt.Files)
	}

	// Overwrite the symlink with a plain file, simulating what
	// WriteNativeConfig's WriteAtomic (os.Rename) does to it.
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkPath, []byte(`{"configured":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := restoreConfigReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected the restored path to be a symlink, got mode %v", info.Mode())
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if target != realTarget {
		t.Fatalf("symlink target = %q, want %q", target, realTarget)
	}
}

// TestDeleteContextRemovesCredentialsAndProfileOnSuccess verifies the
// ordinary successful path still deletes credentials and persists state.
func TestDeleteContextRemovesCredentialsAndProfileOnSuccess(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	store := memorySecretStore{"ctx1:virtual-key": "sk-bf-test"}
	profile := config.Profile{ID: "ctx1", Name: "Test Context", BaseURL: "https://gateway.example"}
	state := &config.State{Profiles: []config.Profile{profile}, LastProfileID: "ctx1", Selections: map[string]config.Selection{}}

	outputBuffer := &bytes.Buffer{}
	runner := &Runner{Out: outputBuffer, ErrOut: &bytes.Buffer{}, Secrets: store}
	if err := runner.deleteContext(statePath, state, []string{"ctx1", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if got := store["ctx1:virtual-key"]; got != "" {
		t.Fatalf("virtual-key = %q, want deleted", got)
	}
	if len(state.Profiles) != 0 {
		t.Fatalf("profiles = %#v, want empty", state.Profiles)
	}
	if !strings.Contains(outputBuffer.String(), "Deleted context") {
		t.Fatalf("output = %q", outputBuffer.String())
	}
	saved, err := config.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Profiles) != 0 {
		t.Fatalf("saved profiles = %#v, want empty", saved.Profiles)
	}
}

// TestDeleteContextRestoresCredentialsWhenSaveStateFails verifies a context's
// keyring credentials are restored if config.SaveState fails after they were
// already deleted, since secrets.Delete is idempotent and a plain retry
// cannot otherwise recover them.
func TestDeleteContextRestoresCredentialsWhenSaveStateFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(blocker, "state.json")

	store := memorySecretStore{"ctx1:virtual-key": "sk-bf-test", "ctx1:management-key": "bfst-test"}
	profile := config.Profile{ID: "ctx1", Name: "Test Context", BaseURL: "https://gateway.example"}
	state := &config.State{Profiles: []config.Profile{profile}, LastProfileID: "ctx1", Selections: map[string]config.Selection{}}

	runner := &Runner{Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, Secrets: store}
	if err := runner.deleteContext(statePath, state, []string{"ctx1", "--yes"}); err == nil {
		t.Fatal("expected the state-save failure to be reported")
	}
	if got := store["ctx1:virtual-key"]; got != "sk-bf-test" {
		t.Fatalf("virtual-key = %q, want restored %q", got, "sk-bf-test")
	}
	if got := store["ctx1:management-key"]; got != "bfst-test" {
		t.Fatalf("management-key = %q, want restored %q", got, "bfst-test")
	}
}

// TestRunRequestUsageWriteFailureReturnsError verifies a broken output pipe
// while printing "bifrost request" usage is reported.
func TestRunRequestUsageWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.runRequest(context.Background(), nil, []string{"--help"}); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestRunResourcesUsageWriteFailureReturnsError verifies a broken output
// pipe while printing "bifrost resources" usage is reported.
func TestRunResourcesUsageWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.runResources("table", []string{"--help"}); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestPrintResourceHelpWriteFailureReturnsError verifies a broken output
// pipe while printing resource help (via either call site) is reported.
func TestPrintResourceHelpWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	descriptor := resourceRegistry["providers"]
	if err := runner.printResourceHelp("providers", descriptor); err == nil {
		t.Fatal("expected a write failure while printing resource help to be reported")
	}
}

// TestVersionCommandWriteFailureReturnsError verifies a broken output pipe
// while printing "bifrost version" is reported.
func TestVersionCommandWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}, Build: BuildInfo{Version: "test", Commit: "abc"}}
	if err := runner.Run(context.Background(), []string{"version"}); err == nil {
		t.Fatal("expected a write failure while printing the version to be reported")
	}
}

// TestHarnessAliasHelpWriteFailureReturnsError verifies a broken output pipe
// while printing a harness alias's offline help is reported.
func TestHarnessAliasHelpWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}, StatePath: filepath.Join(t.TempDir(), "state.json")}
	if err := runner.Run(context.Background(), []string{"claude", "--help"}); err == nil {
		t.Fatal("expected a write failure while printing harness help to be reported")
	}
}

// TestHTTPCommandUsageWriteFailureReturnsError verifies a broken output pipe
// while printing "bifrost http" usage is reported.
func TestHTTPCommandUsageWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.Run(context.Background(), []string{"http", "--help"}); err == nil {
		t.Fatal("expected a write failure while printing http usage to be reported")
	}
}

// TestPrintHelpWriteFailureReturnsError verifies a broken output pipe while
// printing the top-level help overview is reported.
func TestPrintHelpWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.Run(context.Background(), []string{"help"}); err == nil {
		t.Fatal("expected a write failure while printing help to be reported")
	}
}

// TestConfigureHarnessUsageWriteFailureReturnsError verifies a broken output
// pipe while printing usage is reported instead of a false success.
func TestConfigureHarnessUsageWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.configureHarness(nil, nil); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestUnconfigureHarnessUsageWriteFailureReturnsError mirrors the configure case.
func TestUnconfigureHarnessUsageWriteFailureReturnsError(t *testing.T) {
	runner := &Runner{Out: erroringWriter{err: errors.New("broken pipe")}}
	if err := runner.unconfigureHarness(nil, nil); err == nil {
		t.Fatal("expected a write failure while printing usage to be reported")
	}
}

// TestConfigureHarnessSuccessMessageWriteFailureReturnsError verifies a
// broken output pipe while printing the final confirmation is reported,
// even though the configuration itself succeeded.
func TestConfigureHarnessSuccessMessageWriteFailureReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("configure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-test"}); err == nil {
		t.Fatal("expected a write failure while printing the success message to be reported")
	}
}

// TestUnconfigureHarnessSuccessMessageWriteFailureReturnsError mirrors the
// configure case for the restore confirmation message.
func TestUnconfigureHarnessSuccessMessageWriteFailureReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unconfigure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	runner.Out = erroringWriter{err: errors.New("broken pipe")}
	if err := runner.Run(context.Background(), []string{"unconfigure", "claude"}); err == nil {
		t.Fatal("expected a write failure while printing the restore confirmation to be reported")
	}
}

// TestRollbackFailedConfigurePreservesReceiptWhenRollbackFails verifies the
// only backup of the user's original config is never deleted when rollback
// itself fails after a failed configuration write.
func TestRollbackFailedConfigurePreservesReceiptWhenRollbackFails(t *testing.T) {
	dir := t.TempDir()
	// blocker is a regular file, so os.MkdirAll(filepath.Dir(...)) inside
	// restoreConfigReceipt fails for any path nested under it, simulating a
	// rollback failure.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(dir, "receipt.json")
	receipt := &configReceipt{
		Version: 1, ContextID: "test", Agent: "claude",
		Files: []configSnapshot{{
			Path:    filepath.Join(blocker, "settings.json"),
			Existed: true, Mode: 0o600, Original: []byte(`{"original":true}`),
		}},
	}
	if err := saveConfigReceipt(receiptPath, receipt); err != nil {
		t.Fatal(err)
	}

	writeErr := errors.New("write native config failed")
	err := rollbackFailedConfigure(receipt, receiptPath, writeErr)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected the original write error to be wrapped, got %v", err)
	}
	if _, statErr := os.Stat(receiptPath); statErr != nil {
		t.Fatalf("receipt was deleted despite a failed rollback: %v", statErr)
	}
}

// TestRollbackFailedConfigureRemovesReceiptWhenRollbackSucceeds verifies the
// receipt is still cleaned up on the ordinary path, where rollback succeeds.
func TestRollbackFailedConfigureRemovesReceiptWhenRollbackSucceeds(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"configured":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(dir, "receipt.json")
	receipt := &configReceipt{
		Version: 1, ContextID: "test", Agent: "claude",
		Files: []configSnapshot{{
			Path: settingsPath, Existed: true, Mode: 0o600, Original: []byte(`{"original":true}`),
		}},
	}
	if err := saveConfigReceipt(receiptPath, receipt); err != nil {
		t.Fatal(err)
	}

	writeErr := errors.New("write native config failed")
	err := rollbackFailedConfigure(receipt, receiptPath, writeErr)
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected the original write error to be returned, got %v", err)
	}
	restored, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(restored) != `{"original":true}` {
		t.Fatalf("settings were not restored: %s", restored)
	}
	if _, statErr := os.Stat(receiptPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected receipt to be removed after successful rollback, stat err = %v", statErr)
	}
}
