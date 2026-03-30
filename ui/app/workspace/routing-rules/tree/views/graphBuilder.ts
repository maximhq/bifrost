/**
 * Converts a list of RoutingRules into a React Flow node/edge graph.
 *
 * Pipeline:
 *   rules → buildTrie → mergeSubtrees → collectDAGStructure → Dagre LR layout → buildGraph
 */

import { RoutingRule } from "@/lib/types/routingRules";
import dagre from "@dagrejs/dagre";
import type { Edge, Node } from "@xyflow/react";
import { evalChainCondition, expandCEL, normalizeCond } from "./celParser";
import {
	COND_H,
	COND_W,
	DAGRE_MARGIN,
	DAGRE_NODESEP,
	DAGRE_RANKSEP,
	RULE_H,
	RULE_W,
	SCOPE_CONFIG,
	SRC_H,
	SRC_W,
	type ScopeKey,
} from "./constants";

// ─── Color mixing ──────────────────────────────────────────────────────────

function hexToRgb(hex: string): [number, number, number] {
	const n = parseInt(hex.slice(1), 16);
	return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}
function rgbToHex(r: number, g: number, b: number): string {
	return "#" + [r, g, b].map((v) => Math.round(v).toString(16).padStart(2, "0")).join("");
}
/** Weighted-blend hex colours. weights default to equal if omitted. */
function blendColors(colors: string[], weights?: number[]): string {
	if (colors.length === 1) return colors[0];
	const w = weights ?? colors.map(() => 1);
	const total = w.reduce((s, v) => s + v, 0);
	const [r, g, b] = colors
		.map((c, i) => hexToRgb(c).map((ch) => ch * (w[i] / total)) as [number, number, number])
		.reduce(([ar, ag, ab], [cr, cg, cb]) => [ar + cr, ag + cg, ab + cb], [0, 0, 0]);
	return rgbToHex(r, g, b);
}

// ─── Trie / DAG types ─────────────────────────────────────────────────────

export interface TrieNode {
	id: string;
	condition: string | null;
	children: Map<string, TrieNode>;
	terminals: RoutingRule[];
}

interface LNode { id: string; kind: "source" | "condition" | "rule" | "target"; data: any; w: number; h: number; }
interface LEdge { source: string; target: string; label?: string; color?: string; isChainBack?: boolean; isChainWeak?: boolean; sourceHandle?: string; targetHandle?: string; }

// ─── Trie construction ────────────────────────────────────────────────────

export function buildTrie(rules: RoutingRule[]): TrieNode {
	let uid = 0;
	const mkNode = (c: string | null): TrieNode =>
		({ id: c === null ? "root" : `n${++uid}`, condition: c, children: new Map(), terminals: [] });
	const root = mkNode(null);

	// Pre-collect all (rule, normalized-path) pairs so we can compute frequencies.
	const allPaths: { rule: RoutingRule; path: string[] }[] = [];
	for (const rule of rules) {
		for (const path of expandCEL(rule.cel_expression ?? "")) {
			allPaths.push({ rule, path: path.map(normalizeCond) });
		}
	}

	// Count how many paths each condition appears in.
	// Conditions shared by more paths sort earlier → maximum prefix sharing.
	const freq = new Map<string, number>();
	for (const { path } of allPaths) {
		for (const cond of new Set(path)) {
			freq.set(cond, (freq.get(cond) ?? 0) + 1);
		}
	}

	// Insert into trie with paths sorted by frequency desc, then alphabetically.
	for (const { rule, path } of allPaths) {
		const sorted = [...path].sort((a, b) => {
			const d = (freq.get(b) ?? 0) - (freq.get(a) ?? 0);
			return d !== 0 ? d : a.localeCompare(b);
		});
		let node = root;
		for (const cond of sorted) {
			if (!node.children.has(cond)) node.children.set(cond, mkNode(cond));
			node = node.children.get(cond)!;
		}
		if (!node.terminals.find((r) => r.id === rule.id)) node.terminals.push(rule);
	}

	return root;
}

