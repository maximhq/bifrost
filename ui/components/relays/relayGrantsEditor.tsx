import { RelaySelector } from "@/components/entitySelectors/relaySelector";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { MultiSelect } from "@/components/ui/multiSelect";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useGetRelayQuery } from "@/lib/store/apis/relaysApi";
import type { RelayGrant } from "@/lib/types/relays";
import { describeRelayRoute, isAllRoutes, nextRouteSelection } from "@/lib/utils/relayGrants";
import { Info, Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";

const emphasis = <span className="font-medium" />;

interface RelayGrantsEditorProps {
	/** Granted relays, each with the routes it allows: ["*"] (or unset) for every route, else route IDs. */
	value: RelayGrant[];
	onChange: (grants: RelayGrant[]) => void;
	label?: string;
	/** Overridable so a holder other than a virtual key (an access profile, a project) can say what the grant means. */
	tooltip?: ReactNode;
	testIdPrefix?: string;
}

/**
 * Grants access to relays one by one, and per relay to every route or chosen routes. Mirrors the MCP
 * editor beside it: a picker on top, a row per relay with a route multi-select. There is no
 * allow-all across relays, as there is none for agents.
 */
export function RelayGrantsEditor({ value, onChange, label, tooltip, testIdPrefix = "vk-relays" }: RelayGrantsEditorProps) {
	const { t } = useTranslation("observability");
	const { t: tCommon } = useTranslation("common");
	const resolvedLabel = label ?? t("relayLogs.grants.label");
	const resolvedTooltip = tooltip ?? (
		<p>
			<Trans t={t} i18nKey="relayLogs.grants.tooltip" components={{ strong: emphasis }} />
		</p>
	);
	const names = value.map((g) => g.relay_name);
	const add = (option: { value: string }) => {
		if (names.includes(option.value)) return;
		onChange([...value, { relay_name: option.value, routes: ["*"] }]);
	};
	const remove = (name: string) => onChange(value.filter((g) => g.relay_name !== name));
	const setRoutes = (name: string, routes: string[]) => onChange(value.map((g) => (g.relay_name === name ? { ...g, routes } : g)));

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

			<RelaySelector mode="add" fullWidth placeholder={t("relayLogs.grants.selectRelay")} onSelect={add} excludeIds={names} />
			{value.length > 0 && (
				<div className="divide-y overflow-hidden rounded-md border">
					{value.map((grant) => (
						<RelayGrantRow
							key={grant.relay_name}
							grant={grant}
							testIdPrefix={testIdPrefix}
							onRoutesChange={(routes) => setRoutes(grant.relay_name, routes)}
							onRemove={() => remove(grant.relay_name)}
						/>
					))}
				</div>
			)}
		</div>
	);
}

/** One granted relay: its name, and a route picker fed by that relay's own routes. */
function RelayGrantRow({
	grant,
	testIdPrefix,
	onRoutesChange,
	onRemove,
}: {
	grant: RelayGrant;
	testIdPrefix: string;
	onRoutesChange: (routes: string[]) => void;
	onRemove: () => void;
}) {
	const { t } = useTranslation("observability");
	const name = grant.relay_name;
	const { data: relay } = useGetRelayQuery(name);
	const selected = grant.routes ?? ["*"];
	const known = new Set((relay?.routes ?? []).map((r) => r.id));
	const options = [
		{ label: t("relayLogs.grants.allRoutes"), value: "*", description: t("relayLogs.grants.allRoutesDescription") },
		...(relay?.routes ?? []).filter((r) => r.id).map((r) => ({ label: describeRelayRoute(r), value: r.id as string })),
		// A granted route the relay no longer has stays visible so it can be removed.
		...selected
			.filter((id) => id !== "*" && relay && !known.has(id))
			.map((id) => ({ label: t("relayLogs.grants.deletedRoute"), value: id, description: id })),
	];

	return (
		<div className="flex items-center justify-between gap-3 px-3 py-2" data-testid={`${testIdPrefix}-grant-${name}`}>
			<div className="flex w-40 min-w-0 shrink-0 flex-col">
				<span className="truncate text-sm font-medium">{name}</span>
				<span className="text-muted-foreground truncate text-xs">/relay/{name}</span>
			</div>
			<div className="min-w-0 flex-1" data-testid={`${testIdPrefix}-grant-${name}-routes`}>
				<MultiSelect
					hideSelectAll
					options={options}
					defaultValue={selected}
					onValueChange={(next: string[]) => onRoutesChange(nextRouteSelection(selected, next))}
					placeholder={
						selected.length === 0
							? t("relayLogs.grants.noRoutesSelected")
							: isAllRoutes(selected)
								? t("relayLogs.grants.allRoutesAllowed")
								: t("relayLogs.grants.selectRoutes")
					}
					variant="inverted"
					className="hover:bg-accent w-full bg-white dark:bg-zinc-800"
					commandClassName="w-full max-w-96"
					modalPopover={true}
					animation={0}
				/>
			</div>
			<Button type="button" variant="ghost" size="sm" aria-label={t("relayLogs.grants.remove", { name })} onClick={onRemove}>
				<Trash2 className="h-4 w-4" />
			</Button>
		</div>
	);
}