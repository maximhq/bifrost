const MAX_ATTRIBUTION_LINES = 1;

// AttributionCell resolves an attribution value using a plural-first fallback:
// plural names -> singular name -> plural ids -> singular id. When a plural
// (array) source is used, values render one per line, capped at
// MAX_ATTRIBUTION_LINES with a "+N more" indicator for the remainder.
export function AttributionCell({ names, name, ids, id }: { names?: string[]; name?: string | null; ids?: string[]; id?: string | null }) {
	let values: string[] = [];
	if (Array.isArray(names) && names.filter(Boolean).length > 0) {
		values = names.filter(Boolean);
	} else if (name) {
		values = [name];
	} else if (Array.isArray(ids) && ids.filter(Boolean).length > 0) {
		values = ids.filter(Boolean);
	} else if (id) {
		values = [id];
	}

	if (values.length === 0) {
		return <div className="max-w-[180px] truncate font-mono text-xs">-</div>;
	}

	const visible = values.slice(0, MAX_ATTRIBUTION_LINES);
	const remaining = values.length - visible.length;

	return (
		<div className="flex max-w-[180px] flex-col gap-0.5 font-mono text-xs leading-tight" title={values.join("\n")}>
			{visible.map((value, index) => (
				<span key={index} className="truncate">
					{value}
				</span>
			))}
			{remaining > 0 && <span className="text-muted-foreground">+{remaining} more</span>}
		</div>
	);
}