import { FolderKanban } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function ProjectsIndexView() {
	const { t } = useTranslation("governance");

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<FolderKanban className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("projects.unlockTitle")}
				description={t("projects.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/projects"
				testIdPrefix="projects"
			/>
		</div>
	);
}
