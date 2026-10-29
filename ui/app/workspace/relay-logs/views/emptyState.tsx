import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getExampleBaseUrl } from "@/lib/utils/port";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, ArrowRight, Copy } from "lucide-react";
import { useMemo } from "react";

// Same editor options as the MCP logs empty state, so the two pages read alike.
const EditorOptions = {
	scrollBeyondLastLine: false,
	minimap: { enabled: false },
	lineNumbers: "off",
	folding: false,
	lineDecorationsWidth: 0,
	lineNumbersMinChars: 0,
	glyphMargin: false,
} as const;

function CodeBlock({ code }: { code: string }) {
	const { copy: copyToClipboard } = useCopyToClipboard();

	return (
		<div className="relative">
			<div className="absolute top-4 right-4 z-10 flex items-center gap-2">
				<Button variant="ghost" size="icon" onClick={() => copyToClipboard(code)} aria-label="Copy to clipboard">
					<Copy className="size-4" />
				</Button>
			</div>
			<CodeEditor className="w-full" code={code} lang="shell" readonly height={300} fontSize={14} options={EditorOptions} />
		</div>
	);
}

interface RelayEmptyStateProps {
	error?: string | null;
}

/** The Relay Logs page before any row exists, laid out like the MCP logs empty state with one example tab. */
export function RelayEmptyState({ error }: RelayEmptyStateProps) {
	// The Relays page is gated on Relays access, so a logs-only viewer is not sent to a denied page.
	const canOpenRelays = useRbac(RbacResource.Relays, RbacOperation.View);

	const example = useMemo(() => {
		const baseUrl = getExampleBaseUrl();
		return `# Relay a call through Bifrost: the relay's credential is injected,
# the caller's is stripped, and the reply is priced with the relay's usage mapping.

curl -X POST "${baseUrl}/relay/<relay-name>/<path>" \\
  -H "Content-Type: application/json" \\
  -H "x-bf-vk: <virtual-key>" \\
  -d '{
    "query": "Hello from Bifrost"
  }'

# Every relayed call appears here with its status, latency and cost.`;
	}, []);

	const isUnexpectedError = error && error.includes("An unexpected error occurred");

	return (
		<div className="dark:bg-card flex w-full flex-col items-center justify-center space-y-8 bg-white" data-testid="relay-logs-empty">
			{error && (
				<Alert>
					<AlertTriangle className="h-4 w-4" />
					<AlertDescription>
						{isUnexpectedError ? "Looks like you haven't configured the log store in your config file." : error}
					</AlertDescription>
				</Alert>
			)}

			<div className="w-full space-y-6">
				<div className="flex flex-row items-center gap-2">
					<div>
						<h3 className="text-lg font-semibold">Get Started with Relays</h3>
						<p className="text-muted-foreground text-sm">Relay your first request to an upstream API to see logs appear</p>
					</div>
					{canOpenRelays && (
						<div className="ml-auto">
							<Button asChild variant="outline" size="sm" data-testid="relay-logs-empty-open-relays">
								<Link to="/workspace/relays">
									Open Relays
									<ArrowRight className="size-4" />
								</Link>
							</Button>
						</div>
					)}
				</div>

				<Tabs defaultValue="request" className="w-full rounded-lg border">
					<TabsList className="flex h-10 w-full justify-start rounded-t-lg rounded-b-none">
						<TabsTrigger value="request">Relay a Request</TabsTrigger>
					</TabsList>

					<TabsContent value="request" className="px-4">
						<div className="text-muted-foreground mb-3 text-sm">
							<p>The request is relayed as-is to the relay&apos;s base URL, with the relay&apos;s credential added.</p>
						</div>
						<CodeBlock code={example} />
					</TabsContent>
				</Tabs>

				<div className="bg-muted/50 rounded-lg border p-4">
					<h4 className="mb-2 text-sm font-semibold">Prerequisites</h4>
					<ul className="text-muted-foreground space-y-1 text-sm">
						<li className="flex items-start gap-2">
							<span className="text-primary">1.</span>
							<span>Add a relay in Relays with its base URL and credential</span>
						</li>
						<li className="flex items-start gap-2">
							<span className="text-primary">2.</span>
							<span>
								Replace <code className="bg-muted rounded px-1">&lt;relay-name&gt;</code> with its name and{" "}
								<code className="bg-muted rounded px-1">&lt;path&gt;</code> with one of its routes, or turn on Relay Any Path
							</span>
						</li>
						<li className="flex items-start gap-2">
							<span className="text-primary">3.</span>
							<span>
								Use a virtual key that grants the relay (or all relays), or drop the <code className="bg-muted rounded px-1">x-bf-vk</code>{" "}
								header when authentication is disabled
							</span>
						</li>
					</ul>
				</div>
			</div>
		</div>
	);
}