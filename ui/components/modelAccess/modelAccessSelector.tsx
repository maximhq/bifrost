import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import { type ReactNode, useState } from "react";
import { ModelAccessChipLabel } from "./modelAccessChip";
import { RegexPatternInput } from "./regexPatternInput";
import { addPattern, type ModelAccessMode, modelAccessPlaceholder, resolveWildcardSelection } from "./utils";

type EditorTab = "models" | "regex";

export interface ModelAccessSelectorProps {
	/** The exact list: model names, or "*" alone for every model. */
	value: string[];
	onChange: (next: string[]) => void;
	/** The pattern twin of `value`: raw RE2 patterns. */
	patterns: string[];
	onPatternsChange: (next: string[]) => void;
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
	menuPosition?: "absolute" | "fixed";
	menuPortalTarget?: HTMLElement | null;
	className?: string;
}

/**
 * The one editor for a model allow or block side. A small toggle switches
 * between picking concrete models (the multiselect everyone used before), which
 * edits the exact list, and adding regex patterns, which edits the pattern
 * list. The two lists are separate fields on the wire and stay separate here.
 */
export function ModelAccessSelector({
	value,
	onChange,
	patterns,
	onPatternsChange,
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
	const list = value ?? [];
	const patternList = patterns ?? [];
	// Open on the view that matches what is already configured, so a
	// patterns-only rule is visible right away instead of hidden behind the toggle.
	const [tab, setTab] = useState<EditorTab>(() => (patternList.length > 0 && list.length === 0 ? "regex" : "models"));
	const hasWildcard = list.includes("*");

	const removePattern = (pattern: string) => onPatternsChange(patternList.filter((p) => p !== pattern));

	const toggle = (
		<Tabs value={tab} onValueChange={(next) => setTab(next as EditorTab)} className="shrink-0">
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
					inputId={inputId}
					provider={provider}
					keys={keys}
					unfiltered={unfiltered}
					loadModelsOnEmptyProvider={loadModelsOnEmptyProvider}
					disabled={disabled}
					menuPosition={menuPosition}
					menuPortalTarget={menuPortalTarget}
					value={hasWildcard ? ["*"] : list}
					onChange={(models: string[]) => onChange(resolveWildcardSelection(list, models))}
					placeholder={modelAccessPlaceholder(list, mode, patternList)}
					renderValueLabel={(option) => <ModelAccessChipLabel entry={option.value} />}
				/>
			) : (
				<div className="space-y-1.5">
					<RegexPatternInput
						data-testid={testId ? `${testId}-regex` : undefined}
						inputId={inputId}
						disabled={disabled}
						onAdd={(pattern) => onPatternsChange(addPattern(patternList, pattern))}
					/>
					{patternList.length > 0 ? (
						<div className="flex flex-wrap gap-1" data-testid={testId ? `${testId}-entries` : undefined}>
							{patternList.map((pattern) => (
								<span
									key={pattern}
									className="bg-accent inline-flex max-w-full items-center gap-1 rounded-sm px-1.5 py-0.5 font-mono text-sm"
								>
									<ModelAccessChipLabel entry={pattern} kind="pattern" />
									<button
										type="button"
										aria-label={`Remove ${pattern}`}
										disabled={disabled}
										onClick={() => removePattern(pattern)}
										className="text-muted-foreground hover:text-foreground shrink-0"
									>
										<X className="h-3.5 w-3.5" />
									</button>
								</span>
							))}
						</div>
					) : (
						<p className="text-muted-foreground text-xs">
							{mode === "allow" ? "No patterns. Add an RE2 pattern to allow models by name shape." : "No patterns. Add an RE2 pattern to block models by name shape."}
						</p>
					)}
				</div>
			)}
		</div>
	);
}
