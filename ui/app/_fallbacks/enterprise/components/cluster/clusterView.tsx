import { Layers } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function ClusterPage() {
	const { t } = useTranslation("config");
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Layers className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("enterprise.clusterTitle")}
				description={t("enterprise.sharedDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/clustering"
			/>
		</div>
	);
}
