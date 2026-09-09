import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { VirtualKeyListItem } from "@/components/virtualKeyListItem";
import { useGetVirtualKeysQuery } from "@/lib/store";
import VirtualMcpAccessProfiles from "@enterprise/components/virtual-mcps/virtualMcpAccessProfiles";
import { Plus } from "lucide-react";

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
	// Access-profile-managed keys derive their MCP access from their profile, so they cannot be assigned
	// directly here (the API rejects them too).
	const { data, isLoading, isError, refetch } = useGetVirtualKeysQuery(
		{ limit: 1000, exclude_access_profile_managed_virtual: true },
		{ skip: !active || (isCreate && !stagedCreate) },
	);

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
					onSelect={(opt) => addVk(opt.value)}
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
		</div>
	);
}