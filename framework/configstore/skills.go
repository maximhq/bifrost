package configstore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/objectstore"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const allSkillsVersionConfigKey = "skills_repository.all_skills_version"

type allSkillsVersionBump string

type pendingSkillObjectWrite struct {
	key      string
	data     []byte
	metadata map[string]string
}

// runPendingSkillObjectWrites uploads pending objects to the store after the DB
// transaction has committed. If any upload fails it performs a compensating DB
// delete so the committed rows don't reference missing objects:
//   - isCreate=true  → deletes the entire skill (cascades to versions/files)
//   - isCreate=false → deletes only the newly created version row
//
// Already-written objects from earlier iterations are best-effort cleaned up
// to avoid orphans. Any stragglers are caught by the startup orphan cleanup.
func runPendingSkillObjectWrites(ctx context.Context, db *gorm.DB, objectStore objectstore.ObjectStore, writes []pendingSkillObjectWrite, isCreate bool, skillID, versionID string) error {
	if objectStore == nil || len(writes) == 0 {
		return nil
	}
	for i, write := range writes {
		if err := objectStore.Put(ctx, write.key, write.data, write.metadata); err != nil {
			writeErr := fmt.Errorf("failed to write skill file object %s: %w", write.key, err)

			// Use a bounded detached context: request cancellation often causes the
			// Put failure, but compensation must still get a chance to repair DB rows.
			compensationCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			compensationDB := db.WithContext(compensationCtx)

			// Compensating delete: remove the DB rows that reference the missing objects.
			var compensationErr error
			if isCreate {
				compensationErr = compensationDB.Where("id = ?", skillID).Delete(&tables.TableSkill{}).Error
			} else {
				compensationErr = compensationDB.Where("id = ?", versionID).Delete(&tables.TableSkillVersion{}).Error
			}
			if compensationErr != nil {
				return fmt.Errorf("%w; failed to compensate committed skill rows: %v", writeErr, compensationErr)
			}

			// Best-effort cleanup of objects written in earlier iterations.
			if i > 0 {
				written := make([]string, i)
				for j := range i {
					written[j] = writes[j].key
				}
				_ = objectStore.DeleteBatch(compensationCtx, written)
			}
			return writeErr
		}
	}
	return nil
}

func runSkillObjectCleanup(ctx context.Context, logger schemas.Logger, objectStore objectstore.ObjectStore, keys []string) {
	if objectStore == nil || len(keys) == 0 {
		return
	}
	if err := objectStore.DeleteBatch(ctx, keys); err != nil && logger != nil {
		logger.Warn("failed to cleanup skill file objects, will be cleanedup in the restart automatically: %v", err)
	}
}

const (
	allSkillsVersionBumpPatch allSkillsVersionBump = "patch"
	allSkillsVersionBumpMinor allSkillsVersionBump = "minor"
	allSkillsVersionBumpMajor allSkillsVersionBump = "major"

	// MaxSkillFileContentSize is the maximum allowed size for any single skill
	// file, regardless of source type.
	MaxSkillFileContentSize = 50 * 1024 * 1024

	// SkillObjectPrefix is the object-store prefix for all skill files.
	SkillObjectPrefix = "skills/"
)

// SkillHarnessNames lists the supported agent harness identifiers
// (e.g. "claude-code", "codex"). Used for Git smart HTTP route
// registration and reserved-name validation.
var SkillHarnessNames = []string{"claude-code", "codex"}

// SkillReservedNames lists skill names that are reserved by the serving
// layer (static routes that would shadow dynamic skill-name routes).
// Built from SkillHarnessNames plus additional synthetic names.
var SkillReservedNames = append([]string{"all-skills", "all"}, SkillHarnessNames...)

