"use client";

import { Position } from "@xyflow/react";
import { Network } from "lucide-react";
import { SRC_H, SRC_W } from "../constants";
import { RFEdgeHandle } from "./rfEdgeHandle";

export function RFSourceNode() {
	return (
		<div className="relative" style={{ width: SRC_W, height: SRC_H }}>
			<RFEdgeHandle
				type="source"
				position={Position.Right}
				accentColor="var(--primary)"
			/>
			<div className="relative z-10 flex h-full flex-col justify-center rounded-xl border-2 border-primary bg-white dark:bg-card px-5 shadow-md cursor-grab active:cursor-grabbing">
				<div className="flex items-center gap-2 font-semibold text-foreground">
					<Network className="h-4 w-4 text-primary" />
					Incoming Request
				</div>
				<p className="mt-0.5 text-[11px] text-muted-foreground">provider · model · headers · params · limits</p>
			</div>
		</div>
	);
}
