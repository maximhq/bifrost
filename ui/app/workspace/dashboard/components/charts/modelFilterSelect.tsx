import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { StartTruncatedLabel } from "@/components/ui/truncatedLabel";

interface ModelFilterSelectProps {
	models: string[];
	selectedModel: string;
	onModelChange: (model: string) => void;
	placeholder?: string;
	"data-testid"?: string;
}

export function ModelFilterSelect({
	models,
	selectedModel,
	onModelChange,
	placeholder = "All Models",
	"data-testid": testId,
}: ModelFilterSelectProps) {
	return (
		<Select value={selectedModel} onValueChange={onModelChange}>
			<SelectTrigger className="!h-7.5 w-[110px] text-xs sm:w-[130px]" data-testid={testId} size="sm">
				<SelectValue placeholder={placeholder} />
			</SelectTrigger>
			<SelectContent className="w-[300px] max-w-[var(--radix-popper-available-width)]">
				<SelectItem value="all">{placeholder}</SelectItem>
				{models.filter(Boolean).map((model) => (
					<SelectItem key={model} value={model} className="text-xs [&>span:last-child]:min-w-0">
						<StartTruncatedLabel>{model}</StartTruncatedLabel>
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}