var (
	skillNamePattern   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	skillSemverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z][0-9A-Za-z.-]*))?$`)
	// skillReservedFrontmatterFields contains frontmatter keys that are managed
	// as first-class DB columns and must not appear in extra_frontmatter.
	skillReservedFrontmatterFields = map[string]struct{}{
		"name":          {},
		"description":   {},
		"license":       {},
		"compatibility": {},
		"metadata":      {},
		"allowed-tools": {},
	}
)

// IsSkillReservedFrontmatterField returns true if the given key is a reserved
// frontmatter field managed as a first-class DB column.
func IsSkillReservedFrontmatterField(key string) bool {
	_, reserved := skillReservedFrontmatterFields[key]
	return reserved
}

type skillVersionParts struct {
	major  int
	minor  int
	patch  int
	suffix string
}

// ValidateSkill validates the skill-level fields required by the Agent Skills spec.
func ValidateSkill(skill *tables.TableSkill, version string) error {
	if skill == nil {
		return fmt.Errorf("skill is required")
	}
	if err := ValidateSkillName(skill.Name); err != nil {
		return err
	}
	if strings.TrimSpace(skill.Description) == "" {
		return fmt.Errorf("skill description is required")
	}
	if len(skill.Description) > 1024 {
		return fmt.Errorf("skill description must be 1024 characters or fewer")
	}
	if skill.Compatibility != nil && len(*skill.Compatibility) > 500 {
		return fmt.Errorf("skill compatibility must be 500 characters or fewer")
	}
	if strings.TrimSpace(skill.SkillMDBody) == "" {
		return fmt.Errorf("skill_md_body is required")
	}
	if !utf8.ValidString(skill.SkillMDBody) {
		return fmt.Errorf("skill_md_body must be valid UTF-8")
	}
	if err := ValidateSkillVersion(version); err != nil {
		return err
	}
	for key, value := range skill.Metadata {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("metadata keys must be non-empty")
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf("metadata value for %q must be valid UTF-8", key)
		}
	}
	for key := range skill.ExtraFrontmatter {
		if IsSkillReservedFrontmatterField(key) {
			return fmt.Errorf("extra_frontmatter key %q conflicts with a reserved frontmatter field", key)
		}
	}
	return nil
}

// ValidateSkillName validates lowercase alphanumeric + hyphen skill names.
func ValidateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("skill name must be 64 characters or fewer")
	}
	if !skillNamePattern.MatchString(name) {
		return fmt.Errorf("skill name must contain only lowercase letters, numbers, and single hyphens with no leading, trailing, or consecutive hyphens")
	}
	if slices.Contains(SkillReservedNames, name) {
		return fmt.Errorf("%q is a reserved name used by the skills serving layer", name)
	}
	return nil
}

// ValidateSkillFile validates a skill file definition before storage writes.
func ValidateSkillFile(file *tables.TableSkillFile) error {
	if file == nil {
		return fmt.Errorf("skill file is required")
	}
	if err := ValidateSkillFilePath(file.Path); err != nil {
		return err
	}
	if err := validateSkillSourceType(file.SourceType); err != nil {
		return err
	}
	return nil
}

// ValidateSkillFilePath validates safe relative file paths within a skill.
func ValidateSkillFilePath(filePath string) error {
	p := strings.TrimSpace(filePath)
	if p == "" {
		return fmt.Errorf("skill file path is required")
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return fmt.Errorf("skill file path must be relative")
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("skill file path must not have a trailing slash")
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("skill file path must use forward slashes")
	}
	for segment := range strings.SplitSeq(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("skill file path must not contain empty, current, or parent directory segments")
		}
	}
	return nil
}

func validateSkillSourceType(sourceType string) error {
	switch sourceType {
	case tables.SkillSourceTypeURL, tables.SkillSourceTypeDataURL, tables.SkillSourceTypeText, tables.SkillSourceTypeUpload:
		return nil
	default:
		return fmt.Errorf("skill file source_type must be one of url, dataurl, text, or upload")
	}
}

// ValidateSkillVersion validates skill versions as MAJOR.MINOR.PATCH with an optional -suffix.
func ValidateSkillVersion(version string) error {
	_, err := parseSkillSemver(version)
	return err
}

func parseSkillSemver(version string) (skillVersionParts, error) {
	if version != strings.TrimSpace(version) {
		return skillVersionParts{}, fmt.Errorf("skill version must not have leading or trailing whitespace")
	}
	matches := skillSemverPattern.FindStringSubmatch(version)
	if matches == nil {
		return skillVersionParts{}, fmt.Errorf("skill version must be semver MAJOR.MINOR.PATCH with optional -suffix")
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return skillVersionParts{}, fmt.Errorf("skill version major component overflows: %w", err)
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return skillVersionParts{}, fmt.Errorf("skill version minor component overflows: %w", err)
	}
	patch, err := strconv.Atoi(matches[3])
	if err != nil {
		return skillVersionParts{}, fmt.Errorf("skill version patch component overflows: %w", err)
	}
	return skillVersionParts{major: major, minor: minor, patch: patch, suffix: matches[4]}, nil
}

func validateSkillVersionIncrement(previousVersion, nextVersion string) error {
	if previousVersion == "" {
		_, err := parseSkillSemver(nextVersion)
		return err
	}
	prev, err := parseSkillSemver(previousVersion)
	if err != nil {
		return fmt.Errorf("stored latest skill version is invalid: %w", err)
	}
	next, err := parseSkillSemver(nextVersion)
	if err != nil {
		return err
	}
	if next.major < prev.major || (next.major == prev.major && next.minor < prev.minor) || (next.major == prev.major && next.minor == prev.minor && next.patch < prev.patch) {
		return fmt.Errorf("skill version %q must not go below latest version %q", nextVersion, previousVersion)
	}
	if next.major == prev.major && next.minor == prev.minor && next.patch == prev.patch && next.suffix == prev.suffix {
		return fmt.Errorf("skill version %q already matches the latest version; choose a new version", nextVersion)
	}
	return nil
}

func latestCreatedSkillVersion(tx *gorm.DB, skillID string) (string, error) {
	var latest string
	err := tx.Model(&tables.TableSkillVersion{}).
		Where("skill_id = ?", skillID).
		Select("version").
		Order("created_at DESC").
		Limit(1).
		Scan(&latest).Error
	return latest, err
}

func allSkillsBumpForVersionChange(previousVersion, nextVersion string) (allSkillsVersionBump, error) {
	prev, err := parseSkillSemver(previousVersion)
	if err != nil {
		return "", fmt.Errorf("stored latest skill version is invalid: %w", err)
	}
	next, err := parseSkillSemver(nextVersion)
	if err != nil {
		return "", err
	}
	if next.major > prev.major {
		return allSkillsVersionBumpMajor, nil
	}
	if next.minor > prev.minor {
		return allSkillsVersionBumpMinor, nil
	}
	return allSkillsVersionBumpPatch, nil
}

func nextAllSkillsVersion(current string, bump allSkillsVersionBump, firstSkill bool) (string, error) {
	if current == "" {
		current = "0.0.0"
	}
	if firstSkill && current == "0.0.0" {
		return "1.0.0", nil
	}
	parts, err := parseSkillSemver(current)
	if err != nil {
		return "", fmt.Errorf("stored all-skills version is invalid: %w", err)
	}
	switch bump {
	case allSkillsVersionBumpMajor:
		return fmt.Sprintf("%d.0.0", parts.major+1), nil
	case allSkillsVersionBumpMinor:
		return fmt.Sprintf("%d.%d.0", parts.major, parts.minor+1), nil
	case allSkillsVersionBumpPatch:
		return fmt.Sprintf("%d.%d.%d", parts.major, parts.minor, parts.patch+1), nil
	default:
		return "", fmt.Errorf("unsupported all-skills version bump %q", bump)
	}
}

func bumpAllSkillsVersionTx(ctx context.Context, tx *gorm.DB, bump allSkillsVersionBump, firstSkill bool) error {
	var config tables.TableGovernanceConfig
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&config, "key = ?", allSkillsVersionConfigKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		config = tables.TableGovernanceConfig{Key: allSkillsVersionConfigKey, Value: "0.0.0"}
		if err := tx.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&config).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&config, "key = ?", allSkillsVersionConfigKey).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	next, err := nextAllSkillsVersion(config.Value, bump, firstSkill)
	if err != nil {
		return err
	}
	config.Value = next
	return tx.WithContext(ctx).Save(&config).Error
}

// GetAllSkillsVersion returns the repository-level version for the bundled all-skills plugin.
func (s *RDBConfigStore) GetAllSkillsVersion(ctx context.Context) (string, error) {
	config, err := s.GetConfig(ctx, allSkillsVersionConfigKey)
	if errors.Is(err, ErrNotFound) {
		return "0.0.0", nil
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(config.Value) == "" {
		return "0.0.0", nil
	}
	return config.Value, nil
}

// BumpAllSkillsVersion manually advances the repository-level all-skills version.
func (s *RDBConfigStore) BumpAllSkillsVersion(ctx context.Context, bump string) (string, error) {
	bumpType := allSkillsVersionBump(bump)
	switch bumpType {
	case allSkillsVersionBumpPatch, allSkillsVersionBumpMinor, allSkillsVersionBumpMajor:
	default:
		return "", fmt.Errorf("unsupported all-skills version bump %q", bump)
	}
	if err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return bumpAllSkillsVersionTx(ctx, tx, bumpType, false)
	}); err != nil {
		return "", err
	}
	return s.GetAllSkillsVersion(ctx)
}

// StoreSkillFileContent stores inline file content in the DB blob fallback, or
// prepares an object-storage write for the caller to run after DB commit.
func StoreSkillFileContent(ctx context.Context, tx *gorm.DB, objectStore objectstore.ObjectStore, skillID string, file *tables.TableSkillFile, data []byte) (*pendingSkillObjectWrite, error) {
	if file == nil {
		return nil, fmt.Errorf("skill file is required")
	}
	if objectStore != nil {
		key := file.StorageKey
		if key == nil || strings.TrimSpace(*key) == "" {
			generated := fmt.Sprintf(SkillObjectPrefix+"%s/%s/%s", skillID, file.ID, path.Base(file.Path))
			key = &generated
		}
		file.StorageKey = key
		file.BlobID = nil
		return &pendingSkillObjectWrite{
			key:      *key,
			data:     data,
			metadata: map[string]string{"skill_id": skillID, "file_id": file.ID},
		}, nil
	}

	blobID := uuid.NewString()
	blob := tables.TableSkillFileBlob{
		ID:   blobID,
		Data: data,
	}
	if err := tx.Create(&blob).Error; err != nil {
		return nil, err
	}
	file.BlobID = &blobID
	file.StorageKey = nil
	return nil, nil
}

func validateSkillStorageKeyScope(skillID string, file *tables.TableSkillFile, key string) error {
	if !strings.HasPrefix(key, SkillObjectPrefix) {
		return fmt.Errorf("storage_key %q is not under the skills object prefix", key)
	}

	uploadPrefix := SkillObjectPrefix + "uploads/"
	skillPrefix := SkillObjectPrefix + skillID + "/"

	switch file.SourceType {
	case tables.SkillSourceTypeUpload:
		if file.UploadID != nil && strings.TrimSpace(*file.UploadID) != "" {
			expectedPrefix := uploadPrefix + strings.TrimSpace(*file.UploadID) + "/"
			if !strings.HasPrefix(key, expectedPrefix) {
				return fmt.Errorf("storage_key %q does not belong to upload_id %q", key, strings.TrimSpace(*file.UploadID))
			}
			return nil
		}
		if !strings.HasPrefix(key, uploadPrefix) {
			return fmt.Errorf("storage_key %q is not under the staged upload prefix", key)
		}
	case tables.SkillSourceTypeText, tables.SkillSourceTypeDataURL:
		if !strings.HasPrefix(key, skillPrefix) {
			return fmt.Errorf("storage_key %q is not scoped to skill %q", key, skillID)
		}
	}
	return nil
}

func validateSkillFileReference(tx *gorm.DB, skillID string, file *tables.TableSkillFile) error {
	if file.BlobID != nil && strings.TrimSpace(*file.BlobID) != "" {
		var blobCount int64
		if err := tx.Model(&tables.TableSkillFileBlob{}).Where("id = ?", *file.BlobID).Count(&blobCount).Error; err != nil {
			return err
		}
		if blobCount == 0 {
			return fmt.Errorf("blob_id %q does not exist", *file.BlobID)
		}

		var foreignCount int64
		if err := tx.Model(&tables.TableSkillFile{}).
			Joins("JOIN skill_versions ON skill_versions.id = skill_files.skill_version_id").
			Where("skill_files.blob_id = ? AND skill_versions.skill_id <> ?", *file.BlobID, skillID).
			Count(&foreignCount).Error; err != nil {
			return err
		}
		if foreignCount > 0 {
			return fmt.Errorf("blob_id %q is already bound to another skill", *file.BlobID)
		}
	}

	if file.StorageKey != nil && strings.TrimSpace(*file.StorageKey) != "" {
		key := strings.TrimSpace(*file.StorageKey)
		if err := validateSkillStorageKeyScope(skillID, file, key); err != nil {
			return err
		}

		var foreignCount int64
		if err := tx.Model(&tables.TableSkillFile{}).
			Joins("JOIN skill_versions ON skill_versions.id = skill_files.skill_version_id").
			Where("skill_files.storage_key = ? AND skill_versions.skill_id <> ?", key, skillID).
			Count(&foreignCount).Error; err != nil {
			return err
		}
		if foreignCount > 0 {
			return fmt.Errorf("storage_key %q is already bound to another skill", key)
		}
		file.StorageKey = &key
	}
	return nil
}

func decodeSkillDataURL(dataURL string) ([]byte, string, error) {
	parsed, err := url.Parse(dataURL)
	if err != nil || parsed.Scheme != "data" {
		return nil, "", fmt.Errorf("dataurl must be a valid data: URL")
	}
	parts := strings.SplitN(dataURL, ",", 2)
	if len(parts) != 2 || !strings.Contains(parts[0], ";base64") {
		return nil, "", fmt.Errorf("dataurl must contain ;base64,")
	}
	// Reject oversized payloads before materializing the full decode in memory.
	if base64.StdEncoding.DecodedLen(len(parts[1])) > MaxSkillFileContentSize {
		return nil, "", fmt.Errorf("dataurl payload exceeds maximum size of %d bytes", MaxSkillFileContentSize)
	}
	mimeType := strings.TrimPrefix(strings.Split(parts[0], ";")[0], "data:")
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, "", fmt.Errorf("dataurl base64 decode failed: %w", err)
	}
	return data, mimeType, nil
}

func validateSkillFileSource(file *tables.TableSkillFile) ([]byte, error) {
	// Reject files with both blob_id and storage_key — exactly one backing store is allowed.
	hasBlobID := file.BlobID != nil && strings.TrimSpace(*file.BlobID) != ""
	hasStorageKey := file.StorageKey != nil && strings.TrimSpace(*file.StorageKey) != ""
	if hasBlobID && hasStorageKey {
		return nil, fmt.Errorf("file %q must have either blob_id or storage_key, not both", file.Path)
	}
	switch file.SourceType {
	case tables.SkillSourceTypeURL:
		if file.SourceURL == nil || strings.TrimSpace(*file.SourceURL) == "" {
			return nil, fmt.Errorf("url source_type requires source_url")
		}
		u, err := url.Parse(*file.SourceURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("source_url must be an http or https URL")
		}
		file.StorageKey = nil
		file.BlobID = nil
		return nil, nil
	case tables.SkillSourceTypeText:
		if file.InlineContent == nil || *file.InlineContent == "" {
			if hasStorageKey || hasBlobID {
				return nil, nil
			}
			return nil, fmt.Errorf("text source_type requires content or an existing file reference")
		}
		data := []byte(*file.InlineContent)
		if len(data) > MaxSkillFileContentSize {
			return nil, fmt.Errorf("text file content exceeds maximum size of %d bytes", MaxSkillFileContentSize)
		}
		file.StorageKey = nil
		file.BlobID = nil
		return data, nil
	case tables.SkillSourceTypeDataURL:
		if file.DataURL == nil || strings.TrimSpace(*file.DataURL) == "" {
			if hasStorageKey || hasBlobID {
				return nil, nil
			}
			return nil, fmt.Errorf("dataurl source_type requires dataurl or an existing file reference")
		}
		data, mimeType, err := decodeSkillDataURL(*file.DataURL)
		if err != nil {
			return nil, err
		}
		if len(data) > MaxSkillFileContentSize {
			return nil, fmt.Errorf("dataurl file content exceeds maximum size of %d bytes", MaxSkillFileContentSize)
		}
		if file.MimeType == "" {
			file.MimeType = mimeType
		}
		file.StorageKey = nil
		file.BlobID = nil
		return data, nil
	case tables.SkillSourceTypeUpload:
		if !hasStorageKey && !hasBlobID {
			return nil, fmt.Errorf("upload source_type requires storage_key or blob_id")
		}
		return nil, nil
	default:
		return nil, validateSkillSourceType(file.SourceType)
	}
}

// CreateSkill creates a skill with an initial version and its files.
func (s *RDBConfigStore) CreateSkill(ctx context.Context, skill *tables.TableSkill, version string, objectStore objectstore.ObjectStore) error {
	if err := ValidateSkill(skill, version); err != nil {
		return err
	}
	var objectWrites []pendingSkillObjectWrite
	var firstSkill bool
	if err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing tables.TableSkill
		if err := tx.First(&existing, "name = ?", skill.Name).Error; err == nil {
			return fmt.Errorf("skill name %q already exists", skill.Name)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var existingSkillCount int64
		if err := tx.Model(&tables.TableSkill{}).Count(&existingSkillCount).Error; err != nil {
			return err
		}
		firstSkill = existingSkillCount == 0
		if skill.ID == "" {
			skill.ID = uuid.NewString()
		}
		skill.LatestVersion = version
		files := skill.Files
		skill.Files = nil
		skill.Versions = nil
		if err := tx.Create(skill).Error; err != nil {
			return err
		}
		// Create the version row, then create files under it.
		versionRow, err := createSkillVersion(tx, skill, version)
		if err != nil {
			return err
		}
		writes, err := createVersionFiles(ctx, tx, objectStore, skill.ID, versionRow.ID, files)
		if err != nil {
			return err
		}
		objectWrites = append(objectWrites, writes...)
		// Populate transient Files for the response.
		return populateSkillFiles(tx, skill)
	}); err != nil {
		return err
	}
	if err := runPendingSkillObjectWrites(ctx, s.DB().WithContext(ctx), objectStore, objectWrites, true, skill.ID, ""); err != nil {
		return err
	}
	if err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return bumpAllSkillsVersionTx(ctx, tx, allSkillsVersionBumpMinor, firstSkill)
	}); err != nil {
		return fmt.Errorf("skill %q was created successfully but all-skills version could not be bumped; manually bump all-skills version with a minor bump to publish this create: %w", skill.Name, err)
	}
	return nil
}

// GetSkill returns a skill with serving files. Version history is loaded through ListSkillVersions.
func (s *RDBConfigStore) GetSkill(ctx context.Context, id string) (*tables.TableSkill, error) {
	var skill tables.TableSkill
	if err := s.ScopedDB(ctx).
		First(&skill, "skills.id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := populateSkillFiles(s.ScopedDB(ctx), &skill); err != nil {
		return nil, fmt.Errorf("load serving files: %w", err)
	}
	// Populate the most recently created version for bump validation without
	// returning the full version history from this endpoint.
	latest, err := latestCreatedSkillVersion(s.ScopedDB(ctx), skill.ID)
	if err != nil {
		return nil, fmt.Errorf("load latest created skill version: %w", err)
	}
	if latest != "" {
		skill.HighestVersion = latest
	}
	return &skill, nil
}

// GetSkillLean returns a skill without versions preloaded (only serving files).
func (s *RDBConfigStore) GetSkillLean(ctx context.Context, id string) (*tables.TableSkill, error) {
	var skill tables.TableSkill
	if err := s.ScopedDB(ctx).
		First(&skill, "skills.id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := populateSkillFiles(s.ScopedDB(ctx), &skill); err != nil {
		return nil, fmt.Errorf("load serving files: %w", err)
	}
	// Populate the most recently created version for bump validation.
	latest, err := latestCreatedSkillVersion(s.ScopedDB(ctx), skill.ID)
	if err != nil {
		return nil, fmt.Errorf("load latest created skill version: %w", err)
	}
	if latest != "" {
		skill.HighestVersion = latest
	}
	return &skill, nil
}

// GetSkillByName returns a skill by name with lean version summaries and serving files.
func (s *RDBConfigStore) GetSkillByName(ctx context.Context, name string) (*tables.TableSkill, error) {
	var skill tables.TableSkill
	if err := s.ScopedDB(ctx).
		Preload("Versions", func(db *gorm.DB) *gorm.DB {
			return db.Select("id", "skill_id", "version", "created_at").Order("created_at DESC")
		}).
		First(&skill, "skills.name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := populateSkillFiles(s.ScopedDB(ctx), &skill); err != nil {
		return nil, fmt.Errorf("load serving files: %w", err)
	}
	return &skill, nil
}

// ListSkillVersions returns paginated versions for a skill.
func (s *RDBConfigStore) ListSkillVersions(ctx context.Context, skillID string, params SkillVersionListQueryParams) ([]tables.TableSkillVersion, int64, error) {
	var total int64
	db := s.ScopedDB(ctx).Model(&tables.TableSkillVersion{}).Where("skill_id = ?", skillID)
	if params.Search != "" {
		needle := strings.ToLower(strings.TrimSpace(params.Search))
		needle = strings.ReplaceAll(needle, `\`, `\\`)
		needle = strings.ReplaceAll(needle, `%`, `\%`)
		needle = strings.ReplaceAll(needle, `_`, `\_`)
		like := "%" + needle + "%"
		db = db.Where(`LOWER(version) LIKE ? ESCAPE '\'`, like)
	}
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var versions []tables.TableSkillVersion
	query := db.
		Select("id", "skill_id", "version", "created_by", "created_at").
		Order(skillVersionListOrder(params))
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	if params.Offset > 0 {
		query = query.Offset(params.Offset)
	}
	if err := query.Find(&versions).Error; err != nil {
		return nil, 0, err
	}
	return versions, total, nil
}

