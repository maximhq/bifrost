/**
 * Routing Tree View — left-to-right flow lane.
 *
 * Source → conditions (shared prefix / OR merge) → rule node → target node(s)
 *
 * OR branches are split into parallel paths that converge on the same
 * shared child via subtree-hash deduplication.  Each rule target gets its
 * own leaf node.  Nodes are draggable for exploration; nothing is editable.
 */

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useGetRoutingRulesQuery } from "@/lib/store/apis/routingRulesApi";
import { useNavigate } from "@tanstack/react-router";
import type { Node, NodeChange } from "@xyflow/react";
import { Background, BackgroundVariant, Controls, Panel, ReactFlow, useEdgesState, useNodesState } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { AlertCircle, ArrowLeft, GitBranch, Info, Link2, Loader2, RotateCcw, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useCookies } from "react-cookie";
import { FIT_VIEW_PADDING, SCOPE_CONFIG, SCOPE_ORDER } from "./constants";
import { buildGraph } from "./graphBuilder";
import { RFConditionNode } from "./node/rfConditionNode";
import { RFRuleNode } from "./node/rfRuleNode";
import { RFSourceNode } from "./node/rfSourceNode";
import { POSITIONS_COOKIE, PositionCookie, computeFingerprint } from "./positionPersistence";
import { RfChainEdge } from "./rfChainEdge";

// ─── Node types (stable reference) ────────────────────────────────────────

const nodeTypes = {
	rfSource: RFSourceNode,
	rfCondition: RFConditionNode,
	rfRule: RFRuleNode,
};

const edgeTypes = { rfChain: RfChainEdge };

// ─── Main component ────────────────────────────────────────────────────────

