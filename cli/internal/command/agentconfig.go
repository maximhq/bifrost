package command

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maximhq/bifrost/cli/internal/apis"
	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/harness"
)

// configReceipt records enough state to safely undo explicit agent configuration.
type configReceipt struct {
	Version   int              `json:"version"`
	ContextID string           `json:"context_id"`
	Agent     string           `json:"agent"`
	CreatedAt time.Time        `json:"created_at"`
	Pending   bool             `json:"pending"`
	Files     []configSnapshot `json:"files"`
}

// configSnapshot records the original and configured state of one exact file.
// If LinkTarget is set, Path was a symlink rather than a regular file: only
// the link target is captured (Original/Mode/ConfiguredSHA256 don't apply),
// and restoration recreates the symlink instead of writing byte content.
type configSnapshot struct {
	Path             string `json:"path"`
	Existed          bool   `json:"existed"`
	Mode             uint32 `json:"mode"`
	Original         []byte `json:"original,omitempty"`
	ConfiguredSHA256 string `json:"configured_sha256,omitempty"`
	LinkTarget       string `json:"link_target,omitempty"`
}

// configureHarness explicitly writes reversible native configuration for an agent.
func (r *Runner) configureHarness(env *environment, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(r.Out, "Usage: bifrost configure <claude|codex> [--model MODEL] [--force]\n")
		return err
	}
	agent, ok := harness.Get(args[0])
	if !ok || agent.WriteNativeConfig == nil {
		return fmt.Errorf("agent %q does not support persistent Bifrost configuration", args[0])
	}
	fs := flag.NewFlagSet("bifrost configure "+agent.ID, flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var model string
	var force bool
	fs.StringVar(&model, "model", defaultModel(env), "default model")
	fs.BoolVar(&force, "force", false, "replace files changed since the last Bifrost configuration")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected configure arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(env.VirtualKey) == "" {
		return fmt.Errorf("a virtual key is required; run 'bifrost auth set-virtual-key'")
	}
	endpoint, err := apis.BuildEndpoint(env.Client.BaseURL, agent.BasePath)
	if err != nil {
		return err
	}
	paths, err := nativeConfigPaths(agent.ID)
	if err != nil {
		return err
	}
	receiptPath := r.agentReceiptPath(env, agent.ID)
	receipt, err := loadConfigReceipt(receiptPath)
	if err != nil {
		return err
	}
	reconfiguring := receipt != nil && !receipt.Pending
	if receipt != nil {
		if !receiptMatchesManifest(receipt, env.ProfileID, agent.ID, paths) {
			return fmt.Errorf("configuration receipt for %s in context %q does not match the current file manifest; discard it with 'bifrost unconfigure %s --force' before reconfiguring", agent.Label, env.ProfileID, agent.ID)
		}
		if receipt.Pending && !force {
			return fmt.Errorf("a previous configuration attempt for %s did not complete; rerun with --force to continue, or 'bifrost unconfigure %s --force' to discard it", agent.Label, agent.ID)
		}
		if err := verifyConfiguredFiles(receipt, force); err != nil {
			return err
		}
	} else {
		receipt, err = snapshotConfigFiles(env.ProfileID, agent.ID, paths)
		if err != nil {
			return err
		}
		if err := saveConfigReceipt(receiptPath, receipt); err != nil {
			return err
		}
	}
	// Reconfiguring an already-configured agent (receipt.Original still
	// holds the very first pre-Bifrost snapshot) needs its own rollback
	// target: the state immediately before THIS attempt, not the original
	// one. Otherwise a failed reconfigure would revert all the way back to
	// pre-Bifrost and delete the canonical receipt, losing a prior working
	// configuration and its own restore path.
	var attemptSnapshot *configReceipt
	if reconfiguring {
		attemptSnapshot, err = snapshotConfigFiles(env.ProfileID, agent.ID, paths)
		if err != nil {
			return err
		}
	}
	if err := agent.WriteNativeConfig(endpoint, env.VirtualKey, strings.TrimSpace(model)); err != nil {
		if reconfiguring {
			return rollbackReconfigureFailure(attemptSnapshot, err)
		}
		return rollbackFailedConfigure(receipt, receiptPath, err)
	}
	if err := recordConfiguredHashes(receipt); err != nil {
		return fmt.Errorf("configuration was written but its restoration receipt could not be finalized: %w", err)
	}
	receipt.Pending = false
	if err := saveConfigReceipt(receiptPath, receipt); err != nil {
		return fmt.Errorf("configuration was written but its restoration receipt could not be finalized: %w", err)
	}
	_, err = fmt.Fprintf(r.Out, "Configured %s for context %q. Restore it with 'bifrost unconfigure %s'.\n", agent.Label, env.ProfileID, agent.ID)
	return err
}

