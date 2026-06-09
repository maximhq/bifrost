package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ============================================================================
// Validation constants
// ============================================================================

const skillUploadObjectPrefix = "skills/uploads/"

// Reserved frontmatter keys that cannot appear in extra_frontmatter.
var reservedFrontmatterKeys = map[string]struct{}{
	"name":          {},
	"description":   {},
	"license":       {},
	"compatibility": {},
	"metadata":      {},
	"allowed-tools": {},
}

// ============================================================================
// SkillsHandler
// ============================================================================

// SkillsHandler handles skill repository management endpoints.
type SkillsHandler struct {
	store       configstore.ConfigStore
	objectStore objectstore.ObjectStore // nullable — object storage may not be configured
}

type SkillOrphanCleanupResult struct {
	DeletedDBBlobs        int64 `json:"deleted_db_blobs"`
	DeletedStorageObjects int   `json:"deleted_storage_objects"`
}

// NewSkillsHandler creates a new SkillsHandler.
// objectStore may be nil; when nil, file uploads fall back to DB blobs.
func NewSkillsHandler(store configstore.ConfigStore, objectStore objectstore.ObjectStore) *SkillsHandler {
	if store == nil {
		return nil
	}
	return &SkillsHandler{store: store, objectStore: objectStore}
}

// RegisterRoutes registers the routes for the SkillsHandler.
func (h *SkillsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// File uploads (before skill CRUD so /files/ prefix matches first)
	r.POST("/api/skills/files/upload", lib.ChainMiddlewares(h.uploadFile, middlewares...))
	r.POST("/api/skills/files/upload-url", lib.ChainMiddlewares(h.uploadURL, middlewares...))
	r.DELETE("/api/skills/files/orphans", lib.ChainMiddlewares(h.cleanupOrphanFiles, middlewares...))

	// Skill CRUD
	r.POST("/api/skills", lib.ChainMiddlewares(h.createSkill, middlewares...))
	r.GET("/api/skills", lib.ChainMiddlewares(h.listSkills, middlewares...))
	r.GET("/api/skills/{id}", lib.ChainMiddlewares(h.getSkill, middlewares...))
	r.GET("/api/skills/{id}/versions", lib.ChainMiddlewares(h.listSkillVersions, middlewares...))
	r.PUT("/api/skills/{id}", lib.ChainMiddlewares(h.updateSkill, middlewares...))
	r.DELETE("/api/skills/{id}", lib.ChainMiddlewares(h.deleteSkill, middlewares...))
	r.POST("/api/skills/{id}/shift-version", lib.ChainMiddlewares(h.shiftVersion, middlewares...))
}

// ============================================================================
// Request/Response Types
// ============================================================================

// CreateSkillRequest is the JSON payload for creating a skill.
type CreateSkillRequest struct {
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	License          *string           `json:"license,omitempty"`
	Compatibility    *string           `json:"compatibility,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	ExtraFrontmatter map[string]any    `json:"extra_frontmatter,omitempty"`
	AllowedTools     *string           `json:"allowed_tools,omitempty"`
	SkillMDBody      string            `json:"skill_md_body"`
	Version          string            `json:"version"`
	Files            []SkillFileEntry  `json:"files,omitempty"`
}

// UpdateSkillRequest is the JSON payload for updating a skill.
type UpdateSkillRequest struct {
	Description      string            `json:"description"`
	License          *string           `json:"license,omitempty"`
	Compatibility    *string           `json:"compatibility,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	ExtraFrontmatter map[string]any    `json:"extra_frontmatter,omitempty"`
	AllowedTools     *string           `json:"allowed_tools,omitempty"`
	SkillMDBody      string            `json:"skill_md_body"`
	Version          string            `json:"version"`
	Files            []SkillFileEntry  `json:"files,omitempty"`
	Serve            *bool             `json:"serve,omitempty"` // when false, creates a new version without switching serving
}

// SkillFileEntry represents a single file in the create/update request.
type SkillFileEntry struct {
	Path       string  `json:"path"`
	SourceType string  `json:"source_type"`
	Content    *string `json:"content,omitempty"`     // for source_type "text"
	SourceURL  *string `json:"source_url,omitempty"`  // for source_type "url"
	DataURL    *string `json:"dataurl,omitempty"`     // for source_type "dataurl"
	Filepath   *string `json:"filepath,omitempty"`    // for source_type "filepath"
	UploadID   *string `json:"upload_id,omitempty"`   // for source_type "upload"
	StorageKey *string `json:"storage_key,omitempty"` // existing stored file reference
	BlobID     *string `json:"blob_id,omitempty"`     // existing DB fallback blob reference
	MimeType   string  `json:"mime_type"`
}

