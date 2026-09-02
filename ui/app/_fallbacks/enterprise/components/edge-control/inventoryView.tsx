import { ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import EdgeControlFallbackView from "./fallbackWrapper";

export default function InventoryView() {
	const { t } = useTranslation("config");
	return (
		<EdgeControlFallbackView
			icon={<ShieldCheck className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title={t("enterprise.edgeInventoryTitle")}
			description={t("enterprise.sharedDescription")}
			readmeLink="https://docs.getbifrost.ai/edge/admin-approvals"
			testIdPrefix="edge-inventory"
		/>
	);
}
