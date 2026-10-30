import { Position } from "@xyflow/react";
import { Network } from "lucide-react";
import { useTranslation } from "react-i18next";
import { SRC_H, SRC_W } from "../constants";
import { RFEdgeHandle } from "./rfEdgeHandle";

export function RFSourceNode() {
	const { t } = useTranslation("models");
	return (
		<div className="relative" style={{ width: SRC_W, height: SRC_H }}>
			<RFEdgeHandle type="source" position={Position.Right} accentColor="var(--primary)" />
			<div className="border-primary dark:bg-card relative z-10 flex h-full cursor-grab flex-col justify-center rounded-xl border-2 bg-white px-5 shadow-md active:cursor-grabbing">
				<div className="text-foreground flex items-center gap-2 font-semibold">
					<Network className="text-primary h-4 w-4" />
					{t("routing.treeIncomingRequest")}
				</div>
				<p className="text-muted-foreground mt-0.5 text-[11px]">{t("routing.treeIncomingRequestFields")}</p>
			</div>
		</div>
	);
}