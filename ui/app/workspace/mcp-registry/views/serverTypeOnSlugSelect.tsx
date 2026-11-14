import type { ComponentProps } from "react";
import { useTranslation } from "react-i18next";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { MCPServerTypeOnSlug } from "@/lib/types/mcp";

// id and aria-* come from FormControl so FormLabel targets the trigger.
interface ServerTypeOnSlugSelectProps extends Pick<ComponentProps<typeof SelectTrigger>, "id" | "aria-describedby" | "aria-invalid"> {
	value: MCPServerTypeOnSlug | undefined;
	onChange: (value: MCPServerTypeOnSlug) => void;
	codeModeServer: boolean; // the Code Mode Server value "inherit" follows
	disabled?: boolean;
}

/** Picks code mode for one MCP server on its own /mcp/<slug> endpoint: the Code Mode Server setting, on, or off. */
export function ServerTypeOnSlugSelect({ value, onChange, codeModeServer, disabled, ...triggerProps }: ServerTypeOnSlugSelectProps) {
	const { t } = useTranslation("mcp");
	const options = [
		{
			value: "inherit",
			label: t("registry.form.serverTypeOnSlugInherit", { value: codeModeServer ? t("common.enabled") : t("common.disabled") }),
		},
		{ value: "enabled", label: t("registry.form.serverTypeOnSlugEnabled") },
		{ value: "disabled", label: t("registry.form.serverTypeOnSlugDisabled") },
	] as const satisfies ReadonlyArray<{ value: MCPServerTypeOnSlug; label: string }>;
	return (
		<Select value={value ?? "inherit"} onValueChange={(v) => onChange(v as MCPServerTypeOnSlug)} disabled={disabled}>
			<SelectTrigger {...triggerProps} className="w-56" data-testid="mcp-client-server-type-on-slug-select">
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				{options.map((option) => (
					<SelectItem key={option.value} value={option.value} data-testid={`mcp-client-server-type-on-slug-${option.value}`}>
						{option.label}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}