// SBS fork: MetaMCP-style MCP features on top of Bifrost's MCP gateway.
//
//   - Endpoints: a public URL name (/mcp/<name>) that points at a Virtual MCP ("namespace"),
//     can be re-pointed at any time, and carries its own auth options: virtual key required
//     (default), API key in a query parameter, or public (requests without credentials act as
//     a bound virtual key, so governance, budgets and logs still apply).
//   - Tool overrides: per Virtual MCP name/title/description/annotation overrides, applied on
//     tools/list and mapped back on tools/call (stored in MCPToolSpec.Overrides).
//   - OpenAPI export: /mcp/<name>/openapi.json and POST /mcp/<name>/tools/<tool> for REST
//     clients such as Open WebUI. Both loop back through /mcp/<name>, so auth, governance,
//     overrides and logging are exactly the MCP endpoint's.
//
// Everything lives in this file; upstream files only call the hooks marked "SBS fork".
package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------------------------
// Storage

// SBS endpoint auth modes.
const (
	SBSEndpointAuthVirtualKey = "virtual_key" // the caller must present a virtual key (or OAuth token)
	SBSEndpointAuthPublic     = "public"      // no credential needed: the request acts as PublicVirtualKeyID
)

// TableSBSMCPEndpoint is a MetaMCP-style endpoint: a URL name mapped to a Virtual MCP.
type TableSBSMCPEndpoint struct {
	ID                 uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name               string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"`
	Description        *string   `gorm:"type:text" json:"description,omitempty"`
	VirtualMCPID       uint      `gorm:"not null;index" json:"virtual_mcp_id"`
	Enabled            bool      `gorm:"not null" json:"enabled"`
	AuthMode           string    `gorm:"type:varchar(20);not null" json:"auth_mode"`
	PublicVirtualKeyID *string   `gorm:"type:varchar(255)" json:"public_virtual_key_id,omitempty"`
	AllowQueryParamKey bool      `gorm:"not null" json:"allow_query_param_key"`
	OpenAPIEnabled     bool      `gorm:"not null" json:"openapi_enabled"`
	PublicGrantAdded   bool      `gorm:"not null;default:false" json:"public_grant_added"` // this endpoint assigned its namespace to its public key
	CreatedAt          time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time `gorm:"not null" json:"updated_at"`
}

// TableName keeps the fork's tables visibly apart from upstream ones.
func (TableSBSMCPEndpoint) TableName() string { return "sbs_mcp_endpoints" }

// sbsEndpointNamePattern matches MetaMCP's endpoint names so migrated URLs keep working.
var sbsEndpointNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// sbsToolNamePattern is what an overridden tool name may look like (MCP tool-name charset).
var sbsToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

// sbsQueryParamKeys are the query parameters an endpoint with AllowQueryParamKey reads a key from.
var sbsQueryParamKeys = []string{"api_key", "apiKey", "key"}

// ---------------------------------------------------------------------------------------------
// Module

// sbsVirtualMCPView is a Virtual MCP with its overrides resolved to gateway tool names.
type sbsVirtualMCPView struct {
	def       configstoreTables.TableVirtualMCP
	overrides map[string]configstoreTables.MCPToolOverride // gateway name (client-tool) -> override
	reverse   map[string]string                            // overridden name -> gateway name
}

type sbsSnapshot struct {
	loadedAt     time.Time
	endpoints    map[string]TableSBSMCPEndpoint // by name
	vmcpsByID    map[uint]*sbsVirtualMCPView
	vmcpsBySlug  map[string]*sbsVirtualMCPView
	publicVKVals map[string]string // virtual key ID -> value, for public endpoints
}

// SBSMCP holds the fork's state. One per process (see sbsMCP).
type SBSMCP struct {
	config *lib.Config
	db     *gorm.DB
	snap   atomic.Pointer[sbsSnapshot]
	mu     sync.Mutex // serializes reloads
	http   *http.Client
}

// sbsMCP is the process-wide instance; nil until InitSBSMCP runs, and every hook is a no-op then.
var sbsMCP *SBSMCP

// sbsSnapshotTTL bounds how stale a Virtual MCP edit made through the upstream API can be.
const sbsSnapshotTTL = 3 * time.Second

// InitSBSMCP creates the fork's table and loads its state. Safe to call without a config store
// (the features are then off).
func InitSBSMCP(ctx context.Context, config *lib.Config) error {
	if config == nil || config.ConfigStore == nil {
		return nil
	}
	db := config.ConfigStore.DB()
	if db == nil {
		return nil
	}
	if err := db.WithContext(ctx).AutoMigrate(&TableSBSMCPEndpoint{}); err != nil {
		return fmt.Errorf("sbs: migrate sbs_mcp_endpoints: %w", err)
	}
	s := &SBSMCP{config: config, db: db, http: &http.Client{Timeout: 10 * time.Minute}}
	if _, err := s.reload(ctx); err != nil {
		return err
	}
	sbsMCP = s
	return nil
}

