import { useTranslation } from "react-i18next";
import FinOpsPlaceholderView from "./finopsPlaceholderView";

export default function FinOpsOverviewView() {
	const { t } = useTranslation("governance");

	return (
		<FinOpsPlaceholderView
			title={t("finops.overview.unlockTitle")}
			description={t("finops.overview.unlockDescription")}
			testIdPrefix="finops-overview"
		/>
	);
}