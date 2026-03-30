"use client";

import { cn } from "@/lib/utils";
import { Position } from "@xyflow/react";
import { COND_H, COND_W, SCOPE_CONFIG, type ScopeKey } from "../constants";
import { RFEdgeHandle } from "./rfEdgeHandle";

/** Width of the left scope-color strip (matches common “start node” accent bars). */
const ACCENT_STRIP_CLASS = "w-2.5";

export function RFConditionNode({ data }: { data: any }) {
	const condition = data.condition as string;
	const color = data.color as string | null;
	const scopes = (data.scopes as string[] | undefined) ?? [];
	const accent = color ?? undefined;

	return (
		<div className="relative" style={{ width: COND_W, minHeight: COND_H }}>
			<RFEdgeHandle
				type="target"
				position={Position.Left}
				accentColor={accent ?? "var(--muted-foreground)"}
			/>
			<RFEdgeHandle
				type="source"
				position={Position.Right}
				accentColor={accent ?? "var(--muted-foreground)"}
			/>
			<div
				className="relative z-10 flex w-full overflow-hidden rounded-lg border bg-white dark:bg-card shadow-sm cursor-grab active:cursor-grabbing"
				style={{ minHeight: COND_H, borderColor: accent }}
			>
				<div
					className={cn("shrink-0 self-stretch", ACCENT_STRIP_CLASS, !accent && "bg-muted")}
					style={accent ? { backgroundColor: accent } : undefined}
					aria-hidden
				/>
				<div className="flex min-w-0 flex-1 flex-col gap-4 px-3 py-2.5">
					<code className="flex-1 break-all font-mono text-[12px] leading-snug text-foreground">
						{condition}
					</code>
					{scopes.length > 0 && (
						<div className="flex flex-wrap gap-1">
							{scopes.map((sc) => {
								const cfg = SCOPE_CONFIG[sc as ScopeKey];
								return cfg ? (
									<span
										key={sc}
										className="rounded px-1 py-0 text-[9px] font-semibold"
										style={{ backgroundColor: `${cfg.color}18`, color: cfg.color }}
									>
										{cfg.label}
									</span>
								) : null;
							})}
						</div>
					)}
				</div>
			</div>
		</div>
	);
}
