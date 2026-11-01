import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { importOpenAPI, parseOpenAPIText, type OpenAPIImport } from "@/lib/utils/openapiImport";
import { getErrorMessage, useFetchRelayOpenAPISpecMutation } from "@/lib/store";
import { relayOpenAPISpecUrlSchema } from "@/lib/types/schemas";
import { useCallback, useRef, useState } from "react";
import { specFingerprint, type OpenAPISourceMode } from "../../mcp-registry/views/mcpClientOpenApiUtils";
import { OpenAPISpecSource } from "../../mcp-registry/views/openApiSpecSource";
import { useTranslation } from "react-i18next";

interface OpenAPIImportDialogProps {
	open: boolean;
	onClose: () => void;
	onImport: (result: OpenAPIImport) => void;
}

/**
 * Turns an OpenAPI or Swagger document (pasted, uploaded or fetched from a URL; JSON or YAML) into
 * routes, an auth suggestion and a base URL hint, through the same source picker as an MCP OpenAPI
 * server. A pasted or uploaded document is parsed as soon as it settles; a URL is fetched by the
 * gateway on request (the browser cannot fetch most spec URLs itself) and parsed here the same way.
 * The summary is what Import hands over. Nothing is saved until the relay is.
 */
export function OpenAPIImportDialog({ open, onClose, onImport }: OpenAPIImportDialogProps) {
	const { t } = useTranslation("config");

	const [mode, setMode] = useState<OpenAPISourceMode>("paste");
	const [text, setText] = useState("");
	const [specUrl, setSpecUrl] = useState("");
	const [specUrlError, setSpecUrlError] = useState<string>();
	const [fetchSpec] = useFetchRelayOpenAPISpecMutation();
	const [fileName, setFileName] = useState<string | null>(null);
	const [parseError, setParseError] = useState<string | null>(null);
	const [isParsing, setIsParsing] = useState(false);
	const [preview, setPreview] = useState<{ key: string; result: OpenAPIImport } | null>(null);

	const sourceKey = specFingerprint(mode, text, specUrl);
	const isCurrent = preview?.key === sourceKey;

	// Each parse gets a sequence number; a result from an older attempt (the YAML parser loading
	// while the text was replaced with JSON, say) is dropped, error and loading state included, so
	// it can never overwrite a newer preview and leave Import disabled.
	const parseSeq = useRef(0);
	const parse = useCallback(async () => {
		const checked = mode === "url" ? relayOpenAPISpecUrlSchema.safeParse(specUrl) : undefined;
		if (checked && !checked.success) {
			setSpecUrlError(checked.error.issues[0]?.message ?? t("relays.openapiImport.urlInvalid"));
			return;
		}
		const url = checked?.data ?? "";
		const seq = ++parseSeq.current;
		setIsParsing(true);
		setParseError(null);
		setSpecUrlError(undefined);
		try {
			// A fetched document resolves relative servers against the URL it was read from.
			const source = mode === "url" ? await fetchSpec({ spec_url: url }).unwrap() : undefined;
			if (seq !== parseSeq.current) return;
			const doc = await parseOpenAPIText(source?.spec ?? text);
			if (seq !== parseSeq.current) return;
			setPreview({ key: sourceKey, result: importOpenAPI(doc, source?.spec_url) });
		} catch (e) {
			if (seq !== parseSeq.current) return;
			setPreview(null);
			setParseError(e instanceof Error ? e.message : getErrorMessage(e) || t("relays.openapiImport.readFailed"));
		} finally {
			if (seq === parseSeq.current) setIsParsing(false);
		}
	}, [mode, specUrl, text, sourceKey, fetchSpec, t]);

	const reset = () => {
		// A fetch still in flight belongs to the dialog being closed: retire it so its result or error
		// never reaches a reopened dialog, and stop the spinner so Fetch is usable again at once.
		parseSeq.current++;
		setIsParsing(false);
		setMode("paste");
		setText("");
		setSpecUrl("");
		setSpecUrlError(undefined);
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
					<DialogTitle>{t("relays.openapiImport.title")}</DialogTitle>
					<DialogDescription>
						{t("relays.openapiImport.description")}
					</DialogDescription>
				</DialogHeader>
				<div data-testid="relay-openapi-source">
					<OpenAPISpecSource
						mode={mode}
						onModeChange={(next) => {
							setMode(next);
							setParseError(null);
							setSpecUrlError(undefined);
						}}
						specText={text}
						onSpecTextChange={setText}
						specUrl={specUrl}
						onSpecUrlChange={(next) => {
							setSpecUrl(next);
							setSpecUrlError(undefined);
						}}
						specUrlError={specUrlError}
						urlHint={t("relays.openapiImport.urlHint")}
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
							<span className="font-medium">{result.title ?? t("relays.openapiImport.untitled")}</span>
							{result.version && <Badge variant="outline">v{result.version}</Badge>}
							<Badge variant="secondary">
								{t("relays.openapiImport.routeCount", { count: result.routes.length })}
								</Badge>
							{result.partial && <Badge variant="outline">{t("relays.openapiImport.partial")}</Badge>}
						</div>
						<div className="text-muted-foreground text-xs">
							{result.baseUrl ? (
								<>
									{t("relays.openapiImport.baseUrl")} <code className="font-mono">{result.baseUrl}</code>
								</>
							) : (
								t("relays.openapiImport.noServer")
							)}
							{result.auth ? ` · ${t("relays.openapiImport.authSuggestion", { type: result.auth.type })}` : ""}
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
						{t("cancel", { ns: "common" })}

					</Button>
					<Button onClick={apply} disabled={!result || isParsing} data-testid="relay-openapi-import">
						{result ? t("relays.openapiImport.importCount", { count: result.routes.length }) : t("relays.openapiImport.import")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}