import { Construction } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function GuardrailsConfigurationView() {
	const { t } = useTranslation("governance");

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Construction className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("guardrails.unlockTitle")}
				description={t("guardrails.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/guardrails"
			/>
		</div>
	);
}