// GetSkillVersion returns a specific version by skill ID and version string.
func (s *RDBConfigStore) GetSkillVersion(ctx context.Context, skillID, version string) (*tables.TableSkillVersion, error) {
	var v tables.TableSkillVersion
	if err := s.ScopedDB(ctx).
		Preload("Files").
		Preload("Files.Blob").
		First(&v, "skill_id = ? AND version = ?", skillID, version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &v, nil
}

// UpdateSkill saves skill fields, creates a new version, and creates files under it.
// When serve=true, the skill row is updated to serve this new version.
// When serve=false, only the version and files are created; the skill continues serving its current version.
//
// The serve switch is deferred until after object-store writes succeed so the
// skill never points at a version whose files failed to upload.
func (s *RDBConfigStore) UpdateSkill(ctx context.Context, skill *tables.TableSkill, version string, serve bool, objectStore objectstore.ObjectStore) error {
	if err := ValidateSkill(skill, version); err != nil {
		return err
	}

	// Phase 1: Create the version and files inside a transaction, but do NOT
	// update the skill's serving state yet. This keeps the skill pointing at
	// the old version until uploads are confirmed.
	var objectWrites []pendingSkillObjectWrite
	var versionID string
	var existingLatestVersion string
	if err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing tables.TableSkill
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, "id = ?", skill.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		existingLatestVersion = existing.LatestVersion
		latestCreatedVersion, err := latestCreatedSkillVersion(tx, skill.ID)
		if err != nil {
			return err
		}
		if latestCreatedVersion == "" {
			latestCreatedVersion = existing.LatestVersion
		}
		if err := validateSkillVersionIncrement(latestCreatedVersion, version); err != nil {
			return err
		}
		// Also check that this version doesn't already exist (handles shift-back scenarios
		// where LatestVersion is older but higher versions exist in history).
		var existingVersion tables.TableSkillVersion
		if err := tx.First(&existingVersion, "skill_id = ? AND version = ?", skill.ID, version).Error; err == nil {
			return fmt.Errorf("skill version %q already exists in version history", version)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		files := skill.Files

		versionRow, err := createSkillVersion(tx, skill, version)
		if err != nil {
			return err
		}
		versionID = versionRow.ID
		writes, err := createVersionFiles(ctx, tx, objectStore, skill.ID, versionRow.ID, files)
		if err != nil {
			return err
		}
		objectWrites = append(objectWrites, writes...)

		if !serve {
			// Restore the existing serving data for the response.
			skill.LatestVersion = existing.LatestVersion
			skill.SkillMDBody = existing.SkillMDBody
			skill.Description = existing.Description
			skill.License = existing.License
			skill.Compatibility = existing.Compatibility
			skill.AllowedTools = existing.AllowedTools
			skill.Metadata = existing.Metadata
			skill.ExtraFrontmatter = existing.ExtraFrontmatter
		}
		return populateSkillFiles(tx, skill)
	}); err != nil {
		return err
	}

	// Phase 2: Upload objects to the store. On failure, delete the version row
	// as compensation — the skill still serves its previous version safely.
	if err := runPendingSkillObjectWrites(ctx, s.DB().WithContext(ctx), objectStore, objectWrites, false, skill.ID, versionID); err != nil {
		return err
	}

	if !serve {
		return nil
	}

	// Phase 3: Object writes succeeded — now atomically flip the skill to
	// serve the new version and bump the all-skills governance version.
	// If this fails, the skill continues serving its previous version safely.
	// The new version row and its objects remain intact so the caller can
	// retry serving (e.g. via shift-version) without re-uploading.
	if err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		skill.LatestVersion = version
		result := tx.Model(&tables.TableSkill{}).
			Where("id = ?", skill.ID).
			Select("Description", "License", "Compatibility", "Metadata", "ExtraFrontmatter", "AllowedTools", "SkillMDBody", "LatestVersion", "UpdatedAt").
			Updates(skill)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		bump, err := allSkillsBumpForVersionChange(existingLatestVersion, version)
		if err != nil {
			return err
		}
		return bumpAllSkillsVersionTx(ctx, tx, bump, false)
	}); err != nil {
		return fmt.Errorf("version %s was created successfully but could not be served — use shift-version to retry: %w", version, err)
	}
	return nil
}

