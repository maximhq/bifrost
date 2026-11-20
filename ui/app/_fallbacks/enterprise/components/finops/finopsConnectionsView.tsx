import { useTranslation } from "react-i18next";
import FinOpsPlaceholderView from "./finopsPlaceholderView";

export default function FinOpsConnectionsView() {
	const { t } = useTranslation("governance");

	return (
		<FinOpsPlaceholderView
			title={t("finops.connections.unlockTitle")}
			description={t("finops.connections.unlockDescription")}
			testIdPrefix="finops-connections"
		/>
	);
}