"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { getExampleBaseUrl } from "@/lib/utils/port";
import { AlertTriangle, Copy } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { CodeEditor } from "../../logs/views/codeEditor";

type Language = "python" | "typescript";

type Examples = {
	manual: {
		[L in Language]: string;
	};
	agentMode: {
		[L in Language]: string;
	};
};

// Common editor options to reduce duplication
const EditorOptions = {
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
	language: string;
	onLanguageChange?: (language: string) => void;
	showLanguageSelect?: boolean;
	readonly?: boolean;
}

function CodeBlock({ code, language, onLanguageChange, showLanguageSelect = false, readonly = true }: CodeBlockProps) {
	const copyToClipboard = () => {
		navigator.clipboard.writeText(code);
		toast.success("Copied to clipboard");
	};

	return (
		<div className="relative">
			<div className="absolute top-4 right-4 z-10 flex items-center gap-2">
				{showLanguageSelect && onLanguageChange && (
					<Select value={language} onValueChange={onLanguageChange}>
						<SelectTrigger className="h-8 w-fit text-xs">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem className="text-xs" value="python">
								Python
							</SelectItem>
							<SelectItem className="text-xs" value="typescript">
								TypeScript
							</SelectItem>
						</SelectContent>
					</Select>
				)}
				<Button variant="ghost" size="icon" onClick={copyToClipboard} aria-label="Copy to clipboard">
					<Copy className="size-4" />
				</Button>
			</div>
			<CodeEditor className="w-full" code={code} lang={language} readonly={readonly} height={300} fontSize={14} options={EditorOptions} />
		</div>
	);
}

interface MCPEmptyStateProps {
	error?: string | null;
	statusIndicator?: React.ReactNode;
}