// UploadURLRequest is the JSON payload for requesting a signed upload URL.
type UploadURLRequest struct {
	Path     string `json:"path"`
	MimeType string `json:"mime_type"`
}

// ============================================================================
// File Upload Handlers
// ============================================================================

// uploadFile handles POST /api/skills/files/upload — multipart file upload.
func (h *SkillsHandler) uploadFile(ctx *fasthttp.RequestCtx) {
	form, err := ctx.MultipartForm()
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid multipart form data")
		return
	}

	filePath := ""
	if paths, ok := form.Value["path"]; ok && len(paths) > 0 {
		filePath = paths[0]
	}

	files := form.File["file"]
	if len(files) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "file is required")
		return
	}
	fileHeader := files[0]
	filename := fileHeader.Filename
	if filePath == "" {
		filePath = filename
	}
	if errMsg := validateSkillFilePath(filePath); errMsg != "" {
		SendError(ctx, fasthttp.StatusBadRequest, errMsg)
		return
	}

	// Read file bytes.
	f, err := fileHeader.Open()
	if err != nil {
		logger.Error("failed to open uploaded file: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to read uploaded file")
		return
	}
	defer f.Close()

	data := make([]byte, fileHeader.Size)
	n, err := f.Read(data)
	if err != nil && n == 0 {
		logger.Error("failed to read uploaded file: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to read uploaded file")
		return
	}
	data = data[:n]

	uploadID := uuid.New().String()
	mimeType := fileHeader.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Store in object storage if available, else create a DB blob.
	if h.objectStore != nil {
		storageKey := fmt.Sprintf("skills/uploads/%s/%s", uploadID, path.Base(filePath))
		if err := h.objectStore.Put(ctx, storageKey, data, map[string]string{
			"upload_id": uploadID,
			"path":      filePath,
		}); err != nil {
			logger.Error("failed to store file in object storage: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to store uploaded file")
			return
		}

		SendJSON(ctx, map[string]any{
			"upload_id":       uploadID,
			"storage_key":     storageKey,
			"path":            filePath,
			"filename":        filename,
			"mime_type":       mimeType,
			"file_size_bytes": int64(len(data)),
		})
		return
	}

	// Fallback: store as a DB blob.
	blobID := uuid.New().String()
	blob := &tables.TableSkillFileBlob{
		ID:   blobID,
		Data: data,
	}
	if err := h.store.CreateSkillFileBlob(ctx, blob); err != nil {
		logger.Error("failed to store file blob: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to store uploaded file")
		return
	}

	SendJSON(ctx, map[string]any{
		"upload_id":       uploadID,
		"blob_id":         blobID,
		"path":            filePath,
		"filename":        filename,
		"mime_type":       mimeType,
		"file_size_bytes": int64(len(data)),
	})
}

// cleanupOrphanFiles handles DELETE /api/skills/files/orphans.
func (h *SkillsHandler) cleanupOrphanFiles(ctx *fasthttp.RequestCtx) {
	force := string(ctx.QueryArgs().Peek("force")) == "true"
	result, err := CleanupOrphanSkillFiles(ctx, h.store, h.objectStore, force)
	if err != nil {
		logger.Error("failed to cleanup orphan skill files: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to cleanup orphan files")
		return
	}

	SendJSON(ctx, map[string]any{
		"deleted_db_blobs":          result.DeletedDBBlobs,
		"deleted_storage_objects":   result.DeletedStorageObjects,
		"object_storage_configured": h.objectStore != nil,
		"message":                   "orphan skill files cleaned up successfully",
	})
}

