"use client";

import { cn } from "@/lib/utils";
import { useCallback, useEffect, useMemo, useState } from "react";

// ---- Types ----

export interface BaseNodeData {
	id: string;
	name: string;
}

export interface TreeNode<T extends BaseNodeData> {
	children?: TreeNode<T>[];
	data: T;
}

export interface TreeProps<T extends BaseNodeData> {
	data: TreeNode<T>[];
	renderItem: (props: {
		item: T;
		level: number;
		isExpanded: boolean;
		hasChildren: boolean;
		onToggle: () => void;
		onExpandAll: () => void;
		onCollapseAll: () => void;
		isAllExpanded: boolean;
		isAllCollapsed: boolean;
	}) => React.ReactNode;
	className?: string;
	indentSize?: number;
	lineColor?: string;
	lineWidth?: number;
	levelsToExpandByDefault?: number;
	/**
	 * When true, rows fill the container width and long labels truncate instead of
	 * growing the tree (and the parent) horizontally. Defaults to false, where the
	 * tree expands to its widest content (enabling horizontal scroll).
	 */
	fitContainer?: boolean;
	/** External expansion state control — parent can manage expandedNodes directly */
	states?: {
		expandedNodes: Record<string, boolean>;
		setExpandedNodes: React.Dispatch<React.SetStateAction<Record<string, boolean>>>;
	};
}

// ---- Helpers ----

function collectDefaultExpanded<T extends BaseNodeData>(nodes: TreeNode<T>[], maxLevel: number): Record<string, boolean> {
	const result: Record<string, boolean> = {};
	const traverse = (node: TreeNode<T>, level: number) => {
		if (level >= maxLevel) return;
		if (node.children && node.children.length > 0) {
			result[node.data.id] = true;
		}
		node.children?.forEach((child) => traverse(child, level + 1));
	};
	nodes.forEach((n) => traverse(n, 0));
	return result;
}

function collectExpandableNodeIds<T extends BaseNodeData>(nodes: TreeNode<T>[]): string[] {
	const result: string[] = [];
	const traverse = (node: TreeNode<T>) => {
		if (node.children && node.children.length > 0) {
			result.push(node.data.id);
			node.children.forEach(traverse);
		}
	};
	nodes.forEach(traverse);
	return result;
}

// ---- Node Component ----

function TreeNodeComponent<T extends BaseNodeData>({
	node,
	level,
	isLast,
	renderItem,
	indentSize,
	lineColor,
	lineWidth,
	expandedNodes,
	onToggle,
	onExpandAll,
	onCollapseAll,
	isAllExpanded,
	isAllCollapsed,
	fitContainer,
}: {
	node: TreeNode<T>;
	level: number;
	isLast: boolean;
	renderItem: TreeProps<T>["renderItem"];
	indentSize: number;
	lineColor: string;
	lineWidth: number;
	expandedNodes: Record<string, boolean>;
	onToggle: (id: string) => void;
	onExpandAll: () => void;
	onCollapseAll: () => void;
	isAllExpanded: boolean;
	isAllCollapsed: boolean;
	fitContainer: boolean;
}) {
	const hasChildren = Boolean(node.children?.length);
	const isExpanded = expandedNodes[node.data.id] ?? false;
	const widthClass = fitContainer ? "min-w-0" : "min-w-max";

	return (
		<div className={cn("relative", widthClass)}>
			{/* Vertical line from parent — spans the full node height (row + children) for sibling continuation */}
			{level > 0 && !isLast && (
				<div
					className="absolute"
					style={{
						left: `${(level - 1) * indentSize + indentSize / 2}px`,
						top: 0,
						bottom: 0,
						width: lineWidth,
						backgroundColor: lineColor,
					}}
				/>
			)}

			{/* Row wrapper — horizontal line positions relative to just this row */}
			<div className={cn("relative", widthClass)}>
				{/* Vertical line stub for last child — only goes to row center */}
				{level > 0 && isLast && (
					<div
						className="absolute"
						style={{
							left: `${(level - 1) * indentSize + indentSize / 2}px`,
							top: 0,
							height: "50%",
							width: lineWidth,
							backgroundColor: lineColor,
						}}
					/>
				)}

				{/* Horizontal connector from vertical line to the node */}
				{level > 0 && (
					<div
						className="absolute"
						style={{
							left: `${(level - 1) * indentSize + indentSize / 2}px`,
							top: "50%",
							width: `${indentSize / 2 + 2}px`,
							height: lineWidth,
							backgroundColor: lineColor,
						}}
					/>
				)}

				{/* Node content */}
				<div className={cn("relative", widthClass)} style={{ paddingLeft: `${level * indentSize}px` }}>
					<div className="py-0.5">
						{renderItem({
							item: node.data,
							level,
							isExpanded,
							hasChildren,
							onToggle: () => onToggle(node.data.id),
							onExpandAll,
							onCollapseAll,
							isAllExpanded,
							isAllCollapsed,
						})}
					</div>
				</div>
			</div>

			{/* Children */}
			{isExpanded && node.children && (
				<div className={cn("relative", widthClass)}>
					{node.children.map((child, idx) => (
						<TreeNodeComponent
							key={child.data.id}
							node={child}
							level={level + 1}
							isLast={idx === node.children!.length - 1}
							renderItem={renderItem}
							indentSize={indentSize}
							lineColor={lineColor}
							lineWidth={lineWidth}
							expandedNodes={expandedNodes}
							onToggle={onToggle}
							onExpandAll={onExpandAll}
							onCollapseAll={onCollapseAll}
							isAllExpanded={isAllExpanded}
							isAllCollapsed={isAllCollapsed}
							fitContainer={fitContainer}
						/>
					))}
				</div>
			)}
		</div>
	);
}

