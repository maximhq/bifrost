"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { RoutingRule } from "@/lib/types/routingRules";
import { getScopeLabel } from "@/lib/utils/routingRules";
import { getOperatorLabel } from "@/lib/config/celOperatorsRouting";
import { baseRoutingFields } from "@/lib/config/celFieldsRouting";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { useGetTeamsQuery, useGetCustomersQuery, useGetVirtualKeysQuery } from "@/lib/store/apis/governanceApi";
import { formatDistanceToNow } from "date-fns";
import { Check, Copy, GitMerge, Key } from "lucide-react";
import { useMemo, useState } from "react";
import { RuleGroupType, RuleType } from "react-querybuilder";
import { toast } from "sonner";

interface Props {
	rule: RoutingRule | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

// ─── helpers ────────────────────────────────────────────────────────────────

function getFieldLabel(fieldName: string): string {
	const field = baseRoutingFields.find((f) => f.name === fieldName);
	return field?.label ?? fieldName;
}

function formatRuleValue(value: any): string {
	if (Array.isArray(value)) return value.join(", ");
	if (typeof value === "string") return value;
	return String(value ?? "");
}

function useScopeName(scope: string, scopeId?: string): string | undefined {
	const { data: teamsData } = useGetTeamsQuery(undefined, { skip: scope !== "team" || !scopeId });
	const { data: customersData } = useGetCustomersQuery(undefined, { skip: scope !== "customer" || !scopeId });
	const { data: vksData } = useGetVirtualKeysQuery(undefined, { skip: scope !== "virtual_key" || !scopeId });

	return useMemo(() => {
		if (!scopeId) return undefined;
		if (scope === "team") return teamsData?.teams?.find((t) => t.id === scopeId)?.name;
		if (scope === "customer") return customersData?.customers?.find((c) => c.id === scopeId)?.name;
		if (scope === "virtual_key") return vksData?.virtual_keys?.find((v) => v.id === scopeId)?.name;
		return undefined;
	}, [scope, scopeId, teamsData, customersData, vksData]);
}

// ─── copy button ─────────────────────────────────────────────────────────────

function CopyButton({ value, label }: { value: string; label?: string }) {
	const [copied, setCopied] = useState(false);

	const handleCopy = async () => {
		try {
			await navigator.clipboard.writeText(value);
			setCopied(true);
			setTimeout(() => setCopied(false), 1500);
		} catch {
			toast.error("Failed to copy to clipboard");
		}
	};

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Button variant="ghost" size="icon" className="h-6 w-6 shrink-0" onClick={handleCopy}>
					{copied ? <Check className="h-3.5 w-3.5 text-green-500" /> : <Copy className="h-3.5 w-3.5" />}
				</Button>
			</TooltipTrigger>
			<TooltipContent>{copied ? "Copied!" : `Copy ${label ?? "value"}`}</TooltipContent>
		</Tooltip>
	);
}

// ─── condition rendering ─────────────────────────────────────────────────────

function ConditionRow({ rule }: { rule: RuleType }) {
	const fieldLabel = getFieldLabel(rule.field);
	const opLabel = getOperatorLabel(rule.operator);
	const value = formatRuleValue(rule.value);
	const isExistence = rule.operator === "null" || rule.operator === "notNull";

	// Detect header/param fields for richer display
	const isHeader = rule.field.startsWith("headers[") || rule.field === "headers";
	const isParam = rule.field.startsWith("params[") || rule.field === "params";
	const keyMatch = rule.field.match(/\["([^"]+)"\]/);
	// Bare field (e.g. headers / params) may encode key:value in the value string
	const bareKeyValue = !keyMatch && (isHeader || isParam) && value
		? value.includes(":") ? { key: value.slice(0, value.indexOf(":")), val: value.slice(value.indexOf(":") + 1) } : { key: value, val: "" }
		: null;
	const keyName = keyMatch?.[1] ?? bareKeyValue?.key;
	const displayValue = bareKeyValue !== null ? bareKeyValue.val : value;

	return (
		<div className="flex items-start gap-1.5 px-3 py-2 text-xs">
			<div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
				<Badge variant="outline" className="shrink-0 font-medium">
					{isHeader && keyName ? (
						<span className="flex items-center gap-1">
							<span className="text-muted-foreground font-normal">header</span>
							<span className="font-mono">{keyName}</span>
						</span>
					) : isParam && keyName ? (
						<span className="flex items-center gap-1">
							<span className="text-muted-foreground font-normal">param</span>
							<span className="font-mono">{keyName}</span>
						</span>
					) : (
						fieldLabel
					)}
				</Badge>
				<span className="text-muted-foreground shrink-0">{opLabel}</span>
				{!isExistence && displayValue && (
					<code className="bg-muted text-foreground rounded px-1.5 py-0.5 font-mono break-all">{displayValue}</code>
				)}
			</div>
		</div>
	);
}

