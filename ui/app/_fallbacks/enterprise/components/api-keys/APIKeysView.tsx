"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useGetCoreConfigQuery } from "@/lib/store";
import { Copy, InfoIcon, KeyRound } from "lucide-react";
import Link from "next/link";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import ContactUsView from "../views/contactUsView";

export default function APIKeysView() {
	const { data: bifrostConfig, isLoading } = useGetCoreConfigQuery({ fromDB: true });
	const [isTokenVisible, setIsTokenVisible] = useState(false);
	const isAuthConfigure = useMemo(() => {
		return bifrostConfig?.auth_config?.is_enabled;
	}, [bifrostConfig]);

	const curlExample = `# Base64 encode your username:password
# Example: echo -n "username:password" | base64
curl --location 'http://localhost:8080/v1/chat/completions'
--header 'Content-Type: application/json' 
--header 'Accept: application/json' 
--header 'Authorization: Basic <base64_encoded_username:password>' 
--data '{ 
  "model": "openai/gpt-4", 
  "messages": [ 
    { 
      "role": "user", 
      "content": "explain big bang?" 
    } 
  ] 
}'`;

	const maskToken = (token: string, revealed: boolean) => {
		if (revealed) return token;
		return token.substring(0, 8) + "•".repeat(Math.max(0, token.length - 8));
	};

	const copyToClipboard = (text: string) => {
		navigator.clipboard.writeText(text);
		toast.success("Copied to clipboard");
	};

	if (isLoading) {
		return <div>Loading...</div>;
	}
	if (!isAuthConfigure) {
		return (
			<Alert variant="default">
				<InfoIcon className="text-muted h-4 w-4" />
				<AlertDescription>
					<p className="text-md text-gray-600">
						To generate API keys, you need to set up admin username and password first.{" "}
						<Link href="/workspace/config?tab=security" className="text-md text-primary underline">
							Configure Security Settings
						</Link>
						.<br />
						<br />
						Once generated you will need to use this API key for all API calls to the Bifrost admin APIs and UI.
					</p>
				</AlertDescription>
			</Alert>
		);
	}

	return (
		<div className="space-y-4">
			<Alert variant="default">
				<InfoIcon className="text-muted h-4 w-4" />
				<AlertDescription>
					<p className="text-md text-gray-600">
						Use Basic auth with your admin credentials when making API calls to Bifrost. Encode your credentials in the standard{" "}
						<code className="bg-muted rounded px-1 py-0.5 text-sm">username:password</code> format with base64 encoding.
					</p>
					<br />
					<p className="text-md text-gray-600">
						<strong>Example:</strong>
					</p>

					<div className="relative mt-2 w-full min-w-0 overflow-x-auto">
						<Button
							variant="ghost"
							size="sm"
							onClick={() => copyToClipboard(curlExample)}
							className="absolute right-2 top-2 h-8 z-10"
						>
							<Copy className="h-4 w-4" />
						</Button>
						<pre className="bg-muted min-w-max rounded p-3 pr-12 font-mono text-sm whitespace-pre">
							{curlExample}
						</pre>
					</div>
				</AlertDescription>
			</Alert>

			<ContactUsView
				className=" mt-4 rounded-md border px-3 py-8"
				icon={<KeyRound size={48} />}
				title="Scope Based API Keys"
				description="Need granular access control with scope-based API keys? Enterprise customers can create multiple API keys with specific permissions for different services, teams, or environments."
				readmeLink="https://docs.getbifrost.io/enterprise/api-keys"
			/>
		</div>
	);
}
