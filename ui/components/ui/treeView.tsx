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
  isExpanded?: boolean;
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
}

// ---- Helpers ----

function collectDefaultExpanded<T extends BaseNodeData>(
  nodes: TreeNode<T>[],
  maxLevel: number,
): Record<string, boolean> {
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

function collectExpandableNodeIds<T extends BaseNodeData>(
  nodes: TreeNode<T>[],
): string[] {
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
}) {
  const hasChildren = Boolean(node.children?.length);
  const isExpanded = expandedNodes[node.data.id] ?? false;

  return (
    <div className="relative">
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
      <div className="relative">
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
        <div
          className="relative"
          style={{ paddingLeft: `${level * indentSize}px` }}
        >
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
        <div className="relative">
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
}: TreeProps<T>) {
  const [expandedNodes, setExpandedNodes] = useState<Record<string, boolean>>(
    () =>
      levelsToExpandByDefault
        ? collectDefaultExpanded(data, levelsToExpandByDefault)
        : {},
  );

  const handleToggle = useCallback((id: string) => {
    setExpandedNodes((prev) => ({ ...prev, [id]: !prev[id] }));
  }, []);

  // Re-sync defaults when data changes and we have levelsToExpandByDefault
  // Capture full tree shape so resync fires when children change, not just top-level IDs
  const dataFingerprint = useMemo(() => {
    let count = 0;
    const walk = (nodes: TreeNode<T>[]) => {
      for (const n of nodes) {
        count++;
        if (n.children) walk(n.children);
      }
    };
    walk(data);
    return `${data.map((n) => n.data.id).join(",")}:${count}`;
  }, [data]);
  const expandableNodeIds = useMemo(
    () => collectExpandableNodeIds(data),
    [dataFingerprint],
  );

  const isAllExpanded = useMemo(
    () =>
      expandableNodeIds.length > 0 &&
      expandableNodeIds.every((id) => expandedNodes[id]),
    [expandableNodeIds, expandedNodes],
  );

  const isAllCollapsed = useMemo(
    () => expandableNodeIds.every((id) => !expandedNodes[id]),
    [expandableNodeIds, expandedNodes],
  );

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
        // Merge: keep user overrides, add new defaults
        return { ...defaults, ...prev };
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataFingerprint, levelsToExpandByDefault]);

  return (
    <div className={cn("", className)}>
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
        />
      ))}
    </div>
  );
}
