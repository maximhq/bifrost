import { useTranslation } from "react-i18next";
import AlertingPlaceholderView from "./alertingPlaceholderView";

export default function AlertChannelsView() {
	const { t } = useTranslation("governance");

	return (
		<AlertingPlaceholderView
			title={t("alerting.channelsUnlockTitle")}
			description={t("alerting.channelsUnlockDescription")}
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-channels"
			testIdPrefix="alert-channels"
		/>
	);
}
