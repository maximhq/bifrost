package lib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
	reconcileSkillsRegistry(ctx, config.ConfigStore, configData.SkillsRegistry.Skills, nil)
}

// reconcileSkillsRegistry processes each config-defined skill entry: creates missing skills
// or appends a new version if the definition changed and the provided version is valid.
func reconcileSkillsRegistry(ctx context.Context, store configstore.ConfigStore, entries []SkillsRegistryEntry, objStore objectstore.ObjectStore) {
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
			return fmt.Errorf("source_type \"upload\" is not supported in config.json; use filepath, dataurl, text, or url instead (file %q)", f.Path)
		}
	}

	// Build TableSkill from the config entry
	skill := configEntryToTableSkill(entry)
	files := configEntryToTableFiles(ctx, entry)

	// Validate using the same pipeline as the management API
	if err := configstore.ValidateSkill(&skill, entry.Version); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	for i := range files {
		if err := configstore.ValidateSkillFile(&files[i]); err != nil {
			return fmt.Errorf("file %q validation failed: %w", files[i].Path, err)
		}
	}

	// Compute config hash for change detection
	configHash, err := generateSkillRegistryEntryHash(entry)
	if err != nil {
		return fmt.Errorf("failed to generate config hash: %w", err)
	}

	// Check if skill already exists
	existing, err := store.GetSkillByName(ctx, entry.Name)
	if err != nil && err != configstore.ErrNotFound {
		return fmt.Errorf("failed to look up existing skill: %w", err)
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

	// Skill exists — check if config definition changed
	if existing.ConfigHash == configHash {
		logger.Debug("skills_registry: skill %q unchanged (hash match), skipping", entry.Name)
		return nil
	}

	// Config changed — update with the new version
	skill.ID = existing.ID
	skill.ConfigHash = configHash
	skill.Files = files
	if err := store.UpdateSkill(ctx, &skill, entry.Version, true, objStore); err != nil {
		// UpdateSkill validates version increment/uniqueness — log and skip on failure
		return fmt.Errorf("failed to update skill (check version %q against latest %q): %w", entry.Version, existing.LatestVersion, err)
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
		case configstoreTables.SkillSourceTypeFilepath:
			file.SourceFilepath = &cf.Filepath
			if mimeType, err := inferConfigLiveFilepathMimeType(cf.Filepath); err != nil {
				logger.Warn("skills_registry: skill %q file %q: could not infer MIME from filepath %q: %v; using application/octet-stream", entry.Name, cf.Path, cf.Filepath, err)
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

func inferConfigLiveFilepathMimeType(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && n == 0 {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("file is empty")
	}
	return http.DetectContentType(buffer[:n]), nil
}

// generateSkillRegistryEntryHash produces a deterministic SHA-256 hash of a config
// entry so that unchanged entries are not re-applied on every restart.
func generateSkillRegistryEntryHash(entry *SkillsRegistryEntry) (string, error) {
	// Marshal the entire entry to JSON for a deterministic content hash.
	// json.Marshal produces deterministic output for the same Go values
	// (map iteration order aside — but our maps are small and we include
	// all fields so even reordering changes the hash, which is fine:
	// a hash change just triggers a version-guarded update attempt).
	data, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}
