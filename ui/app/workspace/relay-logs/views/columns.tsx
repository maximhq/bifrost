import { LogAppCell, LogCostCell, LogLatencyCell, LogTimeCell } from "@/app/workspace/logs/views/columns";
import { formatCostPrecise } from "@/app/workspace/relay-logs/views/relayLogDetailsSheet";
import { AttributionCell } from "@/components/logAttributionCell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { TruncatedLabel } from "@/components/ui/truncatedLabel";
import { Status, StatusBarColors, Statuses } from "@/lib/constants/logs";
import type { RelayLog } from "@/lib/types/relays";
import { formatCompactNumber } from "@/lib/utils/numbers";
import { ColumnDef, Row } from "@tanstack/react-table";
import { ArrowUpDown, MoreHorizontal, Trash2 } from "lucide-react";

const statusOf = (status: string): Status => (Statuses.includes(status as Status) ? (status as Status) : "processing");

export const RELAY_COLUMN_LABELS: Record<string, string> = {
	timestamp: "Time",
	relay_name: "Relay",
	method: "Method",
	request: "Path",
	status_code: "Status",
	app: "App",
	model: "Model",
	cost: "Cost",
	units: "Units",
	latency: "Latency",
	virtual_key: "Virtual Key",
	user: "User",
	team: "Team",
	customer: "Customer",
	business_unit: "Business Unit",
	project: "Project",
};

// Same sort header as the LLM table's Time / Latency / Cost columns.
const sortableHeader =
	(label: string, testId: string) =>
	({ column }: { column: { toggleSorting: (desc?: boolean) => void; getIsSorted: () => false | "asc" | "desc" } }) => (
		<Button variant="ghost" data-testid={testId} onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
			{label}
			<ArrowUpDown className="ml-2 h-4 w-4" />
		</Button>
	);

/**
 * Columns for the Relay Logs table. Every value that also appears in the LLM Logs table (time,
 * latency, cost, app, attribution) renders through that table's cell so the two read alike.
 */
