import ConfigView from "@enterprise/components/edge-control/configView";

export default function EdgeConfigPage() {
	return (
		<div data-testid="edge-config-page" className="no-padding-parent mx-auto flex w-full max-w-7xl p-4">
			<ConfigView />
		</div>
	);
}