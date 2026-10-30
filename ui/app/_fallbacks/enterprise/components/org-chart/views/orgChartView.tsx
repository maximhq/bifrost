import { Network } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../../views/contactUsView";

export function OrgChartView() {
	const { t } = useTranslation("governance");
	return (
		<div className="w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				testIdPrefix="org-chart"
				icon={<Network className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("orgChart.unlockTitle")}
				description={t("orgChart.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}