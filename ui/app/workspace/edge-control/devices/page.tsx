import DevicesView from "@enterprise/components/edge-control/devicesView";

export default function EdgeDevicesPage() {
	return (
		<div data-testid="edge-devices-page" className="dark:bg-card no-padding-parent no-border-parent h-[calc(100vh_-_16px)]">
			<DevicesView />
		</div>
	);
}
