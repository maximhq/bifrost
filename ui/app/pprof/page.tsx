"use client";

import { useGetDevGoroutinesQuery, useGetDevPprofQuery } from "@/lib/store";
import type { AllocationInfo, GoroutineGroup } from "@/lib/store/apis/devApi";
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
	RefreshCw,
	RotateCcw,
	TrendingUp,
} from "lucide-react";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
type AllocationSortState = { field: AllocationSortField; direction: SortDirection };
type LeakSeverity = "high" | "medium" | "low";

interface LeakCandidate {
	key: string;
	function: string;
	file: string;
	line: number;
	stack: string[];
	liveBytes: number;
	cumulativeBytes: number;
	retention: number;
	liveCount: number;
	samples: number[];
	isGrowing: boolean;
	growthBytes: number;
	severity: LeakSeverity;
}

// ~60 seconds of history at 10s polling interval
const LEAK_MAX_SAMPLES = 6;
const LEAK_MIN_GROWTH_SAMPLES = 3;
const LEAK_SEVERITY_RANK: Record<LeakSeverity, number> = { high: 0, medium: 1, low: 2 };

function makeStackKey(stack: string[]): string {
	return stack.join("\n");
}

function isMonotonicGrowing(samples: number[]): boolean {
	if (samples.length < LEAK_MIN_GROWTH_SAMPLES) return false;
	for (let i = 1; i < samples.length; i++) {
		if (samples[i] < samples[i - 1]) return false;
	}
	return samples[samples.length - 1] > samples[0];
}

function classifyLeakSeverity(retention: number, liveBytes: number, isGrowing: boolean): LeakSeverity | null {
	const MB = 1024 * 1024;
	if (isGrowing && retention >= 0.5 && liveBytes >= MB) return "high";
	if (retention >= 0.8 && liveBytes >= 10 * MB) return "high";
	if (retention >= 0.5 && liveBytes >= MB) return "medium";
	if (retention >= 0.3 && liveBytes >= 100 * 1024) return "low";
	return null;
}

function detectLeaks(
	cumulative: AllocationInfo[],
	live: AllocationInfo[],
	inuseHistory: Map<string, number[]>,
): LeakCandidate[] {
	const cumMap = new Map<string, AllocationInfo>();
	for (const c of cumulative) {
		cumMap.set(makeStackKey(c.stack), c);
	}

	const candidates: LeakCandidate[] = [];
	for (const l of live) {
		const key = makeStackKey(l.stack);
		const cum = cumMap.get(key);
		if (!cum || cum.bytes === 0) continue;
		const retention = l.bytes / cum.bytes;
		const samples = inuseHistory.get(key) ?? [];
		const isGrowing = isMonotonicGrowing(samples);
		const growthBytes = samples.length >= 2 ? samples[samples.length - 1] - samples[0] : 0;
		const severity = classifyLeakSeverity(retention, l.bytes, isGrowing);
		if (!severity) continue;
		candidates.push({
			key,
			function: l.function,
			file: l.file,
			line: l.line,
			stack: l.stack,
			liveBytes: l.bytes,
			cumulativeBytes: cum.bytes,
			retention,
			liveCount: l.count,
			samples: [...samples],
			isGrowing,
			growthBytes,
			severity,
		});
	}

	candidates.sort((a, b) => {
		if (a.severity !== b.severity) return LEAK_SEVERITY_RANK[a.severity] - LEAK_SEVERITY_RANK[b.severity];
		return b.liveBytes - a.liveBytes;
	});
	return candidates;
}

function getLeakSeverityClasses(severity: LeakSeverity): string {
	switch (severity) {
		case "high":
			return "text-red-400 bg-red-400/10 border-red-400/20";
		case "medium":
			return "text-amber-400 bg-amber-400/10 border-amber-400/20";
		case "low":
			return "text-zinc-400 bg-zinc-400/10 border-zinc-400/20";
	}
}

function getRetentionColor(retention: number): string {
	if (retention >= 0.8) return "text-red-400";
	if (retention >= 0.5) return "text-amber-400";
	return "text-zinc-400";
}