// snapshot returns the current state, reloading it when older than the TTL.
func (s *SBSMCP) snapshot(ctx context.Context) *sbsSnapshot {
	snap := s.snap.Load()
	if snap != nil && time.Since(snap.loadedAt) < sbsSnapshotTTL {
		return snap
	}
	fresh, err := s.reload(ctx)
	if err != nil {
		logger.Warn("sbs: reload failed, serving the previous state: %v", err)
		if snap == nil {
			return &sbsSnapshot{}
		}
		return snap
	}
	return fresh
}

// invalidate forces the next snapshot to reload.
func (s *SBSMCP) invalidate() {
	if snap := s.snap.Load(); snap != nil {
		stale := *snap
		stale.loadedAt = time.Time{}
		s.snap.Store(&stale)
	}
}

func (s *SBSMCP) reload(ctx context.Context) (*sbsSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap := s.snap.Load(); snap != nil && time.Since(snap.loadedAt) < sbsSnapshotTTL {
		return snap, nil
	}
	ctx = context.WithoutCancel(ctx)

	var rows []TableSBSMCPEndpoint
	if err := s.db.WithContext(ctx).Order("name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("sbs: load endpoints: %w", err)
	}
	defs, err := s.config.ConfigStore.GetVirtualMCPs(ctx)
	if err != nil {
		return nil, fmt.Errorf("sbs: load virtual mcps: %w", err)
	}
	clientNames := s.config.GetMCPClientNames()

	snap := &sbsSnapshot{
		loadedAt:     time.Now(),
		endpoints:    make(map[string]TableSBSMCPEndpoint, len(rows)),
		vmcpsByID:    make(map[uint]*sbsVirtualMCPView, len(defs)),
		vmcpsBySlug:  make(map[string]*sbsVirtualMCPView, len(defs)),
		publicVKVals: map[string]string{},
	}
	for _, row := range rows {
		snap.endpoints[row.Name] = row
		if row.AuthMode == SBSEndpointAuthPublic && row.PublicVirtualKeyID != nil && *row.PublicVirtualKeyID != "" {
			id := *row.PublicVirtualKeyID
			if _, done := snap.publicVKVals[id]; done {
				continue
			}
			vk, err := s.config.ConfigStore.GetVirtualKey(ctx, id)
			if err != nil || vk == nil {
				logger.Warn("sbs: endpoint %q: public virtual key %s not found", row.Name, id)
				continue
			}
			snap.publicVKVals[id] = vk.Value.GetValue()
		}
	}
	for i := range defs {
		view := &sbsVirtualMCPView{def: defs[i], overrides: map[string]configstoreTables.MCPToolOverride{}, reverse: map[string]string{}}
		for _, spec := range defs[i].ParsedTools {
			clientName := clientNames[spec.MCPClientID]
			if clientName == "" {
				continue
			}
			for tool, ov := range spec.Overrides {
				gatewayName := clientName + "-" + tool
				view.overrides[gatewayName] = ov
				if ov.Name != nil && *ov.Name != "" && *ov.Name != gatewayName {
					view.reverse[*ov.Name] = gatewayName
				}
			}
		}
		snap.vmcpsByID[defs[i].ID] = view
		if defs[i].EndpointSlug != "" {
			snap.vmcpsBySlug[defs[i].EndpointSlug] = view
		}
	}
	s.snap.Store(snap)
	return snap, nil
}

// ---------------------------------------------------------------------------------------------
// /mcp/<slug> request handling

// sbsCtxKeyVMCPSlug carries the Virtual MCP slug a request is served from, for the list/call hooks.
type sbsCtxKeyVMCPSlug struct{}

// sbsUserValueEndpoint is the fasthttp user value naming the SBS endpoint a request came in on.
const sbsUserValueEndpoint = "sbs_endpoint"

// sbsHasCredential reports whether the request already carries a credential of its own.
func sbsHasCredential(ctx *fasthttp.RequestCtx) bool {
	return getVKFromRequest(ctx) != "" || len(ctx.Request.Header.Peek("Authorization")) > 0
}

// sbsQueryParamKey returns a key passed in the query string, if any.
func sbsQueryParamKey(ctx *fasthttp.RequestCtx) string {
	for _, name := range sbsQueryParamKeys {
		if v := string(ctx.QueryArgs().Peek(name)); v != "" {
			return v
		}
	}
	return ""
}

// SBSWrapMCPRoute (SBS fork) runs before the whole /mcp/<slug> middleware chain: it resolves an
// SBS endpoint name to its Virtual MCP slug and applies the endpoint's auth options, so the
// upstream auth, governance and admission code sees an ordinary Virtual MCP request.
func SBSWrapMCPRoute(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if sbsMCP != nil && !sbsMCP.preprocess(ctx) {
			return
		}
		next(ctx)
	}
}

