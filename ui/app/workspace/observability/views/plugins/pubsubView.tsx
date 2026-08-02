import PubSubConnectorView from "@enterprise/components/data-connectors/pubsub/pubsubConnectorView";

export default function PubSubView() {
	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<PubSubConnectorView />
			</div>
		</div>
	);
}