import type { ReactNode } from "react";
import { BarStack, type BarShapeProps, Rectangle, usePlotArea } from "recharts";

// Bars round only the open end (the end that points away from the axis). The
// cap scales with the bar's thickness so it reads the same on a 30px column
// and an 8px one: a quarter of the thickness, between 1px and the 4px the
// ranking charts use. It never reaches half the thickness, so bars keep
// straight sides and never become pills. The axis end always stays square.
export function capRadius(size: number): number {
	return Math.min(4, Math.max(1, Math.floor(size / 4)));
}

// Bar `shape` for a single (unstacked) vertical series.
export function barShape(props: BarShapeProps) {
	const r = capRadius(props.width);
	return <Rectangle {...props} radius={[r, r, 0, 0]} />;
}

// Bar `shape` for a horizontal (layout="vertical") ranking bar: the cap sits
// on the right end.
export function rankingBarShape(props: BarShapeProps) {
	const r = capRadius(props.height);
	return <Rectangle {...props} radius={[0, r, r, 0]} />;
}

// Wraps the <Bar>s of one stack so the whole column is clipped to a rounded
// top. A per-segment radius cannot do this: Recharts clamps a corner to half
// the segment's own height, so a thin top segment (a few errors over many
// successes) gets a 1px cap and the column reads as square. BarStack rounds
// the stack as one shape instead.
//
// The cap is sized from the column width Recharts will draw: the plot width
// split across `buckets`, minus the category gap, never wider than `barSize`.
export function CappedBarStack({ buckets, barSize = 30, children }: { buckets: number; barSize?: number; children: ReactNode }) {
	const plot = usePlotArea();
	const columnWidth = plot && buckets > 0 ? Math.min(barSize, plot.width / buckets - 2) : barSize;
	const r = capRadius(columnWidth);
	return <BarStack radius={[r, r, 0, 0]}>{children}</BarStack>;
}