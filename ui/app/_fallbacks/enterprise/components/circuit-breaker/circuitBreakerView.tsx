import { CircuitBoard } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function CircuitBreakerView() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<CircuitBoard className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="Unlock circuit breaker for reliable fallbacks"
				description="This feature is a part of the Bifrost enterprise license. Automatically redirect traffic to a fallback provider when your primary endpoint shows signs of failure."
				readmeLink="https://docs.getbifrost.ai/enterprise/circuit-breaker"
			/>
		</div>
	);
}