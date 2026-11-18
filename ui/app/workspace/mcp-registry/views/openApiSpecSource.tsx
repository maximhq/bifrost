import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useGetCoreConfigQuery } from "@/lib/store";
import { Info, Upload } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	detectSpecLanguage,
	formatBytes,
	OPENAPI_AUTO_PARSE_DELAY_MS,
	knownOpenAPIUploadLimit,
	autoParseStep,
	openAPIParseControls,
	applyUploadedSpec,
	readUploadedSpec,
	serializedSpecBytes,
	uploadOverLimit,
	uploadSpecLimit,
	type OpenAPIUploadLimit,
	type UploadErrorState,
	visibleUploadError,
	type OpenAPISourceMode,
} from "./mcpClientOpenApiUtils";

export interface OpenAPISpecSourceProps {
	mode: OpenAPISourceMode;
	onModeChange: (mode: OpenAPISourceMode) => void;
	specText: string;
	onSpecTextChange: (text: string) => void;
	specUrl: string;
	onSpecUrlChange: (url: string) => void;
	fileName: string | null;
	onFileSelected: (file: File, text: string) => void;
	onParse: () => void;
	isParsing: boolean;
	parseError: string | null;
	/** The preview no longer matches the source (text or URL edited after parsing). */
	isStale: boolean;
	/** The preview describes the current source. */
	isCurrent: boolean;
	/** Identifies the source a parse would describe; a new key re-arms auto-parsing. */
	sourceKey: string;
	specError?: string;
	specUrlError?: string;
	disabled?: boolean;
	/** Narrower copy for the edit sheet's "Replace spec" card. */
	compact?: boolean;
	/** Which sources to offer; a surface without a server-side fetch leaves out "url". */
	modes?: OpenAPISourceMode[];
	/** What happens to a fetched document on this surface, under the URL field; defaults to the MCP wording. */
	urlHint?: ReactNode;
}

const ALL_SOURCE_MODES: OpenAPISourceMode[] = ["paste", "url", "upload"];

/**
 * The three ways to hand Bifrost an OpenAPI document: paste it, point at a URL,
 * or upload a file. The text itself always lands in `specText` (an upload is
 * read client-side), so the API only ever receives JSON. A pasted or uploaded
 * document is parsed on its own once the text settles; a URL is fetched on
 * request. The preview is what the operation picker is built from.
 */
