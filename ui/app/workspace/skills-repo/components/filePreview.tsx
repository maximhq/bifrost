"use client";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Textarea } from "@/components/ui/textarea";
import { SkillFileEntry } from "@/lib/types/skills";
import { getApiBaseUrl } from "@/lib/utils/port";
import { Download, File as FileIcon, Info, Loader2, Save } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { formatFileSize } from "./helpers";

// ---------- helpers ----------

export function getFileServeUrl(skillName: string, path: string) {
  return `${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skillName)}/files/${path
    .split("/")
    .map(encodeURIComponent)
    .join("/")}`;
}

const TEXT_MIME_HINTS = [
  "json",
  "xml",
  "yaml",
  "yml",
  "javascript",
  "typescript",
  "x-sh",
  "x-shellscript",
  "x-python",
  "csv",
  "markdown",
  "toml",
];

function isTextLikeMime(
  mime: string,
  sourceType: SkillFileEntry["source_type"],
) {
  if (sourceType === "text") return true;
  if (!mime) return false;
  if (mime.startsWith("text/")) return true;
  return TEXT_MIME_HINTS.some((hint) => mime.includes(hint));
}

type FileKind = "text" | "image" | "audio" | "video" | "binary";

function detectKind(file: SkillFileEntry): FileKind {
  const mime = file.mime_type || "";
  if (mime.startsWith("image/")) return "image";
  if (mime.startsWith("audio/")) return "audio";
  if (mime.startsWith("video/")) return "video";
  if (isTextLikeMime(mime, file.source_type)) return "text";
  return "binary";
}

/**
 * Resolves what the preview needs: inline text, a media URL, or nothing
 * (e.g. an upload that hasn't been saved yet, so there's no serve URL).
 *
 * text / dataurl: saved files are fetched from the serve endpoint (content
 *   is NOT inlined in the API response). Local/unsaved files use the
 *   in-memory content or data URI directly.
 * upload: same serve-endpoint approach for saved files.
 * url: uses the external source_url directly.
 */
function resolveSource(
  file: SkillFileEntry,
  skillName: string,
): { url?: string; inlineText?: string; unavailable?: boolean } {
  switch (file.source_type) {
    case "text":
    case "dataurl":
      // Saved text/dataurl files: fetch content from the serve endpoint.
      if (!file.__local && skillName && file.path) {
        return { url: getFileServeUrl(skillName, file.path) };
      }
      // Local/unsaved: use in-memory content.
      if (file.source_type === "text") {
        return { inlineText: file.content ?? "" };
      }
      return file.dataurl ? { url: file.dataurl } : { unavailable: true };
    case "url":
      return file.source_url ? { url: file.source_url } : { unavailable: true };
    case "upload":
      // Saved uploads are reachable through the serve endpoint. A freshly
      // selected upload (still local) has no served URL until the skill is saved.
      if (!file.__local && skillName && file.path) {
        return { url: getFileServeUrl(skillName, file.path) };
      }
      return { unavailable: true };
    default:
      return { unavailable: true };
  }
}

// ---------- FilePreview ----------

