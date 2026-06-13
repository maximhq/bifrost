import { MonitorSmartphone } from "lucide-react";
import EdgeControlFallbackView from "./fallbackWrapper";

export default function DevicesView() {
	return (
		<EdgeControlFallbackView
			icon={<MonitorSmartphone className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title="Unlock edge control to manage your devices"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/edge/admin-devices"
			testIdPrefix="edge-devices"
		/>
	);
}
