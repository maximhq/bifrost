"use client";

import { useState, useEffect, useCallback, useMemo } from "react";
import Header from "@/components/header";
import { LogsDataTable } from "@/components/logs/logs-table";
import { LogDetailSheet } from "@/components/logs/log-detail-sheet";
import { createColumns } from "@/components/logs/columns";
import type { LogEntry, LogFilters, Pagination, BifrostMessage, MessageContent, ContentBlock, LogStats } from "@/lib/types/logs";
import { apiService } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AlertCircle, BarChart, CheckCircle, Clock, Hash } from "lucide-react";
import { useWebSocket } from "@/hooks/useWebSocket";

export default function LogsPage() {
	const [logs, setLogs] = useState<LogEntry[]>([]);
	const [totalItems, setTotalItems] = useState(0);
	const [stats, setStats] = useState<LogStats | null>(null);
	const [loading, setLoading] = useState(true);
	const [error, setError] = useState<string | null>(null);

	const [selectedLog, setSelectedLog] = useState<LogEntry | null>(null);

	const [filters, setFilters] = useState<LogFilters>({
		providers: [],
		models: [],
		status: [],
		content_search: "",
	});
	const [pagination, setPagination] = useState<Pagination>({
		limit: 50,
		offset: 0,
		sort_by: "timestamp",
		order: "desc",
	});

	const handleNewLog = useCallback(
		(log: LogEntry) => {
			// Only prepend the new log if we're on the first page and sorted by timestamp desc
			if (pagination.offset === 0 && pagination.sort_by === "timestamp" && pagination.order === "desc") {
				setLogs((prevLogs: LogEntry[]) => {
					// Check if the log matches current filters
					if (!matchesFilters(log, filters)) {
						return prevLogs;
					}

					// Remove the last log if we're at the page limit
					const updatedLogs = [log, ...prevLogs];
					if (updatedLogs.length > pagination.limit) {
						updatedLogs.pop();
					}
					return updatedLogs;
				});
				setTotalItems((prev: number) => prev + 1);

				// Update stats
				setStats((prevStats) => {
					if (!prevStats) return prevStats;

					const newStats = { ...prevStats };
					newStats.total_requests += 1;

					// Update success rate
					const successCount = (prevStats.success_rate / 100) * prevStats.total_requests;
					const newSuccessCount = log.status === "success" ? successCount + 1 : successCount;
					newStats.success_rate = (newSuccessCount / newStats.total_requests) * 100;

					// Update average latency
					if (log.latency) {
						const totalLatency = prevStats.average_latency * prevStats.total_requests;
						newStats.average_latency = (totalLatency + log.latency) / newStats.total_requests;
					}

					// Update total tokens
					if (log.token_usage) {
						newStats.total_tokens += log.token_usage.total_tokens;
					}

					return newStats;
				});
			}
		},
		[pagination.offset, pagination.sort_by, pagination.order, pagination.limit, filters],
	);

	const { ws, isConnected: isSocketConnected } = useWebSocket({ onMessage: handleNewLog });

	const fetchLogs = useCallback(async () => {
		setLoading(true);
		setError(null);

		try {
			const [response, err] = await apiService.getLogs(filters, pagination);

			if (err) {
				setError(err);
				setLogs([]);
				setTotalItems(0);
			} else if (response) {
				setLogs(response.logs || []);
				setTotalItems(response.stats.total_requests);
				setStats(response.stats);
			}
		} catch {
			setError("Failed to fetch logs. Please try again.");
			setLogs([]);
			setTotalItems(0);
		} finally {
			setLoading(false);
		}
	}, [filters, pagination]);

	// Fetch logs when filters or pagination change
	useEffect(() => {
		fetchLogs();
	}, [fetchLogs]);

	const getMessageText = (content: MessageContent): string => {
		if (typeof content === "string") {
			return content;
		}
		if (Array.isArray(content)) {
			return content.reduce((acc: string, block: ContentBlock) => {
				if (block.type === "text" && block.text) {
					return acc + block.text;
				}
				return acc;
			}, "");
		}
		return "";
	};

	// Helper function to check if a log matches the current filters
	const matchesFilters = (log: LogEntry, filters: LogFilters): boolean => {
		if (filters.providers?.length && !filters.providers.includes(log.provider)) {
			return false;
		}
		if (filters.models?.length && !filters.models.includes(log.model)) {
			return false;
		}
		if (filters.status?.length && !filters.status.includes(log.status)) {
			return false;
		}
		if (filters.start_time && new Date(log.timestamp) < new Date(filters.start_time)) {
			return false;
		}
		if (filters.end_time && new Date(log.timestamp) > new Date(filters.end_time)) {
			return false;
		}
		if (filters.min_latency && (!log.latency || log.latency < filters.min_latency)) {
			return false;
		}
		if (filters.max_latency && (!log.latency || log.latency > filters.max_latency)) {
			return false;
		}
		if (filters.min_tokens && (!log.token_usage || log.token_usage.total_tokens < filters.min_tokens)) {
			return false;
		}
		if (filters.max_tokens && (!log.token_usage || log.token_usage.total_tokens > filters.max_tokens)) {
			return false;
		}
		if (filters.content_search) {
			const search = filters.content_search.toLowerCase();
			const content = [
				...(log.input_history || []).map((msg: BifrostMessage) => getMessageText(msg.content)),
				log.output_message ? getMessageText(log.output_message.content) : "",
			]
				.join(" ")
				.toLowerCase();

			if (!content.includes(search)) {
				return false;
			}
		}
		return true;
	};

	const statCards = useMemo(
		() => [
			{
				title: "Total Requests",
				value: stats?.total_requests.toLocaleString() || "-",
				icon: <BarChart className="size-4" />,
			},
			{
				title: "Success Rate",
				value: stats ? `${stats.success_rate.toFixed(2)}%` : "-",
				icon: <CheckCircle className="size-4" />,
			},
			{
				title: "Avg Latency",
				value: stats ? `${stats.average_latency.toFixed(2)}ms` : "-",
				icon: <Clock className="size-4" />,
			},
			{
				title: "Total Tokens",
				value: stats?.total_tokens.toLocaleString() || "-",
				icon: <Hash className="size-4" />,
			},
		],
		[stats],
	);

	const columns = createColumns();

	return (
		<div className="bg-background">
			<Header title="Request Logs" />
			<div className="space-y-6">
				<div>
					<h1 className="mb-2 text-3xl font-bold">Request Logs</h1>
					<p className="text-muted-foreground">Monitor and analyze all API requests and responses</p>
				</div>

				<div className="space-y-6">
					{/* Quick Stats */}
					<div className="grid grid-cols-1 gap-4 md:grid-cols-4">
						{statCards.map((card) => (
							<Card key={card.title} className="py-4">
								<CardContent className="flex items-center justify-between px-4">
									<div>
										<div className="text-muted-foreground text-xs">{card.title}</div>
										<div className="text-2xl font-bold">{card.value}</div>
									</div>
								</CardContent>
							</Card>
						))}
					</div>

					{/* Error Alert */}
					{error && (
						<Alert variant="destructive">
							<AlertCircle className="h-4 w-4" />
							<AlertDescription>{error}</AlertDescription>
						</Alert>
					)}

					<LogsDataTable
						columns={columns}
						data={logs}
						totalItems={totalItems}
						loading={loading}
						filters={filters}
						pagination={pagination}
						onFiltersChange={setFilters}
						onPaginationChange={setPagination}
						onRowClick={setSelectedLog}
						isSocketConnected={isSocketConnected}
					/>
				</div>

				{/* Log Detail Sheet */}
				<LogDetailSheet log={selectedLog} open={selectedLog !== null} onOpenChange={(open) => !open && setSelectedLog(null)} />
			</div>
		</div>
	);
}
