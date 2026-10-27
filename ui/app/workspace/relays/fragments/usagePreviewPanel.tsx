import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, usePreviewRelayUsageMutation } from "@/lib/store";
import { RelayRouteMethods, type Relay, type UsagePreview } from "@/lib/types/relays";
import { relayPreviewSampleSchema } from "@/lib/types/schemas";
import { useState } from "react";
import { UsagePreviewResult } from "./usagePreviewResult";

/**
 * Tests the saved routes and pricing against a sample call: what route it matches, whether it is billed,
 * and what it would cost. Nothing is sent upstream.
 */
export function UsagePreviewPanel({ endpoint }: { endpoint: Relay }) {
	const [preview, { isLoading }] = usePreviewRelayUsageMutation();
	const [method, setMethod] = useState("POST");
	const [path, setPath] = useState("/");
	const [status, setStatus] = useState("200");
	const [headersText, setHeadersText] = useState("");
	const [requestBody, setRequestBody] = useState("");
	const [responseType, setResponseType] = useState("application/json");
	const [responseBody, setResponseBody] = useState("");
	const [errors, setErrors] = useState<Record<string, string>>({});
	const [result, setResult] = useState<UsagePreview>();
	const [requestError, setRequestError] = useState<string>();

	const run = async () => {
		// The parsed number goes to the schema as entered, so a status that is not one shows its error.
		const sample = { method, path, status_code: status.trim() === "" ? 200 : Number(status) };
		const headers = Object.fromEntries(
			headersText
				.split("\n")
				.map((line) => {
					const i = line.indexOf(":");
					return i < 0 ? ["", ""] : [line.slice(0, i).trim(), line.slice(i + 1).trim()];
				})
				.filter(([name, value]) => name && value),
		);
		const parsed = relayPreviewSampleSchema.safeParse(sample);
		if (!parsed.success) {
			setErrors(Object.fromEntries(parsed.error.issues.map((i) => [i.path.join("."), i.message])));
			return;
		}
		setErrors({});
		setRequestError(undefined);
		try {
			const out = await preview({
				name: endpoint.name,
				sample: {
					...sample,
					...(Object.keys(headers).length ? { headers } : {}),
					request_body: requestBody,
					request_content_type: requestBody ? "application/json" : undefined,
					response_content_type: responseType,
					response_body: responseBody,
				},
			}).unwrap();
			setResult(out);
		} catch (e) {
			setRequestError(getErrorMessage(e));
			setResult(undefined);
		}
	};

	return (
		<div className="space-y-4" data-testid="relay-preview-panel">
			<div className="flex w-full flex-row items-start gap-4">
				<div className="w-32 space-y-2">
					<Label>Method</Label>
					<Select value={method} onValueChange={setMethod}>
						<SelectTrigger className="w-full" data-testid="relay-preview-method">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{RelayRouteMethods.map((m) => (
								<SelectItem key={m} value={m}>
									{m}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</div>
				<div className="flex-1 space-y-2">
					<Label>Path</Label>
					<Input value={path} onChange={(e) => setPath(e.target.value)} placeholder="/v1/search?q=1" data-testid="relay-preview-path" />
					{errors.path && <p className="text-destructive text-sm">{errors.path}</p>}
				</div>
				<div className="w-28 space-y-2">
					<Label>Status</Label>
					<Input value={status} onChange={(e) => setStatus(e.target.value)} data-testid="relay-preview-status" />
					{errors.status_code && <p className="text-destructive text-sm">{errors.status_code}</p>}
				</div>
			</div>
			<div className="space-y-2">
				<Label>Request Headers</Label>
				<Textarea
					rows={2}
					className="font-mono text-xs"
					value={headersText}
					onChange={(e) => setHeadersText(e.target.value)}
					placeholder="X-Amz-Target: Textract.AnalyzeDocument"
					data-testid="relay-preview-request-headers"
				/>
				<p className="text-muted-foreground text-xs">One Name: value per line, for routes that match on request headers.</p>
			</div>
			<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
				<div className="space-y-2">
					<Label>Request Body</Label>
					<Textarea
						className="h-44 resize-none overflow-auto font-mono text-xs"
						value={requestBody}
						onChange={(e) => setRequestBody(e.target.value)}
						placeholder='{"query":"..."}'
						data-testid="relay-preview-request-body"
					/>
				</div>
				<div className="space-y-2">
					<Label>Response Body</Label>
					<Textarea
						className="h-44 resize-none overflow-auto font-mono text-xs"
						value={responseBody}
						onChange={(e) => setResponseBody(e.target.value)}
						placeholder='{"usage":{"credits":4}}'
						data-testid="relay-preview-response-body"
					/>
					<div className="space-y-1">
						<Label htmlFor="relay-preview-response-content-type" className="text-xs">
							Response Content-Type
						</Label>
						<Input
							id="relay-preview-response-content-type"
							className="h-8 text-xs"
							value={responseType}
							onChange={(e) => setResponseType(e.target.value)}
							placeholder="application/json"
							data-testid="relay-preview-response-content-type"
						/>
						<p className="text-muted-foreground text-xs">
							What the upstream replies with. A JSON body is read for units, cost and model; text/event-stream previews the streamed path;
							anything else is measured as bytes or characters only.
						</p>
					</div>
				</div>
			</div>
			<div className="flex justify-end">
				<Button type="button" variant="outline" onClick={run} isLoading={isLoading} data-testid="relay-preview-run">
					Run Test
				</Button>
			</div>
			{requestError && <p className="text-destructive text-sm">{requestError}</p>}
			{result && <UsagePreviewResult result={result} endpoint={endpoint} />}
		</div>
	);
}