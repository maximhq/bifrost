// Virtual MCP assignments are staged locally in the sheet and reconciled against the key's
// persisted set on Save via attach/detach calls.

/**
 * Splits a staged Virtual MCP assignment set against the persisted one into the ids to
 * attach (staged but not original) and to detach (original but not staged). Order follows
 * the input arrays; duplicates within an input are collapsed.
 */
export function diffVmcpAssignments(original: number[], staged: number[]): { toAttach: number[]; toDetach: number[] } {
	const originalSet = new Set(original);
	const stagedSet = new Set(staged);
	const toAttach = [...new Set(staged)].filter((id) => !originalSet.has(id));
	const toDetach = [...new Set(original)].filter((id) => !stagedSet.has(id));
	return { toAttach, toDetach };
}

/** Reports whether a staged assignment set differs from the persisted one, ignoring order. */
export function vmcpAssignmentsDirty(original: number[], staged: number[]): boolean {
	const { toAttach, toDetach } = diffVmcpAssignments(original, staged);
	return toAttach.length > 0 || toDetach.length > 0;
}