export function RoutingTreeView() {
	const navigate = useNavigate();
	const { data, isLoading, isError } = useGetRoutingRulesQuery({ limit: 500 });
	const rules = data?.rules ?? [];

	// ── Position persistence ───────────────────────────────────────────────
	const [cookies, setCookie, removeCookie] = useCookies([POSITIONS_COOKIE]);

	// React Flow instance — captured via onInit so we can call fitView imperatively.
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	const rfInstanceRef = useRef<any>(null);
	const resetTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	useEffect(
		() => () => {
			if (resetTimeoutRef.current) clearTimeout(resetTimeoutRef.current);
		},
		[],
	);

	// Capture cookie value once on mount so re-saves don't trigger re-renders.
	const [initialCookie] = useState<PositionCookie | undefined>(() => cookies[POSITIONS_COOKIE] as PositionCookie | undefined);

	const fingerprint = useMemo(() => computeFingerprint(rules), [rules]);

	const { baseNodes, baseEdges } = useMemo(() => {
		const g = buildGraph(rules);
		return { baseNodes: g.nodes, baseEdges: g.edges };
	}, [rules]);

	// If the cookie fingerprint matches current rules, restore saved positions.
	const { mergedNodes, positionsRestored } = useMemo(() => {
		if (initialCookie?.fingerprint === fingerprint && initialCookie?.positions && Object.keys(initialCookie.positions).length > 0) {
			return {
				mergedNodes: baseNodes.map((n) => ({
					...n,
					position: initialCookie.positions[n.id] ?? n.position,
				})),
				positionsRestored: true,
			};
		}
		return { mergedNodes: baseNodes, positionsRestored: false };
	}, [baseNodes, fingerprint, initialCookie]);

	const [nodes, setNodes, onNodesChange] = useNodesState(mergedNodes);
	const [edges, setEdges, onEdgesChange] = useEdgesState(baseEdges);

	useEffect(() => {
		setNodes(mergedNodes);
	}, [mergedNodes, setNodes]);
	useEffect(() => {
		setEdges(baseEdges);
	}, [baseEdges, setEdges]);

	// Always reflect the latest nodes in a ref so the save handler is not stale.
	const nodesRef = useRef(nodes);
	nodesRef.current = nodes;

	// Tracks the last data written so position-save and viewport-save don't clobber each other.
	const cookieDataRef = useRef<Omit<PositionCookie, "fingerprint">>({ positions: {}, viewport: undefined });

	// Once positions are known to be restored, seed the ref so viewport-only saves keep positions.
	useEffect(() => {
		if (positionsRestored && initialCookie) {
			cookieDataRef.current = { positions: initialCookie.positions, viewport: initialCookie.viewport };
		}
	}, [positionsRestored, initialCookie]);

	const writeCookie = useCallback(
		(update: Partial<Omit<PositionCookie, "fingerprint">>) => {
			cookieDataRef.current = { ...cookieDataRef.current, ...update };
			setCookie(POSITIONS_COOKIE, { fingerprint, ...cookieDataRef.current } satisfies PositionCookie, {
				path: "/",
				maxAge: 30 * 24 * 60 * 60, // 30 days
			});
		},
		[fingerprint, setCookie],
	);

	// Save positions to cookie when a drag ends.
	const handleNodesChange = useCallback(
		(changes: NodeChange[]) => {
			onNodesChange(changes);
			const hasDragEnd = changes.some((c) => c.type === "position" && c.dragging === false);
			if (!hasDragEnd) return;

			const posMap: Record<string, { x: number; y: number }> = {};
			for (const n of nodesRef.current) posMap[n.id] = n.position;
			// Apply the final positions from the change events themselves (state not yet flushed).
			for (const c of changes) {
				if (c.type === "position" && c.dragging === false && c.position) {
					posMap[c.id] = c.position;
				}
			}
			writeCookie({ positions: posMap });
		},
		[onNodesChange, writeCookie],
	);

	// Save viewport (pan + zoom) when the user stops moving.
	const handleMoveEnd = useCallback(
		(_: unknown, viewport: { x: number; y: number; zoom: number }) => {
			writeCookie({ viewport });
		},
		[writeCookie],
	);

	// Reset all saved positions and re-fit to the computed default layout.
	const handleResetLayout = useCallback(() => {
		removeCookie(POSITIONS_COOKIE, { path: "/" });
		cookieDataRef.current = { positions: {}, viewport: undefined };
		setNodes(baseNodes);
		setSelectedNodeId(null);
		setSelectedEdgeId(null);
		if (resetTimeoutRef.current) clearTimeout(resetTimeoutRef.current);
		resetTimeoutRef.current = setTimeout(() => rfInstanceRef.current?.fitView({ padding: FIT_VIEW_PADDING, duration: 300 }), 50);
	}, [baseNodes, removeCookie, setNodes]);

	// ── Selection / path highlight ────────────────────────────────────────
	const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
	const [selectedEdgeId, setSelectedEdgeId] = useState<string | null>(null);

	/**
	 * BFS up (forward edges only — skips chain-backs) to find all ancestors,
	 * and BFS down (all edges including chain-backs) to find all descendants.
	 * Chain-back edges are identified by their id ending in "-chain".
	 */
	const selectedHighlightIds = useMemo<Set<string> | null>(() => {
		// Edge selection: highlight only the two endpoint nodes + the edge itself.
		if (selectedEdgeId) {
			const edge = edges.find((e) => e.id === selectedEdgeId);
			if (!edge) return null;
			return new Set<string>([edge.source, edge.target]);
		}

		if (!selectedNodeId) return null;

		const childrenOf = new Map<string, string[]>();
		const parentsOf = new Map<string, string[]>();
		for (const e of edges) {
			if (!childrenOf.has(e.source)) childrenOf.set(e.source, []);
			childrenOf.get(e.source)!.push(e.target);
			// Exclude chain-back edges from the ancestor map (they reverse flow direction).
			if (!e.id.endsWith("-chain")) {
				if (!parentsOf.has(e.target)) parentsOf.set(e.target, []);
				parentsOf.get(e.target)!.push(e.source);
			}
		}

		const highlighted = new Set<string>([selectedNodeId]);

		const upQ = [selectedNodeId];
		while (upQ.length) {
			const id = upQ.pop()!;
			for (const p of parentsOf.get(id) ?? []) {
				if (!highlighted.has(p)) {
					highlighted.add(p);
					upQ.push(p);
				}
			}
		}

		const downQ = [selectedNodeId];
		while (downQ.length) {
			const id = downQ.pop()!;
			for (const c of childrenOf.get(id) ?? []) {
				if (!highlighted.has(c)) {
					highlighted.add(c);
					downQ.push(c);
				}
			}
		}

		return highlighted;
	}, [selectedNodeId, selectedEdgeId, edges]);

	const handleNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
		setSelectedEdgeId(null);
		setSelectedNodeId((prev) => (prev === node.id ? null : node.id));
	}, []);

	const handleEdgeClick = useCallback((_: React.MouseEvent, edge: { id: string }) => {
		setSelectedNodeId(null);
		setSelectedEdgeId((prev) => (prev === edge.id ? null : edge.id));
	}, []);

	const handlePaneClick = useCallback(() => {
		setSelectedNodeId(null);
		setSelectedEdgeId(null);
	}, []);

	// ── Search / highlight ─────────────────────────────────────────────────
	const [search, setSearch] = useState("");

	/**
	 * Returns null when search is empty (no filtering).
	 * Returns an empty Set when there are no matches (dim everything).
	 * Otherwise returns the set of node IDs that should stay visible:
	 *   directly matching nodes + all their ancestors + all their descendants.
	 */
	const highlightedIds = useMemo<Set<string> | null>(() => {
		const q = search.trim().toLowerCase();
		if (!q) return null;

		const childrenOf = new Map<string, string[]>();
		const parentsOf = new Map<string, string[]>();
		for (const e of edges) {
			if (!childrenOf.has(e.source)) childrenOf.set(e.source, []);
			childrenOf.get(e.source)!.push(e.target);
			if (!parentsOf.has(e.target)) parentsOf.set(e.target, []);
			parentsOf.get(e.target)!.push(e.source);
		}

		const matched = new Set<string>();
		for (const n of nodes) {
			const d = n.data as any;
			const cond = (d?.condition as string | undefined)?.toLowerCase();
			const ruleName = (d?.rule?.name as string | undefined)?.toLowerCase();
			const ruleCel = (d?.rule?.cel_expression as string | undefined)?.toLowerCase();
			if (cond?.includes(q) || ruleName?.includes(q) || ruleCel?.includes(q)) {
				matched.add(n.id);
			}
		}

		if (matched.size === 0) return new Set();

		const highlighted = new Set<string>(matched);

		// BFS upstream → source
		const upQ = [...matched];
		while (upQ.length) {
			const id = upQ.pop()!;
			for (const p of parentsOf.get(id) ?? []) {
				if (!highlighted.has(p)) {
					highlighted.add(p);
					upQ.push(p);
				}
			}
		}

		// BFS downstream → rule leaves
		const downQ = [...matched];
		while (downQ.length) {
			const id = downQ.pop()!;
			for (const c of childrenOf.get(id) ?? []) {
				if (!highlighted.has(c)) {
					highlighted.add(c);
					downQ.push(c);
				}
			}
		}

		return highlighted;
	}, [search, nodes, edges]);

	const matchCount = useMemo(() => {
		if (!highlightedIds) return 0;
		return nodes.filter((n) => n.type === "rfRule" && highlightedIds.has(n.id)).length;
	}, [highlightedIds, nodes]);

	// Selection takes priority; search acts as fallback when nothing is selected.
	const activeHighlightIds = selectedHighlightIds ?? highlightedIds;

	// Derived display nodes/edges — keeps opacity layered on top without
	// disturbing drag state (positions stay in the underlying `nodes` state).
	const displayNodes = useMemo(() => {
		const h = activeHighlightIds;
		const dimOpacity = selectedNodeId ? 0.12 : 0.25;
		return nodes.map((n) => {
			const isSelected = n.id === selectedNodeId;
			if (!h) return { ...n, selected: isSelected };
			const active = h.size > 0;
			return {
				...n,
				selected: isSelected,
				style: {
					...n.style,
					opacity: active && !h.has(n.id) ? dimOpacity : 1,
					transition: "opacity 0.15s",
				},
			};
		});
	}, [nodes, activeHighlightIds, selectedNodeId]);

	const displayEdges = useMemo(() => {
		const h = activeHighlightIds;
		const dimOpacity = selectedNodeId || selectedEdgeId ? 0.1 : 0.2;
		if (!h) return edges;
		const active = h.size > 0;
		return edges.map((e) => {
			const endpointsLit = h.has(e.source) && h.has(e.target);
			const isSelectedEdge = e.id === selectedEdgeId;
			const lit = endpointsLit || isSelectedEdge;
			return {
				...e,
				style: {
					...e.style,
					opacity: active && !lit ? dimOpacity : 1,
					transition: "opacity 0.15s",
				},
			};
		});
	}, [edges, activeHighlightIds, selectedNodeId, selectedEdgeId]);

	if (isLoading) {
		return (
			<div className="flex h-full items-center justify-center">
				<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
			</div>
		);
	}
	if (isError) {
		return (
			<div className="text-muted-foreground flex h-full items-center justify-center gap-2">
				<AlertCircle className="h-5 w-5" />
				<span className="text-sm">Failed to load routing rules</span>
			</div>
		);
	}
	if (rules.length === 0) {
		return (
			<div className="text-muted-foreground flex h-full flex-col items-center justify-center gap-3">
				<GitBranch className="h-10 w-10 opacity-20" />
				<p className="text-sm">No routing rules to display</p>
				<Button
					variant="outline"
					size="sm"
					data-testid="routing-tree-back-empty-btn"
					onClick={() => navigate({ to: "/workspace/routing-rules" })}
				>
					<ArrowLeft className="mr-1.5 h-4 w-4" />
					Back to rules
				</Button>
			</div>
		);
	}

	return (
		<ReactFlow
			nodes={displayNodes}
			edges={displayEdges}
			onNodesChange={handleNodesChange}
			onEdgesChange={onEdgesChange}
			onNodeClick={handleNodeClick}
			onEdgeClick={handleEdgeClick}
			onPaneClick={handlePaneClick}
			onInit={(instance) => {
				rfInstanceRef.current = instance;
			}}
			nodeTypes={nodeTypes}
			edgeTypes={edgeTypes}
			fitView={!positionsRestored}
			fitViewOptions={{ padding: FIT_VIEW_PADDING }}
			defaultViewport={positionsRestored ? (initialCookie?.viewport ?? { x: 0, y: 0, zoom: 1 }) : undefined}
			onMoveEnd={handleMoveEnd}
			nodesDraggable={true}
			nodesConnectable={false}
			elementsSelectable={true}
			zoomOnDoubleClick={false}
			proOptions={{ hideAttribution: true }}
		>
			<Background variant={BackgroundVariant.Dots} gap={20} size={1} color="var(--border)" />
			<Controls showInteractive={false} />

			<Panel position="top-left">
				<div className="flex flex-col gap-2">
					{/* Main toolbar */}
					<div className="dark:bg-card flex items-center gap-3 rounded-md border bg-white px-4 py-2.5 shadow-sm">
						<Button
							variant="ghost"
							size="sm"
							className="-ml-1 gap-1.5 !pl-0 hover:bg-transparent"
							data-testid="routing-tree-back-toolbar-btn"
							onClick={() => navigate({ to: "/workspace/routing-rules" })}
						>
							<ArrowLeft className="h-4 w-4" />
							Back
						</Button>
						<div className="bg-border h-5 w-px" />
						<div className="flex items-center gap-2">
							<GitBranch className="text-muted-foreground h-4 w-4" />
							<p className="text-foreground text-sm leading-tight font-semibold">Routing Tree</p>
							<p className="text-muted-foreground text-[11px]">
								{search
									? highlightedIds && highlightedIds.size > 0
										? `${matchCount} rule${matchCount !== 1 ? "s" : ""}`
										: "no match"
									: `${rules.length} rule${rules.length !== 1 ? "s" : ""}`}
							</p>
						</div>
						<div className="bg-border h-5 w-px" />
						<div className="relative">
							<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
							<Input
								value={search}
								onChange={(e) => setSearch(e.target.value)}
								placeholder="Search conditions or rules…"
								className="h-8 w-56 pl-8 text-sm"
							/>
						</div>
						<div className="bg-border h-5 w-px" />
						<Button
							variant="ghost"
							size="sm"
							className="text-muted-foreground hover:text-foreground gap-1.5"
							onClick={handleResetLayout}
							title="Reset to default layout"
							data-testid="routing-tree-reset-layout-btn"
						>
							<RotateCcw className="h-3.5 w-3.5" />
							Reset layout
						</Button>
					</div>
					{/* Scope + edge legend — floats below */}
					<div className="dark:bg-card flex items-center gap-3 rounded-md border bg-white px-3 py-1.5 shadow-sm">
						{SCOPE_ORDER.map((s) => (
							<div key={s} className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: SCOPE_CONFIG[s].color }} />
								<span className="text-muted-foreground text-[10px] font-medium">{SCOPE_CONFIG[s].label}</span>
							</div>
						))}
						<div className="bg-border h-3 w-px" />
						<div className="flex items-center gap-1.5">
							<Link2 className="text-muted-foreground h-2.5 w-2.5" />
							<span className="text-muted-foreground text-[10px] font-medium">Chain rule</span>
						</div>
						<div className="bg-border h-3 w-px" />
						{/* Chain edge styles — both dashed (long = static, short = dynamic); arrows at path midpoint */}
						<div className="flex items-center gap-1.5">
							<svg width="40" height="12" className="shrink-0" aria-hidden>
								<line
									x1="2"
									y1="6"
									x2="38"
									y2="6"
									stroke="var(--muted-foreground)"
									strokeWidth="1.5"
									strokeDasharray="14 10"
									strokeLinecap="round"
								/>
								<polygon points="20,6 14,2.5 14,9.5" fill="var(--muted-foreground)" />
							</svg>
							<span className="text-muted-foreground text-[10px] font-medium">Static chain</span>
							<Tooltip>
								<TooltipTrigger asChild>
									<Info
										className="text-muted-foreground/60 h-2.5 w-2.5 cursor-default"
										data-testid="routing-tree-static-chain-info-trigger"
									/>
								</TooltipTrigger>
								<TooltipContent side="top" className="max-w-[200px] text-center">
									Re-entry point is fully proven by static analysis — every condition on the path evaluated to a known value.
								</TooltipContent>
							</Tooltip>
						</div>
						<div className="flex items-center gap-1.5">
							<svg width="40" height="12" className="shrink-0 overflow-visible" aria-hidden>
								<line
									className="rf-chain-legend-dynamic-dash"
									x1="2"
									y1="6"
									x2="38"
									y2="6"
									stroke="var(--muted-foreground)"
									strokeWidth="1.5"
									strokeDasharray="3 5"
									strokeLinecap="round"
								/>
								<polyline
									points="14,2.5 26,6 14,9.5"
									fill="none"
									stroke="var(--muted-foreground)"
									strokeWidth="1.5"
									strokeLinecap="round"
									strokeLinejoin="round"
								/>
							</svg>
							<span className="text-muted-foreground text-[10px] font-medium">Dynamic chain</span>
							<Tooltip>
								<TooltipTrigger asChild>
									<Info
										className="text-muted-foreground/60 h-2.5 w-2.5 cursor-default"
										data-testid="routing-tree-dynamic-chain-info-trigger"
									/>
								</TooltipTrigger>
								<TooltipContent side="top" className="max-w-[200px] text-center">
									Re-entry point is a conditional — one or more conditions on the path are not fully evaluated at build time.
								</TooltipContent>
							</Tooltip>
						</div>
					</div>
				</div>
			</Panel>
		</ReactFlow>
	);
}