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
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

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
	const { t } = useTranslation();
	const [query, setQuery] = useState("");

	const allOptions = useMemo(() => {
		const apiKeyOpts = providerKeys.map((k) => ({ label: k.name, value: k.key_id, group: "api" as const }));
		const vkOpts = virtualKeys.map((vk) => ({ label: vk.name, value: vk.value, group: "virtual" as const }));
		return [
			{ label: t("workspace.promptRepository.apiKeySelector.autoDefault"), value: "__auto__", group: "api" as const },
			...apiKeyOpts,
			...vkOpts,
		];
	}, [providerKeys, virtualKeys, t]);

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
			<Label className="text-muted-foreground text-xs font-medium uppercase">{t("workspace.promptRepository.apiKeySelector.label")}</Label>
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
				<ComboboxInput
					placeholder={placeholder ?? t("workspace.promptRepository.apiKeySelector.selectApiKey")}
					showClear={value !== "__auto__"}
					showTrigger
					disabled={disabled}
				/>
				<ComboboxContent>
					<ComboboxList>
						{filteredApiKeys.length > 0 && (
							<ComboboxGroup>
								<ComboboxLabel>{t("workspace.promptRepository.apiKeySelector.apiKeys")}</ComboboxLabel>
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
								<ComboboxLabel>{t("workspace.promptRepository.apiKeySelector.virtualKeys")}</ComboboxLabel>
								{filteredVirtualKeys.map((o) => (
									<ComboboxItem key={o.value} value={o.value}>
										{o.label}
									</ComboboxItem>
								))}
							</ComboboxGroup>
						)}
						{filtered.length === 0 && (
							<div className="text-muted-foreground py-6 text-center text-sm">
								{t("workspace.promptRepository.apiKeySelector.noResults")}
							</div>
						)}
					</ComboboxList>
				</ComboboxContent>
			</Combobox>
		</div>
	);
}