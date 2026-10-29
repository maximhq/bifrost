import type { ComponentProps } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { MCP_MAX_PROTOCOL_VERSION_OPTIONS, MCP_PROTOCOL_VERSION_USE_GLOBAL, mcpMaxProtocolVersionLabel } from "@/lib/constants/mcp";

// id and aria-* come from FormControl so FormLabel targets the trigger.
interface MaxProtocolVersionSelectProps extends Pick<ComponentProps<typeof SelectTrigger>, "id" | "aria-describedby" | "aria-invalid"> {
	// "" or undefined = use the global setting.
	value: string | undefined;
	onChange: (value: string) => void;
	globalValue?: string;
	disabled?: boolean;
}

/** Picks one MCP server's max protocol version: the global setting, Auto, or a fixed version. */
export function MaxProtocolVersionSelect({ value, onChange, globalValue, disabled, ...triggerProps }: MaxProtocolVersionSelectProps) {
	const selected = value ? value : MCP_PROTOCOL_VERSION_USE_GLOBAL;
	const globalLabel = globalValue === undefined ? "Use global setting" : `Use global setting (${mcpMaxProtocolVersionLabel(globalValue)})`;
	return (
		<Select value={selected} onValueChange={(v) => onChange(v === MCP_PROTOCOL_VERSION_USE_GLOBAL ? "" : v)} disabled={disabled}>
			<SelectTrigger {...triggerProps} className="w-56" data-testid="mcp-client-max-protocol-version-select">
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				<SelectItem value={MCP_PROTOCOL_VERSION_USE_GLOBAL} data-testid="mcp-client-max-protocol-version-global">
					{globalLabel}
				</SelectItem>
				{MCP_MAX_PROTOCOL_VERSION_OPTIONS.map((option) => (
					<SelectItem key={option.value} value={option.value} data-testid={`mcp-client-max-protocol-version-${option.value}`}>
						{option.label}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}