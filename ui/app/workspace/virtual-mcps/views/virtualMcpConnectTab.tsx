import { getExternalBaseUrl, quoteShellValue } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { useGetCoreConfigQuery } from "@/lib/store";
import { AlertTriangle, Check, Copy } from "lucide-react";

interface VirtualMCPConnectTabProps {
	slug: string;
	enabled: boolean;
	isCreate: boolean;
	// Only fetch the gateway URL config while the tab is actually reachable.
	active: boolean;
}

export default function VirtualMCPConnectTab({ slug, enabled, isCreate, active }: VirtualMCPConnectTabProps) {
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true }, { skip: !active || isCreate });

	if (isCreate) {
		return (
			<div className="text-muted-foreground rounded-md border border-dashed p-6 text-center text-sm">
				Create this Virtual MCP first to see its endpoint and connection instructions.
			</div>
		);
	}

	const baseUrl = getExternalBaseUrl(bifrostConfig?.client_config);
	const endpoint = `${baseUrl}/mcp/${slug}`;
	const command = [
		`claude mcp add --transport http ${quoteShellValue(slug)} ${quoteShellValue(endpoint)} \\`,
		`  --header ${quoteShellValue("x-bf-vk: <YOUR_VIRTUAL_KEY>")}`,
	].join("\n");

	return (
		<div className="flex flex-col gap-5">
			{!enabled && (
				<div className="flex items-start gap-2 rounded-md border border-amber-500 bg-amber-100 p-3 text-sm text-amber-900 dark:bg-amber-900/30 dark:text-amber-200">
					<AlertTriangle className="mt-0.5 size-4 shrink-0" />
					<span>This Virtual MCP is disabled, so the endpoint below is not being served. Enable it from the General tab to connect.</span>
				</div>
			)}

			<div className="flex flex-col gap-2">
				<Label htmlFor="vmcp-endpoint">Endpoint URL</Label>
				<div className="flex items-center gap-2">
					<Input id="vmcp-endpoint" readOnly value={endpoint} className="font-mono" data-testid="virtual-mcp-endpoint-url" />
					<CopyButton value={endpoint} label="Endpoint URL" testId="virtual-mcp-endpoint-copy" />
				</div>
				<p className="text-muted-foreground text-xs">Point any MCP client at this URL and authenticate with a virtual key.</p>
			</div>

			<div className="flex flex-col gap-2">
				<div className="flex items-center justify-between">
					<Label>Connect with Claude Code</Label>
					<CopyButton value={command} label="Command" testId="virtual-mcp-connect-command-copy" />
				</div>
				<pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs">{command}</pre>
				<p className="text-muted-foreground text-xs">
					Replace <span className="font-mono">&lt;YOUR_VIRTUAL_KEY&gt;</span> with a virtual key that has this Virtual MCP assigned (see the
					Access tab).
				</p>
			</div>
		</div>
	);
}

function CopyButton({ value, label, testId }: { value: string; label: string; testId?: string }) {
	const { copy, copied } = useCopyToClipboard({ successMessage: `${label} copied` });
	return (
		<Button
			type="button"
			variant="outline"
			size="icon"
			className="shrink-0"
			aria-label={`Copy ${label.toLowerCase()}`}
			data-testid={testId}
			onClick={() => copy(value)}
		>
			{copied ? <Check className="size-4" /> : <Copy className="size-4" />}
		</Button>
	);
}
