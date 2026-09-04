import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/hooks/use-toast";
import {
	getErrorMessage,
	useAttachVirtualMCPVirtualKeyMutation,
	useCreateVirtualMCPMutation,
	useDetachVirtualMCPVirtualKeyMutation,
	useGetVirtualMCPQuery,
	useUpdateVirtualMCPMutation,
} from "@/lib/store";
import { VirtualMCPRequest, VirtualMCPToolSpec } from "@/lib/types/virtualMcps";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import VirtualMCPAccessTab from "./virtualMcpAccessTab";
import VirtualMCPConnectTab from "./virtualMcpConnectTab";
import VirtualMCPGeneralTab from "./virtualMcpGeneralTab";
import VirtualMcpToolsEditor from "./virtualMcpToolsEditor";

export type VirtualMCPSheetTarget = { mode: "create" } | { mode: "edit"; id: number };

interface VirtualMCPSheetProps {
	target: VirtualMCPSheetTarget;
	onClose: () => void;
	hasPrev?: boolean;
	hasNext?: boolean;
	onNavigate?: (direction: "prev" | "next") => void;
}

function normalizeTools(tools: VirtualMCPToolSpec[]) {
	return tools
		.map((t) => ({ mcp_client_id: t.mcp_client_id, tool_names: [...t.tool_names].sort() }))
		.sort((a, b) => a.mcp_client_id.localeCompare(b.mcp_client_id));
}

