import type { DropdownOption } from "@/components/ui/custom/dropdown";
import { RichTextarea } from "@/components/ui/custom/richTextarea";
import { JINJA_VAR_HIGHLIGHT_PATTERNS } from "@/lib/message/constant";
import { ArrowBigRight, Repeat } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

// The inputs a test run or a logged request can fill in by name. Any other name is
// allowed too: it becomes a variable the caller supplies.
const SINGLE_TURN_VARIABLES = ["input", "output", "context", "expected_output", "db_schema"] as const;
const MULTI_TURN_VARIABLES = ["history", "session", "expected_steps", "transcript"] as const;

interface InstructionsEditorProps {
	value: string;
	onChange: (value: string) => void;
	disabled?: boolean;
	placeholder?: string;
	ariaLabel: string;
	testId: string;
}

/**
 * The judge's instructions. Typing "{{" opens the list of known variables, and every
 * {{variable}} in the text is highlighted so a typo in a brace is easy to see.
 */
export function InstructionsEditor({ value, onChange, disabled, placeholder, ariaLabel, testId }: InstructionsEditorProps) {
	const { t } = useTranslation("config");

	const suggestions: DropdownOption[] = useMemo(
		() => [
			{
				type: "group",
				label: t("evals.suggestions.singleTurn"),
				icon: <ArrowBigRight className="text-muted-foreground size-3.5" strokeWidth={1.75} />,
				options: SINGLE_TURN_VARIABLES.map((name) => ({
					label: name,
					value: `{{${name}}}`,
					description: t(`evals.suggestions.variables.${name}`),
				})),
			},
			{
				type: "group",
				label: t("evals.suggestions.multiTurn"),
				icon: <Repeat className="text-muted-foreground size-3.5" strokeWidth={1.75} />,
				options: MULTI_TURN_VARIABLES.map((name) => ({
					label: name,
					value: `{{${name}}}`,
					description: t(`evals.suggestions.variables.${name}`),
				})),
			},
		],
		[t],
	);

	return (
		<RichTextarea
			value={value}
			onChange={(e) => onChange(e.target.value)}
			disabled={disabled}
			placeholder={placeholder}
			aria-label={ariaLabel}
			data-testid={testId}
			suggestions={suggestions}
			suggestionsTrigger="{{"
			noSuggestionText={t("evals.suggestions.custom")}
			suggestionDropdownClassName="mt-1 w-96"
			highlightPatterns={JINJA_VAR_HIGHLIGHT_PATTERNS}
			className="h-auto"
			textAreaClassName="min-h-40 rounded-sm px-3 py-2 text-sm"
		/>
	);
}