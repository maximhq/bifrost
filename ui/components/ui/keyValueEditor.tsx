"use client";

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

function recordToPairs(record: Record<string, string>): KeyValuePair[] {
	return Object.entries(record).map(([key, value], index) => ({ id: index, key, value }));
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
	const [pairs, setPairs] = useState<KeyValuePair[]>(() => recordToPairs(value));
	const nextId = useRef(pairs.length);
	const isFirstRender = useRef(true);

	useEffect(() => {
		if (isFirstRender.current) {
			isFirstRender.current = false;
			return;
		}
		onChange(pairsToRecord(pairs));
	}, [pairs, onChange]);

	const addPair = useCallback(() => {
		setPairs((prev) => {
			const id = nextId.current++;
			return [...prev, { id, key: "", value: "" }];
		});
	}, []);

	const removePair = useCallback((index: number) => {
		setPairs((prev) => prev.filter((_, i) => i !== index));
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
				<Button type="button" variant="outline" size="sm" onClick={addPair}>
					<Plus className="mr-1 h-3 w-3" />
					Add
				</Button>
			</div>
			{pairs.length === 0 && (
				<p className="text-muted-foreground py-2 text-center text-xs">
					No entries defined. Click Add to create one.
				</p>
			)}
			{pairs.map((pair, index) => (
				<div key={pair.id} className="mb-2 flex items-center gap-2">
					<div className="flex-1">
						{index === 0 && <Label className="text-xs">Key</Label>}
						<Input
							placeholder={keyPlaceholder}
							value={pair.key}
							onChange={(e) => updatePair(index, "key", e.target.value)}
						/>
					</div>
					<div className="flex-[2]">
						{index === 0 && <Label className="text-xs">Value</Label>}
						<Input
							placeholder={valuePlaceholder}
							value={pair.value}
							onChange={(e) => updatePair(index, "value", e.target.value)}
						/>
					</div>
					<Button
						type="button"
						variant="ghost"
						size="icon"
						className={index === 0 ? "mt-5" : "mt-0"}
						onClick={() => removePair(index)}
					>
						<Trash2 className="h-4 w-4" />
					</Button>
				</div>
			))}
		</div>
	);
}
