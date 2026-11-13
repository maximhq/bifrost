import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useUploadEvalDatasetFileMutation } from "@/lib/store";
import { getTokenFromStorage } from "@/lib/store/apis/baseApi";
import type { DatasetFile } from "@/lib/types/evalDatasets";
import { getApiBaseUrl } from "@/lib/utils/port";
import { type CustomCell, type CustomRenderer, drawTextCell, GridCellKind, type ProvideEditorComponent } from "@maximai/glide-data-grid";
import { FileIcon, Plus, Upload, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { fileCellText, isImageFile, parseFileCell } from "./datasetUtils";

// A cell of a file column: a JSON list of files, uploaded or given by URL.
export interface FileCellData {
	readonly kind: "file-cell";
	readonly text: string;
	// The dataset the cell belongs to, which uploads go to.
	readonly datasetId: string;
}

export type FileCell = CustomCell<FileCellData>;

export function fileCell(text: string, datasetId: string, readonly: boolean): FileCell {
	return { kind: GridCellKind.Custom, data: { kind: "file-cell", text, datasetId }, copyData: text, allowOverlay: true, readonly };
}

const fileName = (file: DatasetFile) => file.name || file.url || file.id || "";

// useFileSource gives the address an image can be shown from. A file somewhere else
// is shown from its own URL. An uploaded file is behind the gateway's sign-in, which
// an <img> tag cannot pass, so it is read with the session and handed over as a blob.
function useFileSource(file: DatasetFile): string | null {
	const uploaded = Boolean(file.id && file.url);
	const [source, setSource] = useState<string | null>(uploaded ? null : (file.url ?? null));
	useEffect(() => {
		if (!uploaded || !file.url) return;
		let stale = false;
		let objectUrl = "";
		void (async () => {
			try {
				const token = await getTokenFromStorage();
				const response = await fetch(getApiBaseUrl() + file.url!.replace(/^\/api/, ""), {
					credentials: "include",
					headers: token ? { Authorization: `Bearer ${token}` } : undefined,
				});
				if (!response.ok || stale) return;
				objectUrl = URL.createObjectURL(await response.blob());
				if (stale) URL.revokeObjectURL(objectUrl);
				else setSource(objectUrl);
			} catch {
				// The name is still shown; only the preview is missing.
			}
		})();
		return () => {
			stale = true;
			if (objectUrl) URL.revokeObjectURL(objectUrl);
		};
	}, [uploaded, file.url]);
	return source;
}

function FileRow({ file, readonly, onRemove }: { file: DatasetFile; readonly: boolean; onRemove: () => void }) {
	const { t } = useTranslation("config");
	const source = useFileSource(file);
	const name = fileName(file);
	return (
		<li className="flex items-center gap-2 border-b px-2 py-1.5 last:border-b-0" data-testid="dataset-file-row">
			{isImageFile(file) && source ? (
				<img src={source} alt="" className="bg-muted h-10 w-10 shrink-0 rounded-sm object-cover" />
			) : (
				<span className="bg-muted text-muted-foreground flex h-10 w-10 shrink-0 items-center justify-center rounded-sm">
					<FileIcon className="h-4 w-4" />
				</span>
			)}
			<span className="min-w-0 flex-1 truncate text-sm" title={name}>
				{name}
			</span>
			{!readonly && (
				<Button variant="ghost" size="icon" className="size-7" onClick={onRemove} aria-label={t("evals.datasets.files.remove", { name })}>
					<X className="h-4 w-4" />
				</Button>
			)}
		</li>
	);
}

// The editor lists the cell's files with a preview for images, and takes more: by
// upload, or by URL. Every change goes straight into the cell's value.
const FileCellEditor: ProvideEditorComponent<FileCell> = ({ value, onChange }) => {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [upload, { isLoading }] = useUploadEvalDatasetFileMutation();
	const [files, setFiles] = useState<DatasetFile[]>(() => parseFileCell(value.data.text));
	const [link, setLink] = useState("");
	const picker = useRef<HTMLInputElement>(null);
	const readonly = value.readonly === true;

	const commit = (next: DatasetFile[]) => {
		setFiles(next);
		const text = fileCellText(next);
		onChange({ ...value, data: { ...value.data, text }, copyData: text });
	};

	const add = async (picked: FileList | null) => {
		if (!picked || picked.length === 0) return;
		let next = files;
		for (const file of Array.from(picked)) {
			try {
				const uploaded = await upload({ id: value.data.datasetId, file }).unwrap();
				next = [...next, uploaded.file];
				commit(next);
			} catch (error) {
				toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
			}
		}
		if (picker.current) picker.current.value = "";
	};

	const addLink = () => {
		const url = link.trim();
		if (!/^https?:\/\//i.test(url)) return;
		const name = url.split(/[?#]/)[0].split("/").filter(Boolean).pop() ?? url;
		commit([...files, { url, name }]);
		setLink("");
	};

	return (
		<div className="bg-card flex w-[26rem] flex-col gap-2 p-2" data-testid="dataset-file-cell-editor">
			{files.length === 0 ? (
				<p className="text-muted-foreground px-1 text-sm">{t("evals.datasets.files.empty")}</p>
			) : (
				<ul className="max-h-64 overflow-y-auto rounded-sm border">
					{files.map((file, index) => (
						<FileRow
							key={`${file.id ?? file.url}-${index}`}
							file={file}
							readonly={readonly}
							onRemove={() => commit(files.filter((_, at) => at !== index))}
						/>
					))}
				</ul>
			)}
			{!readonly && (
				<>
					<input
						ref={picker}
						type="file"
						multiple
						className="hidden"
						onChange={(event) => void add(event.target.files)}
						data-testid="dataset-file-input"
					/>
					<div className="flex items-center gap-2">
						<Button variant="outline" disabled={isLoading} onClick={() => picker.current?.click()} data-testid="dataset-file-upload-btn">
							<Upload className="h-4 w-4" />
							{isLoading ? t("evals.datasets.files.uploading") : t("evals.datasets.files.upload")}
						</Button>
						<Input
							className="h-7.5 min-w-0 flex-1"
							value={link}
							placeholder="https://"
							aria-label={t("evals.datasets.files.addUrl")}
							onChange={(event) => setLink(event.target.value)}
							// Enter belongs to this field while it has the focus: it adds the URL
							// and must not reach the grid, which would close the editor.
							onKeyDown={(event) => {
								if (event.key !== "Enter") return;
								event.preventDefault();
								event.stopPropagation();
								addLink();
							}}
							data-testid="dataset-file-url-input"
						/>
						<Button variant="outline" size="icon" className="size-7.5" onClick={addLink} aria-label={t("evals.datasets.files.addUrl")}>
							<Plus className="h-4 w-4" />
						</Button>
					</div>
				</>
			)}
		</div>
	);
};

// fileCellRenderer draws a file cell as the names of its files, and edits it in a
// panel with previews, upload and remove.
export const fileCellRenderer: CustomRenderer<FileCell> = {
	kind: GridCellKind.Custom,
	isMatch: (cell): cell is FileCell => (cell.data as { kind?: string }).kind === "file-cell",
	draw: (args, cell) => {
		drawTextCell(args, parseFileCell(cell.data.text).map(fileName).join(", "), cell.contentAlign);
		return true;
	},
	provideEditor: () => ({ editor: FileCellEditor, disablePadding: true }),
	// A pasted list of URLs is taken as it is; the server turns it into files.
	onPaste: (value, data) => ({ ...data, text: value }),
	onDelete: (cell) => ({ ...cell, data: { ...cell.data, text: "" }, copyData: "" }),
};