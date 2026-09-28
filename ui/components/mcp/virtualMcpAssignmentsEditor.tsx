import type { EntitySelectorOption } from "@/components/entitySelectors/entitySelector";
import { VirtualMCPSelector } from "@/components/entitySelectors/virtualMcpSelector";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useGetVirtualMCPQuery } from "@/lib/store";
import { Info, Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";

// One assigned Virtual MCP: resolves its name/slug by id, then renders a removable row.
function AssignedVirtualMcpRow({ id, onRemove }: { id: number; onRemove: () => void }) {
	const { t } = useTranslation("governance");
	const { data } = useGetVirtualMCPQuery(id);
	const name = data?.name || t("virtualMcp.fallbackName", { id });
	return (
		<div className="flex items-center justify-between gap-2 px-3 py-2">
			<div className="flex min-w-0 flex-col">
				<span className="truncate text-sm font-medium">{name}</span>
				{data?.endpoint_slug && <span className="text-muted-foreground truncate text-xs">/mcp/{data.endpoint_slug}</span>}
			</div>
			<Button type="button" variant="ghost" size="sm" aria-label={t("virtualMcp.removeAria", { name })} onClick={onRemove}>
				<Trash2 className="h-4 w-4" />
			</Button>
		</div>
	);
}

interface VirtualMcpAssignmentsEditorProps {
	value: number[];
	onChange: (ids: number[]) => void;
	label?: string;
	tooltip?: ReactNode;
}

// Assign a virtual key to Virtual MCPs. The picker is server-backed; assignments commit as
// attach/detach on Save (the parent owns that), mirroring the MCP-server config editor above it.
export function VirtualMcpAssignmentsEditor({ value, onChange, label, tooltip }: VirtualMcpAssignmentsEditorProps) {
	const { t } = useTranslation("governance");
	const resolvedLabel = label ?? t("virtualMcp.title");
	const resolvedTooltip = tooltip ?? (
		<p>
			<Trans
				t={t}
				i18nKey="virtualMcp.tooltip"
				values={{ slug: "<slug>" }}
				components={{ medium0: <span className="font-medium" /> }}
			/>
		</p>
	);
	const add = (option: EntitySelectorOption) => {
		const id = Number(option.value);
		if (value.includes(id)) return;
		onChange([...value, id]);
	};
	const remove = (id: number) => onChange(value.filter((v) => v !== id));

	return (
		<div className="mt-6 space-y-2">
			<div className="flex items-center gap-2">
				<Label className="text-sm font-medium">{resolvedLabel}</Label>
				<TooltipProvider>
					<Tooltip>
						<TooltipTrigger asChild>
							<span>
								<Info className="text-muted-foreground h-3 w-3" />
							</span>
						</TooltipTrigger>
						<TooltipContent>{resolvedTooltip}</TooltipContent>
					</Tooltip>
				</TooltipProvider>
			</div>

			<VirtualMCPSelector
				mode="add"
				fullWidth
				placeholder={t("virtualMcp.selectToAdd")}
				onSelect={add}
				excludeIds={value.map(String)}
			/>

			{value.length > 0 && (
				<div className="divide-y overflow-hidden rounded-md border">
					{value.map((id) => (
						<AssignedVirtualMcpRow key={id} id={id} onRemove={() => remove(id)} />
					))}
				</div>
			)}
		</div>
	);
}
