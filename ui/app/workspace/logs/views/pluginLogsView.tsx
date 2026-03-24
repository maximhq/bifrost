"use client";

import { PluginLogEntry } from "@/lib/types/logs";
import { ChevronDown, ChevronRight } from "lucide-react";
import moment from "moment";
import { useState } from "react";

const levelColors: Record<string, string> = {
	debug: "bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300",
	info: "bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300",
	warn: "bg-amber-100 text-amber-700 dark:bg-amber-900 dark:text-amber-300",
	error: "bg-red-100 text-red-700 dark:bg-red-900 dark:text-red-300",
};

interface PluginLogsViewProps {
	pluginLogs: string;
}

export default function PluginLogsView({ pluginLogs }: PluginLogsViewProps) {
	let parsed: Record<string, PluginLogEntry[]>;
	try {
		const raw: unknown = JSON.parse(pluginLogs);
		if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
		parsed = Object.fromEntries(Object.entries(raw as Record<string, unknown>).filter(([, value]) => Array.isArray(value))) as Record<
			string,
			PluginLogEntry[]
		>;
	} catch {
		return null;
	}

	const pluginNames = Object.keys(parsed);
	if (pluginNames.length === 0) return null;

	return (
		<div>
			<div className="py-3 text-sm font-semibold">Plugin Logs</div>
			<div className="flex flex-col gap-2 pb-3">
				{pluginNames.map((name) => (
					<PluginSection key={name} name={name} entries={parsed[name]} />
				))}
			</div>
		</div>
	);
}

function PluginSection({ name, entries }: { name: string; entries: PluginLogEntry[] }) {
	const [isOpen, setIsOpen] = useState(false);
	const sorted = [...entries].sort((a, b) => a.timestamp - b.timestamp);

	return (
		<div className="rounded-md border">
			<button data-testid={`plugin-logs-toggle-${name}`} onClick={() => setIsOpen(!isOpen)} className="hover:bg-muted/50 flex w-full items-center gap-2 px-4 py-2 text-left text-sm">
				{isOpen ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
				<span className="font-medium">{name}</span>
				<span className="text-muted-foreground text-xs">({entries.length})</span>
			</button>
			{isOpen && (
				<div className="custom-scrollbar max-h-[300px] overflow-y-auto border-t">
					{sorted.map((entry, idx) => (
						<div key={idx} className="flex items-start gap-3 border-b px-4 py-1.5 font-mono text-xs last:border-b-0">
							<span className="text-muted-foreground shrink-0">{moment(entry.timestamp).format("HH:mm:ss.SSS")}</span>
							<span
								className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase ${levelColors[entry.level] || levelColors.info}`}
							>
								{entry.level}
							</span>
							<span className="break-words whitespace-pre-wrap">{entry.message}</span>
						</div>
					))}
				</div>
			)}
		</div>
	);
}
