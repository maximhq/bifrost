import { Shuffle } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function AdaptiveRoutingView() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Shuffle className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="Unlock adaptive routing for better performance"
				description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
				readmeLink="https://docs.getbifrost.ai/enterprise/intelligent-load-balancing"
			/>
		</div>
	);
}