// DeleteSkill deletes a skill, cleaning up object-store files across ALL versions.
func (s *RDBConfigStore) DeleteSkill(ctx context.Context, id string, objectStore objectstore.ObjectStore) error {
	var keys []string
	if err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var skill tables.TableSkill
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&skill, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Collect ALL distinct storage keys across all versions' files for this skill.
		if objectStore != nil {
			if err := tx.Model(&tables.TableSkillFile{}).
				Where("skill_version_id IN (?)",
					tx.Model(&tables.TableSkillVersion{}).Select("id").Where("skill_id = ?", id),
				).
				Where("storage_key IS NOT NULL AND storage_key != ''").
				Distinct("storage_key").
				Pluck("storage_key", &keys).Error; err != nil {
				return err
			}
		}
		// DB cascade handles: skill → versions → files, files → blobs
		if err := tx.Delete(&skill).Error; err != nil {
			return err
		}
		return bumpAllSkillsVersionTx(ctx, tx, allSkillsVersionBumpMajor, false)
	}); err != nil {
		return err
	}
	runSkillObjectCleanup(ctx, s.logger, objectStore, keys)
	return nil
}

// ListSkills returns paginated skills with the serving version's file count and total count.
func (s *RDBConfigStore) ListSkills(ctx context.Context, params SkillListQueryParams) ([]tables.TableSkill, int64, error) {
	var skills []tables.TableSkill
	query := s.ScopedDB(ctx).Model(&tables.TableSkill{})
	if params.Search != "" {
		like := "%" + strings.ToLower(params.Search) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	query = query.Omit("skill_md_body").Order(skillListOrder(params))
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	if params.Offset > 0 {
		query = query.Offset(params.Offset)
	}
	if err := query.Find(&skills).Error; err != nil {
		return nil, 0, err
	}
	if len(skills) > 0 {
		type fileCountRow struct {
			SkillID   string
			FileCount int64
		}
		var rows []fileCountRow
		if err := s.ScopedDB(ctx).
			Table("skill_versions").
			Select("skill_versions.skill_id AS skill_id, COUNT(skill_files.id) AS file_count").
			Joins("JOIN skills ON skills.id = skill_versions.skill_id AND skills.latest_version = skill_versions.version").
			Joins("LEFT JOIN skill_files ON skill_files.skill_version_id = skill_versions.id").
			Where("skill_versions.skill_id IN ?", skillIDs(skills)).
			Group("skill_versions.skill_id").
			Scan(&rows).Error; err != nil {
			return nil, 0, err
		}
		fileCounts := make(map[string]int64, len(rows))
		for _, row := range rows {
			fileCounts[row.SkillID] = row.FileCount
		}
		for i := range skills {
			skills[i].FileCount = fileCounts[skills[i].ID]
		}
	}
	return skills, total, nil
}

func skillListOrder(params SkillListQueryParams) string {
	column := "created_at"
	switch params.SortBy {
	case "name":
		column = "name"
	case "updated_at":
		column = "updated_at"
	case "created_at", "":
		column = "created_at"
	}
	return column + " " + normalizedSortOrder(params.Order) + ", id ASC"
}

func skillVersionListOrder(params SkillVersionListQueryParams) string {
	column := "created_at"
	switch params.SortBy {
	case "version":
		column = "version"
	case "created_at", "":
		column = "created_at"
	}
	return column + " " + normalizedSortOrder(params.Order) + ", id ASC"
}

func normalizedSortOrder(order string) string {
	if strings.EqualFold(order, "asc") {
		return "ASC"
	}
	return "DESC"
}

func skillIDs(skills []tables.TableSkill) []string {
	ids := make([]string, 0, len(skills))
	for _, skill := range skills {
		ids = append(ids, skill.ID)
	}
	return ids
}

// createVersionFiles creates file rows under a version, reusing blob/storage references
// for existing files that already carry a blob_id or storage_key.
func createVersionFiles(ctx context.Context, tx *gorm.DB, objectStore objectstore.ObjectStore, skillID, versionID string, files []tables.TableSkillFile) ([]pendingSkillObjectWrite, error) {
	objectWrites := make([]pendingSkillObjectWrite, 0)
	for i := range files {
		file := &files[i]
		file.ID = uuid.NewString()
		file.SkillVersionID = versionID
		data, err := validateSkillFileSource(file)
		if err != nil {
			return nil, err
		}
		if data != nil {
			file.FileSizeBytes = int64(len(data))
		}
		if err := validateSkillFileReference(tx, skillID, file); err != nil {
			return nil, err
		}
		if err := ValidateSkillFile(file); err != nil {
			return nil, err
		}
		if err := tx.Create(file).Error; err != nil {
			return nil, err
		}
		if data != nil {
			write, err := StoreSkillFileContent(ctx, tx, objectStore, skillID, file, data)
			if err != nil {
				return nil, err
			}
			if write != nil {
				objectWrites = append(objectWrites, *write)
			}
			if err := tx.Model(&tables.TableSkillFile{}).Where("id = ?", file.ID).Updates(map[string]any{
				"storage_key":     file.StorageKey,
				"blob_id":         file.BlobID,
				"file_size_bytes": file.FileSizeBytes,
				"mime_type":       file.MimeType,
			}).Error; err != nil {
				return nil, err
			}
		}
	}
	return objectWrites, nil
}

// createSkillVersion creates a version row with a frontmatter snapshot (no file snapshot needed;
// files belong to the version via FK).
func createSkillVersion(tx *gorm.DB, skill *tables.TableSkill, version string) (*tables.TableSkillVersion, error) {
	frontmatter := tables.SkillJSONMap{
		"description": skill.Description,
	}
	if skill.License != nil {
		frontmatter["license"] = *skill.License
	}
	if skill.Compatibility != nil {
		frontmatter["compatibility"] = *skill.Compatibility
	}
	if skill.AllowedTools != nil {
		frontmatter["allowed-tools"] = *skill.AllowedTools
	}
	for key, value := range skill.ExtraFrontmatter {
		if !IsSkillReservedFrontmatterField(key) {
			frontmatter[key] = value
		}
	}
	if len(skill.Metadata) > 0 {
		frontmatter["metadata"] = skill.Metadata
	}

	versionRow := tables.TableSkillVersion{
		ID:                  uuid.NewString(),
		SkillID:             skill.ID,
		Version:             version,
		SkillMDBody:         skill.SkillMDBody,
		FrontmatterSnapshot: frontmatter,
		CreatedBy:           skill.CreatedBy,
	}
	if err := tx.Create(&versionRow).Error; err != nil {
		return nil, err
	}
	return &versionRow, nil
}

// populateSkillFiles reloads the serving version's files into the transient skill.Files.
func populateSkillFiles(tx *gorm.DB, skill *tables.TableSkill) error {
	var version tables.TableSkillVersion
	if err := tx.Preload("Files").
		Preload("Files.Blob").
		First(&version, "skill_id = ? AND version = ?", skill.ID, skill.LatestVersion).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("serving version %q for skill %q has no version row; data may be inconsistent", skill.LatestVersion, skill.ID)
		}
		return err
	}
	skill.Files = version.Files
	skill.FileCount = int64(len(version.Files))
	return nil
}

