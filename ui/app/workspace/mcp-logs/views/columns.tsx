"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Status, StatusBarColors, Statuses } from "@/lib/constants/logs";
import type { MCPToolLogEntry } from "@/lib/types/logs";
import { ColumnDef } from "@tanstack/react-table";
import { ArrowUpDown, Trash2 } from "lucide-react";
import moment from "moment";

// Helper function to validate status and return a safe Status value
const getValidatedStatus = (status: string): Status => {
	// Check if status is a valid Status by checking against Statuses array
	if (Statuses.includes(status as Status)) {
		return status as Status;
	}
	// Fallback to "processing" for unknown statuses
	return "processing";
};

export const createMCPColumns = (
	handleDelete: (log: MCPToolLogEntry) => Promise<void>,
	hasDeleteAccess: boolean,
): ColumnDef<MCPToolLogEntry>[] => [
	{
		accessorKey: "status",
		header: "",
		size: 8,
		maxSize: 8,
		cell: ({ row }) => {
			const status = getValidatedStatus(row.original.status);
			return <div className={`h-full min-h-[24px] w-1 rounded-sm ${StatusBarColors[status]}`} />;
		},
	},
	{
		accessorKey: "timestamp",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Time
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		size: 180,
		cell: ({ row }) => {
			const timestamp = row.original.timestamp;
			return <div className="text-xs">{moment(timestamp).format("YYYY-MM-DD hh:mm:ss A (Z)")}</div>;
		},
	},
	{
		accessorKey: "tool_name",
		header: "Tool Name",
		size: 300,
		cell: ({ row }) => {
			const toolName = row.getValue("tool_name") as string;
			return <span className="block max-w-full truncate font-mono text-sm">{toolName}</span>;
		},
	},
	{
		accessorKey: "server_label",
		header: "Server",
		size: 150,
		cell: ({ row }) => {
			const serverLabel = row.getValue("server_label") as string;
			return serverLabel ? (
				<Badge variant="secondary" className="font-mono">
					{serverLabel}
				</Badge>
			) : (
				<span className="text-muted-foreground">-</span>
			);
		},
	},
	{
		accessorKey: "latency",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Latency
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		size: 120,
		cell: ({ row }) => {
			const latency = row.original.latency;
			return (
				<div className="pl-4 font-mono text-sm">{latency === undefined || latency === null ? "N/A" : `${latency.toLocaleString()}ms`}</div>
			);
		},
	},
	{
		accessorKey: "cost",
		header: "Cost",
		size: 120,
		cell: ({ row }) => {
			const cost = row.original.cost;
			const isValidNumber = typeof cost === "number" && Number.isFinite(cost);
			return <div className="font-mono text-sm">{isValidNumber ? `${cost.toFixed(4)}` : "N/A"}</div>;
		},
	},
	{
		id: "actions",
		cell: ({ row }) => {
			const log = row.original;
			return (
				<Button variant="outline" size="icon" onClick={() => void handleDelete(log)} disabled={!hasDeleteAccess} aria-label="Delete log">
					<Trash2 />
				</Button>
			);
		},
	},
];
