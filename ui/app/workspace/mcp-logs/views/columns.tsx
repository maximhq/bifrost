import { getMCPLogPresentation, getMCPArgumentPreview } from "@/lib/utils/mcpLogPresentation";
import { AttributionCell } from "@/components/logAttributionCell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { mapAppToClientApp, mapUserAgentToApp, Status, StatusBarColors, Statuses } from "@/lib/constants/logs";
import type { MCPToolLogEntry } from "@/lib/types/logs";
import { ColumnDef, Row } from "@tanstack/react-table";
import { format, formatDistanceToNow, isValid } from "date-fns";
import { ArrowUpDown, MoreHorizontal, Trash2 } from "lucide-react";

// Helper function to validate status and return a safe Status value
const getValidatedStatus = (status: string): Status => {
	// Check if status is a valid Status by checking against Statuses array
	if (Statuses.includes(status as Status)) {
		return status as Status;
	}
	if (status === "unknown") return "cancelled";
	// Fallback to "processing" for unknown statuses
	return "processing";
};

export const createMCPColumns = (
	handleDelete: (log: MCPToolLogEntry) => Promise<void>,
	hasDeleteAccess: boolean,
	customAppIcons: Record<string, string> = {},
): ColumnDef<MCPToolLogEntry>[] => [
	{
		accessorKey: "status",
		header: "",
		size: 8,
		maxSize: 8,
		cell: ({ row }) => {
			const status = getValidatedStatus(row.original.status);
			const presentation = getMCPLogPresentation(row.original);
			return (
				<div
					title={presentation.label}
					aria-label={presentation.label}
					className={`h-full min-h-[24px] w-1 rounded-sm ${presentation.approved ? "bg-blue-500" : StatusBarColors[status]}`}
				/>
			);
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
		accessorKey: "tool_name",
		header: "Tool Name",
		size: 300,
		cell: ({ row }) => {
			const toolName = row.getValue("tool_name") as string;
			const presentation = getMCPLogPresentation(row.original);
			const preview = getMCPArgumentPreview(row.original);
			return (
				<div className="min-w-0 space-y-1 py-1">
					<span className="block truncate font-mono text-sm">{toolName}</span>
					{preview && (
						<span className="text-muted-foreground block truncate text-xs" title={preview}>
							{preview}
						</span>
					)}
					<span
						title={presentation.description}
						className={`block text-xs ${presentation.approved ? "text-blue-600 dark:text-blue-400" : "text-muted-foreground"}`}
					>
						{presentation.label}
					</span>
				</div>
			);
		},
	},
	{
		accessorKey: "source",
		header: "Source",
		size: 90,
		cell: ({ row }) => <Badge variant="secondary">{row.original.source === "native" ? "Native" : "MCP"}</Badge>,
	},
	{
		accessorKey: "server_label",
		header: "Server",
		size: 150,
		cell: ({ row }) => {
			const serverLabel = row.original.source === "native" ? "Local" : (row.getValue("server_label") as string);
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
		id: "app",
		accessorKey: "app",
		header: "App",
		size: 140,
		cell: ({ row }) => {
			const appKey = row.original.app || row.original.app_key;
			const app = appKey ? mapAppToClientApp(appKey) : mapUserAgentToApp(row.original.user_agent);
			const icon = appKey ? customAppIcons[appKey] || app.icon : app.icon;
			return (
				<div className="flex min-w-0 items-center gap-2" title={row.original.user_agent || undefined}>
					{icon ? <img src={icon} alt={app.name} width={20} height={20} loading="lazy" decoding="async" className="shrink-0" /> : null}
					<span className="truncate text-[12px]">{app.name}</span>
				</div>
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
			const presentation = getMCPLogPresentation(row.original);
			const latency = presentation.policy ? presentation.inspectionDuration : (row.original.latency ?? presentation.observedDuration);
			return (
				<div className="pl-4 text-sm" title={presentation.description}>
					<span className="font-mono">
						{latency != null
							? `${latency.toLocaleString()}ms`
							: presentation.inspectionDuration != null
								? `${presentation.inspectionDuration}ms`
								: "Not recorded"}
					</span>
					<span className="text-muted-foreground block text-xs">{presentation.durationLabel}</span>
				</div>
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
		id: "virtual_key",
		header: "Virtual Key",
		size: 170,
		cell: ({ row }) => {
			const value = row.original.virtual_key?.name ?? row.original.virtual_key_name ?? row.original.virtual_key_id;
			return <div className="max-w-[180px] truncate font-mono text-xs">{value || "-"}</div>;
		},
	},
	{ id: "user", header: "User", size: 150, cell: ({ row }) => <AttributionCell name={row.original.user_name} id={row.original.user_id} /> },
	{
		id: "team",
		header: "Team",
		size: 150,
		cell: ({ row }) => (
			<AttributionCell
				names={row.original.team_names}
				name={row.original.team_name}
				ids={row.original.team_ids}
				id={row.original.team_id}
			/>
		),
	},
	{
		id: "customer",
		header: "Customer",
		size: 150,
		cell: ({ row }) => (
			<AttributionCell
				names={row.original.customer_names}
				name={row.original.customer_name}
				ids={row.original.customer_ids}
				id={row.original.customer_id}
			/>
		),
	},
	{
		id: "business_unit",
		header: "Business Unit",
		size: 150,
		cell: ({ row }) => (
			<AttributionCell
				names={row.original.business_unit_names}
				name={row.original.business_unit_name}
				ids={row.original.business_unit_ids}
				id={row.original.business_unit_id}
			/>
		),
	},
	{
		id: "project",
		header: "Project",
		size: 150,
		cell: ({ row }) => <AttributionCell name={row.original.project_name} id={row.original.project_id} />,
	},
	{ id: "device", header: "Device", size: 150, cell: ({ row }) => <AttributionCell name={undefined} id={row.original.device_id} /> },
	...(hasDeleteAccess
		? [
				{
					id: "actions",
					header: "",
					size: 56,
					cell: ({ row }: { row: Row<MCPToolLogEntry> }) => {
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