function CombinatorPill({ combinator }: { combinator: string }) {
	return (
		<div className="flex items-center gap-1.5 px-3">
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
			{"combinator" in rule
				? <ConditionGroup group={rule as RuleGroupType} depth={depth + 1} />
				: <ConditionRow rule={rule as RuleType} />}
		</div>
	));

	if (depth === 0) return <div className="rounded-md border py-1">{content}</div>;

	return (
		<div className="border-foreground/25 relative mx-3 my-1 rounded border border-dashed py-1">
			<span className="bg-background text-muted-foreground absolute -top-2 right-2 rounded px-1 text-[10px] font-medium">Group</span>
			{content}
		</div>
	);
}

// ─── target card ─────────────────────────────────────────────────────────────

function TargetCard({ target, index, total }: { target: RoutingRule["targets"][0]; index: number; total: number }) {
	const providerLabel = target.provider ? getProviderLabel(target.provider) : "Incoming provider";
	const weightPercent = total > 0 ? Math.round(target.weight * 100) : 0;

	return (
		<div className="rounded-lg border p-3 space-y-2">
			<div className="flex items-center justify-between">
				<div className="flex items-center gap-2.5">
					{target.provider && (
						<RenderProviderIcon provider={target.provider as ProviderIconType} size="sm" className="h-5 w-5 shrink-0" />
					)}
					<div className="flex flex-col">
						<span className="text-sm font-medium">{providerLabel}</span>
						{target.model ? (
							<span className="text-muted-foreground font-mono text-xs">{target.model}</span>
						) : (
							<span className="text-muted-foreground text-xs">Incoming model</span>
						)}
					</div>
				</div>
				<Tooltip>
					<TooltipTrigger asChild>
						<div className="flex items-center gap-1.5 cursor-default">
							<div className="bg-muted h-1.5 w-16 overflow-hidden rounded-full">
								<div className="bg-primary h-full rounded-full transition-all" style={{ width: `${weightPercent}%` }} />
							</div>
							<span className="text-muted-foreground w-8 text-right font-mono text-xs">{weightPercent}%</span>
						</div>
					</TooltipTrigger>
					<TooltipContent>Weight: {target.weight} (raw)</TooltipContent>
				</Tooltip>
			</div>
			{target.key_id && (
				<div className="flex items-center gap-1.5 rounded-md bg-muted/50 px-2 py-1">
					<Key className="h-3 w-3 text-muted-foreground shrink-0" />
					<span className="text-muted-foreground text-xs">Pinned key:</span>
					<code className="font-mono text-xs truncate">{target.key_id}</code>
					<CopyButton value={target.key_id} label="key ID" />
				</div>
			)}
		</div>
	);
}

// ─── fallback chain ───────────────────────────────────────────────────────────