export function FilePreview({
  file,
  skillName,
  mode,
  onContentChange,
  onFileUpdate,
  editValue,
  onEditChange,
  downloadUrl,
}: {
  file: SkillFileEntry;
  skillName: string;
  mode: "view" | "edit";
  // Editing applies to text and dataurl source types (text-like content only).
  onContentChange?: (content: string) => void;
  onFileUpdate?: (updates: Partial<SkillFileEntry>) => void;
  // Controlled buffer for the text editor (used by FilePreviewPane to defer commits).
  editValue?: string;
  onEditChange?: (content: string) => void;
  // Explicit download target; falls back to the resolved media URL.
  downloadUrl?: string;
}) {
  const kind = detectKind(file);
  const source = resolveSource(file, skillName);
  const fileName = file.path.split("/").filter(Boolean).pop() || file.path;
  const resolvedDownloadUrl = downloadUrl ?? source.url;

  if (mode === "edit") {
    return <FileSourceEditor file={file} skillName={skillName} onFileUpdate={onFileUpdate} />;
  }

  // Text fetched from a URL/serve endpoint (for both view and edit modes).
  const [fetchedText, setFetchedText] = useState<string | null>(null);
  const [fetchState, setFetchState] = useState<"idle" | "loading" | "error">(
    "idle",
  );

  useEffect(() => {
    if (kind !== "text" || source.inlineText != null || !source.url) {
      setFetchedText(null);
      setFetchState("idle");
      return;
    }
    let cancelled = false;
    setFetchState("loading");
    fetch(source.url)
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.text();
      })
      .then((text) => {
        if (cancelled) return;
        setFetchedText(text);
        setFetchState("idle");
      })
      .catch(() => {
        if (cancelled) return;
        setFetchState("error");
      });
    return () => {
      cancelled = true;
    };
  }, [kind, source.inlineText, source.url]);

  // ---- Unavailable (e.g. unsaved upload) ----
  if (source.unavailable && kind !== "text") {
    return (
      <FallbackBlock
        fileName={fileName}
        file={file}
        downloadUrl={resolvedDownloadUrl}
      >
        Preview available after saving.
      </FallbackBlock>
    );
  }

  // ---- Image ----
  if (kind === "image" && source.url) {
    return (
      <div className="bg-muted/20 flex items-center justify-center p-4">
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          src={source.url}
          alt={fileName}
          className="max-h-full max-w-full object-contain"
        />
      </div>
    );
  }

  // ---- Audio ----
  if (kind === "audio" && source.url) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-4 p-6">
        <FileIcon className="text-muted-foreground h-10 w-10" />
        <span className="text-muted-foreground max-w-full truncate font-mono text-xs">
          {fileName}
        </span>
        <audio controls src={source.url} className="w-full max-w-md" />
      </div>
    );
  }

  // ---- Video ----
  if (kind === "video" && source.url) {
    return (
      <div className="bg-muted/20 flex h-full items-center justify-center p-4">
        <video controls src={source.url} className="max-h-full max-w-full" />
      </div>
    );
  }

  // ---- Text ----
  if (kind === "text") {
    // Show loading state before content is available (applies to both view and edit).
    if (fetchState === "loading") {
      return (
        <div className="text-muted-foreground flex h-full items-center justify-center gap-2 text-xs">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading…
        </div>
      );
    }
    if (fetchState === "error") {
      return (
        <FallbackBlock
          fileName={fileName}
          file={file}
          downloadUrl={resolvedDownloadUrl}
        >
          Could not load file contents.
        </FallbackBlock>
      );
    }

    const textContent = source.inlineText ?? fetchedText ?? "";

    return (
      <ScrollArea className="h-full">
        <pre className="p-4 font-mono text-xs leading-5 whitespace-pre-wrap">
          {textContent || "(empty)"}
        </pre>
      </ScrollArea>
    );
  }

  // ---- Fallback (binary / unknown) ----
  return (
    <FallbackBlock
      fileName={fileName}
      file={file}
      downloadUrl={resolvedDownloadUrl}
    />
  );
}

// ---------- FilePreviewPane ----------
// Full-height bordered box: a header (file path + optional download) over the preview.

