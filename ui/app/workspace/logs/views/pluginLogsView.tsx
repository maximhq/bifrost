import { Puzzle } from "lucide-react";
import moment from "moment";
import CollapsibleBox from "./collapsibleBox";

interface PluginLogEntry {
	level: string;
	message: string;
	timestamp: number;
}

const levelStyles: Record<string, string> = {
	error: "border-red-300 text-red-700 dark:border-red-700 dark:text-red-400",
	warn: "border-yellow-300 text-yellow-700 dark:border-yellow-700 dark:text-yellow-400",
	debug: "border-gray-300 text-gray-500 dark:border-gray-600 dark:text-gray-400",
	info: "border-blue-300 text-blue-700 dark:border-blue-700 dark:text-blue-400",
};

interface PluginLogEntriesViewProps {
	pluginName: string;
	logs: PluginLogEntry[];
}

export function PluginLogEntriesView({ pluginName, logs }: PluginLogEntriesViewProps) {
	return (
		<div data-testid={`plugin-log-entry-${pluginName}`} className="flex flex-col gap-1">
			<div className="mb-1 flex items-center gap-2 text-sm font-semibold">
				<Puzzle className="h-4 w-4" />
				{pluginName}
			</div>
			<div className="space-y-0.5 font-mono text-xs">
				{logs.map((log, i) => (
					<div key={i} className="flex items-center justify-start gap-2">
						<div className="text-muted-foreground shrink-0 pt-0.5 text-[10px]">{moment(log.timestamp).format("YYYY-MM-DD HH:mm:ss")}</div>
						<div className={`shrink-0 pt-0.5 text-[10px] uppercase ${levelStyles[log.level] || levelStyles.info}`}>{log.level}</div>
						<div className="text-sm break-words whitespace-pre-wrap">{log.message}</div>
					</div>
				))}
			</div>
		</div>
	);
}

interface PluginLogsViewProps {
	pluginLogs: string;
}

export function PluginLogsView({ pluginLogs }: PluginLogsViewProps) {
	let parsed: Record<string, PluginLogEntry[]>;
	try {
		const raw = JSON.parse(pluginLogs);
		if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
		parsed = Object.fromEntries(
			Object.entries(raw).filter(([, value]) => Array.isArray(value))
		) as Record<string, PluginLogEntry[]>;
	} catch {
		return null;
	}

	if (Object.keys(parsed).length === 0) return null;

	return (
		<div data-testid="plugin-logs-container">
			<CollapsibleBox title="Plugin Logs" onCopy={() => pluginLogs}>
				<div data-testid="plugin-logs-content" className="custom-scrollbar max-h-[400px] space-y-3 overflow-y-auto px-6 py-3">
					{Object.entries(parsed).map(([pluginName, logs]) => (
						<PluginLogEntriesView key={pluginName} pluginName={pluginName} logs={logs} />
					))}
				</div>
			</CollapsibleBox>
		</div>
	);
}
