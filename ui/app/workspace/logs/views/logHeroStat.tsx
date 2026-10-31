import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import type { Status } from "@/lib/constants/logs";
import { cn } from "@/lib/utils";
import { Clipboard } from "lucide-react";
import type { ReactNode } from "react";

// Shared header primitives for the log detail sheets (LLM, MCP, relay), so every
// kind of log renders the same hero card, status pill and copy affordance.

const statusPillStyles: Record<string, string> = {
	success: "border-chart-success/30 bg-chart-success/10 text-chart-success-ink",
	error: "border-chart-error/30 bg-chart-error/10 text-chart-error-ink",
	processing: "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950/40 dark:text-blue-400 dark:border-blue-900",
	cancelled: "bg-gray-50 text-gray-700 border-gray-200 dark:bg-gray-900/40 dark:text-gray-400 dark:border-gray-800",
};
const statusDotStyles: Record<string, string> = {
	success: "bg-chart-success",
	error: "bg-chart-error",
	processing: "bg-blue-500",
	cancelled: "bg-gray-400",
};

export function StatusPill({ status }: { status: Status }) {
	return (
		<span
			className={cn(
				"inline-flex items-center gap-1.5 rounded-sm border px-2 py-0.5 text-[11px] font-semibold uppercase",
				statusPillStyles[status] ?? statusPillStyles.cancelled,
			)}
		>
			<span className={cn("h-1.5 w-1.5 rounded-sm", statusDotStyles[status] ?? statusDotStyles.cancelled)} />
			{status}
		</span>
	);
}

// Colors an HTTP status code badge by response class.
export function statusCodeBadgeClass(code: number): string {
	if (code >= 200 && code < 300) return "border-chart-success/30 bg-chart-success/10 text-chart-success-ink";
	if (code >= 300 && code < 400)
		return "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950/40 dark:text-blue-400 dark:border-blue-900";
	if (code >= 400 && code < 500)
		return "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950/40 dark:text-amber-400 dark:border-amber-900";
	return "border-chart-error/30 bg-chart-error/10 text-chart-error-ink";
}

export function HeroStat({
	label,
	value,
	sub,
	mono = false,
	valueClass,
	hasRightBorder = false,
}: {
	label: string;
	value: ReactNode;
	sub?: ReactNode;
	mono?: boolean;
	valueClass?: string;
	hasRightBorder?: boolean;
}) {
	return (
		<div className={cn("border-border/70 border-b px-5 py-3 md:border-b-0", hasRightBorder && "md:border-r")}>
			<div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">{label}</div>
			<div className={cn("mt-0.5 truncate text-[18px] font-semibold tabular-nums", mono && "font-mono text-[15px]", valueClass)}>
				{value}
			</div>
			{sub ? <div className="text-muted-foreground mt-0.5 truncate text-[11px]">{sub}</div> : null}
		</div>
	);
}

export function CopyInlineButton({ text, testId }: { text: string; testId?: string }) {
	const { copy } = useCopyToClipboard({ successMessage: "Copied" });
	return (
		<button
			type="button"
			onClick={(e) => {
				e.stopPropagation();
				copy(text);
			}}
			className="text-muted-foreground hover:bg-muted hover:text-foreground inline-flex h-6 w-6 items-center justify-center rounded-sm transition"
			aria-label="Copy"
			data-testid={testId}
		>
			<Clipboard className="h-3.5 w-3.5" />
		</button>
	);
}

/** Labelled row under the hero badges: a small uppercase key and a mono value with a copy button. */
export function HeroIdRow({ label, value, testId, className }: { label: string; value: string; testId?: string; className?: string }) {
	return (
		<div className={cn("flex items-center gap-2", className)}>
			<div className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">{label}</div>
			<code className="text-foreground truncate font-mono text-[13px]">{value || "—"}</code>
			{value ? <CopyInlineButton text={value} testId={testId} /> : null}
		</div>
	);
}