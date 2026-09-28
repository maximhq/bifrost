import { ProviderSelector } from "@/components/ui/providerSelector";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

// The list here comes from the analytics series, not the providers API, so it can name a
// provider that has since been deleted. The sentinel is what "no filter" is stored as.
const ALL_PROVIDERS_VALUE = "all";

interface ProviderFilterSelectProps {
	providers: string[];
	selectedProvider: string;
	onProviderChange: (provider: string) => void;
	"data-testid"?: string;
}

export function ProviderFilterSelect({ providers, selectedProvider, onProviderChange, "data-testid": testId }: ProviderFilterSelectProps) {
	const { t } = useTranslation("observability");
	const allProvidersOption = useMemo(() => ({ value: ALL_PROVIDERS_VALUE, label: t("labels.allProviders") }), [t]);
	return (
		<ProviderSelector
			source="values"
			values={providers}
			size="sm"
			className="!h-7.5 w-[110px] text-xs sm:w-[130px]"
			contentWidth={220}
			allOption={allProvidersOption}
			value={selectedProvider || ALL_PROVIDERS_VALUE}
			onChange={onProviderChange}
			data-testid={testId}
		/>
	);
}
