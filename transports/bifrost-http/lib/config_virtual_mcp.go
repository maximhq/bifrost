package lib

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// reconcileVirtualMCPsConfig performs hash-based reconciliation of Virtual MCPs (mcp.virtual_mcps)
// between config.json and the config store. Tool specs resolve their source MCP client by id or by
// name; a Virtual MCP whose client cannot be resolved is skipped so a bad entry doesn't block the
// rest. When forceFileSync is true, config.json is authoritative and every file entry is written
// even if its hash matches the stored row.
func reconcileVirtualMCPsConfig(ctx context.Context, store configstore.ConfigStore, fileVMCPs []schemas.VirtualMCPConfig, forceFileSync bool) error {
	if store == nil || len(fileVMCPs) == 0 {
		return nil
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
			return fmt.Errorf("failed to get virtual MCPs from store: %w", err)
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

	// Names are unique in the store, and IDs select the row to reconcile. Track them separately so a
	// duplicate name with a different ID is caught too, not just a repeated ID.
	seenNames := map[string]struct{}{}
	seenIDs := map[uint]struct{}{}
vmcpLoop:
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

		parsedTools := make([]configstoreTables.MCPToolSpec, 0, len(fileVMCP.Tools))
		for _, tool := range fileVMCP.Tools {
			clientID := strings.TrimSpace(tool.MCPClientID)
			if clientID == "" && strings.TrimSpace(tool.MCPClientName) != "" {
				client, err := store.GetMCPClientByName(ctx, strings.TrimSpace(tool.MCPClientName))
				if err != nil {
					logger.Warn("skipping virtual MCP %q: failed to resolve MCP client %q: %v", fileVMCP.Name, tool.MCPClientName, err)
					continue vmcpLoop
				}
				clientID = client.ClientID
			}
			if clientID == "" {
				logger.Warn("skipping virtual MCP %q: tool entry missing mcp_client_id and mcp_client_name", fileVMCP.Name)
				continue vmcpLoop
			}
			parsedTools = append(parsedTools, configstoreTables.MCPToolSpec{
				MCPClientID: clientID,
				ToolNames:   tool.ToolNames,
			})
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
			continue
		}

		if !forceFileSync && dbVMCP.ConfigHash == fileHash {
			logger.Debug("virtual MCP hash matches for %s, keeping stored config", fileVMCP.Name)
			// The hash covers virtual_key_ids, and it is persisted before the VK diff below runs, so a
			// prior attach/detach that failed (logged, not returned) leaves a matching hash. Re-run the
			// idempotent diff here so it retries on a later load instead of being skipped forever.
			reconcileVirtualMCPVirtualKeys(ctx, store, dbVMCP.ID, fileVMCP.Name, fileVMCP.VirtualKeyIDs)
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
	}

	return nil
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
