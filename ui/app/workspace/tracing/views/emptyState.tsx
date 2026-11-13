import { Button } from "@/components/ui/button";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useGetTraceConnectInfoQuery } from "@/lib/store";
import { Link } from "@tanstack/react-router";
import { ChartNoAxesGantt, Copy, KeyRound } from "lucide-react";
import { useTranslation } from "react-i18next";

/** How an SDK authenticates, which decides the key lines of each setup. */
type KeyMode = "none" | "admin" | "apiKey";

function setupFor(endpoints: Record<string, string>, mode: KeyMode): { sdk: string; setup: string }[] {
	// With no auth there is no key line at all: nothing to send.
	const key = mode === "admin" ? "<username>:<password>" : mode === "apiKey" ? "<tracing API key>" : "";
	const line = (name: string, value: string) => (value ? `\n${name}=${value}` : "");
	return [
		{ sdk: "Braintrust", setup: `BRAINTRUST_APP_URL=${endpoints.braintrust}${line("BRAINTRUST_API_KEY", key)}` },
		{
			sdk: "Langfuse",
			setup: `LANGFUSE_BASE_URL=${endpoints.langfuse}${
				mode === "admin"
					? line("LANGFUSE_PUBLIC_KEY", "<username>") + line("LANGFUSE_SECRET_KEY", "<password>")
					: line("LANGFUSE_SECRET_KEY", key)
			}`,
		},
		{ sdk: "Arize / Phoenix", setup: `endpoint=${endpoints.arize}/v1/traces${line("api_key", key)}` },
		{
			sdk: "OpenTelemetry",
			setup: `OTEL_EXPORTER_OTLP_ENDPOINT=${endpoints.otel}${line("OTEL_EXPORTER_OTLP_HEADERS", key ? `Authorization=${key}` : "")}`,
		},
	];
}

/**
 * Shown before the first trace arrives. Pointing an SDK at Bifrost is one URL. What it sends as its
 * API key follows the gateway: nothing when admin auth is not set up, the dashboard username and
 * password when it is, and on enterprise an API key made for tracing.
 */
export function TracingEmptyState() {
	const { t } = useTranslation("observability");
	const { data } = useGetTraceConnectInfoQuery();
	const { copy } = useCopyToClipboard();
	const mode: KeyMode = IS_ENTERPRISE ? "apiKey" : data?.auth_required ? "admin" : "none";
	const rows = data ? setupFor(data.endpoints, mode) : [];
	return (
		<div className="flex h-full flex-col items-center justify-center gap-4 p-10 text-center" data-testid="tracing-empty-state">
			<ChartNoAxesGantt className="text-muted-foreground h-8 w-8" />
			<div className="space-y-1">
				<h2 className="text-base font-medium">{t("tracing.empty.title")}</h2>
				<p className="text-muted-foreground max-w-xl text-sm" data-testid="tracing-empty-auth-note">
					{mode === "apiKey"
						? t("tracing.empty.descriptionEnterprise")
						: mode === "admin"
							? t("tracing.empty.descriptionAuth")
							: t("tracing.empty.descriptionOpen")}
				</p>
			</div>
			{mode === "apiKey" && (
				<Button asChild variant="outline" size="sm" data-testid="tracing-empty-create-api-key">
					<Link to="/workspace/config/api-keys">
						<KeyRound className="mr-1 h-4 w-4" />
						{t("tracing.empty.createApiKey")}
					</Link>
				</Button>
			)}
			<div className="grid w-full max-w-3xl gap-2 text-left md:grid-cols-2">
				{rows.map((row) => (
					<div key={row.sdk} className="rounded-md border p-3" data-testid={`tracing-empty-setup-${row.sdk.split(" ")[0].toLowerCase()}`}>
						<div className="mb-1 flex items-center justify-between">
							<span className="text-sm font-medium">{row.sdk}</span>
							<Button variant="ghost" size="icon" className="size-7" aria-label={`Copy ${row.sdk}`} onClick={() => copy(row.setup)}>
								<Copy className="size-3.5" />
							</Button>
						</div>
						<pre className="text-muted-foreground text-xs break-all whitespace-pre-wrap">{row.setup}</pre>
					</div>
				))}
			</div>
		</div>
	);
}