// SkillFrontmatterFields contains skill fields reconstructed from a version frontmatter snapshot.
// Name is not included because it is immutable after creation.
type SkillFrontmatterFields struct {
	Description      string
	License          *string
	Compatibility    *string
	AllowedTools     *string
	Metadata         tables.SkillStringMap
	ExtraFrontmatter tables.SkillJSONMap
}

// ExtractSkillFieldsFromFrontmatter reconstructs skill fields from a version's frontmatter snapshot.
func ExtractSkillFieldsFromFrontmatter(frontmatter tables.SkillJSONMap) SkillFrontmatterFields {
	fields := SkillFrontmatterFields{ExtraFrontmatter: make(tables.SkillJSONMap)}
	if v, ok := frontmatter["description"].(string); ok {
		fields.Description = v
	}
	if v, ok := frontmatter["license"].(string); ok {
		fields.License = &v
	}
	if v, ok := frontmatter["compatibility"].(string); ok {
		fields.Compatibility = &v
	}
	if v, ok := frontmatter["allowed-tools"].(string); ok {
		fields.AllowedTools = &v
	}
	if m, ok := frontmatter["metadata"].(map[string]any); ok {
		fields.Metadata = make(tables.SkillStringMap, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				fields.Metadata[k] = s
			}
		}
	}
	for k, v := range frontmatter {
		if !IsSkillReservedFrontmatterField(k) {
			fields.ExtraFrontmatter[k] = v
		}
	}
	return fields
}

