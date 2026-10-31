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
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { type PickedVirtualKey, type VirtualKeyOwner, resolvePickedVirtualKey } from "./virtualMcpAccessTab.utils";

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
	// be, and so can one assigned to a user, after a confirmation that names who it belongs to.
	const { data, isLoading, isError, refetch } = useGetVirtualKeysQuery(
		{ limit: 1000, exclude_access_profile_managed_virtual: true },
		{ skip: !active || (isCreate && !stagedCreate) },
	);
	const [fetchVirtualKey] = useLazyGetVirtualKeyQuery();
	// A picked key that belongs to a team, business unit, customer or user, waiting on the confirmation.
	const [pendingOwnedVk, setPendingOwnedVk] = useState<{ id: string; name: string; owner: VirtualKeyOwner } | null>(null);
	// One ownership check at a time, so a late result cannot replace the key an open confirmation names.
	const [checkingOwner, setCheckingOwner] = useState(false);
	const checkingOwnerRef = useRef(false);
	// The assignments as of the latest render. A pick that waits on a lookup adds to these, not to the
	// list it saw when it started, so a key picked meanwhile is not dropped.
	const latestValue = useRef(value);
	useEffect(() => {
		latestValue.current = value;
	}, [value]);

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

	const addVk = (vkId: string) => {
		if (latestValue.current.includes(vkId)) return;
		latestValue.current = [...latestValue.current, vkId];
		onChange(latestValue.current);
	};
	const removeVk = (vkId: string) => {
		latestValue.current = latestValue.current.filter((id) => id !== vkId);
		onChange(latestValue.current);
	};

	// The list above holds the first 1000 keys; a key past it is read on its own to find its owner. A
	// failed read assigns nothing: whose key it is is unknown, and the confirmation exists for exactly
	// a key someone else owns.
	const selectVk = async (vkId: string, label: string) => {
		if (checkingOwnerRef.current) return;
		checkingOwnerRef.current = true;
		setCheckingOwner(true);
		const listed = vkById.get(vkId);
		let picked: PickedVirtualKey;
		try {
			// A fresh read: a cached key can name an owner it no longer has.
			picked = await resolvePickedVirtualKey(listed, async () => (await fetchVirtualKey(vkId).unwrap()).virtual_key);
		} finally {
			checkingOwnerRef.current = false;
			setCheckingOwner(false);
		}
		if (picked.outcome === "lookupFailed") {
			toast.error(`Could not check who owns "${label}", so it was not assigned. Try again.`);
			return;
		}
		if (picked.outcome === "owned") {
			setPendingOwnedVk({ id: vkId, name: listed?.name || label, owner: picked.owner });
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
						<Button variant="outline" size="sm" disabled={checkingOwner} data-testid="virtual-mcp-assign-vk-btn">
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

			{/* Granting MCP access to a key that belongs to a team, business unit, customer or user is confirmed. */}
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