package lib

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// reconcileVirtualMCPsConfig performs hash-based reconciliation of Virtual MCPs (mcp.virtual_mcps)
// between config.json and the config store. Tool specs resolve their source MCP client by id or by
// name. A tool spec naming an MCP client that is not registered is left out, and the rest of the
// Virtual MCP is still applied, with the omission recorded on the row (config_issues) so the
// dashboard can show it. Leaving a spec out only removes access, and registering the client later
// re-links it (ReconcileFileVirtualMCPsForClient). A malformed entry (no name, a tool spec naming
// no client) or a failed client lookup still skips the whole entry, so the stored row keeps
// working. When forceFileSync is true, config.json is authoritative and every file entry is
// written even if its hash matches the stored row.
func reconcileVirtualMCPsConfig(ctx context.Context, store configstore.ConfigStore, fileVMCPs []schemas.VirtualMCPConfig, forceFileSync bool) error {
	_, err := reconcileVirtualMCPDeclarations(ctx, store, fileVMCPs, forceFileSync)
	return err
}

// reconcileVirtualMCPDeclarations is the body of reconcileVirtualMCPsConfig. It returns the IDs of
// the Virtual MCPs it created or rewrote, so a caller running it outside startup can refresh their
// cache entries.
func reconcileVirtualMCPDeclarations(ctx context.Context, store configstore.ConfigStore, fileVMCPs []schemas.VirtualMCPConfig, forceFileSync bool) ([]uint, error) {
	if store == nil || len(fileVMCPs) == 0 {
		return nil, nil
	}

	dbByName := make(map[string]configstoreTables.TableVirtualMCP)
	dbByID := make(map[uint]configstoreTables.TableVirtualMCP)
	offset := 0
	const pageSize = 100
	for {
		batch, totalCount, err := store.GetVirtualMCPsPaginated(ctx, configstore.VirtualMCPsQueryParams{
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get virtual MCPs from store: %w", err)
		}
		for _, vmcp := range batch {
			dbByName[vmcp.Name] = vmcp
			dbByID[vmcp.ID] = vmcp
		}
		offset += len(batch)
		if int64(offset) >= totalCount || len(batch) == 0 {
			break
		}
	}

	var written []uint
	// Names are unique in the store, and IDs select the row to reconcile. Track them separately so a
	// duplicate name with a different ID is caught too, not just a repeated ID.
	seenNames := map[string]struct{}{}
	seenIDs := map[uint]struct{}{}
	for _, fileVMCP := range fileVMCPs {
		if strings.TrimSpace(fileVMCP.Name) == "" {
			logger.Warn("skipping virtual MCP with missing name in config.json")
			continue
		}
		if _, dup := seenNames[fileVMCP.Name]; dup {
			logger.Warn("duplicate virtual MCP %q in config.json; skipping duplicate entry", fileVMCP.Name)
			continue
		}
		if fileVMCP.ID != 0 {
			if _, dup := seenIDs[fileVMCP.ID]; dup {
				logger.Warn("duplicate virtual MCP ID %d in config.json; skipping duplicate entry", fileVMCP.ID)
				continue
			}
			seenIDs[fileVMCP.ID] = struct{}{}
		}
		seenNames[fileVMCP.Name] = struct{}{}

		parsedTools, issues, ok := resolveVirtualMCPToolSpecs(ctx, store, fileVMCP)
		if !ok {
			continue
		}

		// Enabled defaults to true when omitted.
		enabled := true
		if fileVMCP.Enabled != nil {
			enabled = *fileVMCP.Enabled
		}

		target := configstoreTables.TableVirtualMCP{
			Name:         fileVMCP.Name,
			EndpointSlug: strings.TrimSpace(fileVMCP.EndpointSlug),
			Description:  fileVMCP.Description,
			Enabled:      enabled,
			ParsedTools:  parsedTools,
		}
		// The hash covers only the specs that resolved, so registering a missing client later changes
		// it and the next reconcile writes the spec in.
		fileHash := GenerateVirtualMCPHash(fileVMCP.Name, fileVMCP.Description, enabled, parsedTools, fileVMCP.VirtualKeyIDs)
		target.ConfigHash = fileHash

		// When id is provided, match by DB id first; fall back to name.
		var dbVMCP configstoreTables.TableVirtualMCP
		var exists bool
		if fileVMCP.ID != 0 {
			dbVMCP, exists = dbByID[fileVMCP.ID]
		}
		if !exists {
			dbVMCP, exists = dbByName[fileVMCP.Name]
		}

		if !exists {
			logger.Info("new virtual MCP from config.json: %s", fileVMCP.Name)
			if fileVMCP.ID != 0 {
				target.ID = fileVMCP.ID
			}
			if err := store.CreateVirtualMCP(ctx, &target); err != nil {
				logger.Warn("skipping virtual MCP %q: failed to create: %v", fileVMCP.Name, err)
				continue
			}
			reconcileVirtualMCPVirtualKeys(ctx, store, target.ID, fileVMCP.Name, fileVMCP.VirtualKeyIDs)
			recordVirtualMCPConfigIssues(ctx, store, target.ID, fileVMCP.Name, nil, issues)
			written = append(written, target.ID)
			continue
		}

		if !forceFileSync && dbVMCP.ConfigHash == fileHash {
			logger.Debug("virtual MCP hash matches for %s, keeping stored config", fileVMCP.Name)
			// The hash covers virtual_key_ids, and it is persisted before the VK diff below runs, so a
			// prior attach/detach that failed (logged, not returned) leaves a matching hash. Re-run the
			// idempotent diff here so it retries on a later load instead of being skipped forever.
			reconcileVirtualMCPVirtualKeys(ctx, store, dbVMCP.ID, fileVMCP.Name, fileVMCP.VirtualKeyIDs)
			recordVirtualMCPConfigIssues(ctx, store, dbVMCP.ID, fileVMCP.Name, dbVMCP.ConfigIssues, issues)
			continue
		}

		logger.Info("virtual MCP hash mismatch for %s, syncing from config.json", fileVMCP.Name)
		target.ID = dbVMCP.ID
		// EndpointSlug is immutable; UpdateVirtualMCP omits it, so the stored slug is preserved.
		if err := store.UpdateVirtualMCP(ctx, &target); err != nil {
			logger.Warn("skipping virtual MCP %q: failed to update: %v", fileVMCP.Name, err)
			continue
		}
		reconcileVirtualMCPVirtualKeys(ctx, store, target.ID, fileVMCP.Name, fileVMCP.VirtualKeyIDs)
		recordVirtualMCPConfigIssues(ctx, store, target.ID, fileVMCP.Name, dbVMCP.ConfigIssues, issues)
		written = append(written, target.ID)
	}

	return written, nil
}

// resolveVirtualMCPToolSpecs turns a declaration's tool specs into stored specs, resolving each
// client named by mcp_client_name to its client_id. A client that is not registered drops only its
// spec and adds an issue. ok is false when the whole entry has to be skipped: a spec naming no
// client is a malformed declaration, and a lookup that failed for any other reason must not strip
// a spec the stored row may still grant.
func resolveVirtualMCPToolSpecs(ctx context.Context, store configstore.ConfigStore, fileVMCP schemas.VirtualMCPConfig) (parsedTools []configstoreTables.MCPToolSpec, issues []string, ok bool) {
	parsedTools = make([]configstoreTables.MCPToolSpec, 0, len(fileVMCP.Tools))
	for _, tool := range fileVMCP.Tools {
		clientID := strings.TrimSpace(tool.MCPClientID)
		clientName := strings.TrimSpace(tool.MCPClientName)
		if clientID == "" && clientName != "" {
			client, err := store.GetMCPClientByName(ctx, clientName)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					logger.Warn("virtual MCP %q: MCP client %q is not registered; applying the virtual MCP without its tools", fileVMCP.Name, clientName)
					issues = append(issues, fmt.Sprintf("MCP client %q is not registered; its tools are not applied", clientName))
					continue
				}
				logger.Warn("skipping virtual MCP %q: failed to resolve MCP client %q: %v", fileVMCP.Name, clientName, err)
				return nil, nil, false
			}
			clientID = client.ClientID
		}
		if clientID == "" {
			logger.Warn("skipping virtual MCP %q: tool entry missing mcp_client_id and mcp_client_name", fileVMCP.Name)
			return nil, nil, false
		}
		parsedTools = append(parsedTools, configstoreTables.MCPToolSpec{
			MCPClientID: clientID,
			ToolNames:   tool.ToolNames,
		})
	}
	return parsedTools, issues, true
}

