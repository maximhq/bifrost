import ConfigView from "@enterprise/components/edge-control/configView";

export default function EdgeConfigPage() {
	return (
		<div data-testid="edge-config-page" className="mx-auto flex w-full max-w-3xl">
			<ConfigView />
		</div>
	);
}
