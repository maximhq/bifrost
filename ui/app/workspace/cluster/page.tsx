import ClusterView from "@enterprise/components/cluster/clusterView";

export default function ClusterPage() {
	return (
		<div className="mx-auto h-[calc(var(--app-content-viewport)_-_50px)] w-full max-w-7xl p-4 md:p-0">
			<ClusterView />
		</div>
	);
}