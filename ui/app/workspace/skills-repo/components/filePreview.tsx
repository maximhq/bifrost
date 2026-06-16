"use client";

import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Textarea } from "@/components/ui/textarea";
import { SkillFileEntry } from "@/lib/types/skills";
import { getApiBaseUrl } from "@/lib/utils/port";
import { cn } from "@/lib/utils";
import { Download, File as FileIcon, Loader2, Save } from "lucide-react";
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

function isTextLikeMime(mime: string, sourceType: SkillFileEntry["source_type"]) {
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
 */
function resolveSource(
  file: SkillFileEntry,
  skillName: string,
): { url?: string; inlineText?: string; unavailable?: boolean } {
  switch (file.source_type) {
    case "text":
      return { inlineText: file.content ?? "" };
    case "dataurl":
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
  editValue,
  onEditChange,
  downloadUrl,
}: {
  file: SkillFileEntry;
  skillName: string;
  mode: "view" | "edit";
  // Editing only applies to locally-held text (source_type "text").
  onContentChange?: (content: string) => void;
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

  // The only inline-editable case is locally-held text content.
  const canEditText = mode === "edit" && file.source_type === "text";

  // Text fetched from a URL/serve endpoint (view-only for non-local text).
  const [fetchedText, setFetchedText] = useState<string | null>(null);
  const [fetchState, setFetchState] = useState<"idle" | "loading" | "error">("idle");

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
      <FallbackBlock fileName={fileName} file={file} downloadUrl={resolvedDownloadUrl}>
        Preview available after saving.
      </FallbackBlock>
    );
  }

  // ---- Image ----
  if (kind === "image" && source.url) {
    return (
      <div className="bg-muted/20 flex h-full items-center justify-center overflow-auto p-4">
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src={source.url} alt={fileName} className="max-h-full max-w-full object-contain" />
      </div>
    );
  }

  // ---- Audio ----
  if (kind === "audio" && source.url) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-4 p-6">
        <FileIcon className="text-muted-foreground h-10 w-10" />
        <span className="text-muted-foreground max-w-full truncate font-mono text-xs">{fileName}</span>
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
    const inline = source.inlineText;
    if (canEditText) {
      const value = editValue ?? inline ?? "";
      const handleChange = onEditChange ?? onContentChange;
      return (
        <Textarea
          value={value}
          onChange={(e) => handleChange?.(e.target.value)}
          placeholder="File content..."
          className="h-full w-full resize-none rounded-none border-0 font-mono text-xs focus-visible:ring-0"
          aria-label={`Edit ${fileName}`}
        />
      );
    }

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
        <FallbackBlock fileName={fileName} file={file} downloadUrl={resolvedDownloadUrl}>
          Could not load file contents.
        </FallbackBlock>
      );
    }

    const text = inline ?? fetchedText ?? "";
    return (
      <ScrollArea className="h-full">
        <pre className="p-4 font-mono text-xs leading-5 whitespace-pre-wrap">{text || "(empty)"}</pre>
      </ScrollArea>
    );
  }

  // ---- Fallback (binary / unknown) ----
  return <FallbackBlock fileName={fileName} file={file} downloadUrl={resolvedDownloadUrl} />;
}

// ---------- FilePreviewPane ----------
// Full-height bordered box: a header (file path + optional download) over the preview.

export function FilePreviewPane({
  file,
  skillName,
  mode,
  onContentChange,
  downloadUrl,
  registerFlush,
}: {
  file: SkillFileEntry;
  skillName: string;
  mode: "view" | "edit";
  onContentChange?: (content: string) => void;
  downloadUrl?: string;
  // Lets the parent commit a pending buffer (e.g. before a version save).
  registerFlush?: (flush: (() => void) | null) => void;
}) {
  const resolved = downloadUrl ?? resolveSource(file, skillName).url;
  const fileName = file.path.split("/").filter(Boolean).pop() || file.path;

  // Editable text is buffered so "Save file" is an explicit, version-free commit.
  const isEditableText = mode === "edit" && file.source_type === "text";
  const [draft, setDraft] = useState(file.content ?? "");
  const committedRef = useRef(file.content ?? "");
  const draftRef = useRef(draft);
  draftRef.current = draft;

  const saveFile = useCallback(() => {
    if (draftRef.current !== committedRef.current) {
      committedRef.current = draftRef.current;
      onContentChange?.(draftRef.current);
    }
  }, [onContentChange]);

  // Flush on unmount (the pane is keyed by path, so switching files commits)
  // and expose the flush to the parent for save-time commits.
  useEffect(() => {
    if (!isEditableText) return;
    registerFlush?.(saveFile);
    return () => {
      saveFile();
      registerFlush?.(null);
    };
  }, [isEditableText, saveFile, registerFlush]);

  const dirty = isEditableText && draft !== committedRef.current;

  return (
    <div className="flex min-h-[320px] min-h-0 flex-1 flex-col overflow-hidden rounded-sm border">
      <div className="bg-muted/30 flex h-9 shrink-0 items-center justify-between gap-2 border-b px-3">
        <span className="flex min-w-0 items-center gap-1.5 truncate font-mono text-xs" title={file.path}>
          {file.path}
          {dirty && <span className="bg-primary inline-block size-1.5 shrink-0 rounded-full" aria-label="Unsaved changes" />}
        </span>
        <div className="flex shrink-0 items-center gap-1">
          {isEditableText && (
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
            <Button variant="ghost" size="sm" className="text-muted-foreground hover:text-foreground h-7 px-2" asChild>
              <a href={resolved} download={fileName} aria-label={`Download ${fileName}`}>
                <Download className="h-3.5 w-3.5" />
              </a>
            </Button>
          )}
        </div>
      </div>
      <div className="min-h-0 flex-1">
        <FilePreview
          file={file}
          skillName={skillName}
          mode={mode}
          editValue={isEditableText ? draft : undefined}
          onEditChange={isEditableText ? setDraft : undefined}
          onContentChange={onContentChange}
          downloadUrl={resolved}
        />
      </div>
    </div>
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
    <div className={cn("flex h-full flex-col items-center justify-center gap-3 p-6 text-center")}>
      <FileIcon className="text-muted-foreground h-12 w-12" />
      <div className="space-y-0.5">
        <p className="max-w-full truncate font-mono text-sm">{fileName}</p>
        <p className="text-muted-foreground text-xs">
          {file.mime_type || "unknown type"}
          {file.file_size_bytes ? ` · ${formatFileSize(file.file_size_bytes)}` : ""}
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