function FallbackChain({ fallbacks }: { fallbacks: string[] }) {
	return (
		<div className="flex flex-wrap items-center gap-y-2">
			{fallbacks.map((fb, i) => {
				const parts = fb.split("/");
				const provider = parts[0] || "Incoming provider";
				const model = parts.length > 1 ? parts.slice(1).join("/") : "Incoming model";

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

// ─── main sheet ──────────────────────────────────────────────────────────────

export function RoutingRuleInfoSheet({ rule, open, onOpenChange }: Props) {
	const targets = rule?.targets ?? [];
	const fallbacks = rule?.fallbacks ?? [];
	const hasQuery = rule?.query && (rule.query.rules?.length ?? 0) > 0;
	const scopeName = useScopeName(rule?.scope ?? "global", rule?.scope_id);

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden p-8 sm:max-w-2xl" data-testid="routing-rule-info">
				{rule && (
					<>
						<SheetHeader className="p-0 flex flex-col items-start gap-1">
							<div className="flex w-full items-center gap-2 flex-wrap">
								<SheetTitle className="text-base">{rule.name}</SheetTitle>
								<Badge variant={rule.enabled ? "default" : "secondary"}>
									{rule.enabled ? "Enabled" : "Disabled"}
								</Badge>
								{rule.chain_rule && (
									<Tooltip>
										<TooltipTrigger asChild>
											<Badge variant="outline" className="gap-1 cursor-default">
												<GitMerge className="h-3 w-3" />
												Chain Rule
											</Badge>
										</TooltipTrigger>
										<TooltipContent className="max-w-64">
											After this rule matches, routing rules are re-evaluated using the resolved provider/model as the new context.
										</TooltipContent>
									</Tooltip>
								)}
							</div>
							{rule.description && (
								<SheetDescription className="text-sm mt-0.5">{rule.description}</SheetDescription>
							)}
						</SheetHeader>

						<div className="space-y-6 overflow-y-auto -mx-8 px-8 pb-8">
							{/* Overview */}
							<div className="space-y-3">
								<h3 className="font-semibold text-sm">Overview</h3>
								<div className="grid gap-3">
									<div className="grid grid-cols-3 items-center gap-4">
										<span className="text-muted-foreground text-sm">Scope</span>
										<div className="col-span-2 flex items-center gap-1.5">
											<Badge variant="secondary">{getScopeLabel(rule.scope)}</Badge>
											{scopeName && <span className="text-sm">{scopeName}</span>}
										</div>
									</div>
									<div className="grid grid-cols-3 items-center gap-4">
										<span className="text-muted-foreground text-sm">Priority</span>
										<div className="col-span-2">
											<span className="bg-primary text-primary-foreground inline-block rounded px-2.5 py-0.5 text-xs font-medium">
												{rule.priority}
											</span>
										</div>
									</div>
								</div>
							</div>

							<DottedSeparator />

							{/* Conditions */}
							<div className="space-y-3">
								<h3 className="font-semibold text-sm">Conditions</h3>
								{hasQuery ? (
									<ConditionGroup group={rule.query!} />
								) : (
									<p className="text-muted-foreground text-sm">Matches all requests</p>
								)}

								{/* CEL expression */}
								<div className="space-y-1.5">
									<div className="flex items-center justify-between">
										<span className="font-semibold text-sm">CEL Expression</span>
										<CopyButton value={rule.cel_expression} label="expression" />
									</div>
									<code className="block w-full rounded-md border bg-muted/50 px-3 py-2 font-mono text-xs break-all">
										{rule.cel_expression || <span className="text-muted-foreground italic">true</span>}
									</code>
								</div>
							</div>

							<DottedSeparator />

							{/* Targets */}
							<div className="space-y-3">
								<h3 className="font-semibold text-sm">Targets ({targets.length})</h3>
								{targets.length > 0 ? (
									<div className="space-y-2">
										{targets.map((target, i) => (
											<TargetCard key={i} target={target} index={i} total={targets.length} />
										))}
									</div>
								) : (
									<p className="text-muted-foreground text-sm">No targets configured</p>
								)}
							</div>

							<DottedSeparator />

							{/* Fallback Chain */}
							<div className="space-y-3">
								<h3 className="font-semibold text-sm">Fallback Chain</h3>
								{fallbacks.length > 0 ? (
									<FallbackChain fallbacks={fallbacks} />
								) : (
									<p className="text-muted-foreground text-sm">No fallbacks configured</p>
								)}
							</div>

							<DottedSeparator />

							{/* Timestamps */}
							<div className="grid grid-cols-2 gap-4">
								<div>
									<p className="text-muted-foreground mb-1 text-xs font-medium uppercase tracking-wider">Created</p>
									<span className="text-sm">
										{formatDistanceToNow(new Date(rule.created_at), { addSuffix: true })}
									</span>
								</div>
								<div>
									<p className="text-muted-foreground mb-1 text-xs font-medium uppercase tracking-wider">Last Updated</p>
									<span className="text-sm">
										{formatDistanceToNow(new Date(rule.updated_at), { addSuffix: true })}
									</span>
								</div>
							</div>
						</div>
					</>
				)}
			</SheetContent>
		</Sheet>
	);
}
