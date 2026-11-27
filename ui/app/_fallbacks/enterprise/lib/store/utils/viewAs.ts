// Non-enterprise builds have no view-as mode, so nothing is ever active.

export const getViewAsUserId = (): string | null => null;

export const openViewAsTab = (_userId: string) => {
	// No-op in non-enterprise builds
};

export const stopViewAs = () => {
	// No-op in non-enterprise builds
};