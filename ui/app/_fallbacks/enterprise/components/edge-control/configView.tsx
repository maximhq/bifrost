import { SlidersHorizontal } from "lucide-react";
import EdgeControlFallbackView from "./fallbackWrapper";

export default function ConfigView() {
	return (
		<EdgeControlFallbackView
			icon={<SlidersHorizontal className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title="Unlock edge control to govern devices at the edge"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/edge/admin-configurations"
			testIdPrefix="edge-config"
		/>
	);
}