// ShiftSkillVersion shifts a skill to serve a previously-saved version.
// Files already exist on the target version; only the skill row fields and latest_version pointer change.
func (s *RDBConfigStore) ShiftSkillVersion(ctx context.Context, skillID string, targetVersion string, objectStore objectstore.ObjectStore) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var skill tables.TableSkill
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&skill, "id = ?", skillID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		var version tables.TableSkillVersion
		if err := tx.First(&version, "skill_id = ? AND version = ?", skillID, targetVersion).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("version %q not found for this skill", targetVersion)
			}
			return err
		}

		if skill.LatestVersion == targetVersion {
			return fmt.Errorf("skill is already serving version %q", targetVersion)
		}

		// Snapshot current state if not already versioned.
		var existingSnapshot tables.TableSkillVersion
		if err := tx.First(&existingSnapshot, "skill_id = ? AND version = ?", skillID, skill.LatestVersion).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if _, err := createSkillVersion(tx, &skill, skill.LatestVersion); err != nil {
					return err
				}
			} else {
				return err
			}
		}

		// Extract fields from target version frontmatter and update skill row (name is immutable).
		fields := ExtractSkillFieldsFromFrontmatter(version.FrontmatterSnapshot)
		if err := tx.Model(&tables.TableSkill{}).Where("id = ?", skillID).Updates(map[string]any{
			"description":       fields.Description,
			"license":           fields.License,
			"compatibility":     fields.Compatibility,
			"allowed_tools":     fields.AllowedTools,
			"metadata":          fields.Metadata,
			"extra_frontmatter": fields.ExtraFrontmatter,
			"skill_md_body":     version.SkillMDBody,
			"latest_version":    targetVersion,
		}).Error; err != nil {
			return err
		}
		return bumpAllSkillsVersionTx(ctx, tx, allSkillsVersionBumpPatch, false)
	})
}