// CleanupOrphanSkillFiles removes DB fallback blobs and unreferenced upload objects.
// When force is false, a 30-minute grace period protects freshly uploaded pending files.
func CleanupOrphanSkillFiles(ctx context.Context, store configstore.ConfigStore, objectStore objectstore.ObjectStore, force bool) (SkillOrphanCleanupResult, error) {
	var result SkillOrphanCleanupResult
	if store == nil {
		return result, fmt.Errorf("config store is required")
	}

	deletedDBBlobs, err := store.CleanupOrphanSkillFileBlobs(ctx, force)
	if err != nil {
		return result, fmt.Errorf("cleanup orphan skill file blobs: %w", err)
	}
	result.DeletedDBBlobs = deletedDBBlobs

	if objectStore == nil {
		return result, nil
	}

	uploadObjects, err := objectStore.ListByPrefix(ctx, skillUploadObjectPrefix)
	if err != nil {
		return result, fmt.Errorf("list skill upload objects: %w", err)
	}
	if len(uploadObjects) == 0 {
		return result, nil
	}

	var referencedKeys []string
	if err := store.DB().WithContext(ctx).
		Model(&tables.TableSkillFile{}).
		Where("storage_key IS NOT NULL AND storage_key != ''").
		Distinct("storage_key").
		Pluck("storage_key", &referencedKeys).Error; err != nil {
		return result, fmt.Errorf("list referenced skill file storage keys: %w", err)
	}

	referenced := make(map[string]struct{}, len(referencedKeys))
	for _, key := range referencedKeys {
		referenced[key] = struct{}{}
	}

	// Only reap unreferenced objects older than 30 minutes unless force is set.
	orphanKeys := make([]string, 0)
	if force {
		for _, obj := range uploadObjects {
			if _, ok := referenced[obj.Key]; !ok {
				orphanKeys = append(orphanKeys, obj.Key)
			}
		}
	} else {
		cutoff := time.Now().Add(-30 * time.Minute)
		for _, obj := range uploadObjects {
			if _, ok := referenced[obj.Key]; !ok && obj.LastModified.Before(cutoff) {
				orphanKeys = append(orphanKeys, obj.Key)
			}
		}
	}
	if len(orphanKeys) == 0 {
		return result, nil
	}
	if err := objectStore.DeleteBatch(ctx, orphanKeys); err != nil {
		return result, fmt.Errorf("delete orphan skill upload objects: %w", err)
	}
	result.DeletedStorageObjects = len(orphanKeys)
	return result, nil
}

// uploadURL handles POST /api/skills/files/upload-url — signed URL for direct upload.
func (h *SkillsHandler) uploadURL(ctx *fasthttp.RequestCtx) {
	if h.objectStore == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "object storage is not configured; use the multipart upload endpoint instead")
		return
	}

	var req UploadURLRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}

	if errMsg := validateSkillFilePath(req.Path); errMsg != "" {
		SendError(ctx, fasthttp.StatusBadRequest, errMsg)
		return
	}

	uploadID := uuid.New().String()
	storageKey := fmt.Sprintf("skills/uploads/%s/%s", uploadID, path.Base(req.Path))

	// Presigned URL support is not yet available on the ObjectStore interface.
	// Return 501 with the storage key so clients can fall back to multipart upload.
	ctx.SetStatusCode(fasthttp.StatusNotImplemented)
	ctx.SetContentType("application/json")
	resp := map[string]any{
		"upload_id":   uploadID,
		"storage_key": storageKey,
		"path":        req.Path,
		"message":     "presigned URL upload is not yet supported; use the multipart upload endpoint",
	}
	if err := json.NewEncoder(ctx).Encode(resp); err != nil {
		logger.Error("failed to encode upload-url response: %v", err)
	}
}

// ============================================================================
// Skill CRUD Handlers
// ============================================================================

// createSkill handles POST /api/skills.
func (h *SkillsHandler) createSkill(ctx *fasthttp.RequestCtx) {
	var req CreateSkillRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}

	if err := inferLiveFileMimeTypes(req.Files, "skills api create"); err != nil {
		logger.Warn("%v", err)
	}

	if errs := validateSkillRequest(req.Name, req.Description, req.SkillMDBody, req.Version, req.ExtraFrontmatter, req.Files, true); len(errs) > 0 {
		SendError(ctx, fasthttp.StatusBadRequest, strings.Join(errs, "; "))
		return
	}

	skill := &tables.TableSkill{
		ID:               uuid.New().String(),
		Name:             req.Name,
		Description:      req.Description,
		License:          req.License,
		Compatibility:    req.Compatibility,
		Metadata:         tables.SkillStringMap(req.Metadata),
		ExtraFrontmatter: tables.SkillJSONMap(req.ExtraFrontmatter),
		AllowedTools:     req.AllowedTools,
		SkillMDBody:      req.SkillMDBody,
		LatestVersion:    req.Version,
	}

	// Build file records (SkillVersionID is set by the store).
	for _, fe := range req.Files {
		skill.Files = append(skill.Files, fileEntryToTableSkillFile(fe))
	}

	if err := h.store.CreateSkill(ctx, skill, req.Version, h.objectStore); err != nil {
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, "a skill with this name already exists")
			return
		}
		logger.Error("failed to create skill: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}

	SendJSON(ctx, map[string]any{
		"skill": skill,
	})
}