export function OpenAPISpecSource({
	mode,
	onModeChange,
	specText,
	onSpecTextChange,
	specUrl,
	onSpecUrlChange,
	fileName,
	onFileSelected,
	onParse,
	isParsing,
	parseError,
	isStale,
	isCurrent,
	sourceKey,
	specError,
	specUrlError,
	disabled,
	compact,
	modes = ALL_SOURCE_MODES,
	urlHint,
}: OpenAPISpecSourceProps) {
	const { t } = useTranslation("mcp");
	const sourceModeLabel = (m: OpenAPISourceMode) =>
		m === "paste" ? t("openapi.source.tabPaste") : m === "upload" ? t("openapi.source.tabUpload") : "URL";
	const fileInputRef = useRef<HTMLInputElement>(null);
	// The server enforces the configured limits; this only spares a doomed upload. Until the config
	// has loaded (or when the caller cannot read it) the limit is unknown and only the supported
	// maximum is applied; a file over a cached limit is refused only after the config is re-read.
	// The upload must fit both the spec limit and the request body limit the document travels in.
	const { data: bifrostConfig, refetch: refetchConfig } = useGetCoreConfigQuery({ fromDB: true });
	const uploadLimit = knownOpenAPIUploadLimit(bifrostConfig);
	const sourceEmpty = mode === "url" ? !specUrl.trim() : !specText.trim();
	// Encoding a large upload is not free, so measure it once per text change rather than on every
	// render, and only for an upload: the other modes never show or check the size.
	const specBytes = useMemo(() => (mode === "upload" ? new TextEncoder().encode(specText).length : 0), [mode, specText]);
	const specSerializedBytes = useMemo(
		() => (mode === "upload" && specText && uploadLimit?.bodyBytes != null ? serializedSpecBytes(specText) : 0),
		[mode, specText, uploadLimit?.bodyBytes],
	);
	const uploadOver =
		mode === "upload" && specText && uploadLimit !== null ? uploadOverLimit(specBytes, specSerializedBytes, uploadLimit) : null;
	const uploadTooLarge = uploadOver !== null;
	const bodyLimitMessage = (limit: OpenAPIUploadLimit) =>
		t("openapi.source.fileTooLargeBodyCap", { size: formatBytes(limit.bodyBytes ?? 0) });

	const [uploadError, setUploadError] = useState<UploadErrorState | null>(null);
	// Bumped by every new file, source switch and text edit, so a slow read that
	// finishes after any of them cannot overwrite the newer source.
	const readToken = useRef(0);
	// Unmounting (the replacement was cancelled) invalidates a read still in
	// flight, so it cannot land in a draft opened afterwards.
	useEffect(
		() => () => {
			readToken.current++;
		},
		[],
	);

	const handleFile = async (file: File | undefined) => {
		if (!file) return;
		const token = ++readToken.current;
		let current = uploadLimit;
		const limit = await uploadSpecLimit(file.size, uploadLimit?.bytes ?? null, async () => {
			current = knownOpenAPIUploadLimit(await refetchConfig().unwrap());
			return current?.bytes ?? null;
		});
		if (token !== readToken.current) return;
		const result = await readUploadedSpec(
			file,
			limit,
			current?.bodyBound && current.bytes === limit ? bodyLimitMessage(current) : undefined,
		);
		if (token !== readToken.current) return;
		// A rejected file keeps the document already loaded.
		const { accept, error } = applyUploadedSpec(fileName ?? "", result);
		setUploadError(error);
		if (accept) onFileSelected(file, result.text);
	};
	const handleModeChange = (next: OpenAPISourceMode) => {
		readToken.current++;
		setUploadError(null);
		onModeChange(next);
	};
	const handleSpecTextChange = (text: string) => {
		readToken.current++;
		setUploadError(null);
		onSpecTextChange(text);
	};
	const shownUploadError = visibleUploadError(uploadError, fileName ?? "");

	const controls = openAPIParseControls({
		mode,
		isCurrent,
		hasError: !!parseError,
		canParse: !disabled && !sourceEmpty && !uploadTooLarge,
		isParsing,
	});
	// Each source is auto-parsed once: a failed parse waits for an edit (or the
	// retry button) instead of firing again.
	const autoParsedKey = useRef<string | null>(null);
	const onParseRef = useRef(onParse);
	// Updated after commit, so a timer never calls a callback from a render
	// that was discarded.
	useEffect(() => {
		onParseRef.current = onParse;
	}, [onParse]);
	const shouldAutoParse = controls.autoParse && !disabled && !sourceEmpty && !uploadTooLarge && !isCurrent;
	useEffect(() => {
		const step = autoParseStep(autoParsedKey.current, sourceKey, shouldAutoParse);
		if (!step.parse) {
			autoParsedKey.current = step.remembered;
			return;
		}
		const timer = setTimeout(() => {
			autoParsedKey.current = step.remembered;
			onParseRef.current();
		}, OPENAPI_AUTO_PARSE_DELAY_MS);
		return () => clearTimeout(timer);
	}, [shouldAutoParse, sourceKey]);

	// A URL is fetched from the button beside it; the other sources keep their retry button below.
	const parseButton = (
		<Button
			type="button"
			size="sm"
			className={mode === "url" ? "h-9 shrink-0" : undefined}
			onClick={onParse}
			disabled={disabled || isParsing || sourceEmpty || !!uploadTooLarge}
			isLoading={isParsing}
			data-testid="openapi-parse-btn"
		>
			{controls.buttonLabel}
		</Button>
	);

	return (
		<div className="space-y-3" data-testid="openapi-spec-source">
			<Tabs value={mode} onValueChange={(value) => handleModeChange(value as OpenAPISourceMode)}>
				<TabsList data-testid="openapi-spec-source-tabs">
					{modes.map((m) => (
						<TabsTrigger key={m} value={m} data-testid={`openapi-spec-source-tab-${m}`} disabled={disabled}>
							{sourceModeLabel(m)}
						</TabsTrigger>
					))}
				</TabsList>

				<TabsContent value="paste" className="mt-2 space-y-2">
					<div className={specError ? "border-destructive rounded-sm border" : "rounded-sm border"} data-testid="openapi-spec-paste-editor">
						<CodeEditor
							className="z-0 w-full"
							lang={detectSpecLanguage(specText)}
							code={specText}
							onChange={handleSpecTextChange}
							minHeight={compact ? 160 : 220}
							maxHeight={compact ? 320 : 420}
							shouldAdjustInitialHeight={true}
							wrap={true}
							readonly={disabled}
						/>
					</div>
					<p className="text-muted-foreground text-xs">{t("openapi.source.pasteHelp")}</p>
				</TabsContent>

				<TabsContent value="url" className="mt-2 space-y-2">
					<div className="flex flex-col gap-2">
						<Label htmlFor="openapi-spec-url">{t("openapi.source.specUrl")}</Label>
						<div className="flex items-center gap-2">
							<Input
								id="openapi-spec-url"
								className="min-w-0 flex-1"
								data-testid="openapi-spec-url-input"
								placeholder="https://api.example.com/openapi.json"
								value={specUrl}
								disabled={disabled}
								onChange={(e) => onSpecUrlChange(e.target.value)}
								aria-invalid={!!specUrlError}
							/>
							{controls.showButton && parseButton}
						</div>
					</div>
					<p className="text-muted-foreground text-xs">{urlHint ?? t("openapi.source.urlHelp")}</p>
				</TabsContent>

				<TabsContent value="upload" className="mt-2 space-y-2">
					<div className="flex flex-wrap items-center gap-3">
						<Button
							type="button"
							variant="outline"
							size="sm"
							disabled={disabled}
							onClick={() => fileInputRef.current?.click()}
							data-testid="openapi-spec-upload-btn"
						>
							<Upload className="mr-2 h-4 w-4" />
							{t("openapi.source.chooseFile")}
						</Button>
						<input
							ref={fileInputRef}
							type="file"
							accept=".json,.yaml,.yml,application/json,application/yaml,application/x-yaml,text/yaml"
							className="hidden"
							data-testid="openapi-spec-upload-input"
							onChange={(e) => {
								void handleFile(e.target.files?.[0]);
								e.target.value = "";
							}}
						/>
						{fileName && (
							<span className="text-muted-foreground font-mono text-xs" data-testid="openapi-spec-upload-filename">
								{fileName}
								{specText ? ` · ${formatBytes(specBytes)}` : ""}
							</span>
						)}
					</div>
					{shownUploadError || uploadTooLarge ? (
						<p className="text-destructive text-xs" data-testid="openapi-spec-upload-error">
							{shownUploadError ??
								(uploadOver === "body" && uploadLimit
									? bodyLimitMessage(uploadLimit)
									: t("openapi.source.fileTooLarge", { size: formatBytes(uploadLimit?.specBytes ?? 0) }))}
						</p>
					) : (
						<p className="text-muted-foreground text-xs">
							{uploadLimit === null
								? t("openapi.source.uploadHelpNoLimit")
								: uploadLimit.bodyBound
									? t("openapi.source.uploadHelpBodyCap", { size: formatBytes(uploadLimit.bytes) })
									: t("openapi.source.uploadHelp", { size: formatBytes(uploadLimit.bytes) })}
						</p>
					)}
				</TabsContent>
			</Tabs>

			{specError && mode !== "url" && (
				<p className="text-destructive text-xs" data-testid="openapi-spec-error">
					{specError}
				</p>
			)}
			{specUrlError && mode === "url" && (
				<p className="text-destructive text-xs" data-testid="openapi-spec-url-error">
					{specUrlError}
				</p>
			)}

			<div className="flex items-center justify-between gap-3">
				<div className="min-w-0 flex-1">
					{controls.showPending && (
						<p className="text-muted-foreground text-xs" data-testid="openapi-parse-pending">
							{t("openapi.source.parsing")}
						</p>
					)}
					{mode === "url" && isStale && !parseError && (
						<div
							className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-2 text-xs text-amber-800"
							data-testid="openapi-spec-stale-notice"
						>
							<Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-600" />
							<p>{t("openapi.source.staleNotice")}</p>
						</div>
					)}
					{parseError && (
						<p className="text-destructive text-xs break-words" data-testid="openapi-parse-error">
							{parseError}
						</p>
					)}
				</div>
				{controls.showButton && mode !== "url" && parseButton}
			</div>
		</div>
	);
}