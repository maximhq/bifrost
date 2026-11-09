import { useTranslation } from "react-i18next";
import FinOpsPlaceholderView from "./finopsPlaceholderView";

export default function FinOpsForecastView() {
	const { t } = useTranslation("governance");

	return (
		<FinOpsPlaceholderView
			title={t("finops.forecast.unlockTitle")}
			description={t("finops.forecast.unlockDescription")}
			testIdPrefix="finops-forecast"
		/>
	);
}