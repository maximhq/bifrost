import { Input } from "@/components/ui/input";
import type { MCPCodeModeLimits } from "@/lib/types/config";
import { CODE_MODE_LIMIT_FIELDS, parseCodeModeLimitInput } from "./codeModeLimits.utils";

interface CodeModeLimitsSectionProps {
	value: MCPCodeModeLimits | undefined;
	onChange: (value: MCPCodeModeLimits) => void;
	disabled?: boolean;
}

// Inputs are controlled by the numeric limits: an empty input is 0, which the
// server treats as the default, so the placeholder shows that default.
export function CodeModeLimitsSection({ value, onChange, disabled }: CodeModeLimitsSectionProps) {
	return (
		<div className="space-y-4 rounded-sm border p-4" data-testid="mcp-code-mode-limits">
			<div className="space-y-0.5">
				<p className="text-sm font-medium">Code Mode Limits</p>
				<p className="text-muted-foreground text-sm">
					Limits on each code execution. Leave a field empty to use its default. Executions are not limited in how many run at once.
				</p>
			</div>
			<div className="grid gap-4 sm:grid-cols-2">
				{CODE_MODE_LIMIT_FIELDS.map((field) => {
					const id = `mcp-code-mode-${field.key.replaceAll("_", "-")}`;
					const current = value?.[field.key];
					return (
						<div key={field.key} className="space-y-1">
							<label htmlFor={id} className="text-sm font-medium">
								{field.label}
							</label>
							<p className="text-muted-foreground text-xs">{field.description}</p>
							<Input
								id={id}
								data-testid={`${id}-input`}
								type="number"
								min="0"
								className="w-40"
								placeholder={field.defaultValue.toString()}
								value={current ? current.toString() : ""}
								onChange={(e) => {
									const parsed = parseCodeModeLimitInput(e.target.value, { badInput: e.target.validity.badInput });
									if (parsed !== undefined) {
										onChange({ ...value, [field.key]: parsed });
									}
								}}
								disabled={disabled}
							/>
						</div>
					);
				})}
			</div>
		</div>
	);
}