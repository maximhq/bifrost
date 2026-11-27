import { TruncatedLabel } from "@/components/ui/truncatedLabel";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { Check, Copy } from "lucide-react";
import { useTranslation } from "react-i18next";

// One copyable endpoint row, matching the MCP sheet's endpoint block styling.
function EndpointRow({ label, endpoint, testId }: { label: string; endpoint: string; testId: string }) {
	const { t } = useTranslation("observability");
	const { copy, copied } = useCopyToClipboard({ successMessage: t("agentGateway.endpoints.copied") });
	return (
		<div className="bg-muted/40 text-muted-foreground flex items-center justify-between gap-2 rounded-sm border px-3 py-2 text-sm">
			<div className="flex min-w-0 flex-1 flex-col">
				<span className="text-muted-foreground/70 text-xs">{label}</span>
				<TruncatedLabel className="block font-mono">{endpoint}</TruncatedLabel>
			</div>
			<button
				type="button"
				onClick={() => copy(endpoint)}
				className="text-muted-foreground hover:text-foreground shrink-0 cursor-pointer"
				aria-label={t("agentGateway.endpoints.copyAria", { label })}
				data-testid={testId}
			>
				{copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
			</button>
		</div>
	);
}

// The gateway endpoints Bifrost serves for a registered agent. Rendered in the
// edit sheet so operators can hand callers ready-to-use URLs.
export function AgentEndpoints({
	agentName,
	baseUrl,
	grpc,
}: {
	agentName: string;
	baseUrl: string;
	grpc?: { baseDomain: string; port: number };
}) {
	const { t } = useTranslation("observability");
	const prefix = `/agents/a2a/${agentName}`;
	const grpcEndpoint = grpc && agentName.length <= 63 ? `${agentName}.${grpc.baseDomain}:${grpc.port}` : undefined;
	return (
		<div className="flex flex-col gap-2">
			<div className="text-sm font-medium">{t("agentGateway.endpoints.title")}</div>
			<EndpointRow
				label={t("agentGateway.endpoints.agentCard")}
				endpoint={`${baseUrl}${prefix}/.well-known/agent-card.json`}
				testId={`agent-endpoint-copy-card-${agentName}`}
			/>
			<EndpointRow label="JSON-RPC" endpoint={`${baseUrl}${prefix}/json-rpc`} testId={`agent-endpoint-copy-jsonrpc-${agentName}`} />
			<EndpointRow label="REST" endpoint={`${baseUrl}${prefix}/rest`} testId={`agent-endpoint-copy-rest-${agentName}`} />
			{grpcEndpoint && <EndpointRow label="gRPC" endpoint={grpcEndpoint} testId={`agent-endpoint-copy-grpc-${agentName}`} />}
		</div>
	);
}