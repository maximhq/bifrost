import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { StatusBarColors } from "@/lib/constants/logs";
import type { AgentLogStatus, AgentLogSummary } from "@/lib/types/agentLogs";
import type { ColumnDef, Row } from "@tanstack/react-table";
import { format, formatDistanceToNow, isValid } from "date-fns";
import { ArrowUpDown, MoreHorizontal, Trash2 } from "lucide-react";

// Coerce an unknown status string into one of the three states the backend
// writes, so a surprising value still renders instead of breaking the row.
const getValidatedStatus = (status: string): AgentLogStatus => {
	if (status === "success" || status === "error" || status === "processing") return status;
	return "processing";
};

// Compact rendering of a long correlation id: enough prefix to recognize it,
// full value on hover.
function shortId(id: string | undefined): string {
	if (!id) return "—";
	return id.length > 12 ? `${id.slice(0, 12)}…` : id;
}

export const AGENT_COLUMN_LABELS: Record<string, string> = {
	timestamp: "Time",
	agent_name: "Agent",
	operation: "Operation",
	input: "Message",
	context: "Context",
	task: "Task",
	duration: "Duration",
};

function collectTextParts(value: unknown, result: string[]): void {
	if (typeof value === "string") {
		if (value.trim()) result.push(value.trim());
		return;
	}
	if (Array.isArray(value)) {
		value.forEach((item) => collectTextParts(item, result));
		return;
	}
	if (!value || typeof value !== "object") return;
	const record = value as Record<string, unknown>;
	if (typeof record.text === "string") {
		collectTextParts(record.text, result);
		return;
	}
	if ("parts" in record) collectTextParts(record.parts, result);
}

function getAgentInputPreview(input?: string): string {
	if (!input) return "—";
	try {
		const parsed: unknown = JSON.parse(input);
		if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
			const record = parsed as Record<string, unknown>;
			const text: string[] = [];
			collectTextParts(record.message, text);
			if (text.length > 0) return text.join(" ");
			return Object.entries(record)
				.filter(([key]) => !["message", "metadata", "extensions"].includes(key))
				.map(([key, value]) => `${key}: ${typeof value === "string" ? value : JSON.stringify(value)}`)
				.join(" · ");
		}
	} catch {
		// The bounded preview can be truncated mid-JSON; show it as stored.
	}
	return input;
}

export const createAgentColumns = (handleDelete?: (log: AgentLogSummary) => Promise<void>): ColumnDef<AgentLogSummary>[] => [
	{
		accessorKey: "status",
		header: "",
		size: 8,
		maxSize: 8,
		cell: ({ row }) => {
			const status = getValidatedStatus(row.original.status);
			return <div title={status} aria-label={status} className={`h-full min-h-[24px] w-1 rounded-sm ${StatusBarColors[status]}`} />;
		},
	},
	{
		accessorKey: "timestamp",
		header: ({ column }) => (
			<Button variant="ghost" className="-ml-3 h-8 px-2" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Time
				<ArrowUpDown className="ml-2 size-3.5" />
			</Button>
		),
		size: 130,
		cell: ({ row }) => {
			const timestamp = row.original.timestamp;
			const date = timestamp ? new Date(timestamp) : null;
			if (!date || !isValid(date)) {
				return <div className="truncate text-xs">N/A</div>;
			}
			return (
				<div className="flex flex-col leading-tight">
					<span className="font-mono text-xs tabular-nums">{format(date, "MMM dd  HH:mm:ss")}</span>
					<span className="text-muted-foreground text-[10.5px] tabular-nums">{formatDistanceToNow(date, { addSuffix: true })}</span>
				</div>
			);
		},
	},
	{
		accessorKey: "agent_name",
		header: "Agent",
		size: 160,
		cell: ({ row }) => (
			<Badge variant="secondary" className="max-w-full font-mono">
				<span className="truncate">{row.original.agent_name}</span>
			</Badge>
		),
	},
	{
		accessorKey: "operation",
		header: "Operation",
		size: 220,
		cell: ({ row }) => (
			<div className="min-w-0 space-y-0.5 py-1">
				<span className="block truncate font-mono text-sm">{row.original.operation || "—"}</span>
				{row.original.task_state && <span className="text-muted-foreground block truncate text-xs">{row.original.task_state}</span>}
			</div>
		),
	},
	{
		accessorKey: "input",
		header: "Message",
		size: 350,
		cell: ({ row }) => {
			const preview = getAgentInputPreview(row.original.input);
			return (
				<div className="truncate font-mono text-[12px] font-normal" title={preview}>
					{preview}
				</div>
			);
		},
	},
	{
		id: "context",
		header: "Context",
		size: 130,
		cell: ({ row }) => (
			<span className="font-mono text-xs" title={row.original.context_id}>
				{shortId(row.original.context_id)}
			</span>
		),
	},
	{
		id: "task",
		header: "Task",
		size: 130,
		cell: ({ row }) => (
			<span className="font-mono text-xs" title={row.original.task_id}>
				{shortId(row.original.task_id)}
			</span>
		),
	},
	{
		id: "duration",
		accessorKey: "latency",
		header: ({ column }) => (
			<Button variant="ghost" className="-ml-3 h-8 px-2" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Duration
				<ArrowUpDown className="ml-2 size-3.5" />
			</Button>
		),
		size: 100,
		cell: ({ row }) => {
			const latency = row.original.latency;
			return <span className="font-mono text-sm">{latency != null ? `${latency.toLocaleString()}ms` : "—"}</span>;
		},
	},
	...(handleDelete
		? [
				{
					id: "actions",
					header: "",
					size: 56,
					cell: ({ row }: { row: Row<AgentLogSummary> }) => {
						const log = row.original;
						return (
							<div className="flex justify-center">
								<DropdownMenu>
									<DropdownMenuTrigger asChild onClick={(event) => event.stopPropagation()}>
										<Button variant="ghost" size="icon" data-testid="log-actions-btn" aria-label="Log actions" className="h-7 w-7">
											<MoreHorizontal className="h-4 w-4" />
										</Button>
									</DropdownMenuTrigger>
									<DropdownMenuContent align="end">
										<DropdownMenuItem
											variant="destructive"
											className="cursor-pointer"
											data-testid="log-delete-btn"
											onClick={(event) => {
												event.stopPropagation();
												void handleDelete(log);
											}}
										>
											<Trash2 className="h-4 w-4" />
											Delete
										</DropdownMenuItem>
									</DropdownMenuContent>
								</DropdownMenu>
							</div>
						);
					},
				},
			]
		: []),
];