// ---- Main Tree Component ----

export function Tree<T extends BaseNodeData>({
	data,
	renderItem,
	className,
	indentSize = 24,
	lineColor = "var(--border)",
	lineWidth = 1,
	levelsToExpandByDefault,
	fitContainer = false,
	states,
}: TreeProps<T>) {
	const [localExpandedNodes, localSetExpandedNodes] = useState<Record<string, boolean>>(() =>
		levelsToExpandByDefault ? collectDefaultExpanded(data, levelsToExpandByDefault) : {},
	);

	// Use external state if provided, otherwise use local state
	const expandedNodes = states?.expandedNodes ?? localExpandedNodes;
	const setExpandedNodes = states?.setExpandedNodes ?? localSetExpandedNodes;

	const handleToggle = useCallback(
		(id: string) => {
			setExpandedNodes((prev) => ({ ...prev, [id]: !prev[id] }));
		},
		[setExpandedNodes],
	);

	// Re-sync defaults when data changes and we have levelsToExpandByDefault
	// Capture full tree shape so resync fires when children change, not just top-level IDs
	const dataFingerprint = useMemo(() => {
		const ids: string[] = [];
		const walk = (nodes: TreeNode<T>[]) =>
			nodes.forEach((n) => {
				ids.push(n.data.id);
				if (n.children) walk(n.children);
			});
		walk(data);
		return ids.join(",");
	}, [data]);
	const expandableNodeIds = useMemo(() => collectExpandableNodeIds(data), [dataFingerprint]);

	const isAllExpanded = useMemo(
		() => expandableNodeIds.length > 0 && expandableNodeIds.every((id) => expandedNodes[id]),
		[expandableNodeIds, expandedNodes],
	);

	const isAllCollapsed = useMemo(() => expandableNodeIds.every((id) => !expandedNodes[id]), [expandableNodeIds, expandedNodes]);

	const handleExpandAll = useCallback(() => {
		setExpandedNodes((prev) => {
			const next = { ...prev };
			expandableNodeIds.forEach((id) => {
				next[id] = true;
			});
			return next;
		});
	}, [expandableNodeIds]);

	const handleCollapseAll = useCallback(() => {
		setExpandedNodes((prev) => {
			const next = { ...prev };
			expandableNodeIds.forEach((id) => {
				next[id] = false;
			});
			return next;
		});
	}, [expandableNodeIds]);

	useEffect(() => {
		if (levelsToExpandByDefault) {
			setExpandedNodes((prev) => {
				const defaults = collectDefaultExpanded(data, levelsToExpandByDefault);
				// Merge: keep user overrides for nodes that still exist, add new defaults
				const validIds = new Set(expandableNodeIds);
				const filtered = Object.fromEntries(Object.entries(prev).filter(([id]) => validIds.has(id)));
				return { ...defaults, ...filtered };
			});
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [dataFingerprint, levelsToExpandByDefault]);

	return (
		<div className={cn(fitContainer ? "min-w-0" : "min-w-max", className)}>
			{data.map((node, idx) => (
				<TreeNodeComponent
					key={node.data.id}
					node={node}
					level={0}
					isLast={idx === data.length - 1}
					renderItem={renderItem}
					indentSize={indentSize}
					lineColor={lineColor}
					lineWidth={lineWidth}
					expandedNodes={expandedNodes}
					onToggle={handleToggle}
					onExpandAll={handleExpandAll}
					onCollapseAll={handleCollapseAll}
					isAllExpanded={isAllExpanded}
					isAllCollapsed={isAllCollapsed}
					fitContainer={fitContainer}
				/>
			))}
		</div>
	);
}