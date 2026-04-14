import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

interface ProviderFilterSelectProps {
	providers: string[];
	selectedProvider: string;
	onProviderChange: (provider: string) => void;
	"data-testid"?: string;
}

export function ProviderFilterSelect({ providers, selectedProvider, onProviderChange, "data-testid": testId }: ProviderFilterSelectProps) {
	return (
		<Select value={selectedProvider} onValueChange={onProviderChange}>
			<SelectTrigger className="!h-7.5 w-[110px] text-xs sm:w-[130px]" data-testid={testId} size="sm">
				<SelectValue placeholder="All Providers" />
			</SelectTrigger>
			<SelectContent>
				<SelectItem value="all">All Providers</SelectItem>
				{providers.filter(Boolean).map((provider) => (
					<SelectItem key={provider} value={provider} className="text-xs">
						{provider}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}