export function MCPEmptyState({ error, statusIndicator }: MCPEmptyStateProps) {
	const [language, setLanguage] = useState<Language>("python");

	// Generate examples dynamically using the port utility
	const examples: Examples = useMemo(() => {
		const baseUrl = getExampleBaseUrl();

		return {
			manual: {
				python: `import openai
import requests

# Step 1: Initialize OpenAI client with Bifrost
client = openai.OpenAI(
    base_url="${baseUrl}/openai",
    api_key="dummy-api-key"  # Handled by Bifrost
)

# Step 2: Send chat request
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "List files in current directory"}]
)

# Step 3: Check for tool calls
message = response.choices[0].message
if message.tool_calls:
    for tool_call in message.tool_calls:
        # Step 4: Execute tool via Bifrost
        tool_result = requests.post(
            "${baseUrl}/v1/mcp/tool/execute",
            json={
                "id": tool_call.id,
                "type": "function",
                "function": {
                    "name": tool_call.function.name,
                    "arguments": tool_call.function.arguments
                }
            }
        ).json()
        
        # Step 5: Continue conversation with results
        final_response = client.chat.completions.create(
            model="gpt-4o",
            messages=[
                {"role": "user", "content": "List files in current directory"},
                message,
                tool_result
            ]
        )
        print(final_response.choices[0].message.content)`,
				typescript: `import OpenAI from "openai";

// Step 1: Initialize OpenAI client with Bifrost
const openai = new OpenAI({
  baseURL: "${baseUrl}/openai",
  apiKey: "dummy-api-key", // Handled by Bifrost
});

// Step 2: Send chat request
const response = await openai.chat.completions.create({
  model: "gpt-4o",
  messages: [{ role: "user", content: "List files in current directory" }],
});

const message = response.choices[0].message;

// Step 3: Check for tool calls
if (message.tool_calls) {
  for (const toolCall of message.tool_calls) {
    // Step 4: Execute tool via Bifrost
    const toolResult = await fetch("${baseUrl}/v1/mcp/tool/execute", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        id: toolCall.id,
        type: "function",
        function: {
          name: toolCall.function.name,
          arguments: toolCall.function.arguments,
        },
      }),
    }).then(res => res.json());

    // Step 5: Continue conversation with results
    const finalResponse = await openai.chat.completions.create({
      model: "gpt-4o",
      messages: [
        { role: "user", content: "List files in current directory" },
        message,
        toolResult,
      ],
    });
    console.log(finalResponse.choices[0].message.content);
  }
}`,
			},
			agentMode: {
				python: `import openai

# Agent Mode enables autonomous tool execution
# Configure auto-executable tools in MCP Gateway settings

client = openai.OpenAI(
    base_url="${baseUrl}/openai",
    api_key="dummy-api-key"
)

# With agent mode enabled, Bifrost automatically:
# 1. Receives tool calls from LLM
# 2. Executes auto-approved tools (e.g., read_file, list_directory)
# 3. Feeds results back to LLM
# 4. Returns final response after all iterations

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{
        "role": "user", 
        "content": "List all Python files and summarize their purpose"
    }]
)

# The response includes results from all auto-executed tools
# Non-auto-executable tools (e.g., write_file) are returned for manual approval
print(response.choices[0].message.content)

# If there are pending non-auto-executable tools:
if response.choices[0].message.tool_calls:
    print("Pending tools requiring approval:", 
          [tc.function.name for tc in response.choices[0].message.tool_calls])`,
				typescript: `import OpenAI from "openai";

// Agent Mode enables autonomous tool execution
// Configure auto-executable tools in MCP Gateway settings

const openai = new OpenAI({
  baseURL: "${baseUrl}/openai",
  apiKey: "dummy-api-key",
});

// With agent mode enabled, Bifrost automatically:
// 1. Receives tool calls from LLM
// 2. Executes auto-approved tools (e.g., read_file, list_directory)
// 3. Feeds results back to LLM
// 4. Returns final response after all iterations

const response = await openai.chat.completions.create({
  model: "gpt-4o",
  messages: [{
    role: "user",
    content: "List all Python files and summarize their purpose"
  }],
});

// The response includes results from all auto-executed tools
// Non-auto-executable tools (e.g., write_file) are returned for manual approval
console.log(response.choices[0].message.content);

// If there are pending non-auto-executable tools:
if (response.choices[0].message.tool_calls) {
  console.log("Pending tools requiring approval:", 
    response.choices[0].message.tool_calls.map(tc => tc.function.name)
  );
}`,
			},
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

			<div className="w-full space-y-6">
				<div className="flex flex-row items-center gap-2">
					<div>
						<h3 className="text-lg font-semibold">Get Started with MCP Tool Execution</h3>
						<p className="text-muted-foreground text-sm">Execute your first MCP tool call to see logs appear</p>
					</div>
					<div className="ml-auto">{statusIndicator}</div>
				</div>

				<Tabs defaultValue="manual" className="w-full rounded-lg border">
					<TabsList className="grid h-10 w-full grid-cols-2 rounded-t-lg rounded-b-none">
						<TabsTrigger value="manual">Manual Tool Execution</TabsTrigger>
						<TabsTrigger value="agent">Agent Mode (Auto-Execute)</TabsTrigger>
					</TabsList>

					<TabsContent value="manual" className="px-4">
						<div className="text-muted-foreground mb-3 text-sm">
							<p>Full control over tool approval. You explicitly execute each tool call via the API.</p>
						</div>
						<CodeBlock
							code={examples.manual[language]}
							language={language}
							onLanguageChange={(newLang) => setLanguage(newLang as Language)}
							showLanguageSelect
						/>
					</TabsContent>

					<TabsContent value="agent" className="px-4">
						<div className="text-muted-foreground mb-3 text-sm">
							<p>Autonomous execution for pre-approved tools. Configure auto-executable tools in MCP Gateway settings.</p>
						</div>
						<CodeBlock
							code={examples.agentMode[language]}
							language={language}
							onLanguageChange={(newLang) => setLanguage(newLang as Language)}
							showLanguageSelect
						/>
					</TabsContent>
				</Tabs>

				<div className="bg-muted/50 rounded-lg border p-4">
					<h4 className="mb-2 text-sm font-semibold">Prerequisites</h4>
					<ul className="text-muted-foreground space-y-1 text-sm">
						<li className="flex items-start gap-2">
							<span className="text-primary">1.</span>
							<span>Configure MCP servers in the MCP Gateway (e.g., filesystem, web_search)</span>
						</li>
						<li className="flex items-start gap-2">
							<span className="text-primary">2.</span>
							<span>
								Set <code className="bg-muted rounded px-1">tools_to_execute</code> to whitelist available tools
							</span>
						</li>
						<li className="flex items-start gap-2">
							<span className="text-primary">3.</span>
							<span>
								For Agent Mode: Configure <code className="bg-muted rounded px-1">tools_to_auto_execute</code> for autonomous execution
							</span>
						</li>
					</ul>
				</div>
			</div>
		</div>
	);
}
