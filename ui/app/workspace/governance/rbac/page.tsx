import RBACView from "@enterprise/components/rbac/rbacView";

export default function GovernanceRbacPage() {
	return (
		<div className="no-padding-parent mx-auto h-[calc(100dvh-1rem)] w-full p-4 flex flex-col">
			<RBACView />
		</div>
	);
}