// CreateSkillFileBlob creates an orphan blob record (e.g. for the upload endpoint's DB fallback).
func (s *RDBConfigStore) CreateSkillFileBlob(ctx context.Context, blob *tables.TableSkillFileBlob) error {
	if blob.ID == "" {
		blob.ID = uuid.NewString()
	}
	return s.DB().WithContext(ctx).Create(blob).Error
}

// UpdateSkillConfigHash sets the config_hash on a skill row so config reconciliation
// can detect subsequent no-change restarts without re-running UpdateSkill.
func (s *RDBConfigStore) UpdateSkillConfigHash(ctx context.Context, skillID string, configHash string) error {
	return s.DB().WithContext(ctx).
		Model(&tables.TableSkill{}).
		Where("id = ?", skillID).
		Update("config_hash", configHash).Error
}

const SkillOrphanCleanupGracePeriod = 24 * time.Hour

// CleanupOrphanSkillFileBlobs deletes DB fallback blobs not referenced by any skill file.
// When force is false, a 24-hour grace period protects freshly uploaded pending blobs.
func (s *RDBConfigStore) CleanupOrphanSkillFileBlobs(ctx context.Context, force bool) (int64, error) {
	query := s.DB().WithContext(ctx)
	if !force {
		cutoff := time.Now().Add(-SkillOrphanCleanupGracePeriod)
		query = query.Where("created_at < ?", cutoff)
	}
	result := query.
		Where("NOT EXISTS (?)",
			s.DB().Model(&tables.TableSkillFile{}).
				Select("1").
				Where("skill_files.blob_id = skill_file_blobs.id"),
		).
		Delete(&tables.TableSkillFileBlob{})
	return result.RowsAffected, result.Error
}
