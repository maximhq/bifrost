"use client";

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

interface ModelFilterSelectProps {
	models: string[];
	selectedModel: string;
	onModelChange: (model: string) => void;
	"data-testid"?: string;
}

export function ModelFilterSelect({ models, selectedModel, onModelChange, "data-testid": testId }: ModelFilterSelectProps) {
	return (
		<Select value={selectedModel} onValueChange={onModelChange}>
			<SelectTrigger className="h-5 w-[130px] text-xs" data-testid={testId}>
				<SelectValue placeholder="All Models" />
			</SelectTrigger>
			<SelectContent>
				<SelectItem value="all">All Models</SelectItem>
				{models.filter(Boolean).map((model) => (
					<SelectItem key={model} value={model} className="text-xs">
						{model}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}