export function FilePreviewPane({
  file,
  skillName,
  mode,
  onContentChange,
  onFileUpdate,
  downloadUrl,
  registerFlush,
}: {
  file: SkillFileEntry;
  skillName: string;
  mode: "view" | "edit";
  onContentChange?: (content: string) => void;
  onFileUpdate?: (updates: Partial<SkillFileEntry>) => void;
  downloadUrl?: string;
  // Lets the parent commit a pending buffer (e.g. before a version save).
  registerFlush?: (flush: (() => void) | null) => void;
}) {
  const resolved = mode === "view" ? (downloadUrl ?? resolveSource(file, skillName).url) : undefined;
  const fileName = file.path.split("/").filter(Boolean).pop() || file.path;

  // text and dataurl are editable (when content is text-like).
  const isEditable = false;
  // Draft starts null — before the user types, FilePreview shows the fetched/inline
  // content directly. Once the user types, the draft takes over.
  const [draft, setDraft] = useState<string | null>(null);
  const committedRef = useRef<string | null>(null);
  const draftRef = useRef(draft);
  draftRef.current = draft;

  const saveFile = useCallback(() => {
    if (draftRef.current != null && draftRef.current !== committedRef.current) {
      committedRef.current = draftRef.current;
      onContentChange?.(draftRef.current);
    }
  }, [onContentChange]);

  // Flush on unmount (the pane is keyed by path, so switching files commits)
  // and expose the flush to the parent for save-time commits.
  useEffect(() => {
    if (!isEditable) return;
    registerFlush?.(saveFile);
    return () => {
      saveFile();
      registerFlush?.(null);
    };
  }, [isEditable, saveFile, registerFlush]);

  const dirty = isEditable && draft != null && draft !== committedRef.current;

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden rounded-sm border">
      <div className="bg-muted/30 flex h-9 shrink-0 items-center justify-between gap-2 border-b px-3">
        <span
          className="flex min-w-0 items-center gap-1.5 truncate font-mono text-xs"
          title={file.path}
        >
          {file.path}
          {dirty && (
            <span
              className="bg-primary inline-block size-1.5 shrink-0 rounded-full"
              aria-label="Unsaved changes"
            />
          )}
        </span>
        <div className="flex shrink-0 items-center gap-1">
          {isEditable && (
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-foreground h-7 px-2 text-xs"
              data-testid="skill-file-save-btn"
              disabled={!dirty}
              onClick={saveFile}
            >
              <Save className="h-3.5 w-3.5" />
              Save file
            </Button>
          )}
          {resolved && (
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-foreground h-7 px-2"
              asChild
            >
              <a
                href={resolved}
                download={fileName}
                aria-label={`Download ${fileName}`}
              >
                <Download className="h-3.5 w-3.5" />
              </a>
            </Button>
          )}
        </div>
      </div>
      <div className="min-h-0 grow overflow-y-auto">
        <FilePreview
          file={file}
          skillName={skillName}
          mode={mode}
          onFileUpdate={onFileUpdate}
          editValue={isEditable && draft != null ? draft : undefined}
          onEditChange={isEditable ? (v) => setDraft(v) : undefined}
          onContentChange={onContentChange}
          downloadUrl={resolved}
        />
      </div>
    </div>
  );
}

