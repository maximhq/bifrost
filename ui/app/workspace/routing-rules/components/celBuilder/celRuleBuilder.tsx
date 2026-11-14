/**
 * CEL Rule Builder for Routing Rules
 * Thin wrapper around the reusable CELRuleBuilder with routing-specific config
 */

import { CELBuilderMode, CELRuleBuilder as BaseCELRuleBuilder } from "@/components/ui/custom/celBuilder";
import { getRoutingFields } from "@/lib/config/celFieldsRouting";
import { celOperatorsRouting } from "@/lib/config/celOperatorsRouting";
import { convertRuleGroupToCEL, validateRegexPattern } from "@/lib/utils/celConverterRouting";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { RuleGroupType } from "react-querybuilder";

interface CELRuleBuilderProps {
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

export function CELRuleBuilder({
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
}: CELRuleBuilderProps) {
	const { i18n } = useTranslation();
	// getRoutingFields copies the translated getters into plain strings, so rebuild on language change.
	const fields = useMemo(() => getRoutingFields(providers, models), [providers, models, i18n.language]);

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