import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
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
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { VirtualKeyListItem } from "@/components/virtualKeyListItem";
import { useGetVirtualKeysQuery, useLazyGetVirtualKeyQuery } from "@/lib/store";
import VirtualMcpAccessProfiles from "@enterprise/components/virtual-mcps/virtualMcpAccessProfiles";
import { Plus } from "lucide-react";
import { useState } from "react";
import { type VirtualKeyOwner, virtualKeyOwner } from "./virtualMcpAccessTab.utils";

interface VirtualMCPAccessTabProps {
	vmcpId: number;
	// Staged assigned VK ids; committed on Save (mirrors the MCP sheet's vk_configs staging).
	value: string[];
	onChange: (ids: string[]) => void;
	isCreate: boolean;
	active: boolean;
	// Wizard create flow: stage assignments before the vMCP exists (attached after create).
	// Hides the access-profile reverse lookup, which has no id to resolve yet.
	stagedCreate?: boolean;
}

export default function VirtualMCPAccessTab({ vmcpId, value, onChange, isCreate, active, stagedCreate = false }: VirtualMCPAccessTabProps) {
	// A user's access-profile managed key derives its MCP access from that profile, so it cannot be
	// assigned directly here (the API rejects it too). A key a team, business unit or customer owns can
	// be, after a confirmation that names its owner.
	const { data, isLoading, isError, refetch } = useGetVirtualKeysQuery(
		{ limit: 1000, exclude_access_profile_managed_virtual: true },
		{ skip: !active || (isCreate && !stagedCreate) },
	);
	const [fetchVirtualKey] = useLazyGetVirtualKeyQuery();
	// A picked key that belongs to another team, business unit or customer, waiting on the confirmation.
	const [pendingOwnedVk, setPendingOwnedVk] = useState<{ id: string; name: string; owner: VirtualKeyOwner } | null>(null);

	if (isCreate && !stagedCreate) {
		return (
			<div className="text-muted-foreground rounded-md border border-dashed p-6 text-center text-sm">
				Create this Virtual MCP first, then assign it to virtual keys here.
			</div>
		);
	}

	if (isError) {
		return (
			<div className="text-destructive flex flex-col items-center gap-3 rounded-md border border-dashed p-6 text-center text-sm">
				Could not load virtual keys.
				<Button variant="outline" size="sm" onClick={() => refetch()}>
					Retry
				</Button>
			</div>
		);
	}

	// Names/secrets for the assigned rows below; the searchable picker fetches its own results.
	const vkById = new Map((data?.virtual_keys ?? []).map((v) => [v.id, v]));

	const addVk = (vkId: string) => onChange([...value, vkId]);
	const removeVk = (vkId: string) => onChange(value.filter((id) => id !== vkId));

	// The list above holds the first 1000 keys; a key past it is read on its own to find its owner. A
	// failed read skips the confirmation rather than blocking the assignment, since it only informs.
	const selectVk = async (vkId: string, label: string) => {
		let vk = vkById.get(vkId);
		if (!vk) {
			try {
				vk = (await fetchVirtualKey(vkId, true).unwrap()).virtual_key;
			} catch {
				vk = undefined;
			}
		}
		const owner = virtualKeyOwner(vk);
		if (owner) {
			setPendingOwnedVk({ id: vkId, name: vk?.name || label, owner });
			return;
		}
		addVk(vkId);
	};

	return (
		<div className="flex flex-col gap-4">
			<div className="flex items-center justify-between">
				<div className="flex flex-col gap-0.5">
					<Label>Virtual keys</Label>
					<p className="text-muted-foreground text-xs">Keys assigned here can reach this Virtual MCP at its endpoint.</p>
				</div>
				<VirtualKeySelector
					mode="add"
					filters={{ exclude_access_profile_managed_virtual: true }}
					excludeIds={value}
					onSelect={(opt) => void selectVk(opt.value, opt.label)}
					contentClassName="w-72"
					trigger={
						<Button variant="outline" size="sm" data-testid="virtual-mcp-assign-vk-btn">
							<Plus className="h-4 w-4" />
							Assign virtual key
						</Button>
					}
				/>
			</div>

			{isLoading ? (
				<div className="text-muted-foreground rounded-md border border-dashed p-6 text-center text-sm">Loading virtual keys…</div>
			) : value.length === 0 ? (
				<div className="text-muted-foreground rounded-md border border-dashed p-6 text-center text-sm">
					No virtual keys assigned yet. Assign one so callers can reach this Virtual MCP.
				</div>
			) : (
				<div className="divide-y overflow-hidden rounded-md border">
					{value.map((vkId) => {
						const vk = vkById.get(vkId);
						return (
							<VirtualKeyListItem
								key={vkId}
								name={vk?.name}
								secret={vk?.value}
								fallbackLabel={vkId}
								onRemove={() => removeVk(vkId)}
								removeAriaLabel="Unassign virtual key"
								data-testid={`virtual-mcp-detach-vk-${vkId}`}
							/>
						);
					})}
				</div>
			)}

			{!stagedCreate && <VirtualMcpAccessProfiles vmcpId={vmcpId} active={active} />}

			{/* Granting MCP access to a key another team, business unit or customer owns is confirmed. */}
			<AlertDialog
				open={!!pendingOwnedVk}
				onOpenChange={(next) => {
					if (!next) setPendingOwnedVk(null);
				}}
			>
				<AlertDialogContent data-testid="virtual-mcp-assign-owned-vk-dialog">
					<AlertDialogHeader>
						<AlertDialogTitle>Assign virtual key?</AlertDialogTitle>
						<AlertDialogDescription>
							<strong>{pendingOwnedVk?.name}</strong> belongs to the {pendingOwnedVk?.owner.kind}{" "}
							<strong>{pendingOwnedVk?.owner.name}</strong>. Assigning it lets that {pendingOwnedVk?.owner.kind}&apos;s key reach this
							Virtual MCP.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="virtual-mcp-assign-owned-vk-cancel">Cancel</AlertDialogCancel>
						<AlertDialogAction
							data-testid="virtual-mcp-assign-owned-vk-confirm"
							onClick={() => {
								if (pendingOwnedVk) addVk(pendingOwnedVk.id);
								setPendingOwnedVk(null);
							}}
						>
							Assign
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}