"use client";

import { Badge } from "@/components/ui/badge";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { RoutingRule } from "@/lib/types/routingRules";
import { Position } from "@xyflow/react";
import { Link2 } from "lucide-react";
import { useState } from "react";
import { RULE_W, SCOPE_CONFIG, type ScopeKey } from "../constants";
import { RFEdgeHandle } from "./rfEdgeHandle";

export function RFRuleNode({ data }: { data: any }) {
	const rule = data.rule as RoutingRule;
	const scopeColor = data.scopeColor as string;
	const cfg = SCOPE_CONFIG[rule.scope as ScopeKey];
	const multi = rule.targets.length > 1;
	const [hovered, setHovered] = useState(false);

	return (
		<div
			className="relative"
			style={{ width: RULE_W }}
			tabIndex={0}
			role="button"
			aria-haspopup="true"
			aria-expanded={hovered}
			onMouseEnter={() => setHovered(true)}
			onMouseLeave={() => setHovered(false)}
			onFocus={() => setHovered(true)}
			onBlur={() => setHovered(false)}
			onClick={() => setHovered((v) => !v)}
			onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setHovered((v) => !v); } }}
		>
			<RFEdgeHandle type="target" position={Position.Left} accentColor={scopeColor} />
			{rule.chain_rule && (
				<RFEdgeHandle type="source" id="chain-out" position={Position.Right} accentColor={scopeColor} />
			)}
			<div
				className="relative z-10 rounded-lg border-2 bg-white dark:bg-card shadow-sm cursor-grab active:cursor-grabbing"
				style={{ borderColor: scopeColor, borderStyle: rule.chain_rule ? "dashed" : "solid" }}
			>

			{/* scope header */}
			<div className={`flex items-center gap-1.5 rounded-t-[6px] px-3 py-1.5 ${cfg?.headerClass ?? "bg-gray-100 dark:bg-gray-800/30"}`}>
				<span className="h-1.5 w-1.5 flex-shrink-0 rounded-full" style={{ backgroundColor: scopeColor }} />
				<span className="text-[10px] font-semibold" style={{ color: scopeColor }}>
					{cfg?.label ?? rule.scope}
				</span>
				<div className="ml-auto flex items-center gap-1">
					{rule.chain_rule && (
						<Link2 className="h-3 w-3" style={{ color: scopeColor }} />
					)}
					{!rule.enabled && (
						<Badge variant="secondary" className="px-1 py-0 text-[9px]">Off</Badge>
					)}
				</div>
			</div>

			{/* rule name */}
			<div className="px-3 py-2">
				<p className="truncate text-xs font-semibold text-foreground">{rule.name}</p>
				{rule.priority > 0 && (
					<p className="mt-0.5 text-[10px] text-muted-foreground">Priority {rule.priority}</p>
				)}
			</div>

			{/* targets footer */}
			<div
				className="flex items-center gap-1.5 rounded-b-[6px] border-t px-3 py-1.5"
				style={{ borderColor: `${scopeColor}40`, backgroundColor: `${scopeColor}08` }}
			>
				<div className="flex items-center gap-1">
					{rule.targets.slice(0, 4).map((t, i) =>
						t.provider
							? <RenderProviderIcon key={i} provider={t.provider as ProviderIconType} size={12} />
							: <span key={i} className="h-2 w-2 rounded-full bg-muted-foreground/30" />
					)}
					{rule.targets.length > 4 && (
						<span className="text-[9px] text-muted-foreground">+{rule.targets.length - 4}</span>
					)}
				</div>
				<span className="ml-auto text-[10px] text-muted-foreground">
					{rule.targets.length} target{rule.targets.length !== 1 ? "s" : ""}
				</span>
			</div>

			{/* hover popover */}
			{hovered && (
				<div
					className="nodrag nowheel absolute left-full top-0 z-50 ml-3 min-w-[190px] rounded-lg border-2 bg-white dark:bg-card py-1.5 shadow-xl"
					style={{ borderColor: scopeColor }}
				>
					{rule.scope !== "global" && rule.scope_id && (
						<div className="mb-1 border-b px-3 pb-1.5">
							<p className="text-[10px] text-muted-foreground">
								<span className="font-semibold" style={{ color: scopeColor }}>{cfg?.label ?? rule.scope}: </span>
								<span className="font-medium text-foreground">{rule.scope_id}</span>
							</p>
						</div>
					)}
					{rule.chain_rule && (
						<div className="mb-1 flex items-start gap-2 border-b px-3 pb-1.5">
							<Link2 className="mt-0.5 h-3 w-3 shrink-0" style={{ color: scopeColor }} />
							<p className="text-[10px] text-muted-foreground leading-snug">
								Chain rule — resolved provider/model feeds back as the new input and the full scope chain re-evaluates.
							</p>
						</div>
					)}
					<p className="mb-1 px-3 text-[10px] font-semibold uppercase tracking-wide" style={{ color: scopeColor }}>
						{rule.chain_rule ? "Resolved target (new input)" : "Targets"}
					</p>
					{rule.targets.map((t, i) => {
						const isPassthrough = !t.provider && !t.model;
						return (
							<div key={i} className="flex items-center gap-2 px-3 py-1.5 hover:bg-muted">
								{t.provider
									? <RenderProviderIcon provider={t.provider as ProviderIconType} size={13} />
									: <span className="h-3 w-3 flex-shrink-0 rounded-full bg-muted-foreground/30" />
								}
								<div className="min-w-0 flex-1">
									<p className="truncate text-xs font-medium text-foreground">
										{isPassthrough ? "Passthrough" : (t.provider ? getProviderLabel(t.provider) : t.model)}
									</p>
									{t.model && t.provider && (
										<p className="truncate font-mono text-[10px] text-muted-foreground">{t.model}</p>
									)}
									{isPassthrough && (
										<p className="text-[10px] italic text-muted-foreground/60">original provider &amp; model</p>
									)}
								</div>
								{multi && (
									<span className="ml-1 shrink-0 text-[11px] font-semibold" style={{ color: scopeColor }}>
										{Math.round(t.weight * 100)}%
									</span>
								)}
							</div>
						);
					})}
				</div>
			)}
			</div>
		</div>
	);
}
