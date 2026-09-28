/**
 * Operator Selector Component for CEL Rule Builder
 * Allows selection of operators for CEL expressions
 */

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { OperatorSelectorProps } from "react-querybuilder";
import { useTranslation } from "react-i18next";

export function OperatorSelector({ value, handleOnChange, options }: OperatorSelectorProps) {
	const { t } = useTranslation("observability");
	return (
		<Select value={value || ""} onValueChange={handleOnChange}>
			<SelectTrigger className="w-[160px]">
				<SelectValue placeholder={t("celBuilder.selectOperator")} />
			</SelectTrigger>
			<SelectContent>
				{options.map((option) => {
					// Handle option groups (not currently used, but type-safe)
					if ("options" in option) {
						return null;
					}
					// Handle regular options - skip empty values
					if (!option.name) {
						return null;
					}
					return (
						<SelectItem key={option.name} value={option.name} disabled={option.disabled}>
							{option.label}
						</SelectItem>
					);
				})}
			</SelectContent>
		</Select>
	);
}