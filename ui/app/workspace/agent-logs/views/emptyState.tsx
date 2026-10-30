import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getExampleBaseUrl } from "@/lib/utils/port";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, ArrowRight, Copy } from "lucide-react";
import { useMemo } from "react";
import { Trans, useTranslation } from "react-i18next";

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

const codeTag = <code className="bg-muted rounded px-1" />;

function CodeBlock({ code }: CodeBlockProps) {
	const { t } = useTranslation("observability");
	const { copy } = useCopyToClipboard();

	return (
		<div className="relative">
			<Button
				variant="ghost"
				size="icon"
				className="absolute top-4 right-4 z-10"
				onClick={() => copy(code)}
				aria-label={t("agentLogs.empty.copyRequest")}
			>
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
	const { t } = useTranslation("observability");
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
					<AlertDescription>{isUnexpectedError ? t("logs.empty.logStoreNotConfigured") : error}</AlertDescription>
				</Alert>
			)}

			<div className="w-full space-y-6 p-4">
				<div className="flex items-center gap-4">
					<div>
						<h3 className="text-lg font-semibold">{t("agentLogs.empty.title")}</h3>
						<p className="text-muted-foreground text-sm">{t("agentLogs.empty.description")}</p>
					</div>
					<Button asChild variant="outline" className="ml-auto">
						<Link to="/workspace/agent-gateway">
							{t("agentLogs.empty.openAgentGateway")}
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
						<p className="text-muted-foreground mb-3 text-sm">{t("agentLogs.empty.jsonrpcDescription")}</p>
						<CodeBlock code={examples.jsonrpc} />
					</TabsContent>
					<TabsContent value="http" className="px-4">
						<p className="text-muted-foreground mb-3 text-sm">{t("agentLogs.empty.httpDescription")}</p>
						<CodeBlock code={examples.http} />
					</TabsContent>
				</Tabs>

				<div className="bg-muted/50 rounded-lg border p-4">
					<h4 className="mb-2 text-sm font-semibold">{t("agentLogs.empty.beforeTitle")}</h4>
					<ol className="text-muted-foreground space-y-2 text-sm">
						<li className="flex items-start gap-2">
							<span className="text-primary">1.</span>
							<span>{t("agentLogs.empty.stepRegister")}</span>
						</li>
						<li className="flex items-start gap-2">
							<span className="text-primary">2.</span>
							<span>
								<Trans t={t} i18nKey="agentLogs.empty.stepReplace" values={{ agentName: "<agent-name>" }} components={{ code: codeTag }} />
							</span>
						</li>
						<li className="flex items-start gap-2">
							<span className="text-primary">3.</span>
							<span>
								<Trans t={t} i18nKey="agentLogs.empty.stepVirtualKey" components={{ code: codeTag }} />
							</span>
						</li>
					</ol>
				</div>
			</div>
		</div>
	);
}