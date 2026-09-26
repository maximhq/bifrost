// Bulk rotation returns one previous_value_expires_at per key, each computed from
// its own time.Now() on the server, so the deadlines in one response can differ.

/**
 * Picks the single grace deadline to show for a bulk rotation: the latest
 * deadline across the returned keys, i.e. the time after which no retired
 * value authenticates anymore. Returns null when no key has a grace window.
 */
export function latestGraceDeadline(virtualKeys: { previous_value_expires_at?: string | null }[]): string | null {
	let latest: string | null = null;
	for (const vk of virtualKeys) {
		const deadline = vk.previous_value_expires_at;
		if (deadline && (latest === null || new Date(deadline).getTime() > new Date(latest).getTime())) {
			latest = deadline;
		}
	}
	return latest;
}

/**
 * Renders the "Assigned To" label for a virtual key, or null when it is assigned
 * to nothing. A key is assigned to at most one of a team, a customer, a business
 * unit, or a user.
 *
 * A business unit is named only as its kind: the key carries it as an id, and the
 * VK payload has no business-unit relation to read a name from. That still beats
 * the blank a business-unit-owned key would otherwise show.
 *
 * Shared by the table cell and the CSV export so the two cannot drift: the export
 * used to omit the user branch entirely, which left the column blank for every
 * user-assigned key.
 */
export function assignedToLabel(vk: {
	team?: { name: string };
	customer?: { name: string };
	business_unit_id?: string;
	assigned_user?: { name: string; email: string } | null;
}): string | null {
	if (vk.team) return `Team: ${vk.team.name}`;
	if (vk.customer) return `Customer: ${vk.customer.name}`;
	if (vk.business_unit_id) return "Business unit";
	if (vk.assigned_user) return `User: ${vk.assigned_user.name || vk.assigned_user.email}`;
	return null;
}

/**
 * Renders the "Assigned To" cell for the CSV export.
 *
 * The table can render an unknown assignee as "-" and let the reader ask; an export
 * cannot. A blank cell in a spreadsheet reads as a fact - this key is assigned to
 * nobody - so an assignee the server could not resolve has to say so rather than
 * borrow the blank that means unassigned. `assigned_user` is absent (not null) exactly
 * in that case; null is a resolved "no user".
 */
export function csvAssignedToCell(vk: {
	team?: { name: string };
	customer?: { name: string };
	business_unit_id?: string;
	assigned_user?: { name: string; email: string } | null;
}): string {
	const label = assignedToLabel(vk);
	if (label) return label;
	// Team, customer and business unit ride on the row itself, so they are never the unresolved case.
	if (!vk.team && !vk.customer && !vk.business_unit_id && vk.assigned_user === undefined) return "Unknown (not resolved)";
	return "";
}