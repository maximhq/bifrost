"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { useGetCoreConfigQuery } from "@/lib/store";
import { Copy, Eye, EyeOff, InfoIcon } from "lucide-react";
import Link from "next/link";
import { useMemo, useState } from "react";
import { toast } from "sonner";

export default function APIKeysView() {
	const { data: bifrostConfig, isLoading } = useGetCoreConfigQuery({ fromDB: true });
	const [isTokenVisible, setIsTokenVisible] = useState(false);
	const authToken = useMemo(() => {
		return bifrostConfig?.auth_token;
	}, [bifrostConfig]);

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
	if (!authToken) {
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
						Use this token as <code className="bg-muted rounded px-1 py-0.5 text-sm">Basic auth</code> in the header when making API calls
						to Bifrost.
					</p>
					<br />
					<div className="flex items-center gap-2">
						<p className="text-md text-gray-600">
							<strong>Example:</strong>
						</p>
						<Button
							variant="ghost"
							size="sm"
							onClick={() => setIsTokenVisible(!isTokenVisible)}
							className="h-6 w-6 p-0"
						>
							{isTokenVisible ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
						</Button>
					</div>
					<div className="mt-2 min-w-0 w-full overflow-x-auto">
						<pre className="bg-muted rounded p-3 font-mono text-sm whitespace-pre min-w-max">
							{`curl -X GET "http://localhost:8080/api/v1/config" \\
  -H "Authorization: Basic ${maskToken(authToken, isTokenVisible)}"`}
						</pre>
					</div>
				</AlertDescription>
			</Alert>
			<div className="space-y-2">
				<Label htmlFor="auth-token">Auth Token</Label>
				<div className="dark:bg-input/30  flex w-full items-center rounded-sm border px-1">
					<input
						id="auth-token"
						type="text"
						value={maskToken(authToken, isTokenVisible)}
						readOnly
						className="file:text-foreground placeholder:text-muted-foreground/70 selection:bg-primary selection:text-primary-foreground flex h-9 w-full min-w-0 px-3 py-1 text-base shadow-none transition-[color,box-shadow] outline-none file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm font-mono"
					/>
					<div className="ml-auto flex items-center gap-1">
						<Button
							variant="ghost"
							size="icon"
							onClick={() => setIsTokenVisible(!isTokenVisible)}
							className="h-8 w-8"
						>
							{isTokenVisible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
						</Button>
						<Button
							variant="ghost"
							size="icon"
							onClick={() => copyToClipboard(authToken)}
							className="h-8 w-8"
						>
							<Copy className="h-4 w-4" />
						</Button>
					</div>
				</div>
			</div>
		</div>
	);
}
