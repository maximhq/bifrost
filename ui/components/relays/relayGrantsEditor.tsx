import { RelaySelector } from "@/components/entitySelectors/relaySelector";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Info, Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";

const emphasis = <span className="font-medium" />;

interface RelayGrantsEditorProps {
	/** Granted endpoint names; endpoints are keyed by name, so the name is the grant. */
	value: string[];
	onChange: (endpointNames: string[]) => void;
	label?: string;
	/** Overridable so a holder other than a virtual key (an access profile, a project) can say what the grant means. */
	tooltip?: ReactNode;
	testIdPrefix?: string;
}

/**
 * Grants access to relays one by one. Mirrors the agent and MCP editors beside it: a picker on
 * top, removable rows beneath. There is no allow-all row, as there is none for agents.
 */
export function RelayGrantsEditor({
	value,
	onChange,
	label,
	tooltip,
	testIdPrefix = "vk-relays",
}: RelayGrantsEditorProps) {
	const { t } = useTranslation("observability");
	const { t: tCommon } = useTranslation("common");
	const resolvedLabel = label ?? t("relayLogs.grants.label");
	const resolvedTooltip = tooltip ?? (
		<p>
			<Trans t={t} i18nKey="relayLogs.grants.tooltip" components={{ strong: emphasis }} />
		</p>
	);
	const add = (option: { value: string }) => {
		if (value.includes(option.value)) return;
		onChange([...value, option.value]);
	};
	const remove = (name: string) => onChange(value.filter((v) => v !== name));

	return (
		<div className="mt-6 space-y-2" data-testid={`${testIdPrefix}-editor`}>
			<div className="flex items-center gap-2">
				<Label className="text-sm font-medium">{resolvedLabel}</Label>
				<TooltipProvider>
					<Tooltip>
						<TooltipTrigger asChild>
							<button type="button" aria-label={tCommon("aboutField", { field: resolvedLabel.toLowerCase() })} className="inline-flex">
								<Info className="text-muted-foreground h-3 w-3" />
							</button>
						</TooltipTrigger>
						<TooltipContent className="max-w-xs">{resolvedTooltip}</TooltipContent>
					</Tooltip>
				</TooltipProvider>
			</div>

			<RelaySelector mode="add" fullWidth placeholder={t("relayLogs.grants.selectRelay")} onSelect={add} excludeIds={value} />
			{value.length > 0 && (
				<div className="divide-y overflow-hidden rounded-md border">
					{value.map((name) => (
						<div key={name} className="flex items-center justify-between gap-2 px-3 py-2" data-testid={`${testIdPrefix}-grant-${name}`}>
							<div className="flex min-w-0 flex-col">
								<span className="truncate text-sm font-medium">{name}</span>
								<span className="text-muted-foreground truncate text-xs">/relay/{name}</span>
							</div>
							<Button type="button" variant="ghost" size="sm" aria-label={t("relayLogs.grants.remove", { name })} onClick={() => remove(name)}>
								<Trash2 className="h-4 w-4" />
							</Button>
						</div>
					))}
				</div>
			)}
		</div>
	);
}