import InventoryView from "@enterprise/components/edge-control/inventoryView";

export default function EdgeInventoryPage() {
	return (
		<div data-testid="edge-inventory-page" className="mx-auto flex w-full max-w-7xl">
			<InventoryView />
		</div>
	);
}