// receiptMatchesManifest reports whether receipt was captured for the same
// context, agent, and exact set of file paths currently in effect. A
// mismatch (e.g. a changed $HOME between runs) means the receipt's captured
// originals no longer correspond to the files about to be configured, so it
// cannot be trusted to restore them correctly.
func receiptMatchesManifest(receipt *configReceipt, profileID, agentID string, paths []string) bool {
	if receipt.ContextID != profileID || receipt.Agent != agentID || len(receipt.Files) != len(paths) {
		return false
	}
	for index, path := range paths {
		if receipt.Files[index].Path != path {
			return false
		}
	}
	return true
}

// rollbackFailedConfigure restores original files after a failed
// WriteNativeConfig call. The receipt is the only backup of the user's
// pre-configuration state, so it is preserved for manual recovery whenever
// rollback itself fails, and removed only once rollback has succeeded.
func rollbackFailedConfigure(receipt *configReceipt, receiptPath string, writeErr error) error {
	if rollbackErr := restoreConfigReceipt(receipt); rollbackErr != nil {
		return fmt.Errorf("configuration failed (%w) and could not be rolled back (%w); the restoration receipt was preserved at %s for manual recovery", writeErr, rollbackErr, receiptPath)
	}
	if removeErr := os.Remove(receiptPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("configuration failed (%w) and was rolled back, but the receipt could not be removed: %w", writeErr, removeErr)
	}
	return writeErr
}

// rollbackReconfigureFailure restores the state captured immediately before
// a failed attempt to reconfigure an already-configured agent, preserving
// the existing canonical receipt: the prior configuration this rolls back
// to is still valid and remains restorable via unconfigure.
func rollbackReconfigureFailure(attemptSnapshot *configReceipt, writeErr error) error {
	if rollbackErr := restoreConfigReceipt(attemptSnapshot); rollbackErr != nil {
		return fmt.Errorf("configuration failed (%w) and could not be rolled back to the last working configuration (%w)", writeErr, rollbackErr)
	}
	return writeErr
}

// unconfigureHarness restores the exact files captured before explicit configuration.
func (r *Runner) unconfigureHarness(env *environment, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(r.Out, "Usage: bifrost unconfigure <claude|codex> [--force]\n")
		return err
	}
	agentID := args[0]
	if agent, ok := harness.Get(agentID); !ok || agent.WriteNativeConfig == nil {
		return fmt.Errorf("agent %q does not support persistent Bifrost configuration", agentID)
	}
	fs := flag.NewFlagSet("bifrost unconfigure "+agentID, flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var force bool
	fs.BoolVar(&force, "force", false, "restore even if an agent config changed after configuration")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected unconfigure arguments: %s", strings.Join(fs.Args(), " "))
	}
	receiptPath := r.agentReceiptPath(env, agentID)
	receipt, err := loadConfigReceipt(receiptPath)
	if err != nil {
		return err
	}
	if receipt == nil {
		return fmt.Errorf("no configuration receipt exists for %q in context %q", agentID, env.ProfileID)
	}
	if receipt.ContextID != env.ProfileID || receipt.Agent != agentID {
		return fmt.Errorf("configuration receipt for %q in context %q does not match", agentID, env.ProfileID)
	}
	paths, err := nativeConfigPaths(agentID)
	if err != nil {
		return err
	}
	if !receiptMatchesManifest(receipt, env.ProfileID, agentID, paths) {
		if !force {
			return fmt.Errorf("configuration receipt for %s in context %q has a stale file manifest; rerun with --force to discard it without restoring files", agentID, env.ProfileID)
		}
		if err := os.Remove(receiptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("discard stale configuration receipt: %w", err)
		}
		_, err = fmt.Fprintf(r.Out, "Discarded stale %s configuration receipt without restoring files.\n", agentID)
		return err
	}
	if receipt.Pending {
		if !force {
			return fmt.Errorf("a previous configuration attempt for %q did not complete; rerun with --force to restore the captured originals", agentID)
		}
	} else if err := verifyConfiguredFiles(receipt, force); err != nil {
		return err
	}
	if err := restoreConfigReceipt(receipt); err != nil {
		return err
	}
	if err := os.Remove(receiptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove configuration receipt: %w", err)
	}
	_, err = fmt.Fprintf(r.Out, "Restored the pre-Bifrost %s configuration.\n", agentID)
	return err
}

// nativeConfigPaths resolves the exact files changed by persistent agent configuration.
func nativeConfigPaths(agentID string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	switch agentID {
	case "claude":
		return []string{filepath.Join(home, ".claude", "settings.json")}, nil
	case "codex":
		return []string{filepath.Join(home, ".codex", "auth.json"), filepath.Join(home, ".codex", "config.toml")}, nil
	default:
		return nil, fmt.Errorf("agent %q has no persistent configuration manifest", agentID)
	}
}

