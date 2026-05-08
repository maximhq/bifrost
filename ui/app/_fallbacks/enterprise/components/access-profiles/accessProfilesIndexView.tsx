import { ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function AccessProfilesIndexView() {
	const { t } = useTranslation();

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<ShieldCheck className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("workspace.accessProfiles.unlockTitle")}
				description={t("workspace.accessProfiles.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/access-profiles"
				testIdPrefix="access-profiles"
			/>
		</div>
	);
}