// recordVirtualMCPConfigIssues stores the reconcile issues on the Virtual MCP row, or clears them
// once the declaration applies in full. It skips the write when the stored value already matches.
func recordVirtualMCPConfigIssues(ctx context.Context, store configstore.ConfigStore, id uint, name string, stored *string, issues []string) {
	var next *string
	if len(issues) > 0 {
		joined := strings.Join(issues, "\n")
		next = &joined
	}
	if (stored == nil && next == nil) || (stored != nil && next != nil && *stored == *next) {
		return
	}
	if err := store.SetVirtualMCPConfigIssues(ctx, id, next); err != nil {
		logger.Warn("virtual MCP %q: failed to record config.json issues: %v", name, err)
	}
}

// virtualMCPDeclarationNamesClient reports whether a declaration refers to the named MCP client by
// name in any of its tool specs.
func virtualMCPDeclarationNamesClient(decl schemas.VirtualMCPConfig, clientName string) bool {
	for _, tool := range decl.Tools {
		if strings.TrimSpace(tool.MCPClientID) == "" && strings.TrimSpace(tool.MCPClientName) == clientName {
			return true
		}
	}
	return false
}

// firstVirtualMCPDeclarations returns the declarations reconciliation applies, in order: entries
// with a name, keeping the first of any repeated name or repeated id, as reconcileVirtualMCPDeclarations
// does when it walks the whole list.
func firstVirtualMCPDeclarations(decls []schemas.VirtualMCPConfig) []schemas.VirtualMCPConfig {
	seenNames := map[string]struct{}{}
	seenIDs := map[uint]struct{}{}
	kept := make([]schemas.VirtualMCPConfig, 0, len(decls))
	for _, decl := range decls {
		if strings.TrimSpace(decl.Name) == "" {
			continue
		}
		if _, dup := seenNames[decl.Name]; dup {
			continue
		}
		if decl.ID != 0 {
			if _, dup := seenIDs[decl.ID]; dup {
				continue
			}
			seenIDs[decl.ID] = struct{}{}
		}
		seenNames[decl.Name] = struct{}{}
		kept = append(kept, decl)
	}
	return kept
}

