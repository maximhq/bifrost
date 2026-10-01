import { PluginLogEntry } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { LOG_LEVEL_BADGE_CLASSES, meetsMinLogLevel, type LogLevel } from "@/lib/utils/logLevel";
import { format } from "date-fns";
import { ChevronDown, ChevronRight } from "lucide-react";
import { useMemo, useState } from "react";
import LogLevelTabs from "./logLevelTabs";
import { parsePluginLogs } from "./pluginLogsView.utils";

interface PluginLogsViewProps {
	pluginLogs: string;
}

function formatPluginName(name: string): string {
	return name
		.split(/[-_\s]+/)
		.filter(Boolean)
		.map((word) => word.charAt(0).toUpperCase() + word.slice(1))
		.join(" ");
}

export default function PluginLogsView({ pluginLogs }: PluginLogsViewProps) {
	const [minLevel, setMinLevel] = useState<LogLevel>("debug");
	const parsed = useMemo(() => parsePluginLogs(pluginLogs), [pluginLogs]);

	if (!parsed) return null;

	return (
		<div>
			<div className="flex items-center justify-between gap-3 py-3">
				<div className="text-sm font-semibold">Plugin Logs</div>
				<LogLevelTabs value={minLevel} onChange={setMinLevel} testId="plugin-logs-level-filter" />
			</div>
			<div className="flex flex-col gap-2 pb-3">
				{Object.keys(parsed).map((name) => (
					<PluginSection key={name} name={name} entries={parsed[name]} minLevel={minLevel} />
				))}
			</div>
		</div>
	);
}

function PluginSection({ name, entries, minLevel }: { name: string; entries: PluginLogEntry[]; minLevel: LogLevel }) {
	const [isOpen, setIsOpen] = useState(false);
	const visible = useMemo(
		() => entries.filter((entry) => meetsMinLogLevel(entry.level, minLevel)).sort((a, b) => a.timestamp - b.timestamp),
		[entries, minLevel],
	);
	// At the debug floor nothing is hidden, so the plain total reads better than "N of N".
	const count = minLevel === "debug" ? `(${entries.length})` : `(${visible.length} of ${entries.length})`;

	return (
		<div className="rounded-md border">
			<button
				type="button"
				data-testid={`plugin-logs-toggle-${name
					.toLowerCase()
					.replace(/[^a-z0-9]+/g, "-")
					.replace(/(^-|-$)/g, "")}`}
				onClick={() => setIsOpen(!isOpen)}
				className="hover:bg-muted/50 flex w-full items-center gap-2 px-4 py-2 text-left text-sm"
			>
				{isOpen ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
				<span className="font-medium">{formatPluginName(name)}</span>
				<span className="text-muted-foreground text-xs tabular-nums">{count}</span>
			</button>
			{isOpen && (
				<div className="custom-scrollbar max-h-[300px] overflow-y-auto border-t">
					{visible.length === 0 ? (
						<div className="text-muted-foreground px-4 py-2 text-xs">No entries at or above {minLevel}.</div>
					) : (
						visible.map((entry, idx) => (
							<div key={idx} className="flex items-start gap-3 border-b px-4 py-1.5 font-mono text-xs last:border-b-0">
								<span className="text-muted-foreground shrink-0">{format(new Date(entry.timestamp), "HH:mm:ss.SSS")}</span>
								<span
									className={cn(
										"shrink-0 rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase",
										LOG_LEVEL_BADGE_CLASSES[entry.level] || LOG_LEVEL_BADGE_CLASSES.info,
									)}
								>
									{entry.level}
								</span>
								<span className="break-words whitespace-pre-wrap">{entry.message}</span>
							</div>
						))
					)}
				</div>
			)}
		</div>
	);
}