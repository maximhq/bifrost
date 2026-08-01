import {
	Combobox,
	ComboboxContent,
	ComboboxGroup,
	ComboboxInput,
	ComboboxItem,
	ComboboxLabel,
	ComboboxList,
	ComboboxSeparator,
} from "@/components/ui/combobox";
import { Label } from "@/components/ui/label";
import type { DBKey, VirtualKey } from "@/lib/types/governance";
import { Loader2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

export function ApiKeySelectorView({
	providerKeys,
	virtualKeys,
	value,
	onValueChange,
	disabled,
	placeholder,
	requireVirtualKey,
	onSearchChange,
	isSearching,
}: {
	providerKeys: DBKey[];
	virtualKeys: VirtualKey[];
	value: string;
	onValueChange: (v: string | null) => void;
	disabled?: boolean;
	placeholder?: string;
	requireVirtualKey?: boolean;
	/** Mirrors typing to the caller so virtual keys can be searched server-side. */
	onSearchChange?: (search: string) => void;
	isSearching?: boolean;
}) {
	const [query, setQuery] = useState("");

	// Provider keys are already in memory, so they filter here. Virtual keys
	// arrive from the server pre-narrowed by the same query.
	const apiKeyOptions = useMemo(() => {
		if (requireVirtualKey) return [];
		const opts = [{ label: "Auto (default)", value: "__auto__" }, ...providerKeys.map((k) => ({ label: k.name, value: k.key_id }))];
		if (!query) return opts;
		const q = query.toLowerCase();
		return opts.filter((o) => o.label.toLowerCase().includes(q));
	}, [providerKeys, requireVirtualKey, query]);

	const virtualKeyOptions = useMemo(() => virtualKeys.map((vk) => ({ label: vk.name, value: vk.value })), [virtualKeys]);

	// Only one page of virtual keys is loaded at a time, so the selected key can
	// drop out of the list as the search narrows. Names seen once are kept so the
	// trigger doesn't lose its label.
	const labelCacheRef = useRef<Map<string, string>>(new Map());
	useEffect(() => {
		for (const o of virtualKeyOptions) labelCacheRef.current.set(o.value, o.label);
	}, [virtualKeyOptions]);

	const handleSearchChange = useCallback(
		(v: string) => {
			setQuery(v);
			onSearchChange?.(v);
		},
		[onSearchChange],
	);

	const getLabel = useCallback(
		(val: string | null) => {
			if (!val) return "";
			if (val === "__auto__") return requireVirtualKey ? "" : "Auto (default)";
			const providerKey = providerKeys.find((k) => k.key_id === val);
			if (providerKey) return providerKey.name;
			const virtualKey = virtualKeys.find((vk) => vk.value === val);
			if (virtualKey) return virtualKey.name;
			// Never fall back to `val` — for a virtual key that is the secret itself.
			return labelCacheRef.current.get(val) ?? "";
		},
		[providerKeys, virtualKeys, requireVirtualKey],
	);

	const hasResults = apiKeyOptions.length > 0 || virtualKeyOptions.length > 0;

	return (
		<div className="flex flex-col gap-2">
			<Label className="text-muted-foreground text-xs font-medium uppercase">
				{requireVirtualKey ? "Virtual key" : "Virtual key / API Key"}
			</Label>
			<Combobox
				value={value}
				onValueChange={(v) => onValueChange(v)}
				onOpenChange={(open) => {
					if (open) handleSearchChange("");
				}}
				onInputValueChange={handleSearchChange}
				filter={null}
				itemToStringLabel={getLabel}
			>
				<ComboboxInput
					placeholder={placeholder ?? (requireVirtualKey ? "Select virtual key" : "Select API key")}
					showClear={Boolean(value) && value !== "__auto__"}
					showTrigger
					disabled={disabled}
				/>
				<ComboboxContent>
					<ComboboxList>
						{apiKeyOptions.length > 0 && (
							<ComboboxGroup>
								<ComboboxLabel>API Keys</ComboboxLabel>
								{apiKeyOptions.map((o) => (
									<ComboboxItem key={o.value} value={o.value}>
										{o.label}
									</ComboboxItem>
								))}
							</ComboboxGroup>
						)}
						{apiKeyOptions.length > 0 && virtualKeyOptions.length > 0 && <ComboboxSeparator />}
						{virtualKeyOptions.length > 0 && (
							<ComboboxGroup>
								<ComboboxLabel>Virtual Keys</ComboboxLabel>
								{virtualKeyOptions.map((o) => (
									<ComboboxItem key={o.value} value={o.value}>
										{o.label}
									</ComboboxItem>
								))}
							</ComboboxGroup>
						)}
						{isSearching && !hasResults && (
							<div className="text-muted-foreground flex items-center justify-center gap-2 py-6 text-sm">
								<Loader2 className="size-4 animate-spin" />
								Searching...
							</div>
						)}
						{!isSearching && !hasResults && <div className="text-muted-foreground py-6 text-center text-sm">No results found.</div>}
					</ComboboxList>
				</ComboboxContent>
			</Combobox>
		</div>
	);
}
