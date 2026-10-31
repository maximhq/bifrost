import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getExampleBaseUrl } from "@/lib/utils/port";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, ArrowRight, Copy } from "lucide-react";
import { useMemo } from "react";

const EDITOR_OPTIONS = {
	scrollBeyondLastLine: false,
	minimap: { enabled: false },
	lineNumbers: "off",
	folding: false,
	lineDecorationsWidth: 0,
	lineNumbersMinChars: 0,
	glyphMargin: false,
} as const;

interface CodeBlockProps {
	code: string;
}

function CodeBlock({ code }: CodeBlockProps) {
	const { copy } = useCopyToClipboard();

	return (
		<div className="relative">
			<Button variant="ghost" size="icon" className="absolute top-4 right-4 z-10" onClick={() => copy(code)} aria-label="Copy request">
				<Copy className="size-4" />
			</Button>
			<CodeEditor className="w-full" code={code} lang="shell" readonly height={300} fontSize={14} options={EDITOR_OPTIONS} />
		</div>
	);
}

interface AgentEmptyStateProps {
	error?: string | null;
}

export function AgentEmptyState({ error }: AgentEmptyStateProps) {
	const examples = useMemo(() => {
		const baseUrl = getExampleBaseUrl();
		const headers = `-H "Content-Type: application/json" \\
  -H "x-bf-vk: <virtual-key>"`;
		const message = `"message": {
      "role": "ROLE_USER",
      "parts": [{"text": "Hello from Bifrost"}],
      "messageId": "msg-001"
    }`;

		return {
			jsonrpc: `curl -X POST "${baseUrl}/agents/a2a/<agent-name>/json-rpc" \\
  ${headers} \\
  -d '{
    "jsonrpc": "2.0",
    "id": "request-001",
    "method": "SendMessage",
    "params": {
      ${message}
    }
  }'`,
			http: `curl -X POST "${baseUrl}/agents/a2a/<agent-name>/rest/message:send" \\
  ${headers} \\
  -d '{
    ${message}
  }'`,
		};
	}, []);

	const isUnexpectedError = error && error.includes("An unexpected error occurred");

	return (
		<div className="dark:bg-card flex w-full flex-col items-center justify-center space-y-8 bg-white">
			{error && (
				<Alert>
					<AlertTriangle className="h-4 w-4" />
					<AlertDescription>
						{isUnexpectedError ? "Looks like you haven't configured the log store in your config file." : error}
					</AlertDescription>
				</Alert>
			)}

			<div className="w-full space-y-6 p-4">
				<div className="flex items-center gap-4">
					<div>
						<h3 className="text-lg font-semibold">Send your first agent request</h3>
						<p className="text-muted-foreground text-sm">Route an agent request through Bifrost to start collecting Agent Logs.</p>
					</div>
					<Button asChild variant="outline" className="ml-auto">
						<Link to="/workspace/agent-gateway">
							Open Agent Gateway
							<ArrowRight className="size-4" />
						</Link>
					</Button>
				</div>

				<Tabs defaultValue="jsonrpc" className="w-full rounded-lg border">
					<TabsList className="flex h-10 w-full justify-start rounded-t-lg rounded-b-none">
						<TabsTrigger value="jsonrpc">JSON-RPC</TabsTrigger>
						<TabsTrigger value="http">HTTP+JSON</TabsTrigger>
					</TabsList>
					<TabsContent value="jsonrpc" className="px-4">
						<p className="text-muted-foreground mb-3 text-sm">Send a strict A2A v1 SendMessage request over JSON-RPC.</p>
						<CodeBlock code={examples.jsonrpc} />
					</TabsContent>
					<TabsContent value="http" className="px-4">
						<p className="text-muted-foreground mb-3 text-sm">Send the same message through the A2A HTTP+JSON binding.</p>
						<CodeBlock code={examples.http} />
					</TabsContent>
				</Tabs>

				<div className="bg-muted/50 rounded-lg border p-4">
					<h4 className="mb-2 text-sm font-semibold">Before you send a request</h4>
					<ol className="text-muted-foreground space-y-2 text-sm">
						<li className="flex items-start gap-2">
							<span className="text-primary">1.</span>
							<span>Register and enable the upstream agent in Agent Gateway.</span>
						</li>
						<li className="flex items-start gap-2">
							<span className="text-primary">2.</span>
							<span>
								Replace <code className="bg-muted rounded px-1">&lt;agent-name&gt;</code> with its registered name.
							</span>
						</li>
						<li className="flex items-start gap-2">
							<span className="text-primary">3.</span>
							<span>
								Use a virtual key that can access the agent, or remove the <code className="bg-muted rounded px-1">x-bf-vk</code> header
								when authentication is disabled.
							</span>
						</li>
					</ol>
				</div>
			</div>
		</div>
	);
}