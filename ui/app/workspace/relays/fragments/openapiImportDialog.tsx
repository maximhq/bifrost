import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { importOpenAPI, parseOpenAPIText, type OpenAPIImport } from "@/lib/utils/openapiImport";
import { useCallback, useRef, useState } from "react";
import { specFingerprint, type OpenAPISourceMode } from "../../mcp-registry/views/mcpClientOpenApiUtils";
import { OpenAPISpecSource } from "../../mcp-registry/views/openApiSpecSource";

interface OpenAPIImportDialogProps {
	open: boolean;
	onClose: () => void;
	onImport: (result: OpenAPIImport) => void;
}

// Relays read the document in the browser, so only the two client-side sources apply.
const RELAY_SOURCE_MODES: OpenAPISourceMode[] = ["paste", "upload"];

/**
 * Turns an OpenAPI or Swagger document (pasted or uploaded, JSON or YAML) into routes, an auth
 * suggestion and a base URL hint. The document is parsed as soon as it settles, like the MCP
 * OpenAPI source, and the summary is what Import hands over. Nothing is saved until the relay is.
 */
export function OpenAPIImportDialog({ open, onClose, onImport }: OpenAPIImportDialogProps) {
	const [mode, setMode] = useState<OpenAPISourceMode>("paste");
	const [text, setText] = useState("");
	const [fileName, setFileName] = useState<string | null>(null);
	const [parseError, setParseError] = useState<string | null>(null);
	const [isParsing, setIsParsing] = useState(false);
	const [preview, setPreview] = useState<{ key: string; result: OpenAPIImport } | null>(null);

	const sourceKey = specFingerprint(mode, text, undefined);
	const isCurrent = preview?.key === sourceKey;

	// Each parse gets a sequence number; a result from an older attempt (the YAML parser loading
	// while the text was replaced with JSON, say) is dropped, error and loading state included, so
	// it can never overwrite a newer preview and leave Import disabled.
	const parseSeq = useRef(0);
	const parse = useCallback(async () => {
		const seq = ++parseSeq.current;
		setIsParsing(true);
		setParseError(null);
		try {
			const doc = await parseOpenAPIText(text);
			if (seq !== parseSeq.current) return;
			setPreview({ key: sourceKey, result: importOpenAPI(doc) });
		} catch (e) {
			if (seq !== parseSeq.current) return;
			setPreview(null);
			setParseError(e instanceof Error ? e.message : "The document could not be read");
		} finally {
			if (seq === parseSeq.current) setIsParsing(false);
		}
	}, [text, sourceKey]);

	const reset = () => {
		setMode("paste");
		setText("");
		setFileName(null);
		setParseError(null);
		setPreview(null);
	};
	const close = () => {
		reset();
		onClose();
	};
	const apply = () => {
		if (!preview || !isCurrent) return;
		onImport(preview.result);
		close();
	};

	const result = isCurrent ? preview?.result : undefined;

	return (
		<Dialog open={open} onOpenChange={(o) => !o && close()}>
			<DialogContent className="sm:max-w-2xl">
				<DialogHeader>
					<DialogTitle>Import routes from OpenAPI</DialogTitle>
					<DialogDescription>
						Every operation becomes a route. Imported routes replace the current list; review prices before saving.
					</DialogDescription>
				</DialogHeader>
				<div data-testid="relay-openapi-source">
					<OpenAPISpecSource
						modes={RELAY_SOURCE_MODES}
						mode={mode}
						onModeChange={(next) => {
							setMode(next);
							setParseError(null);
						}}
						specText={text}
						onSpecTextChange={setText}
						specUrl=""
						onSpecUrlChange={() => {}}
						fileName={fileName}
						onFileSelected={(file, content) => {
							setFileName(file.name);
							setText(content);
						}}
						onParse={parse}
						isParsing={isParsing}
						parseError={parseError}
						isStale={!!preview && !isCurrent}
						isCurrent={isCurrent}
						sourceKey={sourceKey}
					/>
				</div>
				{result && (
					<div className="bg-muted/40 space-y-1.5 rounded-md border p-3 text-sm" data-testid="relay-openapi-preview">
						<div className="flex flex-wrap items-center gap-2">
							<span className="font-medium">{result.title ?? "Untitled document"}</span>
							{result.version && <Badge variant="outline">v{result.version}</Badge>}
							<Badge variant="secondary">
								{result.routes.length} {result.routes.length === 1 ? "route" : "routes"}
							</Badge>
							{result.partial && <Badge variant="outline">partial</Badge>}
						</div>
						<div className="text-muted-foreground text-xs">
							{result.baseUrl ? (
								<>
									Base URL <code className="font-mono">{result.baseUrl}</code>
								</>
							) : (
								"No server listed; keep the base URL you set on the relay."
							)}
							{result.auth ? ` · Auth suggestion: ${result.auth.type}` : ""}
						</div>
						{result.warnings.length > 0 && (
							<ul className="list-disc space-y-0.5 pl-4 text-xs text-amber-700 dark:text-amber-400">
								{result.warnings.map((w) => (
									<li key={w}>{w}</li>
								))}
							</ul>
						)}
					</div>
				)}
				<DialogFooter>
					<Button variant="outline" onClick={close}>
						Cancel
					</Button>
					<Button onClick={apply} disabled={!result || isParsing} data-testid="relay-openapi-import">
						Import{result ? ` ${result.routes.length} ${result.routes.length === 1 ? "route" : "routes"}` : ""}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}