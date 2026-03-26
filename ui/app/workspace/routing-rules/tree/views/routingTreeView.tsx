/**
 * Routing Tree View — left-to-right flow lane.
 *
 * Source → conditions (shared prefix / OR merge) → rule node → target node(s)
 *
 * OR branches are split into parallel paths that converge on the same
 * shared child via subtree-hash deduplication.  Each rule target gets its
 * own leaf node.  Nodes are draggable for exploration; nothing is editable.
 */

"use client";

import { useGetRoutingRulesQuery } from "@/lib/store/apis/routingRulesApi";
import { RoutingRule } from "@/lib/types/routingRules";
import {
	ReactFlow,
	Background,
	Controls,
	Panel,
	Handle,
	Position,
	BackgroundVariant,
	useNodesState,
	useEdgesState,
} from "@xyflow/react";
import type { Node, Edge, NodeChange } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ArrowLeft, GitBranch, Link2, Network, AlertCircle, Loader2, Search, X } from "lucide-react";
import { useRouter } from "next/navigation";
import { useCallback, useMemo, useEffect, useRef, useState } from "react";
import { useCookies } from "react-cookie";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";

// ─── Scope config ──────────────────────────────────────────────────────────

const SCOPE_CONFIG = {
	virtual_key: { label: "Virtual Key", color: "#7c3aed", headerClass: "bg-purple-100 dark:bg-purple-900/30" },
	team: { label: "Team", color: "#2563eb", headerClass: "bg-blue-100 dark:bg-blue-900/30" },
	customer: { label: "Customer", color: "#16a34a", headerClass: "bg-green-100 dark:bg-green-900/30" },
	global: { label: "Global", color: "#6b7280", headerClass: "bg-gray-100 dark:bg-gray-800/30" },
} as const;

type ScopeKey = keyof typeof SCOPE_CONFIG;

// ─── Chain condition evaluator ─────────────────────────────────────────────

/**
 * Evaluate a single normalised CEL clause against a resolved variable map.
 * Only handles simple equality/inequality patterns (field == "v", "v" == field,
 * field != "v", "v" != field). Returns null when too complex to evaluate.
 */
