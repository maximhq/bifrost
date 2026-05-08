import { Construction } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function GuardrailsConfigurationView() {
	const { t } = useTranslation();

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Construction className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("workspace.guardrailsConfiguration.unlockTitle")}
				description={t("workspace.guardrailsConfiguration.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/guardrails"
			/>
		</div>
	);
}