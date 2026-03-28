"use client";

import { Badge } from "@/components/ui/badge";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Separator } from "@/components/ui/separator";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { RoutingRule } from "@/lib/types/routingRules";
import { getScopeLabel } from "@/lib/utils/routingRules";
import { getOperatorLabel } from "@/lib/config/celOperatorsRouting";
import { baseRoutingFields } from "@/lib/config/celFieldsRouting";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { useGetTeamsQuery, useGetCustomersQuery, useGetVirtualKeysQuery } from "@/lib/store/apis/governanceApi";
import { useMemo } from "react";
import { RuleGroupType, RuleType } from "react-querybuilder";

interface Props {
	rule: RoutingRule | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

function getFieldLabel(fieldName: string): string {
	const field = baseRoutingFields.find((f) => f.name === fieldName);
	return field?.label ?? fieldName;
}

function formatRuleValue(value: any): string {
	if (Array.isArray(value)) return value.join(", ");
	if (typeof value === "string") return value;
	return String(value ?? "");
}

function ConditionRow({ rule }: { rule: RuleType }) {
	const fieldLabel = getFieldLabel(rule.field);
	const opLabel = getOperatorLabel(rule.operator);
	const value = formatRuleValue(rule.value);
	const isExistence = rule.operator === "null" || rule.operator === "notNull";

	return (
		<div className="flex items-center gap-1.5 px-2.5 py-1.5 text-xs">
			<Badge variant="outline" className="shrink-0 font-medium">
				{fieldLabel}
			</Badge>
			<span className="text-muted-foreground shrink-0">{opLabel}</span>
			{!isExistence && <code className="bg-muted text-foreground truncate rounded px-1.5 py-0.5 font-mono">{value}</code>}
		</div>
	);
}

function CombinatorPill({ combinator }: { combinator: string }) {
	return (
		<div className="flex items-center gap-1.5 px-2.5">
			<div className="bg-border h-px flex-1" />
			<span className="text-muted-foreground text-[10px] font-semibold uppercase">{combinator}</span>
			<div className="bg-border h-px flex-1" />
		</div>
	);
}

function ConditionGroup({ group, depth = 0 }: { group: RuleGroupType; depth?: number }) {
	const rules = group.rules ?? [];
	if (rules.length === 0) return null;

	const content = rules.map((rule, i) => (
		<div key={i}>
			{i > 0 && <CombinatorPill combinator={group.combinator} />}
			{"combinator" in rule ? <ConditionGroup group={rule as RuleGroupType} depth={depth + 1} /> : <ConditionRow rule={rule as RuleType} />}
		</div>
	));

	if (depth === 0) {
		return <div className="rounded-md border py-1">{content}</div>;
	}

	return (
		<div className="border-foreground/25 relative mx-2.5 my-1 rounded border border-dashed py-1">
			<span className="bg-background text-muted-foreground absolute -top-2 right-2 rounded px-1 text-[10px] font-medium">Group</span>
			{content}
		</div>
	);
}

function useScopeName(scope: string, scopeId?: string): string | undefined {
	const { data: teamsData } = useGetTeamsQuery(undefined, { skip: scope !== "team" || !scopeId });
	const { data: customersData } = useGetCustomersQuery(undefined, { skip: scope !== "customer" || !scopeId });
	const { data: vksData } = useGetVirtualKeysQuery(undefined, { skip: scope !== "virtual_key" || !scopeId });

	return useMemo(() => {
		if (!scopeId) return undefined;
		if (scope === "team") {
			return teamsData?.teams?.find((t) => t.id === scopeId)?.name;
		}
		if (scope === "customer") {
			return customersData?.customers?.find((c) => c.id === scopeId)?.name;
		}
		if (scope === "virtual_key") {
			return vksData?.virtual_keys?.find((v) => v.id === scopeId)?.name;
		}
		return undefined;
	}, [scope, scopeId, teamsData, customersData, vksData]);
}

function TargetCard({ target }: { target: RoutingRule["targets"][0] }) {
	const providerLabel = target.provider ? getProviderLabel(target.provider) : "Any provider";
	const weightPercent = Math.round(target.weight * 100);

	return (
		<div className="flex items-center gap-3 rounded-md border px-3 py-2.5">
			<div className="flex min-w-0 flex-1 items-center gap-2.5">
				{target.provider && <RenderProviderIcon provider={target.provider as ProviderIconType} size="sm" className="h-5 w-5 shrink-0" />}
				<div className="flex min-w-0 flex-col">
					<span className="truncate text-sm font-medium">{providerLabel}</span>
					{target.model ? (
						<span className="text-muted-foreground truncate font-mono text-xs">{target.model}</span>
					) : (
						<span className="text-muted-foreground text-xs">All models</span>
					)}
				</div>
			</div>
			<div className="shrink-0">
				<Tooltip>
					<TooltipTrigger asChild>
						<div className="flex items-center gap-1.5">
							<div className="bg-muted h-1.5 w-16 overflow-hidden rounded-full">
								<div className="bg-primary h-full rounded-full transition-all" style={{ width: `${weightPercent}%` }} />
							</div>
							<span className="text-muted-foreground w-8 text-right font-mono text-xs">{weightPercent}%</span>
						</div>
					</TooltipTrigger>
					<TooltipContent>Weight: {target.weight}</TooltipContent>
				</Tooltip>
			</div>
		</div>
	);
}

function FallbackChain({ fallbacks }: { fallbacks: string[] }) {
	return (
		<div className="flex flex-wrap items-center gap-y-1.5">
			{fallbacks.map((fb, i) => {
				const parts = fb.split("/");
				const provider = parts[0];
				const model = parts.length > 1 ? parts.slice(1).join("/") : undefined;

				return (
					<div key={i} className="flex items-center">
						{i > 0 && <span className="text-muted-foreground mx-1.5 text-xs">&rarr;</span>}
						<Badge variant="outline" className="gap-1.5 font-normal">
							{provider && <RenderProviderIcon provider={provider as ProviderIconType} size="sm" className="h-3.5 w-3.5 shrink-0" />}
							<span className="font-mono text-xs">{model ? `${provider}/${model}` : fb}</span>
						</Badge>
					</div>
				);
			})}
		</div>
	);
}

export function RoutingRuleInfoSheet({ rule, open, onOpenChange }: Props) {
	const targets = rule?.targets ?? [];
	const fallbacks = rule?.fallbacks ?? [];
	const hasQuery = rule?.query && (rule.query.rules?.length ?? 0) > 0;
	const scopeName = useScopeName(rule?.scope ?? "global", rule?.scope_id);

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			{rule && (
				<SheetContent className="custom-scrollbar p-8" data-testid="routing-rule-info">
					<SheetHeader className="flex flex-col items-start">
						<SheetTitle>Rule details</SheetTitle>
					</SheetHeader>

					<div className="space-y-6">
						{/* Identity */}
						<div>
							<div className="flex items-center gap-2">
								<h3 className="text-base font-medium">{rule.name}</h3>
								<Badge variant={rule.enabled ? "success" : "secondary"} className="px-1.5 py-0 text-[10px]">
									{rule.enabled ? "Enabled" : "Disabled"}
								</Badge>
							</div>
							{rule.description && <p className="text-muted-foreground mt-1 text-sm">{rule.description}</p>}
						</div>

						<Separator />

						{/* Metadata grid */}
						<div className="grid grid-cols-2 gap-4">
							<div>
								<p className="text-muted-foreground mb-1 text-xs font-medium tracking-wider uppercase">Scope</p>
								<div className="flex items-center gap-1.5">
									<Badge variant="secondary">{getScopeLabel(rule.scope)}</Badge>
									{scopeName && <span className="text-sm font-medium">{scopeName}</span>}
								</div>
							</div>
							<div>
								<p className="text-muted-foreground mb-1 text-xs font-medium tracking-wider uppercase">Priority</p>
								<span className="bg-primary text-primary-foreground inline-block rounded px-2.5 py-0.5 text-xs font-medium">
									{rule.priority}
								</span>
							</div>
						</div>

						{/* Rule Conditions */}
						{hasQuery && (
							<>
								<Separator />
								<div>
									<p className="text-muted-foreground mb-2 text-xs font-medium tracking-wider uppercase">Conditions</p>
									<ConditionGroup group={rule.query!} />
								</div>
							</>
						)}

						{/* Targets */}
						{targets.length > 0 && (
							<>
								<Separator />
								<div>
									<p className="text-muted-foreground mb-2 text-xs font-medium tracking-wider uppercase">Targets ({targets.length})</p>
									<div className="space-y-2">
										{targets.map((target, i) => (
											<TargetCard key={i} target={target} />
										))}
									</div>
								</div>
							</>
						)}

						{/* Fallbacks */}
						{fallbacks.length > 0 && (
							<>
								<Separator />
								<div>
									<p className="text-muted-foreground mb-2 text-xs font-medium tracking-wider uppercase">Fallback Chain</p>
									<FallbackChain fallbacks={fallbacks} />
								</div>
							</>
						)}

						{/* Timestamps */}
						<Separator />
						<div className="grid grid-cols-2 gap-4">
							<div>
								<p className="text-muted-foreground mb-1 text-xs font-medium tracking-wider uppercase">Created</p>
								<span className="text-sm">
									{new Date(rule.created_at).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" })}
								</span>
							</div>
							<div>
								<p className="text-muted-foreground mb-1 text-xs font-medium tracking-wider uppercase">Updated</p>
								<span className="text-sm">
									{new Date(rule.updated_at).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" })}
								</span>
							</div>
						</div>
					</div>
				</SheetContent>
			)}
		</Sheet>
	);
}
