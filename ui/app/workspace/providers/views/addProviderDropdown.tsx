import { Button } from "@/components/ui/button";
import { ProviderSelector, type ProviderSelectorOption } from "@/components/ui/providerSelector";
import { PlusIcon, Settings2Icon } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

export type ProviderOption = { name: string };

// Picked by the row that opens the custom-provider sheet, rather than adding a known one.
const CUSTOM_PROVIDER_VALUE = "__custom_provider__";

interface AddProviderDropdownProps {
	/** Provider names that are already in the sidebar (configured or added) */
	existingInSidebar: Set<string>;
	/** All known provider options to show (e.g. from ProviderNames / allProviders) */
	knownProviders: ProviderOption[];
	onSelectKnownProvider: (name: string) => void;
	onAddCustomProvider: () => void;
	disabled?: boolean;
	/** Optional: use compact trigger for empty state */
	variant?: "default" | "empty";
}

export function AddProviderDropdown({
	existingInSidebar,
	knownProviders,
	onSelectKnownProvider,
	onAddCustomProvider,
	disabled = false,
	variant = "default",
}: AddProviderDropdownProps) {
	const { t } = useTranslation("models");
	const values = useMemo(() => knownProviders.map((p) => p.name), [knownProviders]);
	const excludeValues = useMemo(() => Array.from(existingInSidebar), [existingInSidebar]);
	const customProviderOption = useMemo<ProviderSelectorOption[]>(
		() => [{ value: CUSTOM_PROVIDER_VALUE, label: t("providers.customProviderEllipsis"), icon: <Settings2Icon className="h-4 w-4" /> }],
		[t],
	);

	return (
		<ProviderSelector
			mode="add"
			source="values"
			values={values}
			excludeValues={excludeValues}
			footerOptions={customProviderOption}
			disabled={disabled}
			searchPlaceholder={t("providers.searchProviders")}
			emptyMessage={t("providers.noProvidersLeft")}
			contentClassName="custom-scrollbar max-h-[min(70vh,24rem)]"
			contentTestId="add-provider-dropdown"
			optionTestId={(value) => (value === CUSTOM_PROVIDER_VALUE ? "add-provider-option-custom" : `add-provider-option-${value}`)}
			onSelect={(value) => (value === CUSTOM_PROVIDER_VALUE ? onAddCustomProvider() : onSelectKnownProvider(value))}
			trigger={
				<Button
					size={variant === "empty" ? "default" : "sm"}
					data-testid="add-provider-btn"
					className={variant === "empty" ? "" : "w-full justify-start"}
					aria-label={t("providers.addNewProviderAria")}
					disabled={disabled}
					variant={"outline"}
				>
					<PlusIcon className="h-4 w-4" />
					{variant === "empty" ? <span>{t("providers.addNewProvider")}</span> : <div className="text-xs">{t("providers.addNewProvider")}</div>}
				</Button>
			}
		/>
	);
}
