import { useTranslation } from "react-i18next";
import AlertingPlaceholderView from "./alertingPlaceholderView";

export default function AlertHistoryView() {
	const { t } = useTranslation("governance");

	return (
		<AlertingPlaceholderView
			title={t("alerting.historyUnlockTitle")}
			description={t("alerting.historyUnlockDescription")}
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-history"
			testIdPrefix="alert-history"
		/>
	);
}