// rememberFileVirtualMCPs keeps the config.json Virtual MCP declarations from the last load, so a
// client registered afterwards can be linked into the declarations that name it.
func (c *Config) rememberFileVirtualMCPs(decls []schemas.VirtualMCPConfig) {
	c.muFileVirtualMCPs.Lock()
	defer c.muFileVirtualMCPs.Unlock()
	c.fileVirtualMCPs = append([]schemas.VirtualMCPConfig(nil), decls...)
}

// ReconcileFileVirtualMCPsForClient re-runs config.json reconciliation for the Virtual MCP
// declarations that name clientName, after that client is registered. A declaration loaded while
// the client was missing (or registered under an id since deleted) then links the client in
// without a restart: the resolved client_id changes the declaration's hash, so the row is
// rewritten and its config issues cleared. Returns the IDs of the Virtual MCPs it rewrote, whose
// cache entries the caller must refresh.
func (c *Config) ReconcileFileVirtualMCPsForClient(ctx context.Context, clientName string) ([]uint, error) {
	clientName = strings.TrimSpace(clientName)
	if c == nil || c.ConfigStore == nil || clientName == "" {
		return nil, nil
	}
	c.muFileVirtualMCPs.Lock()
	defer c.muFileVirtualMCPs.Unlock()
	// Settle duplicates across the whole snapshot first, the way the load did, so filtering by client
	// can never pick a later duplicate the load skipped and let it overwrite the first declaration.
	var matching []schemas.VirtualMCPConfig
	for _, decl := range firstVirtualMCPDeclarations(c.fileVirtualMCPs) {
		if virtualMCPDeclarationNamesClient(decl, clientName) {
			matching = append(matching, decl)
		}
	}
	if len(matching) == 0 {
		return nil, nil
	}
	return reconcileVirtualMCPDeclarations(ctx, c.ConfigStore, matching, false)
}

