"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderName, RequestTypeColors, RequestTypeLabels, Status, StatusColors } from "@/lib/constants/logs";
import { LogEntry } from "@/lib/types/logs";
import { ColumnDef } from "@tanstack/react-table";
import { ArrowUpDown } from "lucide-react";
import moment from "moment";

export const createColumns = (): ColumnDef<LogEntry>[] => [
	{
		accessorKey: "timestamp",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Time
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		cell: ({ row }) => {
			const timestamp = row.original.timestamp;
			return <div className="font-mono text-sm">{moment(timestamp).format("YYYY-MM-DD hh:mm:ss A (Z)")}</div>;
		},
	},
	{
		id: "request_type",
		header: "Type",
		cell: ({ row }) => {
			return (
				<Badge variant="outline" className={`${RequestTypeColors[row.original.object as keyof typeof RequestTypeColors]} text-xs`}>
					{RequestTypeLabels[row.original.object as keyof typeof RequestTypeLabels]}
				</Badge>
			);
		},
	},
	{
		accessorKey: "provider",
		header: "Provider",
		cell: ({ row }) => {
			const provider = row.original.provider as ProviderName;
			return (
				<Badge variant="secondary" className={`font-mono text-xs uppercase`}>
					<RenderProviderIcon provider={provider as ProviderIconType} size="sm" />
					{provider}
				</Badge>
			);
		},
	},
	{
		accessorKey: "model",
		header: "Model",
		cell: ({ row }) => <div className="max-w-[120px] truncate font-mono text-xs font-normal">{row.original.model}</div>,
	},
	{
		accessorKey: "latency",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Latency
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		cell: ({ row }) => {
			const latency = row.original.latency;
			return (
				<div className="pl-4 font-mono text-sm">{latency === undefined || latency === null ? "N/A" : `${latency.toLocaleString()}ms`}</div>
			);
		},
	},
	{
		accessorKey: "tokens",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Tokens
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		cell: ({ row }) => {
			const tokenUsage = row.original.token_usage;
			if (!tokenUsage) {
				return <div className="pl-4 font-mono text-sm">N/A</div>;
			}

			return (
				<div className="pl-4 text-sm">
					<div className="font-mono">
						{tokenUsage.total_tokens.toLocaleString()} ({tokenUsage.prompt_tokens}+{tokenUsage.completion_tokens})
					</div>
				</div>
			);
		},
	},
	{
		accessorKey: "cost",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Cost
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		cell: ({ row }) => {
			if (!row.original.cost) {
				return <div className="pl-4 font-mono text-xs">N/A</div>;
			}

			return (
				<div className="pl-4 text-xs">
					<div className="font-mono">{row.original.cost?.toFixed(4)}</div>
				</div>
			);
		},
	},
	{
		accessorKey: "status",
		header: "Status",
		cell: ({ row }) => {
			const status = row.original.status as Status;
			return (
				<Badge variant="secondary" className={`${StatusColors[status] ?? ""} font-mono text-xs uppercase`}>
					{status}
				</Badge>
			);
		},
	},
];
