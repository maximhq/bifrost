import { Layers } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function ClusterPage() {
	const { t } = useTranslation();

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Layers className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("workspace.cluster.unlockTitle")}
				description={t("workspace.cluster.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/clustering"
			/>
		</div>
	);
}