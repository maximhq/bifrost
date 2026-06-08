import { useCallback, useEffect, useRef, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

interface KeyValuePair {
	id: number;
	key: string;
	value: string;
}

function pairsToRecord(pairs: KeyValuePair[]): Record<string, string> {
	const result: Record<string, string> = {};
	for (const { key, value } of pairs) {
		if (key.trim()) {
			result[key.trim()] = value;
		}
	}
	return result;
}

function recordsEqual(a: Record<string, string>, b: Record<string, string>): boolean {
	const aKeys = Object.keys(a);
	const bKeys = Object.keys(b);
	if (aKeys.length !== bKeys.length) {
		return false;
	}
	for (const key of aKeys) {
		if (a[key] !== b[key]) {
			return false;
		}
	}
	return true;
}

interface KeyValueEditorProps {
	value: Record<string, string>;
	onChange: (value: Record<string, string>) => void;
	label?: string;
	description?: string;
	keyPlaceholder?: string;
	valuePlaceholder?: string;
}

// KeyValueEditor provides a reusable key-value pair editor for custom blocks.
// Pairs use stable IDs to avoid React key-mismatch bugs on reorder/remove.
export function KeyValueEditor({
	value,
	onChange,
	label = "Custom Blocks",
	description = "Add custom key-value pairs",
	keyPlaceholder = "Key",
	valuePlaceholder = "Value",
}: KeyValueEditorProps) {
	const nextId = useRef(0);
	// Persisted key -> id map so a given key keeps the same stable React key
	// across external value syncs (reorder / add / remove from the parent).
	const idMapRef = useRef<Map<string, number>>(new Map());

	const buildPairs = useCallback((record: Record<string, string>): KeyValuePair[] => {
		return Object.entries(record).map(([key, val]) => {
			let id = idMapRef.current.get(key);
			if (id === undefined) {
				id = nextId.current++;
				idMapRef.current.set(key, id);
			}
			return { id, key, value: val };
		});
	}, []);

	const [pairs, setPairs] = useState<KeyValuePair[]>(() => buildPairs(value));
	const isFirstRender = useRef(true);
	const suppressOnChange = useRef(false);
	// Last record we emitted to (or received from) the parent. Used to ignore
	// the parent echoing our own onChange back as a new object reference, which
	// would otherwise drop an in-progress empty row and regenerate IDs.
	const lastSyncedRef = useRef<Record<string, string>>(value);
	const onChangeRef = useRef(onChange);
	onChangeRef.current = onChange;

	useEffect(() => {
		// Skip syncing when the incoming value matches what we last emitted/saw.
		if (recordsEqual(value, lastSyncedRef.current)) {
			return;
		}
		lastSyncedRef.current = value;
		suppressOnChange.current = true;
		setPairs(buildPairs(value));
	}, [value, buildPairs]);

	useEffect(() => {
		if (isFirstRender.current) {
			isFirstRender.current = false;
			return;
		}
		if (suppressOnChange.current) {
			suppressOnChange.current = false;
			return;
		}
		const record = pairsToRecord(pairs);
		lastSyncedRef.current = record;
		onChangeRef.current(record);
	}, [pairs]);

	const addPair = useCallback(() => {
		setPairs((prev) => {
			const id = nextId.current++;
			return [...prev, { id, key: "", value: "" }];
		});
	}, []);

	const removePair = useCallback((index: number) => {
		setPairs((prev) => {
			const removed = prev[index];
			if (removed && removed.key.trim()) {
				idMapRef.current.delete(removed.key.trim());
			}
			return prev.filter((_, i) => i !== index);
		});
	}, []);

	const updatePair = useCallback((index: number, field: "key" | "value", newValue: string) => {
		setPairs((prev) => prev.map((pair, i) => (i === index ? { ...pair, [field]: newValue } : pair)));
	}, []);

	return (
		<div className="rounded-md border p-4">
			<div className="mb-3 flex items-center justify-between">
				<div>
					<h4 className="text-sm font-medium">{label}</h4>
					{description && <p className="text-muted-foreground text-xs">{description}</p>}
				</div>
				<Button type="button" variant="outline" size="sm" onClick={addPair} data-testid="key-value-editor-add-button">
					<Plus className="mr-1 h-3 w-3" />
					Add
				</Button>
			</div>
			{pairs.length === 0 && <p className="text-muted-foreground py-2 text-center text-xs">No entries defined. Click Add to create one.</p>}
			{pairs.map((pair, index) => (
				<div key={pair.id} className="mb-2 flex items-center gap-2">
					<div className="flex-1">
						{index === 0 && <Label className="text-xs">Key</Label>}
						<Input
							placeholder={keyPlaceholder}
							value={pair.key}
							onChange={(e) => updatePair(index, "key", e.target.value)}
							data-testid={`key-value-editor-key-input-${index}`}
						/>
					</div>
					<div className="flex-[2]">
						{index === 0 && <Label className="text-xs">Value</Label>}
						<Input
							placeholder={valuePlaceholder}
							value={pair.value}
							onChange={(e) => updatePair(index, "value", e.target.value)}
							data-testid={`key-value-editor-value-input-${index}`}
						/>
					</div>
					<Button
						type="button"
						variant="ghost"
						size="icon"
						className={index === 0 ? "mt-5" : "mt-0"}
						onClick={() => removePair(index)}
						aria-label={`Remove key-value pair ${index + 1}`}
						title="Remove entry"
						data-testid={`key-value-editor-delete-button-${index}`}
					>
						<Trash2 className="h-4 w-4" />
					</Button>
				</div>
			))}
		</div>
	);
}