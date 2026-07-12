import { Siren } from "lucide-react";
import ContactUsView from "../views/contactUsView";

type AlertingPlaceholderViewProps = {
	title: string;
	description: string;
	testIdPrefix: string;
	/** Docs page this placeholder links to. Defaults to the alerting overview. */
	readmeLink?: string;
};

export default function AlertingPlaceholderView({
	title,
	description,
	testIdPrefix,
	readmeLink = "https://docs.getbifrost.ai/enterprise/alerting/overview",
}: AlertingPlaceholderViewProps) {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Siren className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={title}
				description={description}
				readmeLink={readmeLink}
				testIdPrefix={testIdPrefix}
			/>
		</div>
	);
}