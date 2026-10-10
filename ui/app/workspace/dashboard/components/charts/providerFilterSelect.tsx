import { ProviderSelector } from "@/components/ui/providerSelector";

// The list here comes from the analytics series, not the providers API, so it can name a
// provider that has since been deleted. The sentinel is what "no filter" is stored as.
const ALL_PROVIDERS_VALUE = "all";
const ALL_PROVIDERS_OPTION = { value: ALL_PROVIDERS_VALUE, label: "All Providers" };

interface ProviderFilterSelectProps {
	providers: string[];
	selectedProvider: string;
	onProviderChange: (provider: string) => void;
	"data-testid"?: string;
}

export function ProviderFilterSelect({ providers, selectedProvider, onProviderChange, "data-testid": testId }: ProviderFilterSelectProps) {
	return (
		<ProviderSelector
			source="values"
			values={providers}
			size="sm"
			className="!h-7.5 w-[110px] text-xs sm:w-[130px]"
			contentWidth={220}
			allOption={ALL_PROVIDERS_OPTION}
			value={selectedProvider || ALL_PROVIDERS_VALUE}
			onChange={onProviderChange}
			data-testid={testId}
		/>
	);
}