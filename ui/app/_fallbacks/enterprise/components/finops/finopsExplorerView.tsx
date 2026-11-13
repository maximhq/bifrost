import { useTranslation } from "react-i18next";
import FinOpsPlaceholderView from "./finopsPlaceholderView";

export default function FinOpsExplorerView() {
	const { t } = useTranslation("governance");

	return (
		<FinOpsPlaceholderView
			title={t("finops.explorer.unlockTitle")}
			description={t("finops.explorer.unlockDescription")}
			testIdPrefix="finops-explorer"
		/>
	);
}