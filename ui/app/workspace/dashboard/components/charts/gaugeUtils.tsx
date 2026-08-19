import { useEffect, useRef, useState } from "react";

export interface GaugeGeometry {
	cx: number;
	cy: number;
	innerRadius: number;
	outerRadius: number;
}

export function getGaugeGeometry(width: number, height: number): GaugeGeometry | null {
	if (width <= 0 || height <= 0) return null;
	const outerRadius = Math.min(width * 0.4, Math.max(24, height - 10));
	const innerRadius = outerRadius * 0.58;
	const cx = width / 2;
	const cy = Math.min(height - 4, outerRadius + 4);
	return { cx, cy, innerRadius, outerRadius };
}

export function useGaugeSize() {
	const ref = useRef<HTMLDivElement | null>(null);
	const [size, setSize] = useState({ width: 0, height: 0 });

	useEffect(() => {
		const node = ref.current;
		if (!node) return;
		const update = () => {
			const w = node.clientWidth;
			const h = node.clientHeight;
			setSize((cur) => (cur.width === w && cur.height === h ? cur : { width: w, height: h }));
		};
		update();
		const ro = new ResizeObserver(update);
		ro.observe(node);
		return () => ro.disconnect();
	}, []);

	return { ref, ...size };
}

export function GaugeNeedle({ percentage, geometry }: { percentage: number; geometry: GaugeGeometry }) {
	const p = Math.max(0, Math.min(percentage, 100));
	const { cx, cy, outerRadius } = geometry;
	const angle = 180 - (p / 100) * 180;
	const rad = (Math.PI * angle) / 180;
	const len = outerRadius * 0.94;
	const tipX = cx + len * Math.cos(rad);
	const tipY = cy - len * Math.sin(rad);
	const perp = rad + Math.PI / 2;
	const hw = 3.5;
	return (
		<g>
			<path
				d={`M ${cx + hw * Math.cos(perp)} ${cy - hw * Math.sin(perp)} L ${tipX} ${tipY} L ${cx - hw * Math.cos(perp)} ${cy + hw * Math.sin(perp)} Z`}
				fill="#71717a"
			/>
			<circle cx={cx} cy={cy} r={5} fill="#71717a" />
		</g>
	);
}