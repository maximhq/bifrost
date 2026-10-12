import { ProviderSelector } from "@/components/ui/providerSelector";
import { BaseProviderNames, type BaseProvider } from "@/lib/types/config";

interface BaseProviderSelectorProps {
	value: string;
	onChange: (provider: BaseProvider) => void;
	disabled?: boolean;
	placeholder?: string;
	className?: string;
	inputId?: string;
	/**
	 * FormControl injects `id`, `aria-describedby` and `aria-invalid` onto its direct child.
	 * Both custom-provider forms wrap this component in one, so they are taken here and passed
	 * down — otherwise FormLabel's htmlFor names an id nothing carries, and the validation
	 * message is never announced with the control.
	 */
	id?: string;
	"aria-describedby"?: string;
	"aria-invalid"?: boolean;
	"data-testid"?: string;
}

/**
 * Picks the wire format a custom provider speaks, which is a different thing from picking a
 * provider: the list is the fixed set of request shapes Bifrost can translate to, not the
 * providers anyone has configured.
 */
export function BaseProviderSelector({
	value,
	onChange,
	disabled = false,
	placeholder = "Select base format",
	className,
	inputId,
	id,
	"aria-describedby": ariaDescribedBy,
	"aria-invalid": ariaInvalid,
	"data-testid": dataTestId,
}: BaseProviderSelectorProps) {
	return (
		<ProviderSelector
			source="values"
			values={BaseProviderNames}
			value={value}
			onChange={(next: string) => onChange(next as BaseProvider)}
			disabled={disabled}
			placeholder={placeholder}
			searchPlaceholder="Search formats..."
			className={className}
			inputId={inputId ?? id}
			ariaDescribedBy={ariaDescribedBy}
			ariaInvalid={ariaInvalid}
			data-testid={dataTestId}
		/>
	);
}