// listSkills handles GET /api/skills.
func (h *SkillsHandler) listSkills(ctx *fasthttp.RequestCtx) {
	params := configstore.SkillListQueryParams{
		Search: string(ctx.QueryArgs().Peek("search")),
	}

	if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			params.Limit = v
		}
	}
	if params.Limit == 0 {
		params.Limit = 50 // sensible default
	}

	if offsetStr := string(ctx.QueryArgs().Peek("offset")); offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			params.Offset = v
		}
	}

	skills, total, err := h.store.ListSkills(ctx, params)
	if err != nil {
		logger.Error("failed to list skills: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}

	SendJSON(ctx, map[string]any{
		"skills": skills,
		"total":  total,
		"limit":  params.Limit,
		"offset": params.Offset,
	})
}

// getSkill handles GET /api/skills/{id}.
func (h *SkillsHandler) getSkill(ctx *fasthttp.RequestCtx) {
	id, ok := extractStringParam(ctx, "id")
	if !ok {
		return
	}

	// Check for ?version=X.Y.Z query param to return a specific version's data.
	if versionParam := string(ctx.QueryArgs().Peek("version")); versionParam != "" {
		skill, err := h.store.GetSkillLean(ctx, id)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, "skill not found")
				return
			}
			logger.Error("failed to get skill: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
			return
		}
		found, err := h.store.GetSkillVersion(ctx, id, versionParam)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("version %q not found", versionParam))
				return
			}
			logger.Error("failed to get skill version: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
			return
		}
		// Overlay version data onto skill response (name is immutable, stays as current).
		skill.SkillMDBody = found.SkillMDBody
		skill.LatestVersion = found.Version
		fields := configstore.ExtractSkillFieldsFromFrontmatter(found.FrontmatterSnapshot)
		skill.Description = fields.Description
		skill.License = fields.License
		skill.Compatibility = fields.Compatibility
		skill.AllowedTools = fields.AllowedTools
		skill.Metadata = fields.Metadata
		skill.ExtraFrontmatter = fields.ExtraFrontmatter
		skill.Files = found.Files
		SendJSON(ctx, map[string]any{
			"skill": skill,
		})
		return
	}

	skill, err := h.store.GetSkillLean(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "skill not found")
			return
		}
		logger.Error("failed to get skill: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}

	SendJSON(ctx, map[string]any{
		"skill": skill,
	})
}

// listSkillVersions handles GET /api/skills/{id}/versions.
func (h *SkillsHandler) listSkillVersions(ctx *fasthttp.RequestCtx) {
	id, ok := extractStringParam(ctx, "id")
	if !ok {
		return
	}

	limit := 25
	offset := 0
	if n, err := strconv.Atoi(string(ctx.QueryArgs().Peek("limit"))); err == nil && n > 0 {
		limit = min(n, 100)
	}
	if n, err := strconv.Atoi(string(ctx.QueryArgs().Peek("offset"))); err == nil && n >= 0 {
		offset = n
	}

	versions, total, err := h.store.ListSkillVersions(ctx, id, limit, offset)
	if err != nil {
		logger.Error("failed to list skill versions: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}

	SendJSON(ctx, map[string]any{
		"versions": versions,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

// shiftVersion handles POST /api/skills/{id}/shift-version.
func (h *SkillsHandler) shiftVersion(ctx *fasthttp.RequestCtx) {
	id, ok := extractStringParam(ctx, "id")
	if !ok {
		return
	}

	var req struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if req.Version == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "version is required")
		return
	}

	if err := h.store.ShiftSkillVersion(ctx, id, req.Version, h.objectStore); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "skill not found")
			return
		}
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "already serving") {
			SendError(ctx, fasthttp.StatusBadRequest, errMsg)
			return
		}
		logger.Error("failed to shift skill version: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, errMsg)
		return
	}

	skill, err := h.store.GetSkill(ctx, id)
	if err != nil {
		logger.Error("failed to reload skill after version shift: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "version shifted but failed to reload skill")
		return
	}

	SendJSON(ctx, map[string]any{
		"skill": skill,
	})
}

// updateSkill handles PUT /api/skills/{id}.
func (h *SkillsHandler) updateSkill(ctx *fasthttp.RequestCtx) {
	id, ok := extractStringParam(ctx, "id")
	if !ok {
		return
	}

	var req UpdateSkillRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}

	if err := inferLiveFileMimeTypes(req.Files, "skills api update"); err != nil {
		logger.Warn("%v", err)
	}

	if errs := validateSkillRequest("", req.Description, req.SkillMDBody, req.Version, req.ExtraFrontmatter, req.Files, false); len(errs) > 0 {
		SendError(ctx, fasthttp.StatusBadRequest, strings.Join(errs, "; "))
		return
	}

	skill, err := h.store.GetSkill(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "skill not found")
			return
		}
		logger.Error("failed to get skill for update: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}

	// Apply updates (name is immutable after creation).
	skill.Description = req.Description
	skill.License = req.License
	skill.Compatibility = req.Compatibility
	skill.Metadata = tables.SkillStringMap(req.Metadata)
	skill.ExtraFrontmatter = tables.SkillJSONMap(req.ExtraFrontmatter)
	skill.AllowedTools = req.AllowedTools
	skill.SkillMDBody = req.SkillMDBody
	skill.LatestVersion = req.Version

	// Rebuild files from the request (SkillVersionID is set by the store).
	var files []tables.TableSkillFile
	for _, fe := range req.Files {
		files = append(files, fileEntryToTableSkillFile(fe))
	}
	skill.Files = files

	serve := req.Serve == nil || *req.Serve // default true for backward compat
	if err := h.store.UpdateSkill(ctx, skill, req.Version, serve, h.objectStore); err != nil {
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("version %s already exists for this skill; provide a new version", req.Version))
			return
		}
		logger.Error("failed to update skill: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}

	SendJSON(ctx, map[string]any{
		"skill": skill,
	})
}