function FileSourceEditor({
  file,
  skillName,
  onFileUpdate,
}: {
  file: SkillFileEntry;
  skillName: string;
  onFileUpdate?: (updates: Partial<SkillFileEntry>) => void;
}) {
  const fileName = file.path.split("/").filter(Boolean).pop() || file.path;
  const source = resolveSource(file, skillName);
  const kind = detectKind(file);

  // For saved text/dataurl files, content lives on the serve endpoint.
  // Fetch it on mount and seed a local editable buffer.
  const [fetchedContent, setFetchedContent] = useState<string | null>(null);
  const [fetchState, setFetchState] = useState<"idle" | "loading" | "error">("idle");
  const needsFetch =
    (file.source_type === "text" ||
      (file.source_type === "dataurl" && kind === "text")) &&
    !file.__local &&
    source.url != null &&
    source.inlineText == null;

  useEffect(() => {
    if (!needsFetch || !source.url) return;
    let cancelled = false;
    setFetchState("loading");
    fetch(source.url)
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.text();
      })
      .then((text) => {
        if (cancelled) return;
        setFetchedContent(text);
        setFetchState("idle");
      })
      .catch(() => {
        if (cancelled) return;
        setFetchState("error");
      });
    return () => { cancelled = true; };
  }, [needsFetch, source.url]);

  if (file.source_type === "upload") {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
        <FileIcon className="text-muted-foreground h-12 w-12" />
        <div className="flex flex-col gap-0.5">
          <p className="max-w-full truncate font-mono text-sm">{fileName}</p>
          <p className="text-muted-foreground text-xs">
            {file.mime_type || "uploaded file"}
            {file.file_size_bytes ? ` · ${formatFileSize(file.file_size_bytes)}` : ""}
          </p>
        </div>
        <p className="text-muted-foreground max-w-md text-xs">
          Uploaded files cannot be edited here. The preview is available on the view page; to change this file, delete it and upload a replacement.
        </p>
      </div>
    );
  }

  if (file.source_type === "url") {
    return (
      <div className="flex h-full flex-col gap-4 p-4">
        <div className="flex flex-col gap-1.5">
          <Label className="text-muted-foreground text-xs">Source URL</Label>
          <Input
            data-testid="skill-file-url-input"
            value={file.source_url ?? ""}
            onChange={(e) => onFileUpdate?.({ source_url: e.target.value })}
            placeholder="https://example.com/file.py"
            className="font-mono text-xs"
          />
        </div>
        <div className="flex items-start gap-2 rounded-sm border border-blue-200 bg-blue-50 px-3 py-2 text-xs text-blue-900 dark:border-blue-900/50 dark:bg-blue-950/30 dark:text-blue-200">
          <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>This source is saved as a live reference. Bifrost reads from this URL when the skill file is retrieved.</span>
        </div>
      </div>
    );
  }

  // text and dataurl: show loading while fetching saved content
  if (needsFetch && fetchState === "loading") {
    return (
      <div className="text-muted-foreground flex h-full items-center justify-center gap-2 text-xs">
        <Loader2 className="h-4 w-4 animate-spin" />
        Loading…
      </div>
    );
  }
  if (needsFetch && fetchState === "error") {
    return (
      <div className="text-muted-foreground flex h-full items-center justify-center gap-2 text-xs">
        Could not load file contents.
      </div>
    );
  }

  if (file.source_type === "dataurl") {
    // Saved binary data URLs are not text-decodable: reading them through
    // res.text() and re-encoding would corrupt the bytes, and an editable
    // textarea would persist that corruption. Show a read-only file card.
    if (!file.dataurl && kind !== "text") {
      return (
        <FallbackBlock fileName={fileName} file={file} downloadUrl={source.url}>
          Binary data URLs can&apos;t be edited as text. Download to inspect, or
          delete and re-upload to replace this file.
        </FallbackBlock>
      );
    }
    // Text data URLs: the fetched content is the decoded text; local files
    // carry file.dataurl directly.
    const currentValue = file.dataurl ?? (fetchedContent != null ? `data:${file.mime_type || "text/plain"};base64,${btoa(unescape(encodeURIComponent(fetchedContent)))}` : "");
    return (
      <div className="flex h-full min-h-0 flex-col gap-2 p-4">
        <Label className="text-muted-foreground text-xs">Data URL</Label>
        <Textarea
          data-testid="skill-file-dataurl-textarea"
          value={currentValue}
          onChange={(e) => onFileUpdate?.({ dataurl: e.target.value, blob_id: undefined, storage_key: undefined })}
          placeholder="data:text/plain;base64,..."
          className="min-h-0 flex-1 resize-none font-mono text-xs"
        />
      </div>
    );
  }

  // text source
  const currentContent = file.content ?? source.inlineText ?? fetchedContent ?? "";
  return (
    <Textarea
      data-testid="skill-file-content-textarea"
      value={currentContent}
      onChange={(e) => onFileUpdate?.({ content: e.target.value, blob_id: undefined, storage_key: undefined })}
      placeholder="File content..."
      className="h-full w-full resize-none rounded-none border-0 font-mono text-xs focus-visible:ring-0"
    />
  );
}

function FallbackBlock({
  fileName,
  file,
  downloadUrl,
  children,
}: {
  fileName: string;
  file: SkillFileEntry;
  downloadUrl?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
      <FileIcon className="text-muted-foreground h-12 w-12" />
      <div className="flex flex-col gap-0.5">
        <p className="max-w-full truncate font-mono text-sm">{fileName}</p>
        <p className="text-muted-foreground text-xs">
          {file.mime_type || "unknown type"}
          {file.file_size_bytes
            ? ` · ${formatFileSize(file.file_size_bytes)}`
            : ""}
        </p>
      </div>
      {children && <p className="text-muted-foreground text-xs">{children}</p>}
      {downloadUrl && (
        <Button variant="outline" size="sm" asChild>
          <a href={downloadUrl} download={fileName}>
            <Download className="h-3.5 w-3.5" />
            Download
          </a>
        </Button>
      )}
    </div>
  );
}
