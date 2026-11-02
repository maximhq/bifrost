import { Badge } from "@/components/ui/badge";
import type { ReactNode } from "react";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { TriStateCheckbox } from "@/components/ui/tristateCheckbox";
import { useTranslation } from "react-i18next";
import { exposedByRule, exposeRowLabel, exposeRowState, isExposed, nextExposeList, type ExposeRowState } from "./mcpExposeList.utils";
import { SectionHeader } from "./sectionHeader";

export interface MCPExposeItem {
	key: string; // What the allow-list holds: a prompt's own name, a resource URI or a URI template
	label: string;
	detail?: string;
	description?: string;
	arguments?: { name: string; required: boolean }[]; // Prompts only
	mimeType?: string; // Resources only
}

// Read-only status badges, in the palette the server state badges use: served
// reads as healthy, not served as neutral, and a pattern rule as the primary tint.
const SERVED_BADGE: Partial<Record<ExposeRowState, string>> = {
	served: "border-chart-success/30 bg-chart-success/10 text-chart-success-ink",
	notServed: "bg-gray-100 text-gray-800 border-gray-200 dark:border-border dark:bg-muted dark:text-muted-foreground",
};

/**
 * One allow-list (prompts or resources) of an MCP server: what the gateway serves from it.
 * readOnly drops the switches and the serve-all checkbox and states each item's status instead;
 * extraColumn adds one column between the name and its status.
 */
export function MCPExposeList({
	kind,
	title,
	description,
	items,
	value,
	onChange,
	readOnly = false,
	extraColumn,
}: {
	kind: "prompts" | "resources";
	title: string;
	description: string;
	items: MCPExposeItem[];
	value: string[] | null | undefined;
	onChange?: (next: string[]) => void;
	readOnly?: boolean;
	extraColumn?: { header: string; render: (item: MCPExposeItem) => ReactNode };
}) {
	const { t } = useTranslation("mcp");
	const allKeys = items.map((item) => item.key);
	const selected = allKeys.filter((key) => isExposed(value, key));
	const status =
		selected.length === allKeys.length
			? t("registry.expose.allServed")
			: selected.length === 0
				? t("registry.expose.noneServed")
				: t("registry.expose.someServed", { count: selected.length });

	return (
		<div className="space-y-4" data-testid={`mcp-expose-${kind}`}>
			<div className="flex items-start justify-between gap-4">
				<SectionHeader title={`${title} (${items.length})`} description={description} />
				{items.length > 0 && (
					<div className="flex items-center gap-2">
						<span className="text-muted-foreground text-sm" data-testid={`mcp-expose-${kind}-status`}>
							{status}
						</span>
						{!readOnly && (
							<TriStateCheckbox
								allIds={allKeys}
								selectedIds={selected}
								ariaLabel={t("registry.expose.serveAllAria", { kind, status })}
								data-testid={`mcp-expose-${kind}-all`}
								onChange={(next) => onChange?.(next.length === allKeys.length ? ["*"] : next)}
							/>
						)}
					</div>
				)}
			</div>
			{items.length > 0 ? (
				<div className="rounded-sm border">
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead>{t("registry.expose.name")}</TableHead>
								{extraColumn && <TableHead>{extraColumn.header}</TableHead>}
								<TableHead className={`${readOnly ? "w-48" : "w-24"} text-center`}>{t("registry.expose.served")}</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{items.map((item) => {
								const exposed = isExposed(value, item.key);
								const byRule = exposedByRule(value, item.key);
								return (
									<TableRow key={item.key} data-testid={`mcp-expose-${kind}-row-${item.key}`}>
										<TableCell className="max-w-[360px]">
											<div className="text-foreground truncate text-sm font-medium">{item.label}</div>
											{item.detail && <div className="text-muted-foreground truncate font-mono text-xs">{item.detail}</div>}
											{item.description && <p className="text-muted-foreground mt-0.5 truncate text-xs">{item.description}</p>}
										</TableCell>
										{extraColumn && <TableCell className="max-w-[280px]">{extraColumn.render(item)}</TableCell>}
										<TableCell className="text-center">
											<div className="flex h-11 items-center justify-center">
												{readOnly ? (
													<Badge
														variant="default"
														className={SERVED_BADGE[exposeRowState(value, item.key)]}
														data-testid={`mcp-expose-${kind}-label-${item.key}`}
													>
														{exposeRowLabel(value, item.key)}
													</Badge>
												) : (
													<Switch
														size="md"
														checked={exposed}
														disabled={byRule}
														title={byRule ? t("registry.expose.servedByRule") : undefined}
														aria-label={
															byRule
																? t("registry.expose.servedByRuleAria", { label: item.label })
																: t(exposed ? "registry.expose.stopServingAria" : "registry.expose.serveAria", { label: item.label })
														}
														data-testid={`mcp-expose-${kind}-toggle-${item.key}`}
														onCheckedChange={(checked) => onChange?.(nextExposeList(value, allKeys, item.key, checked))}
													/>
												)}
											</div>
										</TableCell>
									</TableRow>
								);
							})}
						</TableBody>
					</Table>
				</div>
			) : (
				<div className="text-muted-foreground rounded-sm border p-6 text-center" data-testid={`mcp-expose-${kind}-empty`}>
					<p className="text-sm">{t("registry.expose.listsNone", { kind })}</p>
				</div>
			)}
		</div>
	);
}