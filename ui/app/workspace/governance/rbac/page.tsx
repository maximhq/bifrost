import RBACView from "@enterprise/components/rbac/rbacView";

export default function GovernanceRbacPage() {
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] w-full flex-col p-4">
			<RBACView />
		</div>
	);
}