// preprocess returns false when it already answered the request.
func (s *SBSMCP) preprocess(ctx *fasthttp.RequestCtx) bool {
	slug, _ := ctx.UserValue("slug").(string)
	if slug == "" {
		return true
	}
	snap := s.snapshot(ctx)
	ep, ok := snap.endpoints[slug]
	if !ok {
		return true // a Virtual MCP or client slug: upstream behaviour
	}
	view := snap.vmcpsByID[ep.VirtualMCPID]
	if !ep.Enabled || view == nil || !view.def.Enabled {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("MCP endpoint %q is not available", slug))
		return false
	}
	ctx.SetUserValue("slug", view.def.EndpointSlug)
	ctx.SetUserValue(sbsUserValueEndpoint, ep.Name)

	if sbsHasCredential(ctx) {
		return true
	}
	if ep.AllowQueryParamKey {
		if key := sbsQueryParamKey(ctx); key != "" {
			ctx.Request.Header.Set("x-bf-vk", key)
			return true
		}
	}
	if ep.AuthMode == SBSEndpointAuthPublic && ep.PublicVirtualKeyID != nil {
		if value := snap.publicVKVals[*ep.PublicVirtualKeyID]; value != "" {
			ctx.Request.Header.Set("x-bf-vk", value)
		}
	}
	return true
}

// SBSStampMCPContext (SBS fork) records the served Virtual MCP slug on the request context.
func SBSStampMCPContext(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) {
	if sbsMCP == nil || bifrostCtx == nil {
		return
	}
	if slug, _ := ctx.UserValue("slug").(string); slug != "" {
		bifrostCtx.SetValue(sbsCtxKeyVMCPSlug{}, slug)
	}
}

// viewFor returns the Virtual MCP the request is served from, or nil.
func (s *SBSMCP) viewFor(ctx context.Context) *sbsVirtualMCPView {
	slug, _ := ctx.Value(sbsCtxKeyVMCPSlug{}).(string)
	if slug == "" {
		return nil
	}
	return s.snapshot(ctx).vmcpsBySlug[slug]
}

// SBSApplyToolOverrides (SBS fork) is an AfterListTools hook: it presents each tool the way the
// Virtual MCP's overrides say.
func SBSApplyToolOverrides(ctx context.Context, _ any, _ *mcp.ListToolsRequest, result *mcp.ListToolsResult) {
	if sbsMCP == nil || result == nil {
		return
	}
	view := sbsMCP.viewFor(ctx)
	if view == nil || len(view.overrides) == 0 {
		return
	}
	for i := range result.Tools {
		ov, ok := view.overrides[result.Tools[i].Name]
		if !ok {
			continue
		}
		sbsApplyOverride(&result.Tools[i], ov)
	}
}

// sbsApplyOverride rewrites one listed tool in place.
func sbsApplyOverride(tool *mcp.Tool, ov configstoreTables.MCPToolOverride) {
	if ov.Name != nil && *ov.Name != "" {
		tool.Name = *ov.Name
	}
	if ov.Description != nil && *ov.Description != "" {
		tool.Description = *ov.Description
	}
	if ov.Title != nil && *ov.Title != "" {
		tool.Title = *ov.Title
		tool.Annotations.Title = *ov.Title
	}
	if len(ov.Annotations) > 0 {
		merged := map[string]any{}
		if raw, err := json.Marshal(tool.Annotations); err == nil {
			_ = json.Unmarshal(raw, &merged)
		}
		for k, v := range ov.Annotations {
			merged[k] = v
		}
		if raw, err := json.Marshal(merged); err == nil {
			var annotations mcp.ToolAnnotation
			if json.Unmarshal(raw, &annotations) == nil {
				tool.Annotations = annotations
			}
		}
	}
}

// SBSResolveToolOverride (SBS fork) is a BeforeCallTool hook: a call by an overridden name runs
// the tool it stands for. It must run before upstream's alias resolution.
func SBSResolveToolOverride(ctx context.Context, _ any, request *mcp.CallToolRequest) {
	if sbsMCP == nil || request == nil {
		return
	}
	view := sbsMCP.viewFor(ctx)
	if view == nil || len(view.reverse) == 0 {
		return
	}
	if target, ok := view.reverse[request.Params.Name]; ok {
		if bc, ok := schemas.BifrostContextFromContext(ctx); ok {
			bc.SetValue(schemas.BifrostContextKeyMCPRequestedToolName, request.Params.Name)
		}
		request.Params.Name = target
	}
}

// SBSPreserveOverrides (SBS fork) keeps the overrides stored on a Virtual MCP when an update
// sends tool specs without them (the upstream editor does not know about overrides). Overrides
// are written through PUT /api/sbs/mcp/virtual-mcps/{id}/overrides instead.
func SBSPreserveOverrides(existing, incoming []configstoreTables.MCPToolSpec) []configstoreTables.MCPToolSpec {
	if len(existing) == 0 {
		return incoming
	}
	byClient := make(map[string]map[string]configstoreTables.MCPToolOverride, len(existing))
	for _, spec := range existing {
		if len(spec.Overrides) > 0 {
			byClient[spec.MCPClientID] = spec.Overrides
		}
	}
	for i := range incoming {
		if incoming[i].Overrides == nil {
			incoming[i].Overrides = byClient[incoming[i].MCPClientID]
		}
	}
	return incoming
}

// ---------------------------------------------------------------------------------------------
// Admin API: /api/sbs/mcp/...

// SBSMCPHandler serves the fork's admin API.
type SBSMCPHandler struct {
	config     *lib.Config
	governance GovernanceManager // keeps governance's in-memory Virtual MCPs in step; may be nil
}

// NewSBSMCPHandler returns the admin handler; governance may be nil.
func NewSBSMCPHandler(config *lib.Config, governance GovernanceManager) *SBSMCPHandler {
	return &SBSMCPHandler{config: config, governance: governance}
}

