import type { RunStatus } from "@/lib/types/evalRuns";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

const statusColor: Record<RunStatus, string> = {
	queued: "text-muted-foreground",
	running: "text-blue-700 dark:text-blue-400",
	completed: "text-emerald-700 dark:text-emerald-400",
	failed: "text-red-700 dark:text-red-400",
	stopped: "text-muted-foreground",
};

// RunStatusLabel is a run's status as a coloured dot and a word. The word carries the
// meaning; the colour only repeats it.
export function RunStatusLabel({ status, className }: { status: RunStatus; className?: string }) {
	const { t } = useTranslation("config");
	return (
		<span
			className={cn("inline-flex items-center gap-1.5 font-medium", statusColor[status], className)}
			data-testid={`run-status-${status}`}
		>
			<span className={cn("h-1.5 w-1.5 rounded-full bg-current", status === "running" && "animate-pulse")} />
			{t(`evals.runs.status.${status}`)}
		</span>
	);
}