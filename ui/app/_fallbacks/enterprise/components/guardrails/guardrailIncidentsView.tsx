import { Construction } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function GuardrailIncidentsView() {
	const { t } = useTranslation("governance");
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Construction className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("guardrails.unlockTitle")}
				description={t("unlockSharedDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/guardrails"
			/>
		</div>
	);
}