import { Button } from "@/components/ui/button";
import { ProviderSelector, type ProviderSelectorOption } from "@/components/ui/providerSelector";
import { PlusIcon, Settings2Icon } from "lucide-react";
import { useMemo } from "react";

export type ProviderOption = { name: string };

// Picked by the row that opens the custom-provider sheet, rather than adding a known one.
const CUSTOM_PROVIDER_VALUE = "__custom_provider__";

// Module level so the identity is stable across renders; the list never varies.
const CUSTOM_PROVIDER_OPTION: ProviderSelectorOption[] = [
	{ value: CUSTOM_PROVIDER_VALUE, label: "Custom provider...", icon: <Settings2Icon className="h-4 w-4" /> },
];

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
	const values = useMemo(() => knownProviders.map((p) => p.name), [knownProviders]);
	const excludeValues = useMemo(() => Array.from(existingInSidebar), [existingInSidebar]);

	return (
		<ProviderSelector
			mode="add"
			source="values"
			values={values}
			excludeValues={excludeValues}
			footerOptions={CUSTOM_PROVIDER_OPTION}
			disabled={disabled}
			searchPlaceholder="Search providers..."
			emptyMessage="No providers left to add"
			contentClassName="custom-scrollbar max-h-[min(70vh,24rem)]"
			contentTestId="add-provider-dropdown"
			optionTestId={(value) => (value === CUSTOM_PROVIDER_VALUE ? "add-provider-option-custom" : `add-provider-option-${value}`)}
			onSelect={(value) => (value === CUSTOM_PROVIDER_VALUE ? onAddCustomProvider() : onSelectKnownProvider(value))}
			trigger={
				<Button
					size={variant === "empty" ? "default" : "sm"}
					data-testid="add-provider-btn"
					className={variant === "empty" ? "" : "w-full justify-start"}
					aria-label="Add Provider"
					disabled={disabled}
					variant={"outline"}
				>
					<PlusIcon className="h-4 w-4" />
					{variant === "empty" ? <span>Add New Provider</span> : <div className="text-xs">Add New Provider</div>}
				</Button>
			}
		/>
	);
}