// refreshVM reloads a Virtual MCP definition into governance after a write.
func (h *SBSMCPHandler) refreshVM(ctx context.Context, id uint) {
	if h.governance == nil {
		return
	}
	if _, err := h.governance.ReloadVirtualMCP(ctx, id); err != nil {
		logger.Warn("sbs: reload virtual mcp %d in governance: %v", id, err)
	}
}

// RegisterRoutes registers the admin API routes.
func (h *SBSMCPHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/sbs/mcp/endpoints", lib.ChainMiddlewares(h.listEndpoints, middlewares...))
	r.POST("/api/sbs/mcp/endpoints", lib.ChainMiddlewares(h.createEndpoint, middlewares...))
	r.GET("/api/sbs/mcp/endpoints/{id}", lib.ChainMiddlewares(h.getEndpoint, middlewares...))
	r.PUT("/api/sbs/mcp/endpoints/{id}", lib.ChainMiddlewares(h.updateEndpoint, middlewares...))
	r.DELETE("/api/sbs/mcp/endpoints/{id}", lib.ChainMiddlewares(h.deleteEndpoint, middlewares...))
	r.PUT("/api/sbs/mcp/virtual-mcps/{id}/overrides", lib.ChainMiddlewares(h.putOverrides, middlewares...))
}

// SBSEndpointRequest is the create/update body.
type SBSEndpointRequest struct {
	Name               string  `json:"name"`
	Description        *string `json:"description,omitempty"`
	VirtualMCPID       uint    `json:"virtual_mcp_id"`
	Enabled            *bool   `json:"enabled,omitempty"`
	AuthMode           string  `json:"auth_mode,omitempty"`
	PublicVirtualKeyID *string `json:"public_virtual_key_id,omitempty"`
	AllowQueryParamKey bool    `json:"allow_query_param_key"`
	OpenAPIEnabled     *bool   `json:"openapi_enabled,omitempty"`
}

// SBSEndpointResponse is an endpoint with the Virtual MCP it points at.
type SBSEndpointResponse struct {
	TableSBSMCPEndpoint
	VirtualMCPName string `json:"virtual_mcp_name,omitempty"`
	VirtualMCPSlug string `json:"virtual_mcp_slug,omitempty"`
	Available      bool   `json:"available"` // enabled and its Virtual MCP exists and is enabled
}

func (h *SBSMCPHandler) ready(ctx *fasthttp.RequestCtx) bool {
	if sbsMCP == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP endpoints need a config store")
		return false
	}
	return true
}

func (h *SBSMCPHandler) toResponse(snap *sbsSnapshot, ep TableSBSMCPEndpoint) SBSEndpointResponse {
	resp := SBSEndpointResponse{TableSBSMCPEndpoint: ep}
	if view := snap.vmcpsByID[ep.VirtualMCPID]; view != nil {
		resp.VirtualMCPName = view.def.Name
		resp.VirtualMCPSlug = view.def.EndpointSlug
		resp.Available = ep.Enabled && view.def.Enabled
	}
	return resp
}

func (h *SBSMCPHandler) listEndpoints(ctx *fasthttp.RequestCtx) {
	if !h.ready(ctx) {
		return
	}
	sbsMCP.invalidate()
	snap := sbsMCP.snapshot(ctx)
	out := make([]SBSEndpointResponse, 0, len(snap.endpoints))
	for _, ep := range snap.endpoints {
		out = append(out, h.toResponse(snap, ep))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	SendJSON(ctx, map[string]any{"endpoints": out, "count": len(out)})
}

func (h *SBSMCPHandler) loadEndpoint(ctx *fasthttp.RequestCtx) (*TableSBSMCPEndpoint, bool) {
	id, err := strconv.ParseUint(fmt.Sprint(ctx.UserValue("id")), 10, 64)
	if err != nil || id == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid endpoint id")
		return nil, false
	}
	var ep TableSBSMCPEndpoint
	if err := sbsMCP.db.WithContext(ctx).First(&ep, uint(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "endpoint not found")
		} else {
			SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		}
		return nil, false
	}
	return &ep, true
}

func (h *SBSMCPHandler) getEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.ready(ctx) {
		return
	}
	ep, ok := h.loadEndpoint(ctx)
	if !ok {
		return
	}
	SendJSON(ctx, h.toResponse(sbsMCP.snapshot(ctx), *ep))
}