// reconcileVirtualMCPVirtualKeys diffs a Virtual MCP's desired VK attachments against the stored
// set, attaching the missing and detaching the extra.
func reconcileVirtualMCPVirtualKeys(ctx context.Context, store configstore.ConfigStore, vmcpID uint, vmcpName string, desiredVKs []string) {
	current, err := store.GetVirtualKeyIDsForVirtualMCP(ctx, vmcpID)
	if err != nil {
		logger.Warn("failed to load VK attachments for virtual MCP %q: %v", vmcpName, err)
		return
	}
	currentSet := make(map[string]bool, len(current))
	for _, id := range current {
		currentSet[id] = true
	}
	desiredSet := make(map[string]bool, len(desiredVKs))
	for _, id := range desiredVKs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		desiredSet[id] = true
		if !currentSet[id] {
			if err := store.AttachVirtualMCPToVirtualKey(ctx, vmcpID, id); err != nil {
				logger.Warn("failed to attach virtual MCP %q to VK %q: %v", vmcpName, id, err)
			}
		}
	}
	for id := range currentSet {
		if !desiredSet[id] {
			if err := store.DetachVirtualMCPFromVirtualKey(ctx, vmcpID, id); err != nil {
				logger.Warn("failed to detach virtual MCP %q from VK %q: %v", vmcpName, id, err)
			}
		}
	}
}

// pruneVirtualMCPsConfigToFile removes Virtual MCPs absent from config.json when config.json is the
// source of truth for mcp.virtual_mcps.
func pruneVirtualMCPsConfigToFile(ctx context.Context, store configstore.ConfigStore, fileVMCPs []schemas.VirtualMCPConfig) error {
	if store == nil {
		return nil
	}
	keepByName := make(map[string]bool, len(fileVMCPs))
	keepByID := make(map[uint]bool, len(fileVMCPs))
	for _, vmcp := range fileVMCPs {
		if vmcp.Name != "" {
			keepByName[vmcp.Name] = true
		}
		if vmcp.ID != 0 {
			keepByID[vmcp.ID] = true
		}
	}

	offset := 0
	const pageSize = 100
	toDelete := make([]configstoreTables.TableVirtualMCP, 0)
	for {
		batch, totalCount, err := store.GetVirtualMCPsPaginated(ctx, configstore.VirtualMCPsQueryParams{
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return fmt.Errorf("failed to get virtual MCPs from store: %w", err)
		}
		for _, dbVMCP := range batch {
			if keepByID[dbVMCP.ID] || (dbVMCP.Name != "" && keepByName[dbVMCP.Name]) {
				continue
			}
			toDelete = append(toDelete, dbVMCP)
		}
		offset += len(batch)
		if int64(offset) >= totalCount || len(batch) == 0 {
			break
		}
	}
	for _, dbVMCP := range toDelete {
		logger.Info("removing virtual MCP %q absent from config.json source of truth", dbVMCP.Name)
		if err := store.DeleteVirtualMCP(ctx, dbVMCP.ID); err != nil {
			return fmt.Errorf("failed to delete virtual MCP %q: %w", dbVMCP.Name, err)
		}
	}
	return nil
}

// GenerateVirtualMCPHash generates a SHA256 hash from a Virtual MCP config payload. Tools and
// virtual key IDs are sorted so the hash is stable regardless of declaration order.
func GenerateVirtualMCPHash(name string, description *string, enabled bool, tools []configstoreTables.MCPToolSpec, virtualKeyIDs []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "name:%s\n", name)
	if description != nil {
		fmt.Fprintf(h, "description:%s\n", *description)
	}
	fmt.Fprintf(h, "enabled:%t\n", enabled)

	sortedTools := append([]configstoreTables.MCPToolSpec(nil), tools...)
	sort.Slice(sortedTools, func(i, j int) bool {
		if sortedTools[i].MCPClientID != sortedTools[j].MCPClientID {
			return sortedTools[i].MCPClientID < sortedTools[j].MCPClientID
		}
		left := append([]string(nil), sortedTools[i].ToolNames...)
		right := append([]string(nil), sortedTools[j].ToolNames...)
		sort.Strings(left)
		sort.Strings(right)
		for idx := 0; idx < len(left) && idx < len(right); idx++ {
			if left[idx] != right[idx] {
				return left[idx] < right[idx]
			}
		}
		return len(left) < len(right)
	})
	for _, tool := range sortedTools {
		fmt.Fprintf(h, "tool.mcp_client_id:%s\n", tool.MCPClientID)
		toolNames := append([]string(nil), tool.ToolNames...)
		sort.Strings(toolNames)
		for _, toolName := range toolNames {
			fmt.Fprintf(h, "tool.name:%s\n", toolName)
		}
	}

	vkIDs := append([]string(nil), virtualKeyIDs...)
	sort.Strings(vkIDs)
	for _, id := range vkIDs {
		fmt.Fprintf(h, "vk:%s\n", id)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}
