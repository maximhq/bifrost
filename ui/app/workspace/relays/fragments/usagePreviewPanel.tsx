import { Button } from "@/components/ui/button";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, usePreviewRelayUsageMutation } from "@/lib/store";
import { RelayRouteMethods, type Relay, type UsagePreview } from "@/lib/types/relays";
import { relayPreviewSampleSchema } from "@/lib/types/schemas";
import { useEffect, useRef, useState } from "react";
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
	const [headers, setHeaders] = useState<Record<string, string>>({});
	const [requestBody, setRequestBody] = useState("");
	const [responseType, setResponseType] = useState("application/json");
	const [responseBody, setResponseBody] = useState("");
	const [errors, setErrors] = useState<Record<string, string>>({});
	const [result, setResult] = useState<UsagePreview>();
	const [requestError, setRequestError] = useState<string>();
	const resultRef = useRef<HTMLDivElement>(null);
	// The result renders below the inputs, out of view behind the sticky Run Test bar; bring it up.
	useEffect(() => {
		if (result) resultRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" });
	}, [result]);

	const run = async () => {
		// Rows with a name and a value only; the table keeps a half-typed row until it is finished.
		const sentHeaders = Object.fromEntries(
			Object.entries(headers)
				.map(([name, value]) => [name.trim(), value.trim()])
				.filter(([name, value]) => name && value),
		);
		// The parsed number goes to the schema as entered, so a status that is not one shows its error.
		const sample = { method, path, status_code: status.trim() === "" ? 200 : Number(status) };
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
					...(Object.keys(sentHeaders).length ? { headers: sentHeaders } : {}),
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
		<>
			<div className="grow space-y-4 px-4 md:px-8" data-testid="relay-preview-panel">
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
						<Input
							value={status}
							inputMode="numeric"
							maxLength={3}
							// An HTTP status is three digits; anything else is refused as typed rather than on Run.
							onChange={(e) => /^\d{0,3}$/.test(e.target.value) && setStatus(e.target.value)}
							data-testid="relay-preview-status"
						/>
						{errors.status_code && <p className="text-destructive text-sm">{errors.status_code}</p>}
					</div>
				</div>
				<div className="space-y-2">
					<Label>Request Headers</Label>
					<HeadersTable
						label=""
						value={headers}
						onChange={setHeaders}
						keyPlaceholder="X-Amz-Target"
						valuePlaceholder="Textract.AnalyzeDocument"
						testIdPrefix="relay-preview-request-header"
					/>
					<p className="text-muted-foreground text-xs">For routes that match on request headers.</p>
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
					</div>
				</div>
				<div className="space-y-2">
					<Label htmlFor="relay-preview-response-content-type">Response Content-Type</Label>
					<Input
						id="relay-preview-response-content-type"
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
				{requestError && <p className="text-destructive text-sm">{requestError}</p>}
				{result && (
					<div ref={resultRef}>
						<UsagePreviewResult result={result} endpoint={endpoint} />
					</div>
				)}
			</div>
			<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
				<Button type="button" onClick={run} isLoading={isLoading} data-testid="relay-preview-run">
					Run Test
				</Button>
			</div>
		</>
	);
}