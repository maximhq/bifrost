import UsersView from "@enterprise/components/user-groups/usersView";

export default function GovernanceUsersPage() {
	return (
		<div className="no-padding-parent mx-auto h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full p-4">
			<UsersView />
		</div>
	);
}