import {
	Combobox,
	ComboboxInput,
	ComboboxContent,
	ComboboxList,
	ComboboxItem,
	ComboboxGroup,
	ComboboxLabel,
	ComboboxSeparator,
} from "@/components/ui/combobox";
import { useCallback, useMemo, useState } from "react";
import type { DBKey, VirtualKey } from "@/lib/types/governance";
import { Label } from "@/components/ui/label";

export function ApiKeySelectorView({
	providerKeys,
	virtualKeys,
	value,
	onValueChange,
	disabled,
	placeholder,
}: {
	providerKeys: DBKey[];
	virtualKeys: VirtualKey[];
	value: string;
	onValueChange: (v: string | null) => void;
	disabled?: boolean;
	placeholder?: string;
}) {
	const [query, setQuery] = useState("");

	const allOptions = useMemo(() => {
		const apiKeyOpts = providerKeys.map((k) => ({ label: k.name, value: k.key_id, group: "api" as const }));
		const vkOpts = virtualKeys.map((vk) => ({ label: vk.name, value: vk.value, group: "virtual" as const }));
		return [{ label: "Auto (default)", value: "__auto__", group: "api" as const }, ...apiKeyOpts, ...vkOpts];
	}, [providerKeys, virtualKeys]);

	const filtered = useMemo(() => {
		if (!query) return allOptions;
		const q = query.toLowerCase();
		return allOptions.filter((o) => o.label.toLowerCase().includes(q));
	}, [allOptions, query]);

	const filteredApiKeys = useMemo(() => filtered.filter((o) => o.group === "api"), [filtered]);
	const filteredVirtualKeys = useMemo(() => filtered.filter((o) => o.group === "virtual"), [filtered]);

	const getLabel = useCallback((val: string | null) => allOptions.find((o) => o.value === val)?.label ?? val ?? "", [allOptions]);

	return (
		<div className="flex flex-col gap-2">
			<Label className="text-muted-foreground text-xs font-medium uppercase">Virtual key/ API Key</Label>
			<Combobox
				value={value}
				onValueChange={(v) => onValueChange(v)}
				onOpenChange={(open) => {
					if (open) setQuery("");
				}}
				onInputValueChange={(v) => setQuery(v)}
				filter={null}
				itemToStringLabel={getLabel}
			>
				<ComboboxInput placeholder={placeholder ?? "Select API key"} showClear={value !== "__auto__"} showTrigger disabled={disabled} />
				<ComboboxContent>
					<ComboboxList>
						{filteredApiKeys.length > 0 && (
							<ComboboxGroup>
								<ComboboxLabel>API Keys</ComboboxLabel>
								{filteredApiKeys.map((o) => (
									<ComboboxItem key={o.value} value={o.value}>
										{o.label}
									</ComboboxItem>
								))}
							</ComboboxGroup>
						)}
						{filteredApiKeys.length > 0 && filteredVirtualKeys.length > 0 && <ComboboxSeparator />}
						{filteredVirtualKeys.length > 0 && (
							<ComboboxGroup>
								<ComboboxLabel>Virtual Keys</ComboboxLabel>
								{filteredVirtualKeys.map((o) => (
									<ComboboxItem key={o.value} value={o.value}>
										{o.label}
									</ComboboxItem>
								))}
							</ComboboxGroup>
						)}
						{filtered.length === 0 && <div className="text-muted-foreground py-6 text-center text-sm">No results found.</div>}
					</ComboboxList>
				</ComboboxContent>
			</Combobox>
		</div>
	);
}
