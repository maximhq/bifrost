import SCIMView from "@enterprise/components/scim/scimView";

export default function SCIMPage() {
	// No no-padding-parent / no-border-parent / bg-background wrapper here: those opt
	// out of the white content card in clientLayout, which left this page reading grey
	// while every sibling placeholder sat on the card. Matches mcp-tool-groups.
	return (
		<div className="mx-auto w-full no-padding-parent overflow-y-auto">
			<SCIMView />
		</div>
	);
}