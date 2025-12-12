import { Dog } from "lucide-react";
import ContactUsView from "../../views/contactUsView";

export default function DatadogConnectorView() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				align="top"
				className="mx-auto min-h-[80vh]"
				icon={<Dog className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="Unlock native Datadog data ingestion for better observability"
				description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
				readmeLink="https://docs.getbifrost.ai/enterprise/data-connectors/datadog"
			/>
		</div>
	);
}
