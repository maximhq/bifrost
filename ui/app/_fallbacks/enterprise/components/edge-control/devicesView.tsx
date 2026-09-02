import { MonitorSmartphone } from "lucide-react";
import { useTranslation } from "react-i18next";
import EdgeControlFallbackView from "./fallbackWrapper";

export default function DevicesView() {
	const { t } = useTranslation("config");
	return (
		<EdgeControlFallbackView
			icon={<MonitorSmartphone className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title={t("enterprise.edgeDevicesTitle")}
			description={t("enterprise.sharedDescription")}
			readmeLink="https://docs.getbifrost.ai/edge/admin-devices"
			testIdPrefix="edge-devices"
		/>
	);
}
