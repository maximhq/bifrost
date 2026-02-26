"use client";

import { useGetDevGoroutinesQuery, useGetDevPoolStatsQuery, useGetDevPprofQuery } from "@/lib/store";
import type { AllocationInfo, GoroutineGroup, PoolStats } from "@/lib/store/apis/devApi";
import {
	Activity,
	AlertTriangle,
	ArrowDown,
	ArrowUp,
	ChevronDown,
	ChevronRight,
	Cpu,
	EyeOff,
	HardDrive,
	Layers,
	RefreshCw,
	RotateCcw,
	TrendingUp,
} from "lucide-react";
import React, { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";

// ============================================================================
// Utility Functions
// ============================================================================

function formatBytes(bytes: number): string {
	if (bytes === 0) return "0 B";
	const k = 1024;
	const sizes = ["B", "KB", "MB", "GB", "TB"];
	const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
	return `${(bytes / Math.pow(k, i)).toFixed(2)} ${sizes[i]}`;
}

function formatNs(ns: number): string {
	if (ns < 1000) return `${ns}ns`;
	if (ns < 1000000) return `${(ns / 1000).toFixed(2)}µs`;
	if (ns < 1000000000) return `${(ns / 1000000).toFixed(2)}ms`;
	return `${(ns / 1000000000).toFixed(3)}s`;
}

function formatTime(timestamp: string): string {
	const date = new Date(timestamp);
	return date.toLocaleTimeString("en-US", {
		hour12: false,
		hour: "2-digit",
		minute: "2-digit",
		second: "2-digit",
	});
}

function getCategoryColor(category: string): string {
	switch (category) {
		case "per-request":
			return "text-amber-400 bg-amber-400/10 border-amber-400/20";
		case "background":
			return "text-blue-400 bg-blue-400/10 border-blue-400/20";
		default:
			return "text-zinc-400 bg-zinc-400/10 border-zinc-400/20";
	}
}

function getStackFilePath(stack: string[]): string {
	for (const line of stack) {
		const match = line.match(/^\s*([^\s]+\.go):\d+/);
		if (match) {
			return match[1];
		}
	}
	return "";
}

function getGoroutineId(g: GoroutineGroup): string {
	const filePath = getStackFilePath(g.stack);
	return `${g.top_func}::${g.state}::${g.category}::${g.count}::${g.wait_minutes ?? 0}::${g.wait_reason ?? ""}::${filePath}`;
}

// localStorage key for skipped goroutine file paths
const SKIPPED_GOROUTINE_FILES_KEY = "pprofPage.skippedGoroutineFiles";

function loadSkippedGoroutineFiles(): Set<string> {
	if (typeof window === "undefined") return new Set();
	try {
		const stored = localStorage.getItem(SKIPPED_GOROUTINE_FILES_KEY);
		return stored ? new Set(JSON.parse(stored)) : new Set();
	} catch {
		return new Set();
	}
}

function saveSkippedGoroutineFiles(skipped: Set<string>): void {
	if (typeof window === "undefined") return;
	try {
		localStorage.setItem(SKIPPED_GOROUTINE_FILES_KEY, JSON.stringify([...skipped]));
	} catch {
		// Ignore storage errors
	}
}

// ============================================================================
// Sort Types
// ============================================================================

type AllocationSortField = "function" | "file" | "bytes" | "count";
type SortDirection = "asc" | "desc";

// ============================================================================
// Components
// ============================================================================

// Stat Card Component
function StatCard({
	label,
	value,
	subValue,
	color,
	icon: Icon,
}: {
	label: string;
	value: string | number;
	subValue?: string;
	color: string;
	icon: React.ElementType;
}) {
	return (
		<div className="rounded-lg border border-zinc-800 bg-zinc-900 p-4">
			<div className="flex items-center gap-2 text-sm text-zinc-500">
				<Icon className={`h-4 w-4 ${color}`} />
				{label}
			</div>
			<div className={`mt-1 text-2xl font-semibold ${color}`}>{value}</div>
			{subValue && <div className="mt-0.5 text-xs text-zinc-500">{subValue}</div>}
		</div>
	);
}

// Allocation Table Component with expandable stack traces
function AllocationTable({
	allocations,
	sortField,
	sortDirection,
	onSort,
}: {
	allocations: AllocationInfo[];
	sortField: AllocationSortField;
	sortDirection: SortDirection;
	onSort: (field: AllocationSortField) => void;
}) {
	const [expandedRows, setExpandedRows] = useState<Set<number>>(new Set())
	const SortIcon = sortDirection === "asc" ? ArrowUp : ArrowDown

	const toggleRow = useCallback((index: number) => {
		setExpandedRows(prev => {
			const next = new Set(prev)
			if (next.has(index)) {
				next.delete(index)
			} else {
				next.add(index)
			}
			return next
		})
	}, [])

	const SortHeader = ({ field, children }: { field: AllocationSortField; children: React.ReactNode }) => (
		<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
			<button type="button" onClick={() => onSort(field)} className="flex cursor-pointer items-center gap-1 hover:text-zinc-200">
				{children}
				{sortField === field && <SortIcon className="h-3 w-3" />}
			</button>
		</th>
	)

	const shortFileName = (fullPath: string) => {
		const parts = fullPath.split('/')
		const idx = parts.findIndex(p => p === 'bifrost')
		if (idx >= 0) return parts.slice(idx).join('/')
		return parts.slice(-3).join('/')
	}

	return (
		<div className="overflow-x-auto">
			<table className="w-full">
				<thead>
					<tr className="border-b border-zinc-800">
						<th className="w-8 px-2 py-3" />
						<SortHeader field="function">Function</SortHeader>
						<SortHeader field="file">File:Line</SortHeader>
						<SortHeader field="bytes">Bytes</SortHeader>
						<SortHeader field="count">Count</SortHeader>
					</tr>
				</thead>
				<tbody>
					{allocations.map((alloc, i) => {
						const isExpanded = expandedRows.has(i)
						const hasStack = alloc.stack && alloc.stack.length > 1
						return (
							<Fragment key={`${alloc.function}-${alloc.line}-${i}`}>
								<tr
									className={`border-b border-zinc-800/50 hover:bg-zinc-800/30 ${hasStack ? 'cursor-pointer' : ''}`}
									onClick={() => hasStack && toggleRow(i)}
									role={hasStack ? "button" : undefined}
									tabIndex={hasStack ? 0 : undefined}
									aria-expanded={hasStack ? isExpanded : undefined}
									onKeyDown={(e) => {
										if (!hasStack) return
										if (e.key === "Enter" || e.key === " ") {
											e.preventDefault()
											toggleRow(i)
										}
									}}
								>
									<td className="px-2 py-3 text-center">
										{hasStack && (
											isExpanded
												? <ChevronDown className="inline h-4 w-4 text-zinc-500" />
												: <ChevronRight className="inline h-4 w-4 text-zinc-500" />
										)}
									</td>
									<td className="px-4 py-3">
										<code className="text-sm break-all text-zinc-200">{alloc.function}</code>
									</td>
									<td className="px-4 py-3">
										<code className="text-sm text-zinc-400">
											{alloc.file}:{alloc.line}
										</code>
									</td>
									<td className="px-4 py-3">
										<span className="font-mono text-sm text-rose-400">{formatBytes(alloc.bytes)}</span>
									</td>
									<td className="px-4 py-3">
										<span className="font-mono text-sm text-zinc-300">{alloc.count.toLocaleString()}</span>
									</td>
								</tr>
								{isExpanded && hasStack && (
									<tr className="border-b border-zinc-800/50 bg-zinc-900/50">
										<td colSpan={5} className="px-6 py-3">
											<div className="rounded border border-zinc-800 bg-zinc-950/50 p-3">
												<div className="mb-2 text-xs font-medium text-zinc-500">Call Stack (innermost first)</div>
												<div className="space-y-0.5">
													{alloc.stack.map((frame, fi) => (
														<div key={fi} className="flex items-start gap-2 font-mono text-xs">
															<span className="mt-0.5 shrink-0 text-zinc-600">{fi === 0 ? '→' : ' '}</span>
															<span className="text-zinc-300">{frame.function}</span>
															<span className="shrink-0 text-zinc-600">
																{shortFileName(frame.file)}:{frame.line}
															</span>
														</div>
													))}
												</div>
											</div>
										</td>
									</tr>
								)}
							</Fragment>
						)
					})}
					{allocations.length === 0 && (
						<tr>
							<td colSpan={5} className="px-4 py-8 text-center text-zinc-500">
								No in-use memory data available
							</td>
						</tr>
					)}
				</tbody>
			</table>
		</div>
	)
}

// Goroutine Group Component
function GoroutineGroupRow({
	group,
	isExpanded,
	onToggle,
	onSkip,
}: {
	group: GoroutineGroup;
	isExpanded: boolean;
	onToggle: () => void;
	onSkip: (filePath: string) => void;
}) {
	return (
		<div className="border-b border-zinc-800/50">
			<div
				role="button"
				tabIndex={0}
				onClick={onToggle}
				onKeyDown={(e) => {
					if (e.key === "Enter" || e.key === " ") {
						e.preventDefault();
						onToggle();
					}
				}}
				className="group flex w-full cursor-pointer items-start gap-3 px-4 py-3 hover:bg-zinc-800/30"
			>
				<div className="mt-1 shrink-0">
					{isExpanded ? <ChevronDown className="h-4 w-4 text-zinc-500" /> : <ChevronRight className="h-4 w-4 text-zinc-500" />}
				</div>
				<div className="min-w-0 flex-1">
					<div className="flex flex-wrap items-center gap-2">
						<code className="text-sm break-all text-zinc-200">{group.top_func}</code>
						<span className={`rounded border px-2 py-0.5 text-xs ${getCategoryColor(group.category)}`}>{group.category}</span>
						<span className="rounded bg-zinc-800 px-2 py-0.5 text-xs text-zinc-400">{group.count}x</span>
						<span className="rounded bg-zinc-800 px-2 py-0.5 text-xs text-zinc-400">{group.state}</span>
						{group.wait_minutes != null && group.wait_minutes > 0 && (
							<span className="rounded bg-amber-500/10 px-2 py-0.5 text-xs text-amber-400">{group.wait_minutes}m waiting</span>
						)}
					</div>
					{group.wait_reason && (
						<div className="mt-1 text-xs text-zinc-500">
							Wait reason: <span className="text-amber-400">{group.wait_reason}</span>
						</div>
					)}
				</div>
				<button
					type="button"
					onClick={(e) => {
						e.stopPropagation();
						const filePath = getStackFilePath(group.stack);
						if (filePath) onSkip(filePath);
					}}
					className="shrink-0 rounded p-1.5 text-zinc-600 opacity-0 transition-opacity group-hover:opacity-100 hover:bg-zinc-700 hover:text-zinc-300"
					title="Hide goroutines from this file"
					aria-label="Hide goroutines from this file"
				>
					<EyeOff className="h-4 w-4" />
				</button>
			</div>
			{isExpanded && (
				<div className="border-t border-zinc-800/50 bg-zinc-900/50 px-4 py-3">
					<div className="mb-2 text-xs font-medium text-zinc-500">Stack Trace</div>
					<div className="space-y-0.5 font-mono text-xs">
						{group.stack.map((line, j) => (
							<div key={j} className="break-all text-zinc-400">
								{line}
							</div>
						))}
					</div>
				</div>
			)}
		</div>
	);
}

// ============================================================================
// Main Page Component
// ============================================================================

export default function PprofPage() {
	const [expandedGoroutines, setExpandedGoroutines] = useState<Set<string>>(new Set());
	const [skippedGoroutines, setSkippedGoroutines] = useState<Set<string>>(new Set());
	const [hasLoadedSkipped, setHasLoadedSkipped] = useState(false);
	const [allocationSort, setAllocationSort] = useState<{
		field: AllocationSortField;
		direction: SortDirection;
	}>({ field: "bytes", direction: "desc" });

	// Load skipped goroutines from localStorage on client
	useEffect(() => {
		setSkippedGoroutines(loadSkippedGoroutineFiles());
		setHasLoadedSkipped(true);
	}, []);

	// Sync skipped goroutines to localStorage
	useEffect(() => {
		if (!hasLoadedSkipped) return;
		saveSkippedGoroutineFiles(skippedGoroutines);
	}, [skippedGoroutines, hasLoadedSkipped]);

	// Fetch data with 10s polling
	const { data, isLoading, error, refetch } = useGetDevPprofQuery(undefined, {
		pollingInterval: 10000,
	});

	const { data: goroutineData } = useGetDevGoroutinesQuery(undefined, {
		pollingInterval: 10000,
	});

	const { data: poolData } = useGetDevPoolStatsQuery(undefined, {
		pollingInterval: 10000,
	});

	// Memoize chart data transformation
	const memoryChartData = useMemo(() => {
		if (!data?.history) return [];
		return data.history.map((point) => ({
			time: formatTime(point.timestamp),
			alloc: point.alloc / (1024 * 1024),
			heapInuse: point.heap_inuse / (1024 * 1024),
		}));
	}, [data?.history]);

	const cpuChartData = useMemo(() => {
		if (!data?.history) return [];
		return data.history.map((point) => ({
			time: formatTime(point.timestamp),
			cpuPercent: point.cpu_percent,
			goroutines: point.goroutines,
		}));
	}, [data?.history]);

	// Sort allocations
	const sortedAllocations = useMemo(() => {
		if (!data?.top_allocations) return [];
		const sorted = [...data.top_allocations];
		sorted.sort((a, b) => {
			let cmp = 0;
			switch (allocationSort.field) {
				case "function":
					cmp = a.function.localeCompare(b.function);
					break;
				case "file":
					cmp = a.file.localeCompare(b.file);
					break;
				case "bytes":
					cmp = a.bytes - b.bytes;
					break;
				case "count":
					cmp = a.count - b.count;
					break;
			}
			return allocationSort.direction === "asc" ? cmp : -cmp;
		});
		return sorted;
	}, [data?.top_allocations, allocationSort]);

	// Detect goroutine count trend
	const goroutineTrend = useMemo(() => {
		if (!data?.history || data.history.length < 5 || !data?.runtime) return null;
		const recent = data.history.slice(-5);
		const avg = recent.reduce((sum, p) => sum + p.goroutines, 0) / recent.length;
		const current = data.runtime.num_goroutine;
		const isGrowing = current > avg * 1.1;
		const growthPercent = avg > 0 ? ((current - avg) / avg) * 100 : 0;
		return { isGrowing, growthPercent, avg };
	}, [data?.history, data?.runtime?.num_goroutine]);

	// Filter problem goroutines
	const filteredGoroutines = useMemo(() => {
		if (!goroutineData?.groups) return [];
		return goroutineData.groups.filter((g) => {
			const filePath = getStackFilePath(g.stack);
			if (filePath && skippedGoroutines.has(filePath)) return false;
			return true;
		});
	}, [goroutineData?.groups, skippedGoroutines]);

	// Get goroutine health status
	const goroutineHealth = useMemo(() => {
		if (!goroutineData?.summary) return "healthy";
		const { potentially_stuck, long_waiting } = goroutineData.summary;
		if (potentially_stuck > 0) return "critical";
		if (long_waiting > 0) return "warning";
		return "healthy";
	}, [goroutineData?.summary]);

	const handleAllocationSort = useCallback((field: AllocationSortField) => {
		setAllocationSort((prev) => ({
			field,
			direction: prev.field === field && prev.direction === "desc" ? "asc" : "desc",
		}));
	}, []);

	const toggleGoroutineExpand = useCallback((id: string) => {
		setExpandedGoroutines((prev) => {
			const next = new Set(prev);
			if (next.has(id)) {
				next.delete(id);
			} else {
				next.add(id);
			}
			return next;
		});
	}, []);

	const handleSkipGoroutine = useCallback((filePath: string) => {
		setSkippedGoroutines((prev) => {
			const next = new Set(prev);
			next.add(filePath);
			return next;
		});
	}, []);

	const handleClearSkipped = useCallback(() => {
		setSkippedGoroutines(new Set());
	}, []);

	// Loading state
	if (isLoading && !data) {
		return (
			<div className="flex min-h-screen items-center justify-center">
				<div className="flex items-center gap-3 text-zinc-400">
					<RefreshCw className="h-5 w-5 animate-spin" />
					Loading profiling data...
				</div>
			</div>
		);
	}

	// Error state
	if (error) {
		return (
			<div className="flex min-h-screen items-center justify-center">
				<div className="rounded-lg border border-red-800 bg-red-900/20 px-6 py-4 text-red-400">
					Failed to load profiling data. Make sure the backend is running in dev mode.
				</div>
			</div>
		);
	}

	return (
		<div className="mx-auto max-w-7xl px-6 py-8">
			{/* Header */}
			<div className="mb-8 flex items-center justify-between">
				<div>
					<h1 className="text-2xl font-semibold text-zinc-100">Pprof Profiler</h1>
					<p className="mt-1 text-sm text-zinc-500">Development only - Runtime profiling and memory analysis</p>
				</div>
				<div className="flex items-center gap-4">
					<span className="flex items-center gap-2 text-sm text-zinc-500">
						<span className="h-2 w-2 animate-pulse rounded-full bg-emerald-400" />
						Auto-refresh: 10s
					</span>
					<button
						onClick={() => refetch()}
						className="flex items-center gap-2 rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm text-zinc-300 transition-colors hover:bg-zinc-700"
					>
						<RefreshCw className="h-4 w-4" />
						Refresh
					</button>
				</div>
			</div>

			{data && (
				<>
					{/* Overview Stats */}
					<div className="mb-8 grid grid-cols-2 gap-4 md:grid-cols-3 lg:grid-cols-6">
						<StatCard label="CPU Usage" value={`${data.cpu.usage_percent.toFixed(1)}%`} color="text-orange-400" icon={Cpu} />
						<StatCard label="Heap Alloc" value={formatBytes(data.memory.alloc)} color="text-cyan-400" icon={HardDrive} />
						<StatCard label="Heap In-Use" value={formatBytes(data.memory.heap_inuse)} color="text-blue-400" icon={HardDrive} />
						<StatCard label="System Memory" value={formatBytes(data.memory.sys)} color="text-purple-400" icon={HardDrive} />
						<StatCard
							label="Goroutines"
							value={data.runtime.num_goroutine}
							subValue={goroutineTrend?.isGrowing ? `↑ ${goroutineTrend.growthPercent.toFixed(0)}%` : undefined}
							color="text-emerald-400"
							icon={Activity}
						/>
						<StatCard
							label="GC Pause"
							value={formatNs(data.runtime.gc_pause_ns)}
							subValue={`${data.runtime.num_gc} GCs`}
							color="text-amber-400"
							icon={Activity}
						/>
					</div>

					{/* Memory Breakdown */}
					<div className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900 p-4">
						<div className="mb-4 flex items-center gap-2">
							<HardDrive className="h-4 w-4 text-purple-400" />
							<span className="font-medium text-zinc-300">Memory Breakdown</span>
							<span className="text-sm text-zinc-500">where {formatBytes(data.memory.sys)} system memory is used</span>
						</div>
						{(() => {
							const m = data.memory
							const goRuntime = m.mspan_inuse + m.mcache_inuse + m.buck_hash_sys + m.gc_sys + m.other_sys
							const heapFrag = m.heap_idle - m.heap_released
							const cgoEstimate = m.sys > (m.heap_sys + m.stack_sys + goRuntime)
								? m.sys - m.heap_sys - m.stack_sys - goRuntime
								: 0
							const segments = [
								{ label: 'Heap (live objects)', value: m.alloc, color: 'bg-cyan-500' },
								{ label: 'Heap (idle, reclaimable)', value: heapFrag, color: 'bg-cyan-800' },
								{ label: 'Heap (returned to OS)', value: m.heap_released, color: 'bg-zinc-700' },
								{ label: 'Goroutine stacks', value: m.stack_inuse, color: 'bg-emerald-500' },
								{ label: 'GC metadata', value: m.gc_sys, color: 'bg-amber-500' },
								{ label: 'Runtime (mspan+mcache+other)', value: m.mspan_inuse + m.mcache_inuse + m.other_sys, color: 'bg-orange-500' },
								{ label: 'Profiling overhead', value: m.buck_hash_sys, color: 'bg-rose-500' },
								{ label: 'CGo / external (estimated)', value: cgoEstimate, color: 'bg-red-600' },
							].filter(s => s.value > 0)
							const total = segments.reduce((sum, s) => sum + s.value, 0)
							return (
								<div>
									{/* Stacked bar */}
									<div className="mb-4 flex h-6 overflow-hidden rounded-full">
										{segments.map((seg, i) => (
											<div
												key={i}
												className={`${seg.color} transition-all`}
												style={{ width: `${(seg.value / total) * 100}%` }}
												title={`${seg.label}: ${formatBytes(seg.value)}`}
											/>
										))}
									</div>
									{/* Legend */}
									<div className="grid grid-cols-2 gap-x-6 gap-y-1.5 text-sm md:grid-cols-4">
										{segments.map((seg, i) => (
											<div key={i} className="flex items-center gap-2">
												<span className={`h-2.5 w-2.5 shrink-0 rounded-sm ${seg.color}`} />
												<span className="text-zinc-400">{seg.label}</span>
												<span className="ml-auto font-mono text-zinc-200">{formatBytes(seg.value)}</span>
											</div>
										))}
									</div>
								</div>
							)
						})()}
					</div>

					{/* Charts */}
					<div className="mb-8 grid gap-6 lg:grid-cols-2">
						{/* CPU Chart */}
						<div className="rounded-lg border border-zinc-800 bg-zinc-900 p-4">
							<div className="mb-4 flex items-center gap-2">
								<Cpu className="h-4 w-4 text-orange-400" />
								<span className="font-medium text-zinc-300">CPU Usage & Goroutines</span>
								<span className="text-sm text-zinc-500">(last 5 min)</span>
							</div>
							<div className="h-64">
								<ResponsiveContainer width="100%" height="100%">
									<AreaChart data={cpuChartData}>
										<defs>
											<linearGradient id="cpuGradient" x1="0" y1="0" x2="0" y2="1">
												<stop offset="5%" stopColor="#f97316" stopOpacity={0.3} />
												<stop offset="95%" stopColor="#f97316" stopOpacity={0} />
											</linearGradient>
											<linearGradient id="goroutineGradient" x1="0" y1="0" x2="0" y2="1">
												<stop offset="5%" stopColor="#34d399" stopOpacity={0.3} />
												<stop offset="95%" stopColor="#34d399" stopOpacity={0} />
											</linearGradient>
										</defs>
										<CartesianGrid strokeDasharray="3 3" stroke="#3f3f46" />
										<XAxis dataKey="time" tick={{ fill: "#71717a", fontSize: 11 }} tickLine={false} axisLine={false} />
										<YAxis
											yAxisId="left"
											tick={{ fill: "#71717a", fontSize: 11 }}
											tickLine={false}
											axisLine={false}
											tickFormatter={(v) => `${Number(v).toFixed(0)}%`}
											width={45}
											domain={[0, "auto"]}
										/>
										<YAxis
											yAxisId="right"
											orientation="right"
											tick={{ fill: "#71717a", fontSize: 11 }}
											tickLine={false}
											axisLine={false}
											width={40}
										/>
										<Tooltip
											contentStyle={{
												backgroundColor: "#18181b",
												border: "1px solid #3f3f46",
												borderRadius: "8px",
												fontSize: "12px",
											}}
											labelStyle={{ color: "#a1a1aa" }}
										/>
										<Area
											type="monotone"
											dataKey="cpuPercent"
											stroke="#f97316"
											strokeWidth={2}
											fill="url(#cpuGradient)"
											yAxisId="left"
											name="CPU %"
										/>
										<Area
											type="monotone"
											dataKey="goroutines"
											stroke="#34d399"
											strokeWidth={2}
											fill="url(#goroutineGradient)"
											yAxisId="right"
											name="Goroutines"
										/>
									</AreaChart>
								</ResponsiveContainer>
							</div>
							<div className="mt-3 flex gap-6 text-sm">
								<span className="flex items-center gap-2">
									<span className="h-3 w-3 rounded-full bg-orange-500" />
									CPU %
								</span>
								<span className="flex items-center gap-2">
									<span className="h-3 w-3 rounded-full bg-emerald-400" />
									Goroutines
								</span>
							</div>
						</div>

						{/* Memory Chart */}
						<div className="rounded-lg border border-zinc-800 bg-zinc-900 p-4">
							<div className="mb-4 flex items-center gap-2">
								<HardDrive className="h-4 w-4 text-cyan-400" />
								<span className="font-medium text-zinc-300">Memory Usage</span>
								<span className="text-sm text-zinc-500">(last 5 min)</span>
							</div>
							<div className="h-64">
								<ResponsiveContainer width="100%" height="100%">
									<AreaChart data={memoryChartData}>
										<defs>
											<linearGradient id="allocGradient" x1="0" y1="0" x2="0" y2="1">
												<stop offset="5%" stopColor="#22d3ee" stopOpacity={0.3} />
												<stop offset="95%" stopColor="#22d3ee" stopOpacity={0} />
											</linearGradient>
											<linearGradient id="heapGradient" x1="0" y1="0" x2="0" y2="1">
												<stop offset="5%" stopColor="#3b82f6" stopOpacity={0.3} />
												<stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
											</linearGradient>
										</defs>
										<CartesianGrid strokeDasharray="3 3" stroke="#3f3f46" />
										<XAxis dataKey="time" tick={{ fill: "#71717a", fontSize: 11 }} tickLine={false} axisLine={false} />
										<YAxis
											tick={{ fill: "#71717a", fontSize: 11 }}
											tickLine={false}
											axisLine={false}
											tickFormatter={(v) => `${Number(v).toFixed(0)}MB`}
											width={50}
										/>
										<Tooltip
											contentStyle={{
												backgroundColor: "#18181b",
												border: "1px solid #3f3f46",
												borderRadius: "8px",
												fontSize: "12px",
											}}
											labelStyle={{ color: "#a1a1aa" }}
										/>
										<Area type="monotone" dataKey="alloc" stroke="#22d3ee" strokeWidth={2} fill="url(#allocGradient)" name="Alloc (MB)" />
										<Area
											type="monotone"
											dataKey="heapInuse"
											stroke="#3b82f6"
											strokeWidth={2}
											fill="url(#heapGradient)"
											name="Heap In-Use (MB)"
										/>
									</AreaChart>
								</ResponsiveContainer>
							</div>
							<div className="mt-3 flex gap-6 text-sm">
								<span className="flex items-center gap-2">
									<span className="h-3 w-3 rounded-full bg-cyan-400" />
									Alloc
								</span>
								<span className="flex items-center gap-2">
									<span className="h-3 w-3 rounded-full bg-blue-500" />
									Heap In-Use
								</span>
							</div>
						</div>
					</div>

					{/* Allocations Table */}
					<div className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900">
						<div className="flex items-center gap-2 border-b border-zinc-800 px-4 py-3">
							<HardDrive className="h-4 w-4 text-rose-400" />
							<span className="font-medium text-zinc-300">Memory In-Use</span>
							<span className="text-sm text-zinc-500">({sortedAllocations.length} allocation sites)</span>
						</div>
						<AllocationTable
							allocations={sortedAllocations}
							sortField={allocationSort.field}
							sortDirection={allocationSort.direction}
							onSort={handleAllocationSort}
						/>
					</div>

					{/* Object Pool Stats */}
					{poolData && (
						<div className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900">
							<div className="flex items-center gap-2 border-b border-zinc-800 px-4 py-3">
								<Layers className="h-4 w-4 text-violet-400" />
								<span className="font-medium text-zinc-300">Object Pools</span>
								<span className={`rounded px-2 py-0.5 text-xs ${poolData.build === "debug" ? "bg-violet-500/10 text-violet-400" : "bg-zinc-700 text-zinc-400"}`}>
									{poolData.build}
								</span>
								<span className="text-sm text-zinc-500">({poolData.pools.length} pools)</span>
							</div>
							{poolData.build === "production" ? (
								<div className="px-4 py-8 text-center text-zinc-500">
									Pool tracking is disabled in production builds. Rebuild with <code className="rounded bg-zinc-800 px-1.5 py-0.5 text-xs text-violet-400">-tags pooldebug</code> to enable.
								</div>
							) : poolData.pools.length === 0 ? (
								<div className="px-4 py-8 text-center text-zinc-500">
									No pools registered yet. Pools are created on first use.
								</div>
							) : (
								<div className="overflow-x-auto">
									<table className="w-full">
										<thead>
											<tr className="border-b border-zinc-800">
												<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">Pool</th>
												<th scope="col" className="px-4 py-3 text-right text-sm font-medium text-zinc-400">Acquires</th>
												<th scope="col" className="px-4 py-3 text-right text-sm font-medium text-zinc-400">Releases</th>
												<th scope="col" className="px-4 py-3 text-right text-sm font-medium text-zinc-400">Creates (Misses)</th>
												<th scope="col" className="px-4 py-3 text-right text-sm font-medium text-zinc-400">Reused</th>
												<th scope="col" className="px-4 py-3 text-right text-sm font-medium text-zinc-400">Active</th>
												<th scope="col" className="px-4 py-3 text-right text-sm font-medium text-zinc-400">Hit Rate</th>
											</tr>
										</thead>
										<tbody>
											{poolData.pools.map((p) => {
												const reused = p.acquires - p.creates;
												const hitPct = p.hit_rate * 100;
												const hitColor = hitPct >= 90 ? "text-emerald-400" : hitPct >= 70 ? "text-amber-400" : "text-red-400";
												return (
													<tr key={p.name} className="border-b border-zinc-800/50 hover:bg-zinc-800/30">
														<td className="px-4 py-3">
															<code className="text-sm text-zinc-200">{p.name}</code>
														</td>
														<td className="px-4 py-3 text-right font-mono text-sm text-zinc-300">{p.acquires.toLocaleString()}</td>
														<td className="px-4 py-3 text-right font-mono text-sm text-zinc-300">{p.releases.toLocaleString()}</td>
														<td className="px-4 py-3 text-right font-mono text-sm text-rose-400">{p.creates.toLocaleString()}</td>
														<td className="px-4 py-3 text-right font-mono text-sm text-emerald-400">{reused.toLocaleString()}</td>
														<td className="px-4 py-3 text-right">
															<span className={`font-mono text-sm ${p.active > 0 ? "text-amber-400" : "text-zinc-500"}`}>
																{p.active.toLocaleString()}
															</span>
														</td>
														<td className="px-4 py-3 text-right">
															<span className={`font-mono text-sm font-semibold ${hitColor}`}>
																{hitPct.toFixed(1)}%
															</span>
														</td>
													</tr>
												);
											})}
										</tbody>
										<tfoot>
											<tr className="border-t border-zinc-700">
												<td className="px-4 py-3 text-sm font-medium text-zinc-400">Totals</td>
												<td className="px-4 py-3 text-right font-mono text-sm font-medium text-zinc-300">
													{poolData.pools.reduce((s, p) => s + p.acquires, 0).toLocaleString()}
												</td>
												<td className="px-4 py-3 text-right font-mono text-sm font-medium text-zinc-300">
													{poolData.pools.reduce((s, p) => s + p.releases, 0).toLocaleString()}
												</td>
												<td className="px-4 py-3 text-right font-mono text-sm font-medium text-rose-400">
													{poolData.pools.reduce((s, p) => s + p.creates, 0).toLocaleString()}
												</td>
												<td className="px-4 py-3 text-right font-mono text-sm font-medium text-emerald-400">
													{poolData.pools.reduce((s, p) => s + p.acquires - p.creates, 0).toLocaleString()}
												</td>
												<td className="px-4 py-3 text-right font-mono text-sm font-medium text-zinc-300">
													{poolData.pools.reduce((s, p) => s + p.active, 0).toLocaleString()}
												</td>
												<td className="px-4 py-3 text-right">
													{(() => {
														const totalAcquires = poolData.pools.reduce((s, p) => s + p.acquires, 0);
														const totalCreates = poolData.pools.reduce((s, p) => s + p.creates, 0);
														const avgHit = totalAcquires > 0 ? ((1 - totalCreates / totalAcquires) * 100) : 0;
														const color = avgHit >= 90 ? "text-emerald-400" : avgHit >= 70 ? "text-amber-400" : "text-red-400";
														return <span className={`font-mono text-sm font-semibold ${color}`}>{avgHit.toFixed(1)}%</span>;
													})()}
												</td>
											</tr>
										</tfoot>
									</table>
								</div>
							)}
						</div>
					)}

					{/* Goroutine Health */}
					<div className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900">
						<div className="flex items-center justify-between border-b border-zinc-800 px-4 py-3">
							<div className="flex items-center gap-2">
								<Activity className="h-4 w-4 text-emerald-400" />
								<span className="font-medium text-zinc-300">Goroutine Health</span>
								{goroutineTrend?.isGrowing && (
									<span className="flex items-center gap-1 rounded bg-amber-500/10 px-2 py-0.5 text-xs text-amber-400">
										<TrendingUp className="h-3 w-3" />
										Growing +{goroutineTrend.growthPercent.toFixed(0)}%
									</span>
								)}
								{goroutineHealth === "critical" && (
									<span className="flex items-center gap-1 rounded bg-red-500/10 px-2 py-0.5 text-xs text-red-400">
										<AlertTriangle className="h-3 w-3" />
										Stuck Goroutines
									</span>
								)}
								{goroutineHealth === "warning" && (
									<span className="flex items-center gap-1 rounded bg-amber-500/10 px-2 py-0.5 text-xs text-amber-400">
										<AlertTriangle className="h-3 w-3" />
										Long Waiting
									</span>
								)}
								{goroutineHealth === "healthy" && (
									<span className="rounded bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-400">Healthy</span>
								)}
							</div>
							{skippedGoroutines.size > 0 && (
								<button
									onClick={handleClearSkipped}
									className="flex items-center gap-1 rounded px-2 py-1 text-sm text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
								>
									<RotateCcw className="h-3 w-3" />
									Clear {skippedGoroutines.size} hidden
								</button>
							)}
						</div>

						{/* Summary Stats */}
						{goroutineData?.summary && (
							<div className="grid grid-cols-4 gap-4 border-b border-zinc-800 p-4">
								<div className="text-center">
									<div className="text-2xl font-semibold text-emerald-400">{goroutineData.total_goroutines}</div>
									<div className="text-sm text-zinc-500">Total</div>
								</div>
								<div className="text-center">
									<div className="text-2xl font-semibold text-blue-400">{goroutineData.summary.background}</div>
									<div className="text-sm text-zinc-500">Background</div>
								</div>
								<div className="text-center">
									<div className="text-2xl font-semibold text-amber-400">{goroutineData.summary.per_request}</div>
									<div className="text-sm text-zinc-500">Per-Request</div>
								</div>
								<div className="text-center">
									<div
										className={`text-2xl font-semibold ${goroutineData.summary.potentially_stuck > 0 ? "text-red-400" : "text-zinc-500"}`}
									>
										{goroutineData.summary.potentially_stuck}
									</div>
									<div className="text-sm text-zinc-500">Stuck</div>
								</div>
							</div>
						)}

						{/* Goroutine Groups */}
						<div className="max-h-[600px] overflow-y-auto">
							{filteredGoroutines.map((g) => {
								const gid = getGoroutineId(g);
								return (
									<GoroutineGroupRow
										key={gid}
										group={g}
										isExpanded={expandedGoroutines.has(gid)}
										onToggle={() => toggleGoroutineExpand(gid)}
										onSkip={handleSkipGoroutine}
									/>
								);
							})}
							{filteredGoroutines.length === 0 && (
								<div className="px-4 py-8 text-center text-zinc-500">
									{skippedGoroutines.size > 0
										? 'All goroutines are hidden. Click "Clear hidden" to show them.'
										: "No goroutine data available"}
								</div>
							)}
						</div>
					</div>

					{/* Runtime Info Footer */}
					<div className="rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3">
						<div className="flex flex-wrap items-center gap-6 text-sm text-zinc-400">
							<span>
								<span className="text-zinc-500">CPUs:</span> {data.runtime.num_cpu}
							</span>
							<span>
								<span className="text-zinc-500">GOMAXPROCS:</span> {data.runtime.gomaxprocs}
							</span>
							<span>
								<span className="text-zinc-500">GC Runs:</span> {data.runtime.num_gc}
							</span>
							<span>
								<span className="text-zinc-500">Heap Objects:</span> {data.memory.heap_objects.toLocaleString()}
							</span>
							<span>
								<span className="text-zinc-500">Total Alloc:</span> {formatBytes(data.memory.total_alloc)}
							</span>
						</div>
					</div>
				</>
			)}
		</div>
	);
}
