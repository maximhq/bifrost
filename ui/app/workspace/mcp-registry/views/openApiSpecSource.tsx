import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Info, Upload } from "lucide-react";
import { useRef } from "react";
import { detectSpecLanguage, formatBytes, isSpecTooLarge, OPENAPI_MAX_SPEC_BYTES, type OpenAPISourceMode } from "./mcpClientOpenApi.utils";

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
	specError?: string;
	specUrlError?: string;
	disabled?: boolean;
	/** Narrower copy for the edit sheet's "Replace spec" card. */
	compact?: boolean;
}

/**
 * The three ways to hand Bifrost an OpenAPI document: paste it, point at a URL,
 * or upload a file. The text itself always lands in `specText` (an upload is
 * read client-side), so the API only ever receives JSON. Parsing is explicit:
 * the Parse button hands the source to the preview endpoint, and the preview is
 * what the operation picker is built from.
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
	specError,
	specUrlError,
	disabled,
	compact,
}: OpenAPISpecSourceProps) {
	const fileInputRef = useRef<HTMLInputElement>(null);
	const sourceEmpty = mode === "url" ? !specUrl.trim() : !specText.trim();
	const uploadTooLarge = mode === "upload" && specText && isSpecTooLarge(new TextEncoder().encode(specText).length);

	const handleFile = async (file: File | undefined) => {
		if (!file) return;
		if (isSpecTooLarge(file.size)) {
			onFileSelected(file, "");
			return;
		}
		const text = await file.text();
		onFileSelected(file, text);
	};

	return (
		<div className="space-y-3" data-testid="openapi-spec-source">
			<Tabs value={mode} onValueChange={(value) => onModeChange(value as OpenAPISourceMode)}>
				<TabsList data-testid="openapi-spec-source-tabs">
					<TabsTrigger value="paste" data-testid="openapi-spec-source-tab-paste" disabled={disabled}>
						Paste
					</TabsTrigger>
					<TabsTrigger value="url" data-testid="openapi-spec-source-tab-url" disabled={disabled}>
						URL
					</TabsTrigger>
					<TabsTrigger value="upload" data-testid="openapi-spec-source-tab-upload" disabled={disabled}>
						Upload
					</TabsTrigger>
				</TabsList>

				<TabsContent value="paste" className="space-y-2">
					<div className={specError ? "border-destructive rounded-sm border" : "rounded-sm border"} data-testid="openapi-spec-paste-editor">
						<CodeEditor
							className="z-0 w-full"
							lang={detectSpecLanguage(specText)}
							code={specText}
							onChange={onSpecTextChange}
							minHeight={compact ? 160 : 220}
							maxHeight={compact ? 320 : 420}
							shouldAdjustInitialHeight={true}
							wrap={true}
							readonly={disabled}
						/>
					</div>
					<p className="text-muted-foreground text-xs">
						Paste the OpenAPI or Swagger document as JSON or YAML (2.0, 3.0, 3.1 and 3.2 are supported).
					</p>
				</TabsContent>

				<TabsContent value="url" className="space-y-2">
					<Label htmlFor="openapi-spec-url">Spec URL</Label>
					<Input
						id="openapi-spec-url"
						data-testid="openapi-spec-url-input"
						placeholder="https://api.example.com/openapi.json"
						value={specUrl}
						disabled={disabled}
						onChange={(e) => onSpecUrlChange(e.target.value)}
						aria-invalid={!!specUrlError}
					/>
					<p className="text-muted-foreground text-xs">
						Bifrost fetches the document once and stores it with the server; the URL is kept as its source.
					</p>
				</TabsContent>

				<TabsContent value="upload" className="space-y-2">
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
							Choose file
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
								{specText ? ` · ${formatBytes(new TextEncoder().encode(specText).length)}` : ""}
							</span>
						)}
					</div>
					{uploadTooLarge || (fileName && !specText) ? (
						<p className="text-destructive text-xs" data-testid="openapi-spec-upload-error">
							The file exceeds the {formatBytes(OPENAPI_MAX_SPEC_BYTES)} limit for OpenAPI documents.
						</p>
					) : (
						<p className="text-muted-foreground text-xs">
							JSON or YAML, up to {formatBytes(OPENAPI_MAX_SPEC_BYTES)}. The file is read in your browser and stored with the server.
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
					{isStale && !parseError && (
						<div
							className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-2 text-xs text-amber-800"
							data-testid="openapi-spec-stale-notice"
						>
							<Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-600" />
							<p>The document changed since it was last parsed. Parse it again to refresh the operations below.</p>
						</div>
					)}
					{parseError && (
						<p className="text-destructive text-xs break-words" data-testid="openapi-parse-error">
							{parseError}
						</p>
					)}
				</div>
				<Button
					type="button"
					size="sm"
					onClick={onParse}
					disabled={disabled || isParsing || sourceEmpty || !!uploadTooLarge}
					isLoading={isParsing}
					data-testid="openapi-parse-btn"
				>
					Parse spec
				</Button>
			</div>
		</div>
	);
}