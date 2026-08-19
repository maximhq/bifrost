/**
 * CEL Rule Builder Component
 * Reusable visual query builder for creating CEL expressions
 */

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { cn } from "@/lib/utils";
import { Check, Copy, Loader2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Field, QueryBuilder, RuleGroupType } from "react-querybuilder";
import "react-querybuilder/dist/query-builder.css";
import { normalizeRoutingRuleGroupQuery } from "@/lib/utils/routingRuleGroupQuery";
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

export type CELBuilderMode = "builder" | "cel";

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
	/**
	 * When true, a Builder | CEL toggle is shown. In CEL mode the CEL expression
	 * becomes hand-editable and drives onChange directly (with an empty query),
	 * so rules authored outside the visual builder can be viewed and edited.
	 */
	allowCelMode?: boolean;
	/** Mode to open in when allowCelMode is set. Defaults to "builder". */
	initialMode?: CELBuilderMode;
	/** Seed text for the editable CEL textarea when opening in CEL mode. */
	initialCel?: string;
	/** Notified when the user switches between Builder and CEL mode. */
	onModeChange?: (mode: CELBuilderMode) => void;
	/**
	 * Validation error to surface inline beneath the editable CEL textarea (CEL mode only),
	 * e.g. a server-side compile error returned on save. Cleared by the parent on edit.
	 */
	celError?: string | null;
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
	allowCelMode = false,
	initialMode = "builder",
	initialCel = "",
	onModeChange,
	celError = null,
	options = {
		hideCELExpression: false,
	},
}: CELRuleBuilderProps) {
	const normalizedInitial = normalizeRoutingRuleGroupQuery(initialQuery ?? defaultQuery);
	const [query, setQuery] = useState<RuleGroupType>(normalizedInitial);
	const [celExpression, setCelExpression] = useState("");
	const [mode, setMode] = useState<CELBuilderMode>(allowCelMode ? initialMode : "builder");
	/** Hand-editable CEL text, used only in CEL mode. */
	const [celText, setCelText] = useState(initialCel);
	/** Guards the Cancel action of the CEL→Builder confirm dialog. */
	const [confirmSwitchToBuilder, setConfirmSwitchToBuilder] = useState(false);
	const onChangeRef = useRef(onChange);
	const convertToCELRef = useRef(convertToCEL);
	const onModeChangeRef = useRef(onModeChange);
	/** Skip notifying parent on the first run so opening the editor does not clear an existing CEL from the form when query is empty or invalid. */
	const skipOnChangeOnMount = useRef(true);
	const { copy, copied } = useCopyToClipboard();

	useEffect(() => {
		onModeChangeRef.current = onModeChange;
	}, [onModeChange]);

	// Keep refs updated so the query effect always invokes the latest callbacks
	useEffect(() => {
		onChangeRef.current = onChange;
	}, [onChange]);
	useEffect(() => {
		convertToCELRef.current = convertToCEL;
	}, [convertToCEL]);

	// Convert field definitions to react-querybuilder Field format
	const fields = useMemo(() => {
		return fieldDefinitions.map((field) => ({
			...field,
			value: field.name,
		})) as Field[];
	}, [fieldDefinitions]);

	useEffect(() => {
		const expression = convertToCELRef.current(query);
		setCelExpression(expression);
		if (skipOnChangeOnMount.current) {
			skipOnChangeOnMount.current = false;
			return;
		}
		onChangeRef.current?.(expression, query);
	}, [query]);

	const handleCelTextChange = (value: string) => {
		setCelText(value);
		// CEL mode owns the expression directly; the query builder state is left empty.
		onChangeRef.current?.(value, defaultQuery);
	};

	const switchToCel = () => {
		// Seed the editable text from whatever the builder currently produces so nothing is lost.
		const seed = convertToCELRef.current(query);
		setCelText(seed);
		setMode("cel");
		onModeChangeRef.current?.("cel");
		onChangeRef.current?.(seed, defaultQuery);
	};

	const applySwitchToBuilder = () => {
		setMode("builder");
		onModeChangeRef.current?.("builder");
		const expression = convertToCELRef.current(query);
		onChangeRef.current?.(expression, query);
	};

	const requestSwitchToBuilder = () => {
		// Switching back to the builder discards any hand-written CEL (there is no CEL→query parser),
		// so confirm first when the text would actually be lost.
		if (celText.trim() && celText.trim() !== convertToCELRef.current(query).trim()) {
			setConfirmSwitchToBuilder(true);
			return;
		}
		applySwitchToBuilder();
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

	const copyValue = mode === "cel" ? celText : celExpression;

	return (
		<div className="space-y-4">
			{allowCelMode && (
				<div className="flex items-center justify-end">
					<div className="bg-muted inline-flex rounded-md p-0.5 text-sm">
						<button
							type="button"
							onClick={requestSwitchToBuilder}
							className={cn(
								"rounded px-3 py-1 font-medium transition-colors",
								mode === "builder" ? "bg-card text-foreground shadow-sm" : "text-muted-foreground",
							)}
							data-testid="cel-builder-mode-builder"
						>
							Builder
						</button>
						<button
							type="button"
							onClick={switchToCel}
							className={cn(
								"rounded px-3 py-1 font-medium transition-colors",
								mode === "cel" ? "bg-card text-foreground shadow-sm" : "text-muted-foreground",
							)}
							data-testid="cel-builder-mode-cel"
						>
							CEL
						</button>
					</div>
				</div>
			)}

			{mode === "builder" && (
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
			)}

			{(mode === "cel" || !options.hideCELExpression) && (
				<div className="space-y-2">
					<div className="flex items-center justify-between">
						<Label>{mode === "cel" ? "CEL Expression" : "CEL Expression Preview"}</Label>
						<Button
							variant="outline"
							size="sm"
							onClick={() => copy(copyValue)}
							disabled={!copyValue}
							className="gap-2"
							type="button"
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
					{mode === "cel" ? (
						<>
							<Textarea
								value={celText}
								onChange={(e) => handleCelTextChange(e.target.value)}
								className={cn("font-mono text-sm", celError && "border-destructive focus-visible:ring-destructive")}
								rows={4}
								placeholder='e.g. model == "claude-sonnet-4-6"'
								aria-invalid={!!celError}
								data-testid="cel-builder-cel-textarea"
							/>
							{celError ? (
								<p className="text-destructive text-xs whitespace-pre-wrap" data-testid="cel-builder-cel-error">
									{celError}
								</p>
							) : (
								<p className="text-muted-foreground text-xs">Leave empty to match all requests.</p>
							)}
						</>
					) : (
						<Textarea value={celExpression || "No rules defined yet"} readOnly className="font-mono text-sm" rows={4} />
					)}
				</div>
			)}

			<AlertDialog open={confirmSwitchToBuilder} onOpenChange={setConfirmSwitchToBuilder}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Switch to the visual builder?</AlertDialogTitle>
						<AlertDialogDescription>
							The visual builder can&apos;t import a hand-written CEL expression, so your current CEL will be discarded and the builder
							will start empty. Copy it first if you want to keep it.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								setConfirmSwitchToBuilder(false);
								applySwitchToBuilder();
							}}
						>
							Discard CEL &amp; switch
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}