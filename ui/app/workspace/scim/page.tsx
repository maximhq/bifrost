import SCIMView from "@enterprise/components/scim/scimView";

export default function SCIMPage() {
	return (
		<div className="no-padding-parent bg-background no-border-parent flex flex-col w-full h-[calc(100dvh-1rem)]">
			<div className="mx-auto w-full grow overflow-y-auto">
				<SCIMView />
			</div>
		</div>
	);
}