export const createRelayColumns = (
	handleDelete: (log: RelayLog) => Promise<void>,
	hasDeleteAccess: boolean,
	customAppIcons: Record<string, string> = {},
): ColumnDef<RelayLog>[] => [
	{
		accessorKey: "status",
		header: "",
		size: 8,
		maxSize: 8,
		cell: ({ row }) => (
			<div title={row.original.status} className={`h-full min-h-[24px] w-1 rounded-sm ${StatusBarColors[statusOf(row.original.status)]}`} />
		),
	},
	{
		accessorKey: "timestamp",
		header: sortableHeader("Time", "relay-logs-time-sort-btn"),
		size: 150,
		cell: ({ row }) => <LogTimeCell timestamp={row.original.timestamp} testId="relay-logs-row-open-btn" />,
	},
	{
		accessorKey: "relay_name",
		header: "Relay",
		size: 150,
		cell: ({ row }) => (
			<Badge variant="outline" className="px-1.5 py-0.5 font-mono text-[11px]">
				{row.original.relay_name}
			</Badge>
		),
	},
	{
		// Its own column, as in the relay's route table, so the paths beside it line up.
		id: "method",
		header: "Method",
		size: 90,
		cell: ({ row }) => (
			<Badge variant="secondary" className="px-1.5 py-0.5 font-mono text-[11px]">
				{row.original.kind === "job" ? "JOB" : row.original.method}
			</Badge>
		),
	},
	{
		id: "request",
		header: "Path",
		size: 260,
		cell: ({ row }) => {
			// Same two-line shape as the LLM table's model cell: primary line in mono, detail beneath.
			// The query string is left to the detail sheet, where it is listed parameter by parameter;
			// here it would push the path out of view.
			const log = row.original;
			const pathOnly = log.path.split("?")[0];
			const primary = log.kind === "job" ? (log.job_id ?? "") : pathOnly;
			// A literal (non-glob) route's path is identical to the request's own path, so "Route X"
			// would just restate the primary line above it; show it only when it's new information
			// (a glob, or no match at all).
			const routeLabel = !log.route_path ? "No route" : log.route_path !== pathOnly ? `Route ${log.route_path}` : null;
			// "Streamed" is not repeated here as plain text: it's already a colored badge on the
			// detail sheet, the same way LLM's streaming variants get a distinct badge color/label
			// rather than a text suffix, so a bare "streamed" string in this muted line read as out
			// of place next to that pattern.
			const secondary =
				log.kind === "job"
					? `Settled: ${log.job_status ?? "-"}`
					: [routeLabel, log.billable ? "" : "not billable"].filter(Boolean).join(" · ");
			return (
				<div className="flex min-w-0 flex-col leading-tight">
					<TruncatedLabel truncateFrom="start" className="font-mono text-[12px]">
						{primary}
					</TruncatedLabel>
					<span className="text-muted-foreground truncate text-[10.5px]">{secondary}</span>
				</div>
			);
		},
	},
	{
		id: "app",
		accessorKey: "app",
		header: "App",
		size: 140,
		cell: ({ row }) => <LogAppCell app={row.original.app} userAgent={row.original.user_agent} customAppIcons={customAppIcons} />,
	},
	{
		id: "model",
		accessorKey: "model",
		header: "Model",
		size: 180,
		cell: ({ row }) =>
			row.original.model ? (
				<TruncatedLabel truncateFrom="start" className="font-mono text-[12px]">
					{row.original.model}
				</TruncatedLabel>
			) : (
				<div className="font-mono text-xs">-</div>
			),
	},
	{
		accessorKey: "latency",
		header: sortableHeader("Latency", "relay-logs-latency-sort-btn"),
		size: 170,
		cell: ({ row }) => <LogLatencyCell latency={row.original.latency} />,
	},
	{
		id: "units",
		header: "Units",
		size: 120,
		cell: ({ row }) => {
			// Same numeric style as the LLM table's token count.
			const units = row.original.billed_units;
			if (units == null) return <div className="pl-4 font-mono text-xs">N/A</div>;
			return (
				<div className="flex items-center pl-4 leading-tight">
					<span className="font-mono text-[12px] tabular-nums">{formatCompactNumber(units)}</span>
				</div>
			);
		},
	},
	{
		accessorKey: "cost",
		header: sortableHeader("Cost", "relay-logs-cost-sort-btn"),
		size: 120,
		// Relay charges are often sub-cent, so the column keeps the sheet's six-decimal precision.
		cell: ({ row }) => <LogCostCell cost={row.original.cost} format={formatCostPrecise} />,
	},
	{
		id: "virtual_key",
		header: "Virtual Key",
		size: 170,
		cell: ({ row }) => <AttributionCell name={row.original.virtual_key_name} id={row.original.virtual_key_id} />,
	},
	{ id: "team", header: "Team", size: 150, cell: ({ row }) => <AttributionCell name={row.original.team_name} id={row.original.team_id} /> },
	{
		id: "customer",
		header: "Customer",
		size: 150,
		cell: ({ row }) => <AttributionCell name={row.original.customer_name} id={row.original.customer_id} />,
	},
	{ id: "user", header: "User", size: 150, cell: ({ row }) => <AttributionCell name={row.original.user_name} id={row.original.user_id} /> },
	{
		id: "business_unit",
		header: "Business Unit",
		size: 150,
		cell: ({ row }) => <AttributionCell name={row.original.business_unit_name} id={row.original.business_unit_id} />,
	},
	{
		id: "project",
		header: "Project",
		size: 150,
		cell: ({ row }) => <AttributionCell name={row.original.project_name} id={row.original.project_id} />,
	},
	...(hasDeleteAccess
		? [
				{
					id: "actions",
					header: "",
					size: 56,
					cell: ({ row }: { row: Row<RelayLog> }) => (
						<div className="flex justify-center">
							<DropdownMenu>
								<DropdownMenuTrigger asChild onClick={(event) => event.stopPropagation()}>
									<Button variant="ghost" size="icon" data-testid="relay-log-actions-btn" aria-label="Log actions" className="h-7 w-7">
										<MoreHorizontal className="h-4 w-4" />
									</Button>
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end">
									<DropdownMenuItem
										variant="destructive"
										className="cursor-pointer"
										data-testid="relay-log-delete-btn"
										onClick={(event) => {
											event.stopPropagation();
											handleDelete(row.original).catch(() => undefined); // the page shows the error
										}}
									>
										<Trash2 className="h-4 w-4" />
										Delete
									</DropdownMenuItem>
								</DropdownMenuContent>
							</DropdownMenu>
						</div>
					),
				},
			]
		: []),
];