// validate checks a request against the current state. selfID is the endpoint being updated (0 on create).
func (h *SBSMCPHandler) validate(ctx *fasthttp.RequestCtx, req *SBSEndpointRequest, selfID uint) string {
	req.Name = strings.TrimSpace(req.Name)
	if !sbsEndpointNamePattern.MatchString(req.Name) {
		return "name must be 1-64 characters of letters, digits, '_' or '-'"
	}
	snap := sbsMCP.snapshot(ctx)
	if other, ok := snap.endpoints[req.Name]; ok && other.ID != selfID {
		return fmt.Sprintf("an endpoint named %q already exists", req.Name)
	}
	if view, ok := snap.vmcpsBySlug[req.Name]; ok && view.def.ID != req.VirtualMCPID {
		return fmt.Sprintf("%q is already the slug of the Virtual MCP %q", req.Name, view.def.Name)
	}
	if _, _, ok := h.config.GetMCPClientBySlug(req.Name); ok {
		return fmt.Sprintf("%q is already the slug of an MCP server", req.Name)
	}
	if _, ok := snap.vmcpsByID[req.VirtualMCPID]; !ok {
		return "virtual_mcp_id does not name an existing Virtual MCP"
	}
	switch req.AuthMode {
	case "":
		req.AuthMode = SBSEndpointAuthVirtualKey
	case SBSEndpointAuthVirtualKey:
	case SBSEndpointAuthPublic:
		if req.PublicVirtualKeyID == nil || *req.PublicVirtualKeyID == "" {
			return "a public endpoint needs public_virtual_key_id: the virtual key its anonymous requests act as"
		}
		vk, err := h.config.ConfigStore.GetVirtualKey(ctx, *req.PublicVirtualKeyID)
		if err != nil || vk == nil {
			return "public_virtual_key_id does not name an existing virtual key"
		}
	default:
		return "auth_mode must be virtual_key or public"
	}
	if req.AuthMode != SBSEndpointAuthPublic {
		req.PublicVirtualKeyID = nil
	}
	return ""
}

// sbsPublicGrant is the (virtual key, Virtual MCP) assignment a public endpoint depends on.
type sbsPublicGrant struct {
	vkID   string
	vmcpID uint
}

// publicGrant returns the assignment ep needs, or ok=false when it is not a public endpoint.
func publicGrant(ep *TableSBSMCPEndpoint) (sbsPublicGrant, bool) {
	if ep.AuthMode != SBSEndpointAuthPublic || ep.PublicVirtualKeyID == nil || *ep.PublicVirtualKeyID == "" {
		return sbsPublicGrant{}, false
	}
	return sbsPublicGrant{vkID: *ep.PublicVirtualKeyID, vmcpID: ep.VirtualMCPID}, true
}

// grantPublicKey assigns the endpoint's Virtual MCP to its public virtual key, which governance
// requires before that key may reach it. It records on ep whether this endpoint made the
// assignment, so releasePublicGrant removes only what an endpoint added, never a manual one.
func (h *SBSMCPHandler) grantPublicKey(ctx context.Context, ep *TableSBSMCPEndpoint) error {
	grant, ok := publicGrant(ep)
	if !ok {
		return nil
	}
	ids, err := h.config.ConfigStore.GetVirtualKeyIDsForVirtualMCP(ctx, grant.vmcpID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == grant.vkID {
			return nil // already assigned (manually, or by this or another endpoint): keep the flag as it is
		}
	}
	if err := h.config.ConfigStore.AttachVirtualMCPToVirtualKey(ctx, grant.vmcpID, grant.vkID); err != nil {
		return err
	}
	ep.PublicGrantAdded = true
	if h.governance != nil {
		return h.governance.AttachVirtualMCPToVirtualKeyInMemory(ctx, grant.vkID, grant.vmcpID)
	}
	return nil
}

// releasePublicGrant removes the assignment old added once no endpoint needs it any more: after
// a re-point, a switch away from public, a key change or a delete. A grant another endpoint added
// for the same pair stays; it is that endpoint's to release.
func (h *SBSMCPHandler) releasePublicGrant(ctx context.Context, old TableSBSMCPEndpoint, now *TableSBSMCPEndpoint) {
	grant, ok := publicGrant(&old)
	if !ok || !old.PublicGrantAdded {
		return
	}
	if now != nil {
		if still, ok := publicGrant(now); ok && still == grant {
			now.PublicGrantAdded = true // same pair, still needed: the endpoint keeps owning it
			return
		}
	}
	var others []TableSBSMCPEndpoint
	if err := sbsMCP.db.WithContext(ctx).
		Where("id <> ? AND auth_mode = ? AND public_virtual_key_id = ? AND virtual_mcp_id = ?", old.ID, SBSEndpointAuthPublic, grant.vkID, grant.vmcpID).
		Find(&others).Error; err != nil {
		logger.Warn("sbs: endpoint %q: check other users of its public grant: %v", old.Name, err)
		return
	}
	if len(others) > 0 {
		// Hand the grant to another endpoint that needs the same pair, so it is released with that one.
		if err := sbsMCP.db.WithContext(ctx).Model(&TableSBSMCPEndpoint{}).Where("id = ?", others[0].ID).Update("public_grant_added", true).Error; err != nil {
			logger.Warn("sbs: endpoint %q: hand over public grant: %v", old.Name, err)
		}
		return
	}
	if err := h.config.ConfigStore.DetachVirtualMCPFromVirtualKey(ctx, grant.vmcpID, grant.vkID); err != nil {
		logger.Warn("sbs: endpoint %q: remove public grant: %v", old.Name, err)
		return
	}
	if h.governance != nil {
		if err := h.governance.DetachVirtualMCPFromVirtualKeyInMemory(ctx, grant.vkID, grant.vmcpID); err != nil {
			logger.Warn("sbs: endpoint %q: remove public grant from governance: %v", old.Name, err)
		}
	}
}

