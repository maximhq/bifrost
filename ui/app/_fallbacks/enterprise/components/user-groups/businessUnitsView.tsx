import { Building2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export function BusinessUnitsView() {
	const { t } = useTranslation();

	return (
		<div className="w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				testIdPrefix="business-units-governance"
				icon={<Building2 className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("workspace.businessUnits.unlockTitle")}
				description={t("workspace.businessUnits.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}