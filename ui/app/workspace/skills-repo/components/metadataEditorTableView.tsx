"use client";

import { HeadersTable } from "@/components/ui/headersTable";

// ---------- MetadataTableEditor ----------

export function MetadataTableEditor({
	metadataJson,
	onChange,
	error,
}: {
	metadataJson: string;
	onChange: (json: string) => void;
	error?: string;
}) {
	// Parse JSON string into key-value pairs for the table
	let parsedValue: Record<string, string> = {};
	if (metadataJson.trim()) {
		try {
			const parsed = JSON.parse(metadataJson) as unknown;
			if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
				parsedValue = Object.fromEntries(
					Object.entries(parsed as Record<string, unknown>).map(([key, value]) => [key, String(value ?? "")]),
				);
			}
		} catch {
			// Invalid JSON, fall back to empty
		}
	}

	const handleChange = (next: Record<string, string>) => {
		// Serialize key-value pairs back to JSON, filtering out empty keys
		const validEntries = Object.entries(next).filter(([key]) => key.trim());
		if (validEntries.length === 0) {
			onChange("");
			return;
		}
		onChange(JSON.stringify(Object.fromEntries(validEntries), null, 2));
	};

	return (
		<div className="flex flex-col gap-2">
			<HeadersTable label="" value={parsedValue} onChange={handleChange} keyPlaceholder="Metadata key" valuePlaceholder="Metadata value" />
			{error && (
				<p className="text-destructive mt-1 text-xs" role="alert">
					{error}
				</p>
			)}
		</div>
	);
}