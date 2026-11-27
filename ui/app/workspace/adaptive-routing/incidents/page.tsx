import IncidentsView from "@enterprise/components/adaptive-routing/incidentsView";

// The incident list lays out its own full-height shell (filter sidebar beside the table), as the dashboard does.
export default function AdaptiveRoutingIncidentsPage() {
	return <IncidentsView />;
}