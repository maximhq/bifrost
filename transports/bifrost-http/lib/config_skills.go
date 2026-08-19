package lib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/objectstore"
)

// loadSkillsRegistry reconciles config-defined skills with the database on startup.
func loadSkillsRegistry(ctx context.Context, config *Config, configData *ConfigData) {
	if config.ConfigStore == nil {
		return
	}
	if configData.SkillsRegistry == nil || len(configData.SkillsRegistry.Skills) == 0 {
		return
	}
	// If enabled is explicitly set to false, skip
	if configData.SkillsRegistry.Enabled != nil && !*configData.SkillsRegistry.Enabled {
		return
	}
	reconcileSkillsRegistry(ctx, config.ConfigStore, configData.SkillsRegistry.Skills, config.ObjectStore)
}

// reconcileSkillsRegistry processes each config-defined skill entry: creates missing skills
// or appends a new version if the definition changed and the provided version is valid.
func reconcileSkillsRegistry(ctx context.Context, store configstore.ConfigStore, entries []SkillsRegistryEntry, objStore objectstore.ObjectStore) {
	// Preflight: reject duplicate skill names before any writes.
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, dup := seen[entry.Name]; dup {
			logger.Error("skills_registry: duplicate skill name %q in config — skipping entire skills_registry reconciliation", entry.Name)
			return
		}
		seen[entry.Name] = struct{}{}
	}

	for i := range entries {
		entry := &entries[i]
		if err := reconcileOneSkill(ctx, store, entry, objStore); err != nil {
			logger.Error("skills_registry: skill %q: %v", entry.Name, err)
		}
	}
}

func reconcileOneSkill(ctx context.Context, store configstore.ConfigStore, entry *SkillsRegistryEntry, objStore objectstore.ObjectStore) error {
	// Reject upload source type in config — it requires the interactive/API upload flow
	for _, f := range entry.Files {
		if f.SourceType == configstoreTables.SkillSourceTypeUpload {
			return fmt.Errorf("source_type \"upload\" is not supported in config.json; use dataurl, text, or url instead (file %q)", f.Path)
		}
	}

	// Compute config hash first (cheap, in-memory) so we can skip unchanged
	// entries before doing any network I/O (e.g. HEAD requests for URL files).
	configHash, err := generateSkillRegistryEntryHash(entry)
	if err != nil {
		return fmt.Errorf("failed to generate config hash: %w", err)
	}

	// Check if skill already exists
	existing, err := store.GetSkillByName(ctx, entry.Name)
	if err != nil && err != configstore.ErrNotFound {
		return fmt.Errorf("failed to look up existing skill: %w", err)
	}

	// Early exit on hash match — avoids file conversion, validation, and
	// network I/O (URL MIME inference) when the config entry hasn't changed.
	if existing != nil {
		switch existing.ConfigHash {
		case "":
			// Skill was created via API/dashboard and is now appearing in config.json.
			// Config takes ownership — warn so operators know the API-managed content
			// is being superseded.
			logger.Warn("skills_registry: skill %q was created via API/dashboard; config.json is now managing it", entry.Name)
		case configHash:
			logger.Debug("skills_registry: skill %q unchanged (hash match), skipping", entry.Name)
			return nil
		}
	}

	// Config changed, new skill, or claiming an API-created skill —
	// now resolve files (may issue HEAD requests for URL sources).
	skill := configEntryToTableSkill(entry)
	files := configEntryToTableFiles(ctx, entry)

	// Validate using the same pipeline as the management API
	if err := configstore.ValidateSkill(&skill, entry.Version); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	seenPaths := make(map[string]struct{}, len(files))
	for i := range files {
		if err := configstore.ValidateSkillFile(&files[i]); err != nil {
			return fmt.Errorf("file %q validation failed: %w", files[i].Path, err)
		}
		if _, dup := seenPaths[files[i].Path]; dup {
			return fmt.Errorf("duplicate file path %q", files[i].Path)
		}
		seenPaths[files[i].Path] = struct{}{}
	}

	if existing == nil {
		// Skill does not exist — create with the provided version
		skill.ConfigHash = configHash
		skill.Files = files
		if err := store.CreateSkill(ctx, &skill, entry.Version, objStore); err != nil {
			return fmt.Errorf("failed to create skill: %w", err)
		}
		logger.Info("skills_registry: created skill %q version %s", entry.Name, entry.Version)
		return nil
	}

	// Publish a new version and serve it directly.
	skill.ID = existing.ID
	skill.Files = files
	if err := store.UpdateSkill(ctx, &skill, entry.Version, true, objStore); err != nil {
		// If the version already exists or is behind, the config entry cannot be applied.
		// Persist the config hash so subsequent restarts see a match and skip cleanly,
		// avoiding noisy errors on every boot.
		if strings.Contains(err.Error(), "already matches the latest version") ||
			strings.Contains(err.Error(), "already exists in version history") {
			if hashErr := store.UpdateSkillConfigHash(ctx, existing.ID, configHash); hashErr != nil {
				logger.Warn("skills_registry: skill %q: version %q already exists but failed to update config hash: %v", entry.Name, entry.Version, hashErr)
			} else {
				logger.Info("skills_registry: skill %q: version %q already exists, adopted by config", entry.Name, entry.Version)
			}
			return nil
		}
		// Validation failure (e.g. version goes backwards) — do nothing, log and move on.
		return fmt.Errorf("failed to update skill (check version %q against latest): %w", entry.Version, err)
	}

	// Version created successfully — update config hash.
	if hashErr := store.UpdateSkillConfigHash(ctx, existing.ID, configHash); hashErr != nil {
		logger.Warn("skills_registry: skill %q: version %s created but failed to update config hash: %v", entry.Name, entry.Version, hashErr)
	}

	logger.Info("skills_registry: updated skill %q to version %s", entry.Name, entry.Version)
	return nil
}