// deleteSkill handles DELETE /api/skills/{id}.
func (h *SkillsHandler) deleteSkill(ctx *fasthttp.RequestCtx) {
	id, ok := extractStringParam(ctx, "id")
	if !ok {
		return
	}

	if err := h.store.DeleteSkill(ctx, id, h.objectStore); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "skill not found")
			return
		}
		logger.Error("failed to delete skill: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}

	SendJSON(ctx, map[string]any{
		"message": "skill deleted successfully",
	})
}

// ============================================================================
// Helpers
// ============================================================================

// extractStringParam extracts a path param and sends an error if missing.
func extractStringParam(ctx *fasthttp.RequestCtx, name string) (string, bool) {
	val := ctx.UserValue(name)
	if val == nil {
		SendError(ctx, fasthttp.StatusBadRequest, name+" is required")
		return "", false
	}
	s, ok := val.(string)
	if !ok || s == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid "+name)
		return "", false
	}
	return s, true
}

// inferLiveFileMimeTypes infers MIME for live references before validation/storage.
// Failures are warnings only: live sources may become reachable later, so we keep
// the reference and fall back to octet-stream instead of blocking registration.
func inferLiveFileMimeTypes(files []SkillFileEntry, source string) error {
	var warnings []string
	for i := range files {
		f := &files[i]
		switch f.SourceType {
		case tables.SkillSourceTypeURL:
			if f.SourceURL == nil || *f.SourceURL == "" {
				continue
			}
			mimeType, err := inferMimeTypeFromURL(*f.SourceURL)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("file %q: could not infer MIME from URL %q: %v", f.Path, *f.SourceURL, err))
				f.MimeType = "application/octet-stream"
				continue
			}
			f.MimeType = mimeType
		case tables.SkillSourceTypeFilepath:
			if f.Filepath == nil || *f.Filepath == "" {
				continue
			}
			mimeType, err := inferMimeTypeFromFilepath(*f.Filepath)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("file %q: could not infer MIME from filepath %q: %v", f.Path, *f.Filepath, err))
				f.MimeType = "application/octet-stream"
				continue
			}
			f.MimeType = mimeType
		}
	}
	if len(warnings) > 0 {
		return fmt.Errorf("%s MIME inference warnings: %s", source, strings.Join(warnings, "; "))
	}
	return nil
}

