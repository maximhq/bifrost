import ClusterIncidentsView from "@enterprise/components/cluster/clusterIncidentsView";

// The incident list lays out its own full-height shell (filter sidebar beside the table), as the
// adaptive-routing incidents page does.
export default function ClusterIncidentsPage() {
	return <ClusterIncidentsView />;
}