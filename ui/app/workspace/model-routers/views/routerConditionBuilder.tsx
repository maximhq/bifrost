/**
 * The condition builder for a model router's routing: the routing rules'
 * builder fields and operators, less complexity_tier, which the router
 * decides only once it has taken the request.
 */

import { CELBuilderMode, CELRuleBuilder as BaseCELRuleBuilder } from "@/components/ui/custom/celBuilder";
import { getRoutingFields } from "@/lib/config/celFieldsRouting";
import { celOperatorsRouting } from "@/lib/config/celOperatorsRouting";
import { convertRuleGroupToCEL, validateRegexPattern } from "@/lib/utils/celConverterRouting";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { RuleGroupType } from "react-querybuilder";

// complexity_tier is what the router decides once it takes the request, so
// its own condition cannot use it.
const EXCLUDED_FIELDS = ["complexity_tier"];

interface RouterConditionBuilderProps {
	onChange?: (celExpression: string, query: RuleGroupType) => void;
	initialQuery?: RuleGroupType;
	providers?: string[];
	models?: string[];
	allowCustomModels?: boolean;
	isLoading?: boolean;
	allowCelMode?: boolean;
	initialMode?: CELBuilderMode;
	initialCel?: string;
	onModeChange?: (mode: CELBuilderMode) => void;
	celError?: string | null;
}

export function RouterConditionBuilder({
	onChange,
	initialQuery,
	providers = [],
	models = [],
	isLoading = false,
	allowCustomModels = false,
	allowCelMode = false,
	initialMode = "builder",
	initialCel = "",
	onModeChange,
	celError = null,
}: RouterConditionBuilderProps) {
	const { i18n } = useTranslation();
	// getRoutingFields copies the translated getters into plain strings, so rebuild on language change.
	const fields = useMemo(
		() => getRoutingFields(providers, models).filter((field) => !EXCLUDED_FIELDS.includes(field.name)),
		[providers, models, i18n.language],
	);

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
			allowCelMode={allowCelMode}
			initialMode={initialMode}
			initialCel={initialCel}
			onModeChange={onModeChange}
			celError={celError}
		/>
	);
}