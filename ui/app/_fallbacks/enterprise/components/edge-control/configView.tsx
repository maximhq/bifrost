import { SlidersHorizontal } from "lucide-react";
import { useTranslation } from "react-i18next";
import EdgeControlFallbackView from "./fallbackWrapper";

export default function ConfigView() {
	const { t } = useTranslation("config");
	return (
		<EdgeControlFallbackView
			icon={<SlidersHorizontal className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title={t("enterprise.edgeConfigTitle")}
			description={t("enterprise.sharedDescription")}
			readmeLink="https://docs.getbifrost.ai/edge/admin-configurations"
			testIdPrefix="edge-config"
		/>
	);
}
