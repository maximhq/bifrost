import AdaptiveRoutingView from "@enterprise/components/adaptive-routing/adaptiveRoutingView";

// The dashboard lays out its own full-height shell (filter sidebar beside the panel), as the logs
// and alert history pages do, so the page adds no padding of its own.
export default function AdaptiveRoutingPage() {
	return <AdaptiveRoutingView />;
}