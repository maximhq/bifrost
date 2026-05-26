import RBACView from "@enterprise/components/rbac/rbacView";

export default function GovernanceRbacPage() {
	return (
		<div className="mx-auto w-full max-w-7xl h-[calc(100vh_-_50px)] flex flex-col overflow-y-auto">
			<RBACView />
		</div>
	);
}