func (h *SBSMCPHandler) createEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.ready(ctx) {
		return
	}
	var req SBSEndpointRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if msg := h.validate(ctx, &req, 0); msg != "" {
		SendError(ctx, fasthttp.StatusBadRequest, msg)
		return
	}
	ep := TableSBSMCPEndpoint{
		Name:               req.Name,
		Description:        req.Description,
		VirtualMCPID:       req.VirtualMCPID,
		Enabled:            req.Enabled == nil || *req.Enabled,
		AuthMode:           req.AuthMode,
		PublicVirtualKeyID: req.PublicVirtualKeyID,
		AllowQueryParamKey: req.AllowQueryParamKey,
		OpenAPIEnabled:     req.OpenAPIEnabled == nil || *req.OpenAPIEnabled,
	}
	if err := sbsMCP.db.WithContext(ctx).Create(&ep).Error; err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	if err := h.grantPublicKey(ctx, &ep); err != nil {
		logger.Warn("sbs: endpoint %q: attach public key: %v", ep.Name, err)
	}
	if ep.PublicGrantAdded {
		if err := sbsMCP.db.WithContext(ctx).Model(&ep).Update("public_grant_added", true).Error; err != nil {
			logger.Warn("sbs: endpoint %q: record public grant: %v", ep.Name, err)
		}
	}
	sbsMCP.invalidate()
	SendJSONWithStatus(ctx, h.toResponse(sbsMCP.snapshot(ctx), ep), fasthttp.StatusCreated)
}

func (h *SBSMCPHandler) updateEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.ready(ctx) {
		return
	}
	ep, ok := h.loadEndpoint(ctx)
	if !ok {
		return
	}
	var req SBSEndpointRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if msg := h.validate(ctx, &req, ep.ID); msg != "" {
		SendError(ctx, fasthttp.StatusBadRequest, msg)
		return
	}
	old := *ep
	ep.PublicGrantAdded = false // re-decided below: kept if the same grant is still needed, set if one is added
	ep.Name = req.Name
	ep.Description = req.Description
	ep.VirtualMCPID = req.VirtualMCPID
	if req.Enabled != nil {
		ep.Enabled = *req.Enabled
	}
	ep.AuthMode = req.AuthMode
	ep.PublicVirtualKeyID = req.PublicVirtualKeyID
	ep.AllowQueryParamKey = req.AllowQueryParamKey
	if req.OpenAPIEnabled != nil {
		ep.OpenAPIEnabled = *req.OpenAPIEnabled
	}
	h.releasePublicGrant(ctx, old, ep)
	if err := h.grantPublicKey(ctx, ep); err != nil {
		logger.Warn("sbs: endpoint %q: attach public key: %v", ep.Name, err)
	}
	if err := sbsMCP.db.WithContext(ctx).Save(ep).Error; err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	sbsMCP.invalidate()
	SendJSON(ctx, h.toResponse(sbsMCP.snapshot(ctx), *ep))
}

func (h *SBSMCPHandler) deleteEndpoint(ctx *fasthttp.RequestCtx) {
	if !h.ready(ctx) {
		return
	}
	ep, ok := h.loadEndpoint(ctx)
	if !ok {
		return
	}
	if err := sbsMCP.db.WithContext(ctx).Delete(ep).Error; err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	h.releasePublicGrant(ctx, *ep, nil)
	sbsMCP.invalidate()
	SendJSON(ctx, map[string]any{"deleted": ep.ID})
}

// SBSOverridesRequest replaces a Virtual MCP's overrides: client ID -> tool name -> override.
type SBSOverridesRequest struct {
	Overrides map[string]map[string]configstoreTables.MCPToolOverride `json:"overrides"`
}

func (h *SBSMCPHandler) putOverrides(ctx *fasthttp.RequestCtx) {
	if !h.ready(ctx) {
		return
	}
	id, err := strconv.ParseUint(fmt.Sprint(ctx.UserValue("id")), 10, 64)
	if err != nil || id == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid virtual mcp id")
		return
	}
	var req SBSOverridesRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	def, err := h.config.ConfigStore.GetVirtualMCPByID(ctx, uint(id))
	if err != nil || def == nil {
		SendError(ctx, fasthttp.StatusNotFound, "virtual mcp not found")
		return
	}

	// Names must stay unique within what this Virtual MCP serves.
	clientNames := h.config.GetMCPClientNames()
	taken := map[string]string{} // presented name -> gateway name
	for _, spec := range def.ParsedTools {
		clientName := clientNames[spec.MCPClientID]
		for tool, ov := range req.Overrides[spec.MCPClientID] {
			gatewayName := clientName + "-" + tool
			name := gatewayName
			if ov.Name != nil && strings.TrimSpace(*ov.Name) != "" {
				trimmed := strings.TrimSpace(*ov.Name)
				if !sbsToolNamePattern.MatchString(trimmed) {
					SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("override name %q: use 1-64 of letters, digits, '_', '.', '-'", trimmed))
					return
				}
				ov.Name = &trimmed
				req.Overrides[spec.MCPClientID][tool] = ov
				name = trimmed
			}
			if other, dup := taken[name]; dup && other != gatewayName {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("two tools would both be named %q", name))
				return
			}
			taken[name] = gatewayName
		}
	}

	for i := range def.ParsedTools {
		ovs := req.Overrides[def.ParsedTools[i].MCPClientID]
		cleaned := map[string]configstoreTables.MCPToolOverride{}
		for tool, ov := range ovs {
			if sbsOverrideEmpty(ov) {
				continue
			}
			cleaned[tool] = ov
		}
		if len(cleaned) == 0 {
			cleaned = nil
		}
		def.ParsedTools[i].Overrides = cleaned
	}
	if err := h.config.ConfigStore.UpdateVirtualMCP(ctx, def); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	h.refreshVM(ctx, def.ID)
	sbsMCP.invalidate()
	SendJSON(ctx, map[string]any{"id": def.ID, "tools": def.ParsedTools})
}

