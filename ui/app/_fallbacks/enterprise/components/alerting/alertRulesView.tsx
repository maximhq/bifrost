import { useTranslation } from "react-i18next";
import AlertingPlaceholderView from "./alertingPlaceholderView";

export default function AlertRulesView() {
	const { t } = useTranslation("governance");

	return (
		<AlertingPlaceholderView
			title={t("alerting.rulesUnlockTitle")}
			description={t("alerting.rulesUnlockDescription")}
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-rules"
			testIdPrefix="alert-rules"
		/>
	);
}