/** Merge structurally identical subtrees so OR-expanded duplicates share one node. */
export function mergeSubtrees(root: TrieNode): void {
	const registry = new Map<string, TrieNode>();
	const nodeCanon = new Map<string, string>();

	function canon(node: TrieNode, seen = new Set<string>()): string {
		if (nodeCanon.has(node.id)) return nodeCanon.get(node.id)!;
		if (seen.has(node.id)) return node.id;
		seen.add(node.id);
		const termKey = node.terminals.map((r) => r.id).sort().join(",");
		const childKey = Array.from(node.children.entries())
			.map(([c, ch]) => `${c}:${canon(ch, new Set(seen))}`).sort().join("|");
		const key = `${node.condition}::${termKey}::${childKey}`;
		nodeCanon.set(node.id, key);
		if (!registry.has(key)) registry.set(key, node);
		return key;
	}

	function postOrder(node: TrieNode, seen = new Set<string>()): void {
		if (seen.has(node.id)) return;
		seen.add(node.id);
		for (const ch of node.children.values()) postOrder(ch, seen);
		canon(node);
	}
	postOrder(root);

	function replace(node: TrieNode, seen = new Set<string>()): void {
		if (seen.has(node.id)) return;
		seen.add(node.id);
		for (const [cond, ch] of Array.from(node.children.entries())) {
			const canonical = registry.get(nodeCanon.get(ch.id)!)!;
			if (canonical.id !== ch.id) node.children.set(cond, canonical);
			replace(canonical, seen);
		}
	}
	replace(root);
}

// ─── Scope colour helpers ──────────────────────────────────────────────────

function collectTerminals(node: TrieNode, seen = new Set<string>()): RoutingRule[] {
	if (seen.has(node.id)) return [];
	seen.add(node.id);
	const acc = [...node.terminals];
	for (const ch of node.children.values()) acc.push(...collectTerminals(ch, seen));
	return acc;
}

function nodeColor(node: TrieNode, cache?: Map<string, string | null>): string | null {
	if (cache?.has(node.id)) return cache.get(node.id)!;
	const rules = collectTerminals(node);
	if (!rules.length) { cache?.set(node.id, null); return null; }
	// Deduplicate by rule.id before counting — collectTerminals returns one entry
	// per OR-expanded path, so a multi-branch rule would otherwise be over-counted.
	const uniqueRules = [...new Map(rules.map((r) => [r.id, r])).values()];
	// Count rules per scope to produce a weighted blend.
	const counts = new Map<string, number>();
	for (const r of uniqueRules) counts.set(r.scope, (counts.get(r.scope) ?? 0) + 1);
	const entries = [...counts.entries()]
		.map(([scope, count]): { color: string | undefined; count: number } => ({
			color: SCOPE_CONFIG[scope as ScopeKey]?.color,
			count,
		}))
		.filter((e): e is { color: string; count: number } => !!e.color);
	const result = entries.length ? blendColors(entries.map((e) => e.color), entries.map((e) => e.count)) : null;
	cache?.set(node.id, result);
	return result;
}

// ─── DAG structure collection ─────────────────────────────────────────────

