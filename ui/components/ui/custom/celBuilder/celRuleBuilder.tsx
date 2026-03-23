/**
 * CEL Rule Builder Component
 * Reusable visual query builder for creating CEL expressions
 */

"use client";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Check, Copy, Loader2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Field, QueryBuilder, RuleGroupType } from "react-querybuilder";
import "react-querybuilder/dist/query-builder.css";
import { ActionButton } from "./actionButton";
import { CombinatorSelector } from "./combinatorSelector";
import { FieldSelector } from "./fieldSelector";
import { OperatorSelector } from "./operatorSelector";
import { QueryBuilderWrapper } from "./queryBuilderWrapper";
import { ValueEditor } from "./valueEditor";

export interface CELFieldDefinition {
	name: string;
	label: string;
	placeholder?: string;
	inputType?: string;
	valueEditorType?: string | ((operator: string) => string);
	operators?: string[];
	defaultOperator?: string;
	defaultValue?: any;
	values?: Array<{ name: string; label: string; disabled?: boolean }>;
	metricOptions?: Array<{ name: string; label: string }>;
	description?: string;
}

export interface CELOperatorDefinition {
	name: string;
	label: string;
	celSyntax: string;
}

export interface CELRuleBuilderProps {
	onChange?: (celExpression: string, query: RuleGroupType) => void;
	initialQuery?: RuleGroupType;
	isLoading?: boolean;
	/** Fields available in the query builder */
	fields: CELFieldDefinition[];
	/** Operators available in the query builder */
	operators: CELOperatorDefinition[];
	/** Function to convert a RuleGroupType to a CEL expression string */
	convertToCEL: (ruleGroup: RuleGroupType) => string;
	/** Optional regex validation function, passed to ValueEditor via context */
	validateRegex?: (pattern: string) => string | null;
	/** Additional context passed to the QueryBuilder controlElements */
	builderContext?: Record<string, any>;
	options?: {
		hideCELExpression?: boolean;
	};
}

const defaultQuery: RuleGroupType = {
	combinator: "and",
	rules: [],
};

export function CELRuleBuilder({
	onChange,
	initialQuery,
	isLoading = false,
	fields: fieldDefinitions,
	operators,
	convertToCEL,
	validateRegex,
	builderContext,
	options = {
		hideCELExpression: false,
	},
}: CELRuleBuilderProps) {
	const [query, setQuery] = useState<RuleGroupType>(initialQuery || defaultQuery);
	const [celExpression, setCelExpression] = useState("");
	const [copied, setCopied] = useState(false);
	const onChangeRef = useRef(onChange);

	// Keep ref updated so the query effect always invokes the latest callback
	useEffect(() => {
		onChangeRef.current = onChange;
	}, [onChange]);

	// Convert field definitions to react-querybuilder Field format
	const fields = useMemo(() => {
		return fieldDefinitions.map((field) => ({
			...field,
			value: field.name,
		})) as Field[];
	}, [fieldDefinitions]);

	useEffect(() => {
		const expression = convertToCEL(query);
		setCelExpression(expression);
		onChangeRef.current?.(expression, query);
	}, [query, convertToCEL]);

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

	const context = {
		...builderContext,
		...(validateRegex ? { validateRegex } : {}),
	};

	return (
		<div className="space-y-4">
			<div className="rounded-md border">
				<div className="custom-scrollbar flex w-full flex-col overflow-scroll">
					<QueryBuilderWrapper>
						<QueryBuilder
							fields={fields}
							query={query}
							onQueryChange={setQuery}
							context={context}
							controlClassnames={{ queryBuilder: "queryBuilder-branches" }}
							operators={operators.map((op) => ({
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

			{!options.hideCELExpression && (
				<div className="space-y-2">
					<div className="flex items-center justify-between">
						<Label>CEL Expression Preview</Label>
						<Button variant="outline" size="sm" onClick={handleCopy} disabled={!celExpression} className="gap-2">
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
					<Textarea value={celExpression || "No rules defined yet"} readOnly className="font-mono text-sm" rows={4} />
				</div>
			)}
		</div>
	);
}
