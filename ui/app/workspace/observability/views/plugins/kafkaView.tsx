import KafkaConnectorView from "@enterprise/components/data-connectors/kafka/kafkaConnectorView";

export default function KafkaView() {
	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<KafkaConnectorView />
			</div>
		</div>
	);
}