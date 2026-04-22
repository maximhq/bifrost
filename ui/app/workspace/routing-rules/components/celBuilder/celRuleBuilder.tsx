/**
 * CEL Rule Builder for Routing Rules
 * Thin wrapper around the reusable CELRuleBuilder with routing-specific config
 */

import { CELRuleBuilder as BaseCELRuleBuilder } from "@/components/ui/custom/celBuilder";
import { CELFieldDefinition, getRoutingFields } from "@/lib/config/celFieldsRouting";
import { celOperatorsRouting } from "@/lib/config/celOperatorsRouting";
import { COMPLEXITY_TIER_VALUES } from "@/lib/types/routingRules";
import { convertRuleGroupToCEL, validateRegexPattern } from "@/lib/utils/celConverterRouting";
import { useMemo } from "react";
import { RuleGroupType } from "react-querybuilder";

interface CELRuleBuilderProps {
	onChange?: (celExpression: string, query: RuleGroupType) => void;
	initialQuery?: RuleGroupType;
	providers?: string[];
	models?: string[];
	allowCustomModels?: boolean;
	isLoading?: boolean;
}

const complexityTierField: CELFieldDefinition = {
	name: "complexity_tier",
	label: "Complexity Tier",
	placeholder: "Select complexity tier",
	inputType: "select",
	valueEditorType: () => "select",
	operators: ["=", "!=", "in", "notIn"],
	defaultOperator: "=",
	values: COMPLEXITY_TIER_VALUES.map((t) => ({ name: t, label: t })),
	description: "Filter rules by the type of complexity tier",
};

export function CELRuleBuilder({
	onChange,
	initialQuery,
	providers = [],
	models = [],
	isLoading = false,
	allowCustomModels = false,
}: CELRuleBuilderProps) {
	const fields = useMemo(() => getRoutingFields(providers, models), [providers, models]);

	return (
		<BaseCELRuleBuilder
			onChange={onChange}
			initialQuery={initialQuery}
			isLoading={isLoading}
			fields={[...fields, complexityTierField]}
			operators={celOperatorsRouting}
			convertToCEL={convertRuleGroupToCEL}
			validateRegex={validateRegexPattern}
			builderContext={{ allowCustomModels }}
		/>
	);
}