// Shared visual building blocks for the MCP-client verification/
// authorization dialogs: OAuth2Authorizer (shared + per-user OAuth),
// MCPHeadersAuthorizer (per-user headers), and the token_exchange
// "Re-verify as me" dialog in mcpClientsTable.tsx. All three walk an admin
// through the same shape of flow (confirm intent, wait on a check, land on
// success/failure), so they share one dialog chrome: a bordered icon
// header, tinted info boxes for body copy, and step-progress dots. Keeping
// this in one place is what stops the flows drifting apart the way
// per-user-headers previously did from the OAuth dialog.

import { cn } from "@/lib/utils";
import React from "react";

export type UiVariant = "muted" | "info" | "success" | "danger" | "warning";

const ICON_TINTS: Record<UiVariant, string> = {
	muted: "bg-muted text-muted-foreground",
	info: "bg-blue-50 text-blue-600 dark:bg-blue-950 dark:text-blue-300",
	success: "bg-green-50 text-green-700 dark:bg-green-950 dark:text-green-300",
	danger: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-300",
	warning: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300",
};

// ── Icon slot ────────────────────────────────────────────────────────────────
// The tinted square in the dialog header, next to the title/subtitle.

export function IconWrap({ variant, icon }: { variant: UiVariant; icon: React.ReactNode }) {
	return <div className={cn("flex size-9 shrink-0 items-center justify-center rounded-md", ICON_TINTS[variant])}>{icon}</div>;
}

// ── Info box ──────────────────────────────────────────────────────────────────
// The bordered, tinted callout used for body copy in every step.

const BOX_TINTS: Record<UiVariant, string> = {
	muted: "border-border bg-muted/40 text-muted-foreground",
	info: "border-blue-200/60 bg-blue-50/70 text-blue-800 dark:border-blue-800/40 dark:bg-blue-950/40 dark:text-blue-200",
	success: "border-green-200/60 bg-green-50/70 text-green-800 dark:border-green-800/40 dark:bg-green-950/40 dark:text-green-200",
	danger: "border-red-200/60 bg-red-50/70 text-red-800 dark:border-red-800/40 dark:bg-red-950/40 dark:text-red-200",
	warning: "border-amber-200/60 bg-amber-50/70 text-amber-800 dark:border-amber-800/40 dark:bg-amber-950/40 dark:text-amber-200",
};

export function InfoBox({
	variant = "muted",
	icon,
	children,
}: {
	variant?: UiVariant;
	icon: React.ReactNode;
	children: React.ReactNode;
}) {
	return (
		<div className={cn("flex gap-3 rounded-md border p-3.5 text-sm", BOX_TINTS[variant])}>
			<span className="mt-0.5 shrink-0">{icon}</span>
			<div className="space-y-1 leading-relaxed">{children}</div>
		</div>
	);
}

// ── Step dots ─────────────────────────────────────────────────────────────────
// Progress indicator shown while a step is waiting on something external
// (a popup redirect, a background verification call).

export function StepDots({ active, total }: { active: number; total: number }) {
	return (
		<div className="flex items-center gap-1">
			{Array.from({ length: total }).map((_, i) => (
				<div key={i} className={cn("size-1.5 rounded-full transition-colors", i < active ? "bg-blue-500" : "bg-border")} />
			))}
		</div>
	);
}
