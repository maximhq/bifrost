/**
 * CEL Rule Builder for Routing Rules
 * Thin wrapper around the reusable CELRuleBuilder with routing-specific config
 */

"use client";

import { CELRuleBuilder as BaseCELRuleBuilder } from "@/components/ui/custom/celBuilder";
import { getRoutingFields } from "@/lib/config/celFieldsRouting";
import { celOperatorsRouting } from "@/lib/config/celOperatorsRouting";
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
			fields={fields}
			operators={celOperatorsRouting}
			convertToCEL={convertRuleGroupToCEL}
			validateRegex={validateRegexPattern}
			builderContext={{ allowCustomModels }}
		/>
	);
}