// agentReceiptPath returns the receipt path for one context and agent pair.
func (r *Runner) agentReceiptPath(env *environment, agentID string) string {
	return filepath.Join(filepath.Dir(env.StatePath), "receipts", env.ProfileID+"-"+agentID+".json")
}

// snapshotConfigFiles captures original files before any configuration mutation.
func snapshotConfigFiles(contextID, agentID string, paths []string) (*configReceipt, error) {
	receipt := &configReceipt{Version: 1, ContextID: contextID, Agent: agentID, CreatedAt: time.Now().UTC(), Pending: true}
	for _, path := range paths {
		snapshot := configSnapshot{Path: path, Mode: 0o600}
		// Check for a symlink first (Lstat, not Stat) so a symlink-backed
		// config file (e.g. one managed by a dotfiles tool) is captured as
		// a link rather than by reading through to its target's bytes —
		// WriteAtomic's os.Rename would otherwise replace the symlink
		// itself with a plain file on restore.
		linkInfo, lstatErr := os.Lstat(path)
		if lstatErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return nil, fmt.Errorf("read symlink %s: %w", path, readErr)
			}
			snapshot.Existed = true
			snapshot.LinkTarget = target
			receipt.Files = append(receipt.Files, snapshot)
			continue
		}
		body, err := os.ReadFile(path)
		if err == nil {
			info, statErr := os.Stat(path)
			if statErr != nil {
				return nil, fmt.Errorf("stat %s: %w", path, statErr)
			}
			snapshot.Existed = true
			snapshot.Mode = uint32(info.Mode().Perm())
			snapshot.Original = body
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		receipt.Files = append(receipt.Files, snapshot)
	}
	return receipt, nil
}

// recordConfiguredHashes records the expected post-configuration file contents.
func recordConfiguredHashes(receipt *configReceipt) error {
	for index := range receipt.Files {
		body, err := os.ReadFile(receipt.Files[index].Path)
		if err != nil {
			return fmt.Errorf("read configured file %s: %w", receipt.Files[index].Path, err)
		}
		receipt.Files[index].ConfiguredSHA256 = sha256Hex(body)
	}
	return nil
}

// verifyConfiguredFiles refuses to overwrite subsequent user changes by default.
func verifyConfiguredFiles(receipt *configReceipt, force bool) error {
	if force {
		return nil
	}
	for _, snapshot := range receipt.Files {
		body, err := os.ReadFile(snapshot.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && snapshot.ConfiguredSHA256 == "" {
				continue
			}
			return fmt.Errorf("%s changed after Bifrost configured it; rerun with --force to overwrite", snapshot.Path)
		}
		if snapshot.ConfiguredSHA256 != "" && sha256Hex(body) != snapshot.ConfiguredSHA256 {
			return fmt.Errorf("%s changed after Bifrost configured it; rerun with --force to overwrite", snapshot.Path)
		}
	}
	return nil
}

// restoreConfigReceipt restores original files or removes files created by configuration.
func restoreConfigReceipt(receipt *configReceipt) error {
	for _, snapshot := range receipt.Files {
		if !snapshot.Existed {
			if err := os.Remove(snapshot.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove generated config %s: %w", snapshot.Path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(snapshot.Path), 0o700); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
		if snapshot.LinkTarget != "" {
			if err := os.Remove(snapshot.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove configured file %s before restoring symlink: %w", snapshot.Path, err)
			}
			if err := os.Symlink(snapshot.LinkTarget, snapshot.Path); err != nil {
				return fmt.Errorf("restore symlink %s: %w", snapshot.Path, err)
			}
			continue
		}
		if err := config.WriteAtomic(snapshot.Path, snapshot.Original, os.FileMode(snapshot.Mode)); err != nil {
			return fmt.Errorf("restore config %s: %w", snapshot.Path, err)
		}
	}
	return nil
}

// saveConfigReceipt writes a secret-bearing restoration receipt with owner-only permissions.
func saveConfigReceipt(path string, receipt *configReceipt) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create receipt directory: %w", err)
	}
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal configuration receipt: %w", err)
	}
	if err := config.WriteAtomic(path, body, 0o600); err != nil {
		return fmt.Errorf("write configuration receipt: %w", err)
	}
	return nil
}

// loadConfigReceipt loads a receipt or returns nil when none exists.
func loadConfigReceipt(path string) (*configReceipt, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read configuration receipt: %w", err)
	}
	var receipt configReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return nil, fmt.Errorf("parse configuration receipt: %w", err)
	}
	if receipt.Version != 1 {
		return nil, fmt.Errorf("unsupported configuration receipt version %d", receipt.Version)
	}
	return &receipt, nil
}

// sha256Hex returns a stable content digest for change detection.
func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