func sbsOverrideEmpty(ov configstoreTables.MCPToolOverride) bool {
	blank := func(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }
	return blank(ov.Name) && blank(ov.Title) && blank(ov.Description) && len(ov.Annotations) == 0
}

// ---------------------------------------------------------------------------------------------
// OpenAPI export: /mcp/<slug>/openapi.json and POST /mcp/<slug>/tools/<tool>

// RegisterSBSMCPOpenAPIRoutes registers the REST face of every endpoint. No auth middleware here:
// each call loops back through /mcp/<slug>, which authenticates exactly as an MCP client would be.
func RegisterSBSMCPOpenAPIRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/mcp/{slug}/openapi.json", lib.ChainMiddlewares(sbsServeOpenAPISpec, middlewares...))
	r.POST("/mcp/{slug}/tools/{tool}", lib.ChainMiddlewares(sbsServeToolCall, middlewares...))
}

// sbsOpenAPIAllowed reports whether slug may be served as REST: an SBS endpoint with OpenAPI on,
// or any Virtual MCP / client slug (those have no switch of their own).
func sbsOpenAPIAllowed(ctx *fasthttp.RequestCtx, slug string) bool {
	if sbsMCP == nil {
		return false
	}
	if ep, ok := sbsMCP.snapshot(ctx).endpoints[slug]; ok {
		return ep.OpenAPIEnabled
	}
	return true
}

// sbsLoopbackBase is where this process serves HTTP.
func sbsLoopbackBase() string {
	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}
	return "http://127.0.0.1:" + port
}

// sbsMCPSession is a minimal Streamable HTTP MCP client over loopback, carrying the caller's credentials.
type sbsMCPSession struct {
	url       string
	headers   http.Header
	sessionID string
	nextID    int
	client    *http.Client
}

func newSBSMCPSession(ctx *fasthttp.RequestCtx, slug string) *sbsMCPSession {
	u := sbsLoopbackBase() + "/mcp/" + slug
	if q := ctx.QueryArgs().QueryString(); len(q) > 0 {
		u += "?" + string(q)
	}
	h := http.Header{}
	for _, name := range []string{"Authorization", "x-bf-vk", "x-api-key", "x-goog-api-key"} {
		if v := ctx.Request.Header.Peek(name); len(v) > 0 {
			h.Set(name, string(v))
		}
	}
	return &sbsMCPSession{url: u, headers: h, client: sbsMCP.http}
}

// sbsRPCError is a JSON-RPC or HTTP failure on the loopback call.
type sbsRPCError struct {
	status  int
	message string
}

func (e *sbsRPCError) Error() string { return e.message }

func (c *sbsMCPSession) send(ctx context.Context, method string, params any, notify bool) (json.RawMessage, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	id := 0
	if !notify {
		c.nextID++
		id = c.nextID
		msg["id"] = id
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, &sbsRPCError{status: fasthttp.StatusBadGateway, message: err.Error()}
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, &sbsRPCError{status: resp.StatusCode, message: strings.TrimSpace(string(raw))}
	}
	if notify {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	var frame []byte
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		frame, err = sbsReadSSEResponse(resp.Body, id)
		if err != nil {
			return nil, &sbsRPCError{status: fasthttp.StatusBadGateway, message: err.Error()}
		}
	} else {
		frame, _ = io.ReadAll(resp.Body)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil {
		return nil, &sbsRPCError{status: fasthttp.StatusBadGateway, message: "invalid MCP response: " + err.Error()}
	}
	if envelope.Error != nil {
		status := fasthttp.StatusBadRequest
		if envelope.Error.Code == -32601 {
			status = fasthttp.StatusNotFound
		}
		return nil, &sbsRPCError{status: status, message: envelope.Error.Message}
	}
	return envelope.Result, nil
}

// sbsReadSSEResponse returns the data of the event answering id.
func sbsReadSSEResponse(r io.Reader, id int) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 32<<20)
	var data bytes.Buffer
	flush := func() []byte {
		defer data.Reset()
		if data.Len() == 0 {
			return nil
		}
		var probe struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(data.Bytes(), &probe) == nil && strings.TrimSpace(string(probe.ID)) == strconv.Itoa(id) {
			return append([]byte(nil), data.Bytes()...)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if frame := flush(); frame != nil {
				return frame, nil
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(v, " "))
		}
	}
	if frame := flush(); frame != nil {
		return frame, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("MCP stream ended without a response")
}

