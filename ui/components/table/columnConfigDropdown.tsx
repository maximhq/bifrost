"use client";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { ColumnConfigEntry } from "./hooks/useColumnConfig";
import { Columns3, RotateCcw } from "lucide-react";

interface ColumnConfigDropdownProps {
	entries: ColumnConfigEntry[];
	labels?: Record<string, string>;
	onToggleVisibility: (columnId: string) => void;
	onReset: () => void;
}

function formatColumnId(id: string): string {
	return id
		.replace(/^metadata_/, "")
		.replace(/_/g, " ")
		.replace(/\b\w/g, (c) => c.toUpperCase());
}

export function ColumnConfigDropdown({ entries, labels = {}, onToggleVisibility, onReset }: ColumnConfigDropdownProps) {
	return (
		<Popover>
			<PopoverTrigger asChild>
				<Button variant="outline" size="sm" className="h-7.5" data-testid="column-config-trigger" aria-label="Column configuration">
					<Columns3 className="h-4 w-4" />
				</Button>
			</PopoverTrigger>
			<PopoverContent className="w-[200px] p-2" align="end">
				<div className="space-y-1">
					<div className="text-muted-foreground px-1 pb-1 text-xs font-medium">Toggle Columns</div>
					{entries.map((entry) => (
						<label key={entry.id} className="hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1 py-1">
							<Checkbox checked={entry.visible} onCheckedChange={() => onToggleVisibility(entry.id)} data-testid={`column-visibility-${entry.id}`} />
							<span className="truncate text-sm">{labels[entry.id] ?? formatColumnId(entry.id)}</span>
						</label>
					))}
					<div className="border-t pt-1">
						<Button type="button" onClick={onReset} variant="ghost" className="w-full justify-start text-sm" data-testid="column-reset-default">
							<RotateCcw className="h-3 w-3" />
							Reset to default
						</Button>
					</div>
				</div>
			</PopoverContent>
		</Popover>
	);
}
