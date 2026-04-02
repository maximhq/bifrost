"use client";

import { BaseEdge, type EdgeProps } from "@xyflow/react";
import { memo, useMemo } from "react";

export type RfChainEdgeData = {
	/** When true, the chain is "dynamic" — static analysis could not fully prove the re-entry path. */
	chainWeak?: boolean;
};

/**
 * Builds a cubic Bézier path for a chain-back edge.
 *
 * Because the source handle exits to the RIGHT and the target is often to the
 * LEFT (the edge loops back), the standard `getSimpleBezierPath` control-point
 * formula places both control points between source and target, causing the
 * edge to immediately swing left from a right-facing handle.
 *
 * Instead we always push cp1 rightward past the source and cp2 leftward past
 * the target, creating a clear outward loop before the edge arrives at the
 * condition node's left handle.
 */
function buildChainPath(
	sx: number, sy: number,
	tx: number, ty: number,
): { path: string; labelX: number; labelY: number; angleDeg: number } {
	// Offset scales with horizontal distance so the loop expands when nodes
	// are far apart, but stays legible when they are close.
	const hDist = Math.abs(sx - tx);
	const offset = Math.max(80, hDist * 0.25);

	const cp1x = sx + offset;   // always to the right of the source
	const cp1y = sy;
	const cp2x = tx - offset;   // always to the left of the target
	const cp2y = ty;

	const path = `M${sx},${sy} C${cp1x},${cp1y} ${cp2x},${cp2y} ${tx},${ty}`;

	// Midpoint of cubic Bézier at t = 0.5
	const labelX = sx / 8 + 3 * cp1x / 8 + 3 * cp2x / 8 + tx / 8;
	const labelY = sy / 8 + 3 * cp1y / 8 + 3 * cp2y / 8 + ty / 8;

	// Tangent direction at t = 0.5 for the mid-edge arrow orientation
	const dx = 0.75 * (cp1x - sx) + 1.5 * (cp2x - cp1x) + 0.75 * (tx - cp2x);
	const dy = 0.75 * (cp1y - sy) + 1.5 * (cp2y - cp1y) + 0.75 * (ty - cp2y);
	const angleDeg = (Math.atan2(dy, dx) * 180) / Math.PI;

	return { path, labelX, labelY, angleDeg };
}

function RfChainEdgeImpl({
	id,
	sourceX,
	sourceY,
	targetX,
	targetY,
	style,
	interactionWidth,
	data,
}: EdgeProps) {
	const weak = (data as RfChainEdgeData | undefined)?.chainWeak === true;
	const strokeColor =
		typeof style?.stroke === "string" && style.stroke.length > 0
			? style.stroke
			: "var(--foreground)";

	const { path, labelX, labelY, angleDeg } = useMemo(
		() => buildChainPath(sourceX, sourceY, targetX, targetY),
		[sourceX, sourceY, targetX, targetY],
	);

	return (
		<>
			<BaseEdge
				id={id}
				path={path}
				style={style}
				interactionWidth={interactionWidth ?? 12}
			/>
			<g
				transform={`translate(${labelX}, ${labelY}) rotate(${angleDeg})`}
				className="pointer-events-none"
				aria-hidden
			>
				{weak ? (
					<polyline
						points="-6,-4 6,0 -6,4"
						fill="none"
						stroke={strokeColor}
						strokeWidth={1.75}
						strokeLinecap="round"
						strokeLinejoin="round"
					/>
				) : (
					<polygon points="8,0 -6,-5 -6,5" fill={strokeColor} />
				)}
			</g>
		</>
	);
}

export const RfChainEdge = memo(RfChainEdgeImpl);
