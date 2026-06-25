import ModelLimitsView from "./views/modelLimitsView";

export default function ModelLimitsPage() {
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] min-h-0 w-full flex-col overflow-hidden p-4">
			<ModelLimitsView />
		</div>
	);
}