/**
 * CEL Rule Builder Component for Routing Rules
 * Visual query builder for creating CEL expressions
 */

"use client";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Loader2, Check, Copy } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Field, QueryBuilder, RuleGroupType } from "react-querybuilder";
import "react-querybuilder/dist/query-builder.css";
import { getRoutingFields } from "@/lib/config/celFieldsRouting";
import { celOperatorsRouting } from "@/lib/config/celOperatorsRouting";
import { convertRuleGroupToCEL } from "@/lib/utils/celConverterRouting";
import { ActionButton } from "./actionButton";
import { CombinatorSelector } from "./combinatorSelector";
import { FieldSelector } from "./fieldSelector";
import { OperatorSelector } from "./operatorSelector";
import { ValueEditor } from "./valueEditor";
import { QueryBuilderWrapper } from "./queryBuilderWrapper";

interface CELRuleBuilderProps {
	onChange?: (celExpression: string, query: RuleGroupType) => void;
	initialQuery?: RuleGroupType;
	providers?: string[];
	models?: string[];
	isLoading?: boolean;
}

const defaultQuery: RuleGroupType = {
	combinator: "and",
	rules: [],
};

export function CELRuleBuilder({
	onChange,
	initialQuery,
	providers = [],
	models = [],
	isLoading = false,
}: CELRuleBuilderProps) {
	const [query, setQuery] = useState<RuleGroupType>(initialQuery || defaultQuery);
	const [celExpression, setCelExpression] = useState("");
	const [copied, setCopied] = useState(false);
	const onChangeRef = useRef(onChange);

	// Keep ref updated so the query effect always invokes the latest callback
	useEffect(() => {
		onChangeRef.current = onChange;
	}, [onChange]);

	// Generate fields with dynamic providers and models
	const fields = useMemo(() => {
		const celFields = getRoutingFields(providers, models);
		return celFields.map((field) => ({
			...field,
			value: field.name,
		})) as Field[];
	}, [providers, models]);

	useEffect(() => {
		const expression = convertRuleGroupToCEL(query);
		setCelExpression(expression);
		onChangeRef.current?.(expression, query);
	}, [query]);

	const handleCopy = async () => {
		await navigator.clipboard.writeText(celExpression);
		setCopied(true);
		setTimeout(() => setCopied(false), 2000);
	};

	// Show loading state
	if (isLoading) {
		return (
			<div className="flex items-center justify-center space-x-2 rounded-md border p-8">
				<Loader2 className="h-5 w-5 animate-spin" />
				<span className="text-muted-foreground text-sm">Loading CEL builder...</span>
			</div>
		);
	}

	return (
		<div className="space-y-4">
			<div className="rounded-md border">
				<div className="custom-scrollbar flex w-full flex-col overflow-scroll">
					<QueryBuilderWrapper>
						<QueryBuilder
							fields={fields}
							query={query}
							onQueryChange={setQuery}
							controlClassnames={{ queryBuilder: "queryBuilder-branches" }}
							operators={celOperatorsRouting.map((op) => ({
								name: op.name,
								label: op.label,
							}))}
							controlElements={{
								fieldSelector: FieldSelector,
								operatorSelector: OperatorSelector,
								valueEditor: ValueEditor,
								addRuleAction: ActionButton,
								addGroupAction: ActionButton,
								removeRuleAction: ActionButton,
								removeGroupAction: ActionButton,
								combinatorSelector: CombinatorSelector,
							}}
							translations={{
								addRule: { label: "Add Rule" },
								addGroup: { label: "Add Rule Group" },
							}}
						/>
					</QueryBuilderWrapper>
				</div>
			</div>

			<div className="space-y-2">
				<div className="flex items-center justify-between">
					<Label>CEL Expression Preview</Label>
					<Button
						variant="outline"
						size="sm"
						onClick={handleCopy}
						disabled={!celExpression}
						className="gap-2"
					>
						{copied ? (
							<>
								<Check className="h-4 w-4" />
								Copied
							</>
						) : (
							<>
								<Copy className="h-4 w-4" />
								Copy
							</>
						)}
					</Button>
				</div>
				<Textarea
					value={celExpression || "No rules defined yet"}
					readOnly
					className="font-mono text-sm"
					rows={4}
				/>
			</div>
		</div>
	);
}