func inferMimeTypeFromURL(rawURL string) (string, error) {
	client := http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
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

func inferMimeTypeFromFilepath(path string) (string, error) {
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

// fileEntryToTableSkillFile converts a request file entry to a TableSkillFile record.
// SkillVersionID is set by the store layer during create/update.
func fileEntryToTableSkillFile(fe SkillFileEntry) tables.TableSkillFile {
	sf := tables.TableSkillFile{
		Path:       fe.Path,
		SourceType: fe.SourceType,
		MimeType:   fe.MimeType,
	}

	switch fe.SourceType {
	case tables.SkillSourceTypeURL:
		sf.SourceURL = fe.SourceURL
	case tables.SkillSourceTypeFilepath:
		sf.SourceFilepath = fe.Filepath
	case tables.SkillSourceTypeText:
		sf.InlineContent = fe.Content
		sf.StorageKey = fe.StorageKey
		sf.BlobID = fe.BlobID
	case tables.SkillSourceTypeDataURL:
		sf.DataURL = fe.DataURL
		sf.StorageKey = fe.StorageKey
		sf.BlobID = fe.BlobID
	case tables.SkillSourceTypeUpload:
		sf.UploadID = fe.UploadID
		sf.StorageKey = fe.StorageKey
		sf.BlobID = fe.BlobID
	}

	return sf
}

// validateSkillRequest performs backend validation on skill fields.
// When validateName is true, the name field is validated (used for create).
// Returns a slice of error messages (empty if valid).
func validateSkillRequest(name, description, body, version string, extraFM map[string]any, files []SkillFileEntry, validateName bool) []string {
	var errs []string

	// Name: 1-64 chars, lowercase alphanum + hyphens, no leading/trailing/consecutive hyphens.
	if validateName {
		if err := configstore.ValidateSkillName(name); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if description == "" {
		errs = append(errs, "description is required")
	} else if len(description) > 1024 {
		errs = append(errs, "description must be at most 1024 characters")
	}

	// Body: non-empty.
	if strings.TrimSpace(body) == "" {
		errs = append(errs, "skill_md_body is required and must not be empty")
	}

	// Version: required.
	if err := configstore.ValidateSkillVersion(version); err != nil {
		errs = append(errs, err.Error())
	}

	// Extra frontmatter: no reserved keys.
	for key := range extraFM {
		if _, reserved := reservedFrontmatterKeys[key]; reserved {
			errs = append(errs, fmt.Sprintf("extra_frontmatter key %q is reserved and cannot be used", key))
		}
	}

	// Files: validate each entry.
	for i, f := range files {
		prefix := fmt.Sprintf("files[%d]", i)
		if f.Path == "" {
			errs = append(errs, prefix+": path is required")
		}
		if f.SourceType == "" {
			errs = append(errs, prefix+": source_type is required")
		}
		if f.Path != "" {
			if errMsg := validateSkillFilePath(f.Path); errMsg != "" {
				errs = append(errs, prefix+": "+errMsg)
			}
		}
		// Source-type-specific checks.
		switch f.SourceType {
		case tables.SkillSourceTypeURL:
			if f.SourceURL == nil || *f.SourceURL == "" {
				errs = append(errs, prefix+": source_url is required for url source type")
			} else if !strings.HasPrefix(*f.SourceURL, "http://") && !strings.HasPrefix(*f.SourceURL, "https://") {
				errs = append(errs, prefix+": source_url must start with http:// or https://")
			}
		case tables.SkillSourceTypeText:
			if (f.Content == nil || *f.Content == "") && f.StorageKey == nil && f.BlobID == nil {
				errs = append(errs, prefix+": content or an existing file reference is required for text source type")
			}
		case tables.SkillSourceTypeDataURL:
			if (f.DataURL == nil || *f.DataURL == "") && f.StorageKey == nil && f.BlobID == nil {
				errs = append(errs, prefix+": dataurl or an existing file reference is required for dataurl source type")
			} else if f.DataURL != nil && *f.DataURL != "" && (!strings.HasPrefix(*f.DataURL, "data:") || !strings.Contains(*f.DataURL, ";base64,")) {
				errs = append(errs, prefix+": dataurl must be a valid data URL (data:...;base64,...)")
			}
		case tables.SkillSourceTypeFilepath:
			if f.Filepath == nil || *f.Filepath == "" {
				errs = append(errs, prefix+": filepath is required for filepath source type")
			} else if !strings.HasPrefix(*f.Filepath, "/") {
				errs = append(errs, prefix+": filepath must be an absolute path")
			}
		case tables.SkillSourceTypeUpload:
			if f.StorageKey == nil && f.BlobID == nil {
				errs = append(errs, prefix+": storage_key or blob_id is required for upload source type")
			}
		default:
			if f.SourceType != "" {
				errs = append(errs, prefix+": unknown source_type "+f.SourceType)
			}
		}
	}

	return errs
}

// validateSkillFilePath checks that a relative file path is safe (no traversal).
func validateSkillFilePath(p string) string {
	if err := configstore.ValidateSkillFilePath(p); err != nil {
		return err.Error()
	}
	return ""
}
