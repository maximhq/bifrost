import { Bell } from "lucide-react";
import ContactUsView from "../views/contactUsView";

type AlertingPlaceholderViewProps = {
	title: string;
	description: string;
	testIdPrefix: string;
};

export default function AlertingPlaceholderView({ title, description, testIdPrefix }: AlertingPlaceholderViewProps) {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Bell className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={title}
				description={description}
				readmeLink="https://docs.getbifrost.ai/enterprise/alerting"
				testIdPrefix={testIdPrefix}
			/>
		</div>
	);
}