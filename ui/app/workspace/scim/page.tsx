import SCIMView from "@enterprise/components/scim/scimView";

export default function SCIMPage() {
	return (
		<div className="no-padding-parent bg-background no-border-parent flex min-h-full w-full flex-col md:h-[calc(100dvh-1rem)]">
			<div className="mx-auto w-full grow overflow-visible md:overflow-y-auto">
				<SCIMView />
			</div>
		</div>
	);
}
