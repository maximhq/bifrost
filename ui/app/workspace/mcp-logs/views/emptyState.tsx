"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { AlertCircle } from "lucide-react";

interface MCPEmptyStateProps {
	error?: string | null;
}

export function MCPEmptyState({ error }: MCPEmptyStateProps) {
	return (
		<div className="dark:bg-card bg-white">
			<div className="mx-auto flex max-w-7xl flex-col items-center justify-center gap-6 py-16">
				{error && (
					<Alert variant="destructive" className="max-w-md">
						<AlertCircle className="h-4 w-4" aria-hidden="true" />
						<AlertDescription>{error}</AlertDescription>
					</Alert>
				)}
				<div className="text-center">
					<h2 className="text-xl font-semibold mb-2">No MCP Tool Calls Yet</h2>
					<p className="text-muted-foreground max-w-md">
						MCP tool executions will appear here when tools are called through Bifrost agent mode or the MCP inference endpoint.
					</p>
				</div>
				<div className="mt-4 flex flex-col gap-2 text-sm text-muted-foreground">
					<p>To get started:</p>
					<ul className="list-disc list-inside space-y-1">
						<li>Configure MCP servers in the MCP Gateway</li>
						<li>Enable agent mode in your chat completion requests</li>
						<li>Or use the <code className="bg-muted px-1 rounded">/v1/mcp/tool/execute</code> endpoint directly</li>
					</ul>
				</div>
			</div>
		</div>
	);
}