function collectDAGStructure(root: TrieNode): { lNodes: LNode[]; lEdges: LEdge[] } {
	const colorCache = new Map<string, string | null>();
	const lNodes: LNode[] = [{ id: "source", kind: "source", data: {}, w: SRC_W, h: SRC_H }];
	const lEdges: LEdge[] = [];
	const addedNodes = new Set<string>(["source"]);
	const addedEdges = new Set<string>();
	const processed = new Set<string>();
	const chainQueue: { ruleId: string; rule: RoutingRule; sc: string }[] = [];

	function addEdge(src: string, tgt: string, label?: string, color?: string, opts?: Partial<LEdge>) {
		const key = `${src}→${tgt}${opts?.isChainBack ? ":chain" : ""}`;
		if (addedEdges.has(key)) return;
		addedEdges.add(key);
		lEdges.push({ source: src, target: tgt, label, color, ...opts });
	}

	function traverse(node: TrieNode, parentId: string) {
		const isRoot = node.condition === null;
		const selfId = isRoot ? "source" : node.id;

		if (!isRoot) {
			if (!addedNodes.has(selfId)) {
				const color = nodeColor(node, colorCache);
				const terminalRules = collectTerminals(node);
				const scopes = [...new Set(terminalRules.map((r) => r.scope))];
				lNodes.push({ id: selfId, kind: "condition", data: { condition: node.condition, color, scopes }, w: COND_W, h: COND_H });
				addedNodes.add(selfId);
			}
			addEdge(parentId, selfId, undefined, nodeColor(node, colorCache) ?? undefined);
		}

		// Don't re-traverse a shared node's subtree from a second parent
		if (!isRoot && processed.has(selfId)) return;
		processed.add(selfId);

		for (const ch of node.children.values()) traverse(ch, selfId);

		for (const rule of node.terminals) {
			const ruleId = `rule-${rule.id}`;
			const sc = SCOPE_CONFIG[rule.scope as ScopeKey]?.color ?? "#9ca3af";
			if (!addedNodes.has(ruleId)) {
				lNodes.push({ id: ruleId, kind: "rule", data: { rule, scopeColor: sc }, w: RULE_W, h: RULE_H });
				addedNodes.add(ruleId);
			}
			addEdge(selfId, ruleId, undefined, sc);
			if (rule.chain_rule) chainQueue.push({ ruleId, rule, sc });
		}
	}

	traverse(root, "");

	// ── Second pass: chain edges to specific matching condition nodes ──────
	// For each chain rule, evaluate its resolved targets against every
	// condition node reachable from source. Connect to the first satisfied
	// condition in each path so the edge shows exactly where the chain lands.
	if (chainQueue.length > 0) {
		// Build an adjacency list (forward edges only, by definition at this point)
		const childrenOf = new Map<string, string[]>();
		for (const e of lEdges) {
			if (!childrenOf.has(e.source)) childrenOf.set(e.source, []);
			childrenOf.get(e.source)!.push(e.target);
		}
		const nodeById = new Map(lNodes.map((n) => [n.id, n]));

		/** Walk forward from `startIds`, following only condition nodes, and
		 *  return the deepest node in each branch whose condition evaluates to
		 *  true for `vars`. Only emits a node if none of its condition children
		 *  also evaluate to true (deepest satisfied entry point semantics).
		 *
		 *  Each result carries a `strong` flag: true when every condition on the
		 *  path evaluated to `true` (static chain), false when any condition was
		 *  `null` / too complex to evaluate (dynamic chain). */
		function findEntries(startIds: string[], vars: Record<string, string>): Array<{ id: string; strong: boolean }> {
			const results: Array<{ id: string; strong: boolean }> = [];
			const visited = new Set<string>();

			/** Returns true if this node (or any descendant condition) matched.
			 *  `strong` is false once we have passed through any `null` hop. */
			function explore(id: string, strong: boolean): boolean {
				if (visited.has(id)) return false;
				visited.add(id);
				const node = nodeById.get(id);
				if (!node || node.kind !== "condition") return false;

				const result = evalChainCondition(node.data.condition as string, vars);
				if (result === false) return false; // branch blocked

				if (result === true) {
					// Continue into children — prefer the deepest static match
					let hasDeeper = false;
					for (const childId of childrenOf.get(id) ?? []) {
						if (explore(childId, strong)) hasDeeper = true;
					}
					if (!hasDeeper) results.push({ id, strong });
					return true;
				}

				// result === null (too complex) — explore children but mark as weak
				let anyMatch = false;
				for (const childId of childrenOf.get(id) ?? []) {
					if (explore(childId, false)) anyMatch = true;
				}
				return anyMatch;
			}

			for (const id of startIds) explore(id, true);
			return results;
		}

		for (const { ruleId, rule, sc } of chainQueue) {
			// Collect unique (provider, model) pairs across all targets
			const seen = new Set<string>();
			for (const t of rule.targets) {
				const vars: Record<string, string> = {};
				if (t.provider) vars.provider = t.provider;
				if (t.model) vars.model = t.model;
				if (!Object.keys(vars).length) {
					// passthrough target — chain loops back to source (static: we know the input is unchanged)
					addEdge(ruleId, "source", "↺", sc, { isChainBack: true, isChainWeak: false, sourceHandle: "chain-out" });
					continue;
				}
				const key = JSON.stringify(vars);
				if (seen.has(key)) continue;
				seen.add(key);

				const entries = findEntries(childrenOf.get("source") ?? [], vars);
				if (entries.length === 0) {
					// resolved vars match no condition node — fall back to source
					addEdge(ruleId, "source", "↺", sc, { isChainBack: true, isChainWeak: false, sourceHandle: "chain-out" });
				}
				for (const { id: condId, strong } of entries) {
					addEdge(ruleId, condId, "↺", sc, { isChainBack: true, isChainWeak: !strong, sourceHandle: "chain-out" });
				}
			}
		}
	}

	return { lNodes, lEdges };
}

// ─── Dagre layered layout (LR) ───────────────────────────────────────────
//
// Uses @dagrejs/dagre with the network-simplex ranker for crossing reduction and
// consistent rank spacing. Chain-back edges are excluded — they are drawn after.

function computeLRLayout(lNodes: LNode[], lEdges: LEdge[]): Map<string, { x: number; y: number }> {
	const g = new dagre.graphlib.Graph({ multigraph: false });
	g.setGraph({
		rankdir: "LR",
		// Network-simplex tends to produce cleaner orderings than longest-path on DAGs.
		ranker: "network-simplex",
		ranksep: DAGRE_RANKSEP,
		nodesep: DAGRE_NODESEP,
		edgesep: 16,
		marginx: DAGRE_MARGIN,
		marginy: DAGRE_MARGIN,
		align: "UL",
	});
	g.setDefaultEdgeLabel(() => ({}));

	for (const n of lNodes) {
		g.setNode(n.id, { width: n.w, height: n.h });
	}

	const forward = lEdges.filter((e) => !e.isChainBack);
	const edgeKey = new Set<string>();
	for (const e of forward) {
		const k = `${e.source}\0${e.target}`;
		if (edgeKey.has(k)) continue;
		edgeKey.add(k);
		if (g.hasNode(e.source) && g.hasNode(e.target)) {
			g.setEdge(e.source, e.target);
		}
	}

	dagre.layout(g);

	const positions = new Map<string, { x: number; y: number }>();
	for (const n of lNodes) {
		const laid = g.node(n.id);
		if (laid === undefined) continue;
		// Dagre uses x,y as the centre of each node.
		positions.set(n.id, {
			x: laid.x - n.w / 2,
			y: laid.y - n.h / 2,
		});
	}

	// Dagre pins the first rank to the top of the layout; shift "source" so its
	// vertical centre matches the midpoint of every *other* node's bounding box.
	centerSourceVertically(positions, lNodes);

	// Pull the source node an extra gap to the left so it visually breathes
	// away from the first condition column.
	const sourcePos = positions.get("source");
	if (sourcePos) sourcePos.x -= 200;

	return positions;
}

/** Move the source node so it sits at the vertical centre of the rest of the graph. */
function centerSourceVertically(
	positions: Map<string, { x: number; y: number }>,
	lNodes: LNode[],
): void {
	const sourceEntry = lNodes.find((n) => n.id === "source");
	const sourcePos = positions.get("source");
	if (!sourceEntry || !sourcePos) return;

	const others = lNodes.filter((n) => n.id !== "source");
	if (others.length === 0) return;

	let minTop = Infinity;
	let maxBottom = -Infinity;
	for (const n of others) {
		const p = positions.get(n.id);
		if (!p) continue;
		minTop = Math.min(minTop, p.y);
		maxBottom = Math.max(maxBottom, p.y + n.h);
	}
	if (!Number.isFinite(minTop) || !Number.isFinite(maxBottom)) return;

	const midY = (minTop + maxBottom) / 2;
	sourcePos.y = midY - sourceEntry.h / 2;
}

// ─── Build React Flow graph ────────────────────────────────────────────────

export function buildGraph(rules: RoutingRule[]): { nodes: Node[]; edges: Edge[] } {
	const trie = buildTrie(rules);
	mergeSubtrees(trie);
	const { lNodes, lEdges } = collectDAGStructure(trie);
	// Chain-back edges form cycles — exclude them from layout (forward edges only).
	const positions = computeLRLayout(lNodes, lEdges.filter((e) => !e.isChainBack));

	const kindType: Record<string, string> = {
		source: "rfSource", condition: "rfCondition", rule: "rfRule",
	};

	const rfNodes: Node[] = lNodes.map((ln) => ({
		id: ln.id,
		type: kindType[ln.kind],
		position: positions.get(ln.id) ?? { x: 0, y: 0 },
		data: ln.data,
		draggable: true,
		selectable: true,
		connectable: false,
	}));

	const rfEdges: Edge[] = lEdges.map((le) => {
		const base = {
			id: `e-${le.source}-${le.target}${le.isChainBack ? "-chain" : ""}`,
			source: le.source,
			target: le.target,
			...(le.sourceHandle ? { sourceHandle: le.sourceHandle } : {}),
			...(le.targetHandle ? { targetHandle: le.targetHandle } : {}),
		};
		if (le.isChainBack) {
			// Both dashed: longer dashes (static) vs shorter dashes (dynamic). Mid-arrow in rfChainEdge.
			const weak = le.isChainWeak;
			return {
				...base,
				type: "rfChain",
				data: { chainWeak: weak },
				animated: false,
				...(weak ? { className: "rf-chain-edge-dynamic" } : {}),
				style: {
					stroke: le.color,
					strokeWidth: 1.5,
					strokeLinecap: "round",
					...(weak ? {} : { strokeDasharray: "14 10" }),
				},
			};
		}
		return {
			...base,
			type: "simplebezier",
			style: { stroke: le.color ?? "var(--border)", strokeWidth: le.color ? 1.5 : 1 },
		};
	});

	return { nodes: rfNodes, edges: rfEdges };
}