// configEntryToTableSkill converts a SkillsRegistryEntry to a TableSkill.
func configEntryToTableSkill(entry *SkillsRegistryEntry) configstoreTables.TableSkill {
	skill := configstoreTables.TableSkill{
		Name:             entry.Name,
		Description:      entry.Description,
		SkillMDBody:      entry.SkillMDBody,
		Metadata:         configstoreTables.SkillStringMap(entry.Metadata),
		ExtraFrontmatter: configstoreTables.SkillJSONMap(entry.ExtraFrontmatter),
	}
	if entry.License != "" {
		skill.License = &entry.License
	}
	if entry.Compatibility != "" {
		skill.Compatibility = &entry.Compatibility
	}
	if entry.AllowedTools != "" {
		skill.AllowedTools = &entry.AllowedTools
	}
	return skill
}

// configEntryToTableFiles converts config file entries to TableSkillFile entries.
func configEntryToTableFiles(ctx context.Context, entry *SkillsRegistryEntry) []configstoreTables.TableSkillFile {
	files := make([]configstoreTables.TableSkillFile, 0, len(entry.Files))
	for _, cf := range entry.Files {
		file := configstoreTables.TableSkillFile{
			Path:       cf.Path,
			SourceType: cf.SourceType,
		}
		switch cf.SourceType {
		case configstoreTables.SkillSourceTypeURL:
			file.SourceURL = &cf.URL
			if mimeType, err := inferConfigLiveURLMimeType(ctx, cf.URL); err != nil {
				logger.Warn("skills_registry: skill %q file %q: could not infer MIME from URL %q: %v; using application/octet-stream", entry.Name, cf.Path, cf.URL, err)
				file.MimeType = "application/octet-stream"
			} else {
				file.MimeType = mimeType
			}
		case configstoreTables.SkillSourceTypeText:
			file.InlineContent = &cf.Content
			file.MimeType = "text/plain"
		case configstoreTables.SkillSourceTypeDataURL:
			file.DataURL = &cf.DataURL
		}
		files = append(files, file)
	}
	return files
}

func inferConfigLiveURLMimeType(ctx context.Context, rawURL string) (string, error) {
	client := http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("HEAD returned status %d", resp.StatusCode)
	}
	mimeType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" {
		return "", fmt.Errorf("HEAD response did not include Content-Type")
	}
	return mimeType, nil
}

// generateSkillRegistryEntryHash produces a deterministic SHA-256 hash of a config
// entry so that unchanged entries are not re-applied on every restart.
func generateSkillRegistryEntryHash(entry *SkillsRegistryEntry) (string, error) {
	// Marshal the entire entry to JSON for a deterministic content hash.
	// json.Marshal sorts map keys alphabetically (since Go 1.12), so the
	// output is stable across restarts for the same Go values.
	data, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}