func (c *sbsMCPSession) open(ctx context.Context) error {
	_, err := c.send(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "bifrost-sbs-openapi", "version": "1.0.0"},
	}, false)
	if err != nil {
		return err
	}
	_, err = c.send(ctx, "notifications/initialized", nil, true)
	return err
}

func (c *sbsMCPSession) listTools(ctx context.Context) ([]mcp.Tool, error) {
	var all []mcp.Tool
	cursor := ""
	for page := 0; page < 50; page++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		raw, err := c.send(ctx, "tools/list", params, false)
		if err != nil {
			return nil, err
		}
		var result struct {
			Tools      []mcp.Tool `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		all = append(all, result.Tools...)
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return all, nil
}

func sbsSendRPCError(ctx *fasthttp.RequestCtx, err error) {
	var rpcErr *sbsRPCError
	if errors.As(err, &rpcErr) {
		SendError(ctx, rpcErr.status, rpcErr.message)
		return
	}
	SendError(ctx, fasthttp.StatusBadGateway, err.Error())
}

// sbsPublicBaseURL is the URL the caller reached us on, for the spec's servers entry.
func sbsPublicBaseURL(ctx *fasthttp.RequestCtx) string {
	scheme := "http"
	if ctx.IsTLS() || strings.EqualFold(string(ctx.Request.Header.Peek("X-Forwarded-Proto")), "https") {
		scheme = "https"
	}
	host := string(ctx.Request.Header.Peek("X-Forwarded-Host"))
	if host == "" {
		host = string(ctx.Host())
	}
	return scheme + "://" + host
}

func sbsServeOpenAPISpec(ctx *fasthttp.RequestCtx) {
	slug, _ := ctx.UserValue("slug").(string)
	if !sbsOpenAPIAllowed(ctx, slug) {
		SendError(ctx, fasthttp.StatusNotFound, "OpenAPI is not enabled for this endpoint")
		return
	}
	session := newSBSMCPSession(ctx, slug)
	if err := session.open(ctx); err != nil {
		sbsSendRPCError(ctx, err)
		return
	}
	tools, err := session.listTools(ctx)
	if err != nil {
		sbsSendRPCError(ctx, err)
		return
	}

	paths := map[string]any{}
	for _, tool := range tools {
		// Read the schema from the tool's own JSON: it covers both InputSchema and RawInputSchema.
		var schema any = map[string]any{"type": "object", "properties": map[string]any{}}
		if raw, err := json.Marshal(tool); err == nil {
			var parsed struct {
				InputSchema map[string]any `json:"inputSchema"`
			}
			if json.Unmarshal(raw, &parsed) == nil && len(parsed.InputSchema) > 0 {
				if parsed.InputSchema["type"] == nil {
					parsed.InputSchema["type"] = "object"
				}
				schema = parsed.InputSchema
			}
		}
		summary := tool.Title
		if summary == "" {
			summary = tool.Annotations.Title
		}
		if summary == "" {
			summary = tool.Name
		}
		paths["/mcp/"+slug+"/tools/"+tool.Name] = map[string]any{
			"post": map[string]any{
				"operationId": tool.Name,
				"summary":     summary,
				"description": tool.Description,
				"requestBody": map[string]any{
					"required": true,
					"content":  map[string]any{"application/json": map[string]any{"schema": schema}},
				},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Tool result",
						"content":     map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}},
					},
					"422": map[string]any{"description": "The tool reported an error"},
				},
			},
		}
	}
	spec := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "MCP endpoint " + slug,
			"version":     "1.0.0",
			"description": "Tools of the Bifrost MCP endpoint /mcp/" + slug + ", exposed as REST operations.",
		},
		"servers": []any{map[string]any{"url": sbsPublicBaseURL(ctx)}},
		"paths":   paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearer":     map[string]any{"type": "http", "scheme": "bearer"},
				"virtualKey": map[string]any{"type": "apiKey", "in": "header", "name": "x-bf-vk"},
			},
		},
		"security": []any{map[string]any{"bearer": []any{}}, map[string]any{"virtualKey": []any{}}},
	}
	SendJSON(ctx, spec)
}

func sbsServeToolCall(ctx *fasthttp.RequestCtx) {
	slug, _ := ctx.UserValue("slug").(string)
	tool, _ := ctx.UserValue("tool").(string)
	if !sbsOpenAPIAllowed(ctx, slug) {
		SendError(ctx, fasthttp.StatusNotFound, "OpenAPI is not enabled for this endpoint")
		return
	}
	args := map[string]any{}
	if body := bytes.TrimSpace(ctx.PostBody()); len(body) > 0 {
		if err := json.Unmarshal(body, &args); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "body must be a JSON object of tool arguments: "+err.Error())
			return
		}
	}
	session := newSBSMCPSession(ctx, slug)
	if err := session.open(ctx); err != nil {
		sbsSendRPCError(ctx, err)
		return
	}
	raw, err := session.send(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, false)
	if err != nil {
		sbsSendRPCError(ctx, err)
		return
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, "invalid tool result")
		return
	}
	status := fasthttp.StatusOK
	if isErr, _ := result["isError"].(bool); isErr {
		status = fasthttp.StatusUnprocessableEntity
	}
	SendJSONWithStatus(ctx, result, status)
}