function sortAllocations(list: AllocationInfo[], sort: AllocationSortState): AllocationInfo[] {
	const sorted = [...list];
	sorted.sort((a, b) => {
		let cmp = 0;
		switch (sort.field) {
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
		return sort.direction === "asc" ? cmp : -cmp;
	});
	return sorted;
}

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

// Allocation Table Component
function AllocationTable({
	allocations,
	sortField,
	sortDirection,
	onSort,
	expandedKeys,
	onToggle,
	bytesColorClass = "text-rose-400",
	testIdPrefix = "pprof-sort",
}: {
	allocations: AllocationInfo[];
	sortField: AllocationSortField;
	sortDirection: SortDirection;
	onSort: (field: AllocationSortField) => void;
	expandedKeys: Set<string>;
	onToggle: (key: string) => void;
	bytesColorClass?: string;
	testIdPrefix?: string;
}) {
	const SortIcon = sortDirection === "asc" ? ArrowUp : ArrowDown;

	const SortHeader = ({ field, children }: { field: AllocationSortField; children: React.ReactNode }) => (
		<th scope="col" aria-sort={sortField === field ? (sortDirection === "asc" ? "ascending" : "descending") : "none"} className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
			<button type="button" onClick={() => onSort(field)} data-testid={`${testIdPrefix}-${field}`} className="flex cursor-pointer items-center gap-1 hover:text-zinc-200">
				{children}
				{sortField === field && <SortIcon className="h-3 w-3" />}
			</button>
		</th>
	);

	return (
		<div className="overflow-x-auto">
			<table className="w-full">
				<thead>
					<tr className="border-b border-zinc-800">
						<th scope="col" className="w-8 px-2 py-3" aria-label="Expand" />
						<SortHeader field="function">Function</SortHeader>
						<SortHeader field="file">File:Line</SortHeader>
						<SortHeader field="bytes">Bytes</SortHeader>
						<SortHeader field="count">Count</SortHeader>
					</tr>
				</thead>
				<tbody>
					{allocations.map((alloc) => {
						const hasStack = alloc.stack && alloc.stack.length > 0;
						const key = hasStack ? makeStackKey(alloc.stack) : `${alloc.function}:${alloc.file}:${alloc.line}`;
						const isExpanded = expandedKeys.has(key);
						return (
							<React.Fragment key={key}>
								<tr
									role={hasStack ? "button" : undefined}
									tabIndex={hasStack ? 0 : undefined}
									aria-expanded={hasStack ? isExpanded : undefined}
									onClick={hasStack ? () => onToggle(key) : undefined}
									onKeyDown={
										hasStack
											? (e) => {
													if (e.key === "Enter" || e.key === " ") {
														e.preventDefault();
														onToggle(key);
													}
												}
											: undefined
									}
									data-testid="pprof-alloc-row"
									className={`border-b border-zinc-800/50 hover:bg-zinc-800/30 ${hasStack ? "cursor-pointer" : ""}`}
								>
									<td className="w-8 px-2 py-3 align-top">
										{hasStack ? (
											isExpanded ? <ChevronDown className="h-4 w-4 text-zinc-500" /> : <ChevronRight className="h-4 w-4 text-zinc-500" />
										) : null}
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
										<span className={`font-mono text-sm ${bytesColorClass}`}>{formatBytes(alloc.bytes)}</span>
									</td>
									<td className="px-4 py-3">
										<span className="font-mono text-sm text-zinc-300">{alloc.count.toLocaleString()}</span>
									</td>
								</tr>
								{isExpanded && hasStack && (
									<tr className="border-b border-zinc-800/50 bg-zinc-900/50">
										<td />
										<td colSpan={4} className="px-4 py-3">
											<div className="mb-2 text-xs font-medium text-zinc-500">Stack Trace</div>
											<div className="space-y-0.5 font-mono text-xs">
												{alloc.stack.map((line, j) => (
													<div key={j} className="break-all text-zinc-400">
														{line}
													</div>
												))}
											</div>
										</td>
									</tr>
								)}
							</React.Fragment>
						);
					})}
					{allocations.length === 0 && (
						<tr>
							<td colSpan={5} className="px-4 py-8 text-center text-zinc-500">
								No allocations data available
							</td>
						</tr>
					)}
				</tbody>
			</table>
		</div>
	);
}

// Leak Candidates Table
function LeakTable({
	candidates,
	expandedKeys,
	onToggle,
}: {
	candidates: LeakCandidate[];
	expandedKeys: Set<string>;
	onToggle: (key: string) => void;
}) {
	return (
		<div className="overflow-x-auto">
			<table className="w-full">
				<thead>
					<tr className="border-b border-zinc-800">
						<th scope="col" className="w-8 px-2 py-3" aria-label="Expand" />
						<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
							Severity
						</th>
						<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
							Function
						</th>
						<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
							File:Line
						</th>
						<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
							Live
						</th>
						<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
							Retention
						</th>
						<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
							Trend
						</th>
						<th scope="col" className="px-4 py-3 text-left text-sm font-medium text-zinc-400">
							Live Count
						</th>
					</tr>
				</thead>
				<tbody>
					{candidates.map((c) => {
						const rowKey = c.key;
						const isExpanded = expandedKeys.has(rowKey);
						return (
							<React.Fragment key={rowKey}>
								<tr
									role="button"
									tabIndex={0}
									aria-expanded={isExpanded}
									onClick={() => onToggle(rowKey)}
									onKeyDown={(e) => {
										if (e.key === "Enter" || e.key === " ") {
											e.preventDefault();
											onToggle(rowKey);
										}
									}}
									data-testid="pprof-leak-row"
									className="cursor-pointer border-b border-zinc-800/50 hover:bg-zinc-800/30"
								>
									<td className="w-8 px-2 py-3 align-top">
										{isExpanded ? <ChevronDown className="h-4 w-4 text-zinc-500" /> : <ChevronRight className="h-4 w-4 text-zinc-500" />}
									</td>
									<td className="px-4 py-3">
										<span className={`rounded border px-2 py-0.5 text-xs uppercase ${getLeakSeverityClasses(c.severity)}`}>
											{c.severity}
										</span>
									</td>
									<td className="px-4 py-3">
										<code className="text-sm break-all text-zinc-200">{c.function}</code>
									</td>
									<td className="px-4 py-3">
										<code className="text-sm text-zinc-400">
											{c.file}:{c.line}
										</code>
									</td>
									<td className="px-4 py-3">
										<span className="font-mono text-sm text-emerald-400">{formatBytes(c.liveBytes)}</span>
									</td>
									<td className="px-4 py-3">
										<span className={`font-mono text-sm ${getRetentionColor(c.retention)}`}>
											{(c.retention * 100).toFixed(0)}%
										</span>
									</td>
									<td className="px-4 py-3">
										{c.isGrowing ? (
											<span className="flex items-center gap-1 text-xs text-rose-400">
												<TrendingUp className="h-3 w-3" />+{formatBytes(c.growthBytes)}
											</span>
										) : (
											<span className="text-xs text-zinc-500">stable</span>
										)}
									</td>
									<td className="px-4 py-3">
										<span className="font-mono text-sm text-zinc-300">{c.liveCount.toLocaleString()}</span>
									</td>
								</tr>
								{isExpanded && (
									<tr className="border-b border-zinc-800/50 bg-zinc-900/50">
										<td />
										<td colSpan={7} className="px-4 py-3">
											<div className="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-zinc-500">
												<span>
													Cumulative: <span className="text-zinc-300">{formatBytes(c.cumulativeBytes)}</span>
												</span>
												<span>
													Retained: <span className="text-zinc-300">{(c.retention * 100).toFixed(1)}%</span>
												</span>
												{c.samples.length >= 2 && (
													<span>
														Last {c.samples.length * 10}s:{" "}
														<span className="text-zinc-300">{c.samples.map((b) => formatBytes(b)).join(" → ")}</span>
													</span>
												)}
											</div>
											<div className="mb-2 text-xs font-medium text-zinc-500">Stack Trace</div>
											<div className="space-y-0.5 font-mono text-xs">
												{c.stack.map((line, j) => (
													<div key={j} className="break-all text-zinc-400">
														{line}
													</div>
												))}
											</div>
										</td>
									</tr>
								)}
							</React.Fragment>
						);
					})}
					{candidates.length === 0 && (
						<tr>
							<td colSpan={8} className="px-4 py-8 text-center text-zinc-500">
								No obvious leak signatures — all live allocations have normal retention ratios.
							</td>
						</tr>
					)}
				</tbody>
			</table>
		</div>
	);
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
					if (e.target !== e.currentTarget) return;
					if (e.key === "Enter" || e.key === " ") {
						e.preventDefault();
						onToggle();
					}
				}}
				aria-expanded={isExpanded}
				data-testid="pprof-goroutine-toggle"
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
					onKeyDown={(e) => e.stopPropagation()}
					onClick={(e) => {
						e.stopPropagation();
						const filePath = getStackFilePath(group.stack);
						if (filePath) onSkip(filePath);
					}}
					data-testid="pprof-goroutine-skip"
					className="shrink-0 rounded p-1.5 text-zinc-600 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-zinc-500 hover:bg-zinc-700 hover:text-zinc-300"
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
	const [allocationSort, setAllocationSort] = useState<AllocationSortState>({ field: "bytes", direction: "desc" });
	const [inuseSort, setInuseSort] = useState<AllocationSortState>({ field: "bytes", direction: "desc" });
	const [expandedAlloc, setExpandedAlloc] = useState<Set<string>>(new Set());
	const [expandedInuse, setExpandedInuse] = useState<Set<string>>(new Set());
	const [expandedLeaks, setExpandedLeaks] = useState<Set<string>>(new Set());
	const inuseHistoryRef = useRef<Map<string, number[]>>(new Map());
	const lastInuseSnapshotRef = useRef<string | null>(null);
	const [historyVersion, setHistoryVersion] = useState(0);

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
	const sortedAllocations = useMemo(
		() => sortAllocations(data?.top_allocations ?? [], allocationSort),
		[data?.top_allocations, allocationSort],
	);
	const sortedInuseAllocations = useMemo(
		() => sortAllocations(data?.inuse_allocations ?? [], inuseSort),
		[data?.inuse_allocations, inuseSort],
	);

	// Roll a ~60s window of inuse bytes per stack signature so we can detect
	// sites whose live memory grows monotonically across polls. Dedupe on
	// data.timestamp (stamped fresh by the backend each poll) rather than
	// array identity: RTK Query's default structural sharing reuses the
	// inuse_allocations reference when the snapshot is deep-equal, which
	// would silently skip samples on idle polls and shrink the window.
	useEffect(() => {
		const inuse = data?.inuse_allocations;
		const snapshotTs = data?.timestamp;
		if (!inuse || !snapshotTs || lastInuseSnapshotRef.current === snapshotTs) return;
		lastInuseSnapshotRef.current = snapshotTs;
		const map = inuseHistoryRef.current;
		const seen = new Set<string>();
		for (const l of inuse) {
			const key = makeStackKey(l.stack);
			seen.add(key);
			const samples = map.get(key) ?? [];
			samples.push(l.bytes);
			while (samples.length > LEAK_MAX_SAMPLES) samples.shift();
			map.set(key, samples);
		}
		// Drop sites absent from the latest snapshot (either freed or evicted
		// from top-N) so the map stays bounded.
		for (const key of [...map.keys()]) {
			if (!seen.has(key)) map.delete(key);
		}
		setHistoryVersion((v) => v + 1);
	}, [data?.timestamp, data?.inuse_allocations]);

	const leakCandidates = useMemo(
		() => detectLeaks(data?.top_allocations ?? [], data?.inuse_allocations ?? [], inuseHistoryRef.current),
		// historyVersion bumps when the ref is mutated; top/inuse refs change per poll
		[data?.top_allocations, data?.inuse_allocations, historyVersion],
	);

	const leakSummary = useMemo(() => {
		const counts: Record<LeakSeverity, number> = { high: 0, medium: 0, low: 0 };
		for (const c of leakCandidates) counts[c.severity]++;
		return counts;
	}, [leakCandidates]);

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

	const handleInuseSort = useCallback((field: AllocationSortField) => {
		setInuseSort((prev) => ({
			field,
			direction: prev.field === field && prev.direction === "desc" ? "asc" : "desc",
		}));
	}, []);

	const toggleAllocExpand = useCallback((key: string) => {
		setExpandedAlloc((prev) => {
			const next = new Set(prev);
			if (next.has(key)) {
				next.delete(key);
			} else {
				next.add(key);
			}
			return next;
		});
	}, []);

	const toggleInuseExpand = useCallback((key: string) => {
		setExpandedInuse((prev) => {
			const next = new Set(prev);
			if (next.has(key)) {
				next.delete(key);
			} else {
				next.add(key);
			}
			return next;
		});
	}, []);

	const toggleLeakExpand = useCallback((key: string) => {
		setExpandedLeaks((prev) => {
			const next = new Set(prev);
			if (next.has(key)) {
				next.delete(key);
			} else {
				next.add(key);
			}
			return next;
		});
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
	if (error && !data) {
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
						data-testid="pprof-data-refresh"
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

					{/* Potential Leaks — stacks accumulating live memory without being freed */}
					<div className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900">
						<div className="border-b border-zinc-800 px-4 py-3">
							<div className="flex items-center gap-2">
								<AlertTriangle className="h-4 w-4 text-amber-400" />
								<span className="font-medium text-zinc-300">Potential Leaks</span>
								<span className="text-sm text-zinc-500">({leakCandidates.length} suspicious)</span>
								{leakSummary.high > 0 && (
									<span className="rounded border border-red-400/20 bg-red-400/10 px-2 py-0.5 text-xs text-red-400">
										{leakSummary.high} high
									</span>
								)}
								{leakSummary.medium > 0 && (
									<span className="rounded border border-amber-400/20 bg-amber-400/10 px-2 py-0.5 text-xs text-amber-400">
										{leakSummary.medium} medium
									</span>
								)}
								{leakSummary.low > 0 && (
									<span className="rounded border border-zinc-400/20 bg-zinc-400/10 px-2 py-0.5 text-xs text-zinc-400">
										{leakSummary.low} low
									</span>
								)}
							</div>
							<p className="mt-1 text-xs text-zinc-500">
								Stacks whose live bytes remain a large fraction of what they ever allocated (retention), optionally with
								live bytes trending upward over the last minute. Growth + high retention together is the strongest leak
								signal.
							</p>
						</div>
						<LeakTable candidates={leakCandidates} expandedKeys={expandedLeaks} onToggle={toggleLeakExpand} />
					</div>

					{/* Live Heap Allocations — what's currently consuming the heap */}
					<div className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900">
						<div className="border-b border-zinc-800 px-4 py-3">
							<div className="flex items-center gap-2">
								<HardDrive className="h-4 w-4 text-emerald-400" />
								<span className="font-medium text-zinc-300">Live Heap Allocations</span>
								<span className="text-sm text-zinc-500">({sortedInuseAllocations.length} sites)</span>
							</div>
							<p className="mt-1 text-xs text-zinc-500">
								Call stacks currently holding memory on the heap right now — expand a row to see the full stack.
							</p>
						</div>
						<AllocationTable
							allocations={sortedInuseAllocations}
							sortField={inuseSort.field}
							sortDirection={inuseSort.direction}
							onSort={handleInuseSort}
							expandedKeys={expandedInuse}
							onToggle={toggleInuseExpand}
							bytesColorClass="text-emerald-400"
							testIdPrefix="pprof-inuse-sort"
						/>
					</div>

					{/* Cumulative Memory Allocations — total since process start */}
					<div className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900">
						<div className="border-b border-zinc-800 px-4 py-3">
							<div className="flex items-center gap-2">
								<HardDrive className="h-4 w-4 text-rose-400" />
								<span className="font-medium text-zinc-300">Cumulative Memory Allocations</span>
								<span className="text-sm text-zinc-500">({sortedAllocations.length} sites)</span>
							</div>
							<p className="mt-1 text-xs text-zinc-500">
								Total bytes allocated since process start (includes memory already freed) — expand a row to see the full stack.
							</p>
						</div>
						<AllocationTable
							allocations={sortedAllocations}
							sortField={allocationSort.field}
							sortDirection={allocationSort.direction}
							onSort={handleAllocationSort}
							expandedKeys={expandedAlloc}
							onToggle={toggleAllocExpand}
						/>
					</div>

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
									data-testid="pprof-goroutine-clearskipped"
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
