import { CircuitBoard } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function CircuitBreakerView() {
	return (
		<FeaturePlaceholderView
			icon={CircuitBoard}
			title="Unlock circuit breaker for reliable fallbacks"
			description="This feature is a part of the Bifrost enterprise license. Automatically redirect traffic to a fallback provider when your primary endpoint shows signs of failure."
			readmeLink="https://docs.getbifrost.ai/enterprise/circuit-breaker"
		/>
	);
}
