// SBS fork: small building blocks shared by the MetaMCP-style MCP pages.
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import type { MCPConnectionState, MCPConnectionType } from "@/lib/types/mcp";
import { cn } from "@/lib/utils";
import { Check, Copy } from "lucide-react";
import type { ReactNode } from "react";

export function CopyButton({ value, label, className }: { value: string; label?: string; className?: string }) {
	const { copy, copied } = useCopyToClipboard();
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Button
					type="button"
					variant="ghost"
					size="icon"
					className={cn("h-7 w-7 shrink-0", className)}
					onClick={(e) => {
						e.stopPropagation();
						copy(value);
					}}
					aria-label={label ?? "Copy"}
				>
					{copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
				</Button>
			</TooltipTrigger>
			<TooltipContent>{label ?? "Copy"}</TooltipContent>
		</Tooltip>
	);
}

/** A monospace value with a copy button, e.g. an endpoint URL. */
export function CopyField({ label, value }: { label?: string; value: string }) {
	return (
		<div className="flex min-w-0 items-center gap-1">
			{label && <span className="text-muted-foreground w-24 shrink-0 text-xs">{label}</span>}
			<code className="bg-muted min-w-0 flex-1 truncate rounded px-2 py-1 font-mono text-xs" title={value}>
				{value}
			</code>
			<CopyButton value={value} label={label ? `Copy ${label}` : undefined} />
		</div>
	);
}

const stateStyle: Record<string, { label: string; variant: "success" | "warning" | "destructive" | "secondary" | "outline" }> = {
	healthy: { label: "Connected", variant: "success" },
	unstable: { label: "Unstable", variant: "warning" },
	degraded: { label: "Degraded", variant: "warning" },
	needs_reauth: { label: "Needs re-auth", variant: "destructive" },
	pending_verification: { label: "Pending verification", variant: "warning" },
	error: { label: "Error", variant: "destructive" },
	disabled: { label: "Disabled", variant: "secondary" },
};

export function ConnectionStateBadge({ state }: { state?: MCPConnectionState }) {
	const style = stateStyle[state ?? ""] ?? { label: state ?? "Unknown", variant: "outline" as const };
	return <Badge variant={style.variant}>{style.label}</Badge>;
}

const typeLabel: Record<MCPConnectionType, string> = { http: "Streamable HTTP", sse: "SSE", stdio: "STDIO", openapi: "OpenAPI" };

export function ConnectionTypeBadge({ type }: { type?: MCPConnectionType }) {
	return <Badge variant="outline">{type ? (typeLabel[type] ?? type) : "—"}</Badge>;
}

export function StatusBadge({
	active,
	activeLabel = "Active",
	inactiveLabel = "Inactive",
}: {
	active: boolean;
	activeLabel?: string;
	inactiveLabel?: string;
}) {
	return <Badge variant={active ? "success" : "secondary"}>{active ? activeLabel : inactiveLabel}</Badge>;
}

/** MetaMCP's "Basic information" / "Statistics" card: a titled list of label/value rows. */
export function InfoCard({ title, rows, className }: { title: string; rows: { label: string; value: ReactNode }[]; className?: string }) {
	return (
		<Card className={cn("gap-3 py-4", className)}>
			<CardHeader className="px-4">
				<CardTitle className="text-sm font-semibold">{title}</CardTitle>
			</CardHeader>
			<CardContent className="space-y-2 px-4 text-sm">
				{rows.map((row) => (
					<div key={row.label} className="flex items-start justify-between gap-4">
						<span className="text-muted-foreground shrink-0">{row.label}</span>
						<span className="min-w-0 text-right break-all">{row.value}</span>
					</div>
				))}
			</CardContent>
		</Card>
	);
}

/** A section header with optional actions on the right, as on MetaMCP's detail pages. */
export function SectionHeader({ title, description, children }: { title: string; description?: string; children?: ReactNode }) {
	return (
		<div className="mb-3 flex flex-wrap items-end justify-between gap-2">
			<div>
				<h3 className="text-base font-semibold">{title}</h3>
				{description && <p className="text-muted-foreground text-sm">{description}</p>}
			</div>
			{children && <div className="flex items-center gap-2">{children}</div>}
		</div>
	);
}

export function formatDate(value?: string): string {
	if (!value) return "—";
	const d = new Date(value);
	return Number.isNaN(d.getTime()) ? value : d.toLocaleString();
}