export default function VirtualMCPSheet({ target, onClose, hasPrev = false, hasNext = false, onNavigate }: VirtualMCPSheetProps) {
	const { toast } = useToast();
	const editId = target.mode === "edit" ? target.id : null;
	const isCreate = target.mode === "create";

	// currentData (not data) so a nav to another vMCP clears it until the new record loads, instead of
	// hydrating the form from the previous one.
	const { currentData: existing, isFetching: loadingExisting } = useGetVirtualMCPQuery(editId ?? 0, { skip: editId == null });
	const [createVirtualMCP, { isLoading: creating }] = useCreateVirtualMCPMutation();
	const [updateVirtualMCP, { isLoading: updating }] = useUpdateVirtualMCPMutation();
	const [attachVk] = useAttachVirtualMCPVirtualKeyMutation();
	const [detachVk] = useDetachVirtualMCPVirtualKeyMutation();

	// RBAC: no-ops to true in OSS (no access profiles / RBAC); enforced in enterprise.
	const canCreate = useRbac(RbacResource.VirtualMCPs, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.VirtualMCPs, RbacOperation.Update);
	const hasSavePermission = isCreate ? canCreate : canUpdate;

	const [tab, setTab] = useState("general");
	const [name, setName] = useState("");
	const [endpointSlug, setEndpointSlug] = useState("");
	const [description, setDescription] = useState("");
	const [enabled, setEnabled] = useState(true);
	const [tools, setTools] = useState<VirtualMCPToolSpec[]>([]);
	const [assignedVkIds, setAssignedVkIds] = useState<string[]>([]);
	const [pendingNav, setPendingNav] = useState<"prev" | "next" | null>(null);

	// Reset to the first tab when the sheet target changes (open / switch record), and arm re-hydration.
	const hydrated = useRef(false);
	useEffect(() => {
		setTab("general");
		hydrated.current = false;
	}, [target]);

	// Hydrate the form once per target: defaults on create, or the record when it loads on edit. Guarded
	// so a background refetch (e.g. after assigning a virtual key) does not stomp the form or active tab.
	useEffect(() => {
		if (hydrated.current) return;
		if (isCreate) {
			setName("");
			setEndpointSlug("");
			setDescription("");
			setEnabled(true);
			setTools([]);
			setAssignedVkIds([]);
			hydrated.current = true;
			return;
		}
		if (existing) {
			setName(existing.name);
			setEndpointSlug(existing.endpoint_slug);
			setDescription(existing.description ?? "");
			setEnabled(existing.enabled);
			setTools(existing.tools ?? []);
			setAssignedVkIds(existing.virtual_key_ids ?? []);
			hydrated.current = true;
		}
	}, [target, existing, isCreate]);

	const saving = creating || updating;
	const showLoader = !isCreate && loadingExisting && !existing;

	// Dirty tracks every staged field (General, Tools, and virtual-key assignments), all committed
	// together on Save. On create, "dirty" just means there is a name to submit.
	const isDirty = useMemo(() => {
		if (isCreate) return name.trim().length > 0;
		if (!existing) return false;
		return (
			name.trim() !== existing.name ||
			(description.trim() || "") !== (existing.description ?? "") ||
			enabled !== existing.enabled ||
			JSON.stringify(normalizeTools(tools)) !== JSON.stringify(normalizeTools(existing.tools ?? [])) ||
			JSON.stringify([...assignedVkIds].sort()) !== JSON.stringify([...(existing.virtual_key_ids ?? [])].sort())
		);
	}, [isCreate, existing, name, description, enabled, tools, assignedVkIds]);

	const canSave = name.trim().length > 0 && !saving && isDirty && hasSavePermission;

	const handleSave = async () => {
		if (!canSave) return;
		const body: VirtualMCPRequest = {
			name: name.trim(),
			description: description.trim() || undefined,
			enabled,
			tools,
		};
		try {
			if (isCreate) {
				body.endpoint_slug = endpointSlug.trim() || undefined;
				await createVirtualMCP(body).unwrap();
				toast({ title: "Virtual MCP created" });
			} else {
				await updateVirtualMCP({ id: target.id, data: body }).unwrap();
				// Commit staged VK assignment changes (attach/detach are separate endpoints).
				const original = existing?.virtual_key_ids ?? [];
				const originalSet = new Set(original);
				const stagedSet = new Set(assignedVkIds);
				for (const vkId of assignedVkIds.filter((id) => !originalSet.has(id))) {
					await attachVk({ id: target.id, vkId }).unwrap();
				}
				for (const vkId of original.filter((id) => !stagedSet.has(id))) {
					await detachVk({ id: target.id, vkId }).unwrap();
				}
				toast({ title: "Virtual MCP updated" });
			}
			onClose();
		} catch (err) {
			toast({
				title: isCreate ? "Failed to create Virtual MCP" : "Failed to update Virtual MCP",
				description: getErrorMessage(err),
				variant: "destructive",
			});
		}
	};

	// Guard row navigation the same way the MCP sheet does: confirm before discarding staged edits.
	const handleNavigate = (direction: "prev" | "next") => {
		if (!onNavigate) return;
		if (isDirty) {
			setPendingNav(direction);
			return;
		}
		onNavigate(direction);
	};

	return (
		<Sheet open onOpenChange={(open) => !open && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-hidden! pt-4 sm:max-w-[60%]">
				<SheetHeader headerClassName="mb-0 px-4 py-4 md:px-8" showCloseButton={false}>
					<div className="flex w-full items-center justify-between gap-2">
						<div className="space-y-2">
							<SheetTitle className="flex w-fit items-center gap-2 font-medium">
								{isCreate ? "New Virtual MCP" : "Edit Virtual MCP"}
								{enabled ? <Badge>Enabled</Badge> : <Badge variant="secondary">Disabled</Badge>}
							</SheetTitle>
							<SheetDescription>Bundle tools from your MCP servers into a single endpoint served at /mcp/&lt;slug&gt;.</SheetDescription>
						</div>
						{!isCreate && onNavigate && (
							<SheetNavigationButtons hasPrev={hasPrev} hasNext={hasNext} onNavigate={handleNavigate} entityLabel="Virtual MCP" />
						)}
					</div>
				</SheetHeader>

				{showLoader ? (
					<div className="flex grow items-center justify-center">
						<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
					</div>
				) : (
					<Tabs value={tab} onValueChange={setTab} className="flex grow flex-col overflow-hidden">
						<TabsList className="mx-4 mt-4 flex justify-start md:mx-8">
							<TabsTrigger value="general" data-testid="virtual-mcp-tab-general">
								General
							</TabsTrigger>
							<TabsTrigger value="tools" data-testid="virtual-mcp-tab-tools">
								Tools
							</TabsTrigger>
							<TabsTrigger value="access" data-testid="virtual-mcp-tab-access">
								Access
							</TabsTrigger>
							<TabsTrigger value="connect" data-testid="virtual-mcp-tab-connect">
								Connect
							</TabsTrigger>
						</TabsList>

						<div className="grow overflow-y-auto px-4 py-5 md:px-8">
							<TabsContent value="general" className="mt-0">
								<VirtualMCPGeneralTab
									name={name}
									setName={setName}
									endpointSlug={endpointSlug}
									setEndpointSlug={setEndpointSlug}
									description={description}
									setDescription={setDescription}
									enabled={enabled}
									setEnabled={setEnabled}
									isCreate={isCreate}
								/>
							</TabsContent>
							<TabsContent value="tools" className="mt-0">
								<VirtualMcpToolsEditor value={tools} onChange={setTools} />
							</TabsContent>
							<TabsContent value="access" className="mt-0">
								<VirtualMCPAccessTab
									vmcpId={editId ?? 0}
									value={assignedVkIds}
									onChange={setAssignedVkIds}
									isCreate={isCreate}
									active={tab === "access"}
								/>
							</TabsContent>
							<TabsContent value="connect" className="mt-0">
								<VirtualMCPConnectTab
									slug={existing?.endpoint_slug ?? ""}
									enabled={enabled}
									isCreate={isCreate}
									active={tab === "connect"}
								/>
							</TabsContent>
						</div>
					</Tabs>
				)}

				<SheetFooter className="flex-row items-center justify-between gap-2 border-t md:px-8">
					<span className="text-muted-foreground text-xs">
						{!hasSavePermission ? `You do not have permission to ${isCreate ? "create" : "edit"} Virtual MCPs.` : ""}
					</span>
					<div className="flex items-center gap-2">
						<Button variant="outline" onClick={onClose} disabled={saving}>
							Cancel
						</Button>
						<Button onClick={handleSave} disabled={!canSave} data-testid="virtual-mcp-save-btn">
							{saving && <Loader2 className="h-4 w-4 animate-spin" />}
							{isCreate ? "Create" : "Save changes"}
						</Button>
					</div>
				</SheetFooter>
			</SheetContent>

			<AlertDialog open={pendingNav !== null} onOpenChange={(open) => !open && setPendingNav(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Discard unsaved changes?</AlertDialogTitle>
						<AlertDialogDescription>You have unsaved changes to this Virtual MCP. Leaving now will discard them.</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel onClick={() => setPendingNav(null)}>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								const dir = pendingNav;
								setPendingNav(null);
								if (dir) onNavigate?.(dir);
							}}
						>
							Discard changes
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</Sheet>
	);
}