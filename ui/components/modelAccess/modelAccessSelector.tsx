import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import { type ReactNode, useState } from "react";
import { ModelAccessChipLabel } from "./modelAccessChip";
import { RegexPatternInput } from "./regexPatternInput";
import {
	addPattern,
	type ModelAccessMode,
	modelAccessPlaceholder,
	removePattern,
	replaceModels,
	splitModelAccess,
	toRegexEntry,
} from "./utils";

type EditorTab = "models" | "regex";

export interface ModelAccessSelectorProps {
	/** The list: model names, "*" alone for every model, and `regex:` entries for patterns. */
	value: string[];
	onChange: (next: string[]) => void;
	/** "allow" for allowed_models / models, "block" for blacklisted_models. Drives placeholders. */
	mode: ModelAccessMode;
	provider?: string;
	/** Key IDs that scope the model suggestions (governance surfaces). */
	keys?: string[];
	/** Offer the "All Models" (*) option in the picker. Defaults to true. */
	allowAllOption?: boolean;
	/** Bypass governance filtering when loading suggestions (provider key form). */
	unfiltered?: boolean;
	loadModelsOnEmptyProvider?: boolean | "base_models";
	disabled?: boolean;
	/**
	 * Optional label row content. When given, the Models | Regex toggle is
	 * rendered on the same row as the label to keep the layout compact.
	 */
	label?: ReactNode;
	"data-testid"?: string;
	inputId?: string;
	/** Injected by FormControl when the selector is used as a form field. */
	id?: string;
	"aria-describedby"?: string;
	"aria-invalid"?: boolean;
	menuPosition?: "absolute" | "fixed";
	menuPortalTarget?: HTMLElement | null;
	className?: string;
}

/**
 * The one editor for a model allow or block side. A small toggle switches
 * between picking concrete models (the multiselect everyone used before) and
 * adding regex patterns. Both views edit the same list: patterns are stored as
 * `regex:` entries next to the names.
 */
export function ModelAccessSelector({
	value,
	onChange,
	mode,
	provider,
	keys,
	allowAllOption = true,
	unfiltered,
	loadModelsOnEmptyProvider,
	disabled,
	label,
	inputId,
	menuPosition,
	menuPortalTarget,
	className,
	...rest
}: ModelAccessSelectorProps) {
	const testId = rest["data-testid"];
	// FormControl injects these onto its child; forward them to whichever input is showing
	// so the label, the error message and the invalid state still point at a real element.
	const controlId = inputId ?? rest.id;
	const describedBy = rest["aria-describedby"];
	const invalid = rest["aria-invalid"];
	const list = value ?? [];
	const { models, patterns } = splitModelAccess(list);
	// Until the user picks a view, follow what is configured, so a patterns-only rule is
	// visible right away instead of hidden behind the toggle. Deriving rather than storing
	// it keeps that true for values that arrive after mount, which is the normal case for a
	// form that loads its record asynchronously and resets afterwards. Once the user picks a
	// view it is theirs, and nothing moves it again.
	const [pickedTab, setPickedTab] = useState<EditorTab | null>(null);
	const tab: EditorTab = pickedTab ?? (patterns.length > 0 && models.length === 0 ? "regex" : "models");
	const hasWildcard = models.includes("*");

	const toggle = (
		<Tabs value={tab} onValueChange={(next) => setPickedTab(next as EditorTab)} className="shrink-0">
			<TabsList aria-label="Model entry type" className="h-6 rounded-sm p-0.5" data-testid={testId ? `${testId}-mode` : undefined}>
				<TabsTrigger
					value="models"
					disabled={disabled}
					className="rounded-[3px] px-2 text-[11px] leading-5"
					data-testid={testId ? `${testId}-mode-models` : undefined}
				>
					Models
				</TabsTrigger>
				<TabsTrigger
					value="regex"
					disabled={disabled}
					className="rounded-[3px] px-2 text-[11px] leading-5"
					data-testid={testId ? `${testId}-mode-regex` : undefined}
				>
					Regex
				</TabsTrigger>
			</TabsList>
		</Tabs>
	);

	return (
		<div className={cn("min-w-0 space-y-1.5", className)}>
			<div className="flex h-5 items-center justify-between gap-2">
				<div className="flex min-w-0 items-center gap-2">{label}</div>
				{toggle}
			</div>

			{tab === "models" ? (
				<ModelMultiselect
					allowAllOption={allowAllOption}
					hideSearchIcon
					data-testid={testId}
					inputId={controlId}
					ariaDescribedBy={describedBy}
					ariaInvalid={invalid}
					provider={provider}
					keys={keys}
					unfiltered={unfiltered}
					loadModelsOnEmptyProvider={loadModelsOnEmptyProvider}
					disabled={disabled}
					menuPosition={menuPosition}
					menuPortalTarget={menuPortalTarget}
					value={hasWildcard ? ["*"] : models}
					onChange={(next: string[]) => onChange(replaceModels(list, next))}
					placeholder={modelAccessPlaceholder(list, mode)}
					renderValueLabel={(option) => <ModelAccessChipLabel entry={option.value} />}
				/>
			) : (
				<div className="space-y-1.5">
					<RegexPatternInput
						data-testid={testId ? `${testId}-regex` : undefined}
						inputId={controlId}
						ariaDescribedBy={describedBy}
						ariaInvalid={invalid}
						disabled={disabled}
						onAdd={(pattern) => onChange(addPattern(list, pattern))}
					/>
					{patterns.length > 0 ? (
						<div className="flex flex-wrap gap-1" data-testid={testId ? `${testId}-entries` : undefined}>
							{patterns.map((pattern) => (
								<span
									key={pattern}
									className="bg-accent inline-flex max-w-full items-center gap-1 rounded-sm px-1.5 py-0.5 font-mono text-sm"
								>
									<ModelAccessChipLabel entry={toRegexEntry(pattern)} />
									<button
										type="button"
										aria-label={`Remove ${pattern}`}
										disabled={disabled}
										onClick={() => onChange(removePattern(list, pattern))}
										className="text-muted-foreground hover:text-foreground shrink-0"
									>
										<X className="h-3.5 w-3.5" />
									</button>
								</span>
							))}
						</div>
					) : (
						<p className="text-muted-foreground text-xs">
							{mode === "allow"
								? "No patterns. Add an RE2 pattern to allow models by name shape."
								: "No patterns. Add an RE2 pattern to block models by name shape."}
						</p>
					)}
				</div>
			)}
		</div>
	);
}