function evalChainCondition(cond: string, vars: Record<string, string>): boolean | null {
	const s = cond.trim();
	let m: RegExpMatchArray | null;

	// Simple equality: field == "v" or "v" == field
	m = s.match(/^(\w+)\s*==\s*["']([^"']*)["']$/);
	if (m && m[1] in vars) return vars[m[1]] === m[2];
	m = s.match(/^["']([^"']*)['"]\s*==\s*(\w+)$/);
	if (m && m[2] in vars) return vars[m[2]] === m[1];

	// Inequality: field != "v" or "v" != field
	m = s.match(/^(\w+)\s*!=\s*["']([^"']*)["']$/);
	if (m && m[1] in vars) return vars[m[1]] !== m[2];
	m = s.match(/^["']([^"']*)['"]\s*!=\s*(\w+)$/);
	if (m && m[2] in vars) return vars[m[2]] !== m[1];

	// startsWith: field.startsWith("prefix")
	m = s.match(/^(\w+)\.startsWith\(["']([^"']*)["']\)$/);
	if (m && m[1] in vars) return vars[m[1]].startsWith(m[2]);

	// contains: field.contains("sub")
	m = s.match(/^(\w+)\.contains\(["']([^"']*)["']\)$/);
	if (m && m[1] in vars) return vars[m[1]].includes(m[2]);

	// in list: field in ["a","b","c"]
	m = s.match(/^(\w+)\s+in\s+\[([^\]]*)\]$/);
	if (m && m[1] in vars) {
		const items = m[2].split(",").map((x) => x.trim().replace(/^["']|["']$/g, ""));
		return items.includes(vars[m[1]]);
	}

	// headers["key"] == "value"
	m = s.match(/^headers\[["']([^"']*)["']\]\s*==\s*["']([^"']*)["']$/);
	if (m) {
		const hVal = vars[`headers.${m[1]}`] ?? vars[`header_${m[1]}`];
		if (hVal !== undefined) return hVal === m[2];
	}

	// Numeric comparisons: field >= n, field <= n, field > n, field < n
	m = s.match(/^(\w+)\s*(>=|<=|>|<)\s*(\d+(?:\.\d+)?)$/);
	if (m && m[1] in vars) {
		const lv = parseFloat(vars[m[1]]);
		const rv = parseFloat(m[3]);
		if (!isNaN(lv)) {
			if (m[2] === ">") return lv > rv;
			if (m[2] === "<") return lv < rv;
			if (m[2] === ">=") return lv >= rv;
			if (m[2] === "<=") return lv <= rv;
		}
	}

	return null; // too complex — skip
}
const SCOPE_ORDER = ["virtual_key", "team", "customer", "global"] as const;

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

// ─── Position persistence ──────────────────────────────────────────────────

const POSITIONS_COOKIE = "bf-routing-tree-positions";

interface PositionCookie {
	fingerprint: string;
	positions: Record<string, { x: number; y: number }>;
	viewport?: { x: number; y: number; zoom: number };
}

/** Changes whenever any rule is added, edited, or deleted. */
function computeFingerprint(rules: RoutingRule[]): string {
	return rules
		.map((r) => `${r.id}:${r.updated_at}`)
		.sort()
		.join("|");
}

// ─── Layout constants (LR: W = horizontal, H = vertical) ──────────────────

const SRC_W = 260; const SRC_H = 80;
const COND_W = 310; const COND_H = 64;
const RULE_W = 220; const RULE_H = 106;
const H_GAP = 360;  // gap between columns
const V_GAP = 60;   // gap between rows within a column

// ─── CEL parser ────────────────────────────────────────────────────────────

function isWrappedInParens(s: string): boolean {
	if (!s.startsWith("(") || !s.endsWith(")")) return false;
	let d = 0;
	for (let i = 0; i < s.length; i++) {
		if (s[i] === "(") d++;
		else if (s[i] === ")") d--;
		if (d === 0 && i < s.length - 1) return false;
	}
	return true;
}

function splitOn(expr: string, op: "&&" | "||"): string[] {
	const s = op === "||" && isWrappedInParens(expr.trim())
		? expr.trim().slice(1, -1)
		: expr.trim();
	const parts: string[] = [];
	let depth = 0, current = "";
	for (let i = 0; i < s.length; i++) {
		const ch = s[i];
		if (ch === "(" || ch === "[") depth++;
		else if (ch === ")" || ch === "]") depth--;
		else if (depth === 0 && s.slice(i, i + 2) === op) {
			const p = current.trim();
			if (p) parts.push(p);
			current = "";
			i++;
			continue;
		}
		current += ch;
	}
	const last = current.trim();
	if (last) parts.push(last);
	if (parts.length < 2) return [expr.trim()];
	return parts;
}

/** Expand a CEL string into one or more condition lists, fanning out on OR. */
function expandCEL(cel: string): string[][] {
	const trimmed = cel?.trim() || "";
	if (!trimmed) return [[]];
	// OR has lower precedence than AND → split on || first (outer level)
	const orBranches = splitOn(trimmed, "||");
	const result: string[][] = [];
	for (const branch of orBranches) {
		const andParts = splitOn(branch.trim(), "&&").map((p) => p.trim()).filter(Boolean);
		result.push(andParts.length ? andParts : [branch.trim()]);
	}
	return result.length ? result : [[]];
}

// ─── Trie / DAG ───────────────────────────────────────────────────────────

interface TrieNode {
	id: string;
	condition: string | null;
	children: Map<string, TrieNode>;
	terminals: RoutingRule[];
}

/**
 * Normalize a CEL condition token for trie key comparison.
 * Collapses whitespace around operators so "a == b" and "a==b" are the same key.
 */
function normalizeCond(cond: string): string {
	return cond.trim()
		.replace(/\s*(==|!=|>=|<=|>|<)\s*/g, (_, op) => ` ${op} `)
		.replace(/\s+/g, " ");
}

function buildTrie(rules: RoutingRule[]): TrieNode {
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
function mergeSubtrees(root: TrieNode): void {
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
	// Count rules per scope to produce a weighted blend.
	const counts = new Map<string, number>();
	for (const r of rules) counts.set(r.scope, (counts.get(r.scope) ?? 0) + 1);
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

// ─── Intermediate graph representation ────────────────────────────────────

interface LNode { id: string; kind: "source" | "condition" | "rule" | "target"; data: any; w: number; h: number; }
interface LEdge { source: string; target: string; label?: string; color?: string; isChainBack?: boolean; isChainWeak?: boolean; sourceHandle?: string; targetHandle?: string; }

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
		 *  path evaluated to `true` (certain chain), false when any condition was
		 *  `null` / too complex to evaluate (dynamic / "maybe" chain). */
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
					// Continue into children — prefer the deepest certain match
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
					// passthrough target — chain loops back to source (certain: we know the input is unchanged)
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

// ─── Left-to-right BFS layer layout with barycenter crossing minimisation ─

function computeLRLayout(lNodes: LNode[], lEdges: LEdge[]): Map<string, { x: number; y: number }> {
	const widthOf = new Map(lNodes.map((n) => [n.id, n.w]));
	const heightOf = new Map(lNodes.map((n) => [n.id, n.h]));

	const childrenOf = new Map<string, string[]>();
	const parentsOf = new Map<string, string[]>();
	for (const { source, target } of lEdges) {
		if (!childrenOf.has(source)) childrenOf.set(source, []);
		childrenOf.get(source)!.push(target);
		if (!parentsOf.has(target)) parentsOf.set(target, []);
		parentsOf.get(target)!.push(source);
	}

	// Longest-path depth: shared/merge nodes land at deepest possible column
	const depth = new Map<string, number>();
	const q: Array<{ id: string; d: number }> = [{ id: "source", d: 0 }];
	while (q.length) {
		const { id, d } = q.shift()!;
		if ((depth.get(id) ?? -1) >= d) continue;
		depth.set(id, d);
		for (const ch of childrenOf.get(id) ?? []) q.push({ id: ch, d: d + 1 });
	}

	const byLayer = new Map<number, string[]>();
	for (const [id, d] of depth) {
		if (!byLayer.has(d)) byLayer.set(d, []);
		byLayer.get(d)!.push(id);
	}

	// X position for each layer = cumulative sum of previous layer widths + H_GAP
	const maxDepth = Math.max(0, ...depth.values());
	const layerMaxW = new Map<number, number>();
	for (const [id, d] of depth)
		layerMaxW.set(d, Math.max(layerMaxW.get(d) ?? 0, widthOf.get(id) ?? COND_W));

	const layerX = new Map<number, number>();
	let xCursor = 0;
	for (let l = 0; l <= maxDepth; l++) {
		layerX.set(l, xCursor);
		xCursor += (layerMaxW.get(l) ?? COND_W) + H_GAP;
	}

	const positions = new Map<string, { x: number; y: number }>();

	// Assign Y positions for one layer given a (possibly reordered) id list
	function placeLayer(l: number, ids: string[]) {
		const x = layerX.get(l) ?? 0;
		const totalH = ids.reduce((s, id) => s + (heightOf.get(id) ?? COND_H), 0)
			+ Math.max(0, ids.length - 1) * V_GAP;
		let y = -totalH / 2;
		for (const id of ids) {
			positions.set(id, { x, y });
			y += (heightOf.get(id) ?? COND_H) + V_GAP;
		}
		byLayer.set(l, ids);
	}

	// Barycenter of a node's parents (for forward sweep)
	function parentBary(id: string): number {
		const ps = parentsOf.get(id) ?? [];
		if (!ps.length) return 0;
		return ps.reduce((s, p) => {
			const pos = positions.get(p);
			return s + (pos ? pos.y + (heightOf.get(p) ?? COND_H) / 2 : 0);
		}, 0) / ps.length;
	}

	// Barycenter of a node's children (for backward sweep)
	function childBary(id: string): number {
		const cs = childrenOf.get(id) ?? [];
		if (!cs.length) return Infinity;
		return cs.reduce((s, c) => {
			const pos = positions.get(c);
			return s + (pos ? pos.y + (heightOf.get(c) ?? COND_H) / 2 : 0);
		}, 0) / cs.length;
	}

	// Initial forward pass
	for (let l = 0; l <= maxDepth; l++) {
		const ids = (byLayer.get(l) ?? []).slice();
		if (l > 0) ids.sort((a, b) => {
			const d = parentBary(a) - parentBary(b);
			return d !== 0 ? d : a.localeCompare(b);
		});
		placeLayer(l, ids);
	}

	// Refinement: alternate forward and backward sweeps (Sugiyama barycenter)
	for (let pass = 0; pass < 4; pass++) {
		// Forward sweep: sort by parent barycentre
		for (let l = 1; l <= maxDepth; l++) {
			const ids = (byLayer.get(l) ?? []).slice();
			ids.sort((a, b) => {
				const d = parentBary(a) - parentBary(b);
				return d !== 0 ? d : a.localeCompare(b);
			});
			placeLayer(l, ids);
		}
		// Backward sweep: sort by child barycentre
		for (let l = maxDepth - 1; l >= 0; l--) {
			const ids = (byLayer.get(l) ?? []).slice();
			ids.sort((a, b) => {
				const d = childBary(a) - childBary(b);
				return d !== 0 ? d : a.localeCompare(b);
			});
			placeLayer(l, ids);
		}
	}

	return positions;
}

// ─── Build React Flow graph ────────────────────────────────────────────────

function buildGraph(rules: RoutingRule[]): { nodes: Node[]; edges: Edge[] } {
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
			// Strong chain (certain): animated bezier — we know exactly where it lands.
			// Weak chain (dynamic/maybe): sparse dotted bezier, dimmed, not animated.
			const weak = le.isChainWeak;
			return {
				...base,
				type: "simplebezier",
				animated: true,
				label: le.label,
				labelStyle: { fontSize: 10, fill: weak ? `${le.color}99` : le.color },
				labelBgStyle: { fill: "transparent" },
				labelBgPadding: [3, 5] as [number, number],
				labelBgBorderRadius: 4,
				style: {
					stroke: le.color,
					strokeWidth: weak ? 1 : 1.5,
					strokeDasharray: weak ? "3 6" : "5 4",
					opacity: weak ? 0.45 : 1,
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

// ─── Custom node components (LR: handles on Left / Right) ─────────────────

function RFSourceNode() {
	return (
		<div
			className="flex flex-col justify-center rounded-xl border-2 border-primary bg-white dark:bg-card px-5 shadow-md cursor-grab active:cursor-grabbing"
			style={{ width: SRC_W, height: SRC_H }}
		>
			<div className="flex items-center gap-2 font-semibold text-foreground">
				<Network className="h-4 w-4 text-primary" />
				Incoming Request
			</div>
			<p className="mt-0.5 text-[11px] text-muted-foreground">provider · model · headers · params · limits</p>
			<Handle type="source" position={Position.Right} style={{ background: "var(--muted-foreground)" }} />
		</div>
	);
}

function RFConditionNode({ data }: { data: any }) {
	const condition = data.condition as string;
	const color = data.color as string | null;
	const scopes = (data.scopes as string[] | undefined) ?? [];
	return (
		<div
			className="flex flex-col gap-1 rounded-lg border-2 bg-white dark:bg-card px-3 py-2.5 shadow-sm cursor-grab active:cursor-grabbing"
			style={{ width: COND_W, minHeight: COND_H, borderColor: color ?? "var(--border)" }}
		>
			<Handle type="target" position={Position.Left} style={{ background: color ?? "var(--muted-foreground)" }} />
			<code className="flex-1 break-all font-mono text-[12px] leading-snug text-foreground">
				{condition}
			</code>
			{scopes.length > 0 && (
				<div className="flex flex-wrap gap-1">
					{scopes.map((sc) => {
						const cfg = SCOPE_CONFIG[sc as ScopeKey];
						return cfg ? (
							<span
								key={sc}
								className="rounded px-1 py-0 text-[9px] font-semibold"
								style={{ backgroundColor: `${cfg.color}18`, color: cfg.color }}
							>
								{cfg.label}
							</span>
						) : null;
					})}
				</div>
			)}
			<Handle type="source" position={Position.Right} style={{ background: color ?? "var(--muted-foreground)" }} />
		</div>
	);
}

function RFRuleNode({ data }: { data: any }) {
	const rule = data.rule as RoutingRule;
	const scopeColor = data.scopeColor as string;
	const cfg = SCOPE_CONFIG[rule.scope as ScopeKey];
	const multi = rule.targets.length > 1;
	const [hovered, setHovered] = useState(false);

	return (
		<div
			className="rounded-lg border-2 bg-white dark:bg-card shadow-sm cursor-grab active:cursor-grabbing"
			style={{ width: RULE_W, borderColor: scopeColor, borderStyle: rule.chain_rule ? "dashed" : "solid" }}
			onMouseEnter={() => setHovered(true)}
			onMouseLeave={() => setHovered(false)}
		>
			<Handle type="target" position={Position.Left} style={{ background: scopeColor }} />
			{rule.chain_rule && (
				<Handle type="source" id="chain-out" position={Position.Bottom} style={{ background: scopeColor }} />
			)}

			{/* scope header */}
			<div className={`flex items-center gap-1.5 rounded-t-[6px] px-3 py-1.5 ${cfg?.headerClass ?? "bg-gray-100 dark:bg-gray-800/30"}`}>
				<span className="h-1.5 w-1.5 flex-shrink-0 rounded-full" style={{ backgroundColor: scopeColor }} />
				<span className="text-[10px] font-semibold" style={{ color: scopeColor }}>
					{cfg?.label ?? rule.scope}
				</span>
				<div className="ml-auto flex items-center gap-1">
					{rule.chain_rule && (
						<Link2 className="h-3 w-3" style={{ color: scopeColor }} />
					)}
					{!rule.enabled && (
						<Badge variant="secondary" className="px-1 py-0 text-[9px]">Off</Badge>
					)}
				</div>
			</div>

			{/* rule name */}
			<div className="px-3 py-2">
				<p className="truncate text-xs font-semibold text-foreground">{rule.name}</p>
				{rule.priority > 0 && (
					<p className="mt-0.5 text-[10px] text-muted-foreground">Priority {rule.priority}</p>
				)}
			</div>

			{/* targets footer */}
			<div
				className="flex items-center gap-1.5 rounded-b-[6px] border-t px-3 py-1.5"
				style={{ borderColor: `${scopeColor}40`, backgroundColor: `${scopeColor}08` }}
			>
				<div className="flex items-center gap-1">
					{rule.targets.slice(0, 4).map((t, i) =>
						t.provider
							? <RenderProviderIcon key={i} provider={t.provider as ProviderIconType} size={12} />
							: <span key={i} className="h-2 w-2 rounded-full bg-muted-foreground/30" />
					)}
					{rule.targets.length > 4 && (
						<span className="text-[9px] text-muted-foreground">+{rule.targets.length - 4}</span>
					)}
				</div>
				<span className="ml-auto text-[10px] text-muted-foreground">
					{rule.targets.length} target{rule.targets.length !== 1 ? "s" : ""}
				</span>
			</div>

			{/* hover popover */}
			{hovered && (
				<div
					className="nodrag nowheel absolute left-full top-0 z-50 ml-3 min-w-[190px] rounded-lg border-2 bg-white dark:bg-card py-1.5 shadow-xl"
					style={{ borderColor: scopeColor }}
				>
					{rule.scope !== "global" && rule.scope_id && (
						<div className="mb-1 border-b px-3 pb-1.5">
							<p className="text-[10px] text-muted-foreground">
								<span className="font-semibold" style={{ color: scopeColor }}>{cfg?.label ?? rule.scope}: </span>
								<span className="font-medium text-foreground">{rule.scope_id}</span>
							</p>
						</div>
					)}
					{rule.chain_rule && (
						<div className="mb-1 flex items-start gap-2 border-b px-3 pb-1.5">
							<Link2 className="mt-0.5 h-3 w-3 shrink-0" style={{ color: scopeColor }} />
							<p className="text-[10px] text-muted-foreground leading-snug">
								Chain rule — resolved provider/model feeds back as the new input and the full scope chain re-evaluates.
							</p>
						</div>
					)}
					<p className="mb-1 px-3 text-[10px] font-semibold uppercase tracking-wide" style={{ color: scopeColor }}>
						{rule.chain_rule ? "Resolved target (new input)" : "Targets"}
					</p>
					{rule.targets.map((t, i) => {
						const isPassthrough = !t.provider && !t.model;
						return (
							<div key={i} className="flex items-center gap-2 px-3 py-1.5 hover:bg-muted">
								{t.provider
									? <RenderProviderIcon provider={t.provider as ProviderIconType} size={13} />
									: <span className="h-3 w-3 flex-shrink-0 rounded-full bg-muted-foreground/30" />
								}
								<div className="min-w-0 flex-1">
									<p className="truncate text-xs font-medium text-foreground">
										{isPassthrough ? "Passthrough" : (t.provider ? getProviderLabel(t.provider) : t.model)}
									</p>
									{t.model && t.provider && (
										<p className="truncate font-mono text-[10px] text-muted-foreground">{t.model}</p>
									)}
									{isPassthrough && (
										<p className="text-[10px] italic text-muted-foreground/60">original provider &amp; model</p>
									)}
								</div>
								{multi && (
									<span className="ml-1 shrink-0 text-[11px] font-semibold" style={{ color: scopeColor }}>
										{Math.round(t.weight * 100)}%
									</span>
								)}
							</div>
						);
					})}
				</div>
			)}
		</div>
	);
}

// ─── Node types (stable reference) ────────────────────────────────────────

const nodeTypes = {
	rfSource: RFSourceNode,
	rfCondition: RFConditionNode,
	rfRule: RFRuleNode,
};

// ─── Main component ────────────────────────────────────────────────────────

export function RoutingTreeView() {
	const router = useRouter();
	const { data, isLoading, isError } = useGetRoutingRulesQuery({ limit: 500 });
	const rules = data?.rules ?? [];

	// ── Position persistence ───────────────────────────────────────────────
	const [cookies, setCookie] = useCookies([POSITIONS_COOKIE]);

	// Capture cookie value once on mount so re-saves don't trigger re-renders.
	const [initialCookie] = useState<PositionCookie | undefined>(
		() => cookies[POSITIONS_COOKIE] as PositionCookie | undefined,
	);

	const fingerprint = useMemo(() => computeFingerprint(rules), [rules]);

	const { baseNodes, baseEdges } = useMemo(() => {
		const g = buildGraph(rules);
		return { baseNodes: g.nodes, baseEdges: g.edges };
	}, [rules]);

	// If the cookie fingerprint matches current rules, restore saved positions.
	const { mergedNodes, positionsRestored } = useMemo(() => {
		if (
			initialCookie?.fingerprint === fingerprint &&
			initialCookie?.positions &&
			Object.keys(initialCookie.positions).length > 0
		) {
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

	useEffect(() => { setNodes(mergedNodes); }, [mergedNodes, setNodes]);
	useEffect(() => { setEdges(baseEdges); }, [baseEdges, setEdges]);

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

	const writeCookie = useCallback((update: Partial<Omit<PositionCookie, "fingerprint">>) => {
		cookieDataRef.current = { ...cookieDataRef.current, ...update };
		setCookie(POSITIONS_COOKIE, { fingerprint, ...cookieDataRef.current } satisfies PositionCookie, {
			path: "/",
			maxAge: 30 * 24 * 60 * 60, // 30 days
		});
	}, [fingerprint, setCookie]);

	// Save positions to cookie when a drag ends.
	const handleNodesChange = useCallback((changes: NodeChange[]) => {
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
	}, [onNodesChange, writeCookie]);

	// Save viewport (pan + zoom) when the user stops moving.
	const handleMoveEnd = useCallback(
		(_: unknown, viewport: { x: number; y: number; zoom: number }) => {
			writeCookie({ viewport });
		},
		[writeCookie],
	);

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
				if (!highlighted.has(p)) { highlighted.add(p); upQ.push(p); }
			}
		}

		// BFS downstream → rule leaves
		const downQ = [...matched];
		while (downQ.length) {
			const id = downQ.pop()!;
			for (const c of childrenOf.get(id) ?? []) {
				if (!highlighted.has(c)) { highlighted.add(c); downQ.push(c); }
			}
		}

		return highlighted;
	}, [search, nodes, edges]);

	const matchCount = useMemo(() => {
		if (!highlightedIds) return 0;
		return nodes.filter((n) => n.type === "rfRule" && highlightedIds.has(n.id)).length;
	}, [highlightedIds, nodes]);

	// Derived display nodes/edges — keeps opacity layered on top without
	// disturbing drag state (positions stay in the underlying `nodes` state).
	const displayNodes = useMemo(() => {
		if (!highlightedIds) return nodes;
		const active = highlightedIds.size > 0;
		return nodes.map((n) => ({
			...n,
			style: {
				...n.style,
				opacity: active && !highlightedIds.has(n.id) ? 0.25 : 1,
				transition: "opacity 0.15s",
			},
		}));
	}, [nodes, highlightedIds]);

	const displayEdges = useMemo(() => {
		if (!highlightedIds) return edges;
		const active = highlightedIds.size > 0;
		return edges.map((e) => ({
			...e,
			style: {
				...e.style,
				opacity: active && !(highlightedIds.has(e.source) && highlightedIds.has(e.target)) ? 0.15 : 1,
				transition: "opacity 0.15s",
			},
		}));
	}, [edges, highlightedIds]);

	if (isLoading) {
		return (
			<div className="flex h-full items-center justify-center">
				<Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
			</div>
		);
	}
	if (isError) {
		return (
			<div className="flex h-full items-center justify-center gap-2 text-muted-foreground">
				<AlertCircle className="h-5 w-5" />
				<span className="text-sm">Failed to load routing rules</span>
			</div>
		);
	}
	if (rules.length === 0) {
		return (
			<div className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground">
				<GitBranch className="h-10 w-10 opacity-20" />
				<p className="text-sm">No routing rules to display</p>
				<Button variant="outline" size="sm" onClick={() => router.push("/workspace/routing-rules")}>
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
			nodeTypes={nodeTypes}
			fitView={!positionsRestored}
			fitViewOptions={{ padding: 0.05 }}
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
					<div className="flex items-center gap-3 rounded-md border bg-white dark:bg-card px-4 py-2.5 shadow-sm">
						<Button
							variant="ghost" size="sm" className="-ml-1 !pl-0 gap-1.5 hover:bg-transparent"
							onClick={() => router.push("/workspace/routing-rules")}
						>
							<ArrowLeft className="h-4 w-4" />
							Back
						</Button>
						<div className="h-5 w-px bg-border" />
						<div className="flex items-center gap-2">
							<GitBranch className="h-4 w-4 text-muted-foreground" />
							<p className="text-sm font-semibold leading-tight text-foreground">Routing Tree</p>
							<p className="text-[11px] text-muted-foreground">
								{search
									? highlightedIds && highlightedIds.size > 0
										? `${matchCount} rule${matchCount !== 1 ? "s" : ""}`
										: "no match"
									: `${rules.length} rule${rules.length !== 1 ? "s" : ""}`}
							</p>
						</div>
						<div className="h-5 w-px bg-border" />
						<div className="relative">
							<Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
							<Input
								value={search}
								onChange={(e) => setSearch(e.target.value)}
								placeholder="Search conditions or rules…"
								className="h-8 w-56 pl-8 text-sm"
							/>
						</div>
					</div>
					{/* Scope + edge legend — floats below */}
					<div className="flex items-center gap-3 rounded-md border bg-white dark:bg-card px-3 py-1.5 shadow-sm">
						{SCOPE_ORDER.map((s) => (
							<div key={s} className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: SCOPE_CONFIG[s].color }} />
								<span className="text-[10px] text-muted-foreground">{SCOPE_CONFIG[s].label}</span>
							</div>
						))}
						<div className="h-3 w-px bg-border" />
						<div className="flex items-center gap-1.5">
							<Link2 className="h-2.5 w-2.5 text-muted-foreground" />
							<span className="text-[10px] text-muted-foreground">Chain rule</span>
						</div>
						<div className="h-3 w-px bg-border" />
						{/* Chain edge styles */}
						<div className="flex items-center gap-1.5">
							<svg width="28" height="10" className="shrink-0">
								<line x1="2" y1="5" x2="26" y2="5" stroke="var(--muted-foreground)" strokeWidth="1.5" strokeDasharray="5 4" />
							</svg>
							<span className="text-[10px] text-muted-foreground">Certain chain</span>
						</div>
						<div className="flex items-center gap-1.5">
							<svg width="28" height="10" className="shrink-0" style={{ opacity: 0.45 }}>
								<line x1="2" y1="5" x2="26" y2="5" stroke="var(--muted-foreground)" strokeWidth="1" strokeDasharray="3 6" />
							</svg>
							<span className="text-[10px] text-muted-foreground" style={{ opacity: 0.6 }}>Maybe chain</span>
						</div>
					</div>
				</div>
			</Panel>
		</ReactFlow>
	);
}
