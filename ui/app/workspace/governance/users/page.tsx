import UsersView from "@enterprise/components/user-groups/usersView";

export default function GovernanceUsersPage() {
	return (
		<div className="no-padding-parent mx-auto h-[calc(var(--app-content-viewport)_-_1rem)] w-full p-4">
			<UsersView />
		</div>
	);
}