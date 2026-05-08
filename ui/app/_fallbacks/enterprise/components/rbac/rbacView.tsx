import { UserRoundCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function RBACView() {
	const { t } = useTranslation();

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<UserRoundCheck className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("workspace.rbac.unlockTitle")}
				description={t("workspace.rbac.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}