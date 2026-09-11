import { KeyRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function LicenseSettingsView() {
	const { t } = useTranslation("config");
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<KeyRound className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("enterprise.licenseTitle")}
				description={t("enterprise.sharedDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/overview"
				testIdPrefix="license"
			/>
		</div>
	);
}
