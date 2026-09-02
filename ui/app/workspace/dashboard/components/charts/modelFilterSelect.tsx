import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useTranslation } from "react-i18next";

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
	placeholder,
	"data-testid": testId,
}: ModelFilterSelectProps) {
	const { t } = useTranslation("observability");
	const resolvedPlaceholder = placeholder ?? t("labels.allModels");
	return (
		<Select value={selectedModel} onValueChange={onModelChange}>
			<SelectTrigger className="!h-7.5 w-[110px] text-xs sm:w-[130px]" data-testid={testId} size="sm">
				<SelectValue placeholder={resolvedPlaceholder} />
			</SelectTrigger>
			<SelectContent>
				<SelectItem value="all">{resolvedPlaceholder}</SelectItem>
				{models.filter(Boolean).map((model) => (
					<SelectItem key={model} value={model} className="text-xs">
						{model}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}