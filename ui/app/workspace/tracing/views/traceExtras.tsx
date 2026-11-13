import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import {
	getErrorMessage,
	useAddEvalDatasetRowsFromSpansMutation,
	useAddTraceCommentMutation,
	useDeleteTraceCommentMutation,
	useDeleteTraceScoreMutation,
	useGetEvalDatasetsQuery,
	useSaveTraceScoreMutation,
} from "@/lib/store";
import type { TraceDetail, TraceSpan } from "@/lib/types/traces";
import i18n from "i18next";
import { Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

/** Copies one span into an evals dataset as a row: its input, output, history and expected value. */
function AddToDataset({ span }: { span: TraceSpan }) {
	const { t } = useTranslation("observability");
	const { data } = useGetEvalDatasetsQuery({ limit: 100 });
	const [addRows, { isLoading }] = useAddEvalDatasetRowsFromSpansMutation();
	const [datasetId, setDatasetId] = useState("");
	const datasets = data?.datasets ?? [];

	const submit = async () => {
		try {
			const result = await addRows({
				id: datasetId,
				spans: [{ project_id: span.project_id, trace_id: span.trace_id, span_id: span.span_id }],
			}).unwrap();
			if (result.rows_added > 0) toast.success(t("tracing.evals.addedToDataset", { count: result.rows_added }));
			else toast.error(t("tracing.evals.skippedFromDataset", { reason: result.skipped?.[0]?.reason ?? "" }));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	if (datasets.length === 0) return <p className="text-muted-foreground text-xs">{t("tracing.evals.noDatasets")}</p>;
	return (
		<div className="flex items-center gap-2" data-testid="trace-add-to-dataset">
			<Select value={datasetId} onValueChange={setDatasetId}>
				<SelectTrigger className="w-64" data-testid="trace-add-to-dataset-select">
					<SelectValue placeholder={t("tracing.evals.chooseDataset")} />
				</SelectTrigger>
				<SelectContent>
					{datasets.map((dataset) => (
						<SelectItem key={dataset.id} value={dataset.id}>
							{dataset.name}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<Button
				variant="outline"
				data-testid="trace-add-to-dataset-button"
				disabled={!datasetId || isLoading || span.content_hidden}
				onClick={submit}
			>
				{t("tracing.evals.addToDataset")}
			</Button>
		</div>
	);
}

/** Scores on one span: what SDKs and evaluators sent, and what reviewers added. A reviewer can add and remove their own. */
function ReviewTab({ detail, span }: { detail: TraceDetail; span: TraceSpan }) {
	const { t } = useTranslation("observability");
	const [saveScore, { isLoading }] = useSaveTraceScoreMutation();
	const [deleteScore] = useDeleteTraceScoreMutation();
	const [name, setName] = useState("");
	const [value, setValue] = useState("");
	const [note, setNote] = useState("");
	const isRoot = span.span_id === detail.trace.root_span_id;
	const scores = (detail.scores ?? []).filter((score) => score.span_id === span.span_id || (isRoot && !score.span_id));
	const sourceLabel = {
		sdk: t("tracing.review.sourceSdk"),
		evaluator: t("tracing.review.sourceEvaluator"),
		human: t("tracing.review.sourceHuman"),
	};

	const submit = async () => {
		const trimmed = value.trim();
		const numeric = trimmed !== "" && !Number.isNaN(Number(trimmed));
		try {
			await saveScore({
				projectId: span.project_id,
				traceId: span.trace_id,
				data: {
					span_id: span.span_id,
					name: name.trim(),
					comment: note.trim() || undefined,
					...(numeric ? { value: Number(trimmed) } : { string_value: trimmed }),
				},
			}).unwrap();
			setName("");
			setValue("");
			setNote("");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="space-y-4" data-testid="trace-review-tab">
			<AddToDataset span={span} />
			{scores.length === 0 && <p className="text-muted-foreground text-sm">{t("tracing.review.noScores")}</p>}
			<div className="divide-y rounded-md border empty:hidden">
				{scores.map((score) => (
					<div key={score.id} className="flex items-center gap-2 px-3 py-2 text-sm" data-testid={`trace-review-score-${score.id}`}>
						<span className="font-medium">{score.name}</span>
						<span className="tabular-nums">{score.value !== undefined && score.value !== null ? score.value : score.string_value}</span>
						<Badge variant="outline">{sourceLabel[score.source] ?? score.source}</Badge>
						{score.author && <span className="text-muted-foreground text-xs">{t("tracing.review.by", { author: score.author })}</span>}
						{score.comment && <span className="text-muted-foreground truncate text-xs">{score.comment}</span>}
						{score.source === "human" && (
							<Button
								variant="ghost"
								size="icon"
								className="ml-auto"
								aria-label={t("tracing.review.deleteScore")}
								data-testid={`trace-review-score-delete-button-${score.id}`}
								onClick={() =>
									deleteScore({ projectId: span.project_id, traceId: span.trace_id, id: score.id })
										.unwrap()
										.catch((error) => toast.error(getErrorMessage(error)))
								}
							>
								<Trash2 className="h-4 w-4" />
							</Button>
						)}
					</div>
				))}
			</div>
			<div className="grid gap-2 md:grid-cols-[1fr_1fr_2fr_auto]">
				<Input
					data-testid="trace-review-score-name-input"
					placeholder={t("tracing.review.scoreName")}
					value={name}
					onChange={(event) => setName(event.target.value)}
				/>
				<Input
					data-testid="trace-review-score-value-input"
					placeholder={t("tracing.review.scoreValue")}
					value={value}
					onChange={(event) => setValue(event.target.value)}
				/>
				<Input
					data-testid="trace-review-score-note-input"
					placeholder={t("tracing.review.note")}
					value={note}
					onChange={(event) => setNote(event.target.value)}
				/>
				<Button data-testid="trace-review-score-add-button" disabled={isLoading || !name.trim() || !value.trim()} onClick={submit}>
					{t("tracing.review.addScore")}
				</Button>
			</div>
		</div>
	);
}

/** Comments on one span, oldest first. */
function ActivityTab({ detail, span }: { detail: TraceDetail; span: TraceSpan }) {
	const { t } = useTranslation("observability");
	const [addComment, { isLoading }] = useAddTraceCommentMutation();
	const [deleteComment] = useDeleteTraceCommentMutation();
	const [text, setText] = useState("");
	const isRoot = span.span_id === detail.trace.root_span_id;
	const comments = (detail.comments ?? []).filter((comment) => comment.span_id === span.span_id || (isRoot && !comment.span_id));

	const submit = async () => {
		try {
			await addComment({ projectId: span.project_id, traceId: span.trace_id, data: { span_id: span.span_id, text: text.trim() } }).unwrap();
			setText("");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="space-y-3" data-testid="trace-activity-tab">
			{comments.length === 0 && <p className="text-muted-foreground text-sm">{t("tracing.review.noComments")}</p>}
			{comments.map((comment) => (
				<div key={comment.id} className="rounded-md border p-3 text-sm" data-testid={`trace-activity-comment-${comment.id}`}>
					<div className="text-muted-foreground mb-1 flex items-center gap-2 text-xs">
						<span>{comment.author || (comment.source === "sdk" ? t("tracing.review.sourceSdk") : t("tracing.review.sourceHuman"))}</span>
						<span>{new Date(comment.created_at).toLocaleString()}</span>
						{comment.source === "human" && (
							<Button
								variant="ghost"
								size="icon"
								className="ml-auto h-6 w-6"
								aria-label={t("tracing.review.deleteComment")}
								data-testid={`trace-activity-comment-delete-button-${comment.id}`}
								onClick={() =>
									deleteComment({ projectId: span.project_id, traceId: span.trace_id, id: comment.id })
										.unwrap()
										.catch((error) => toast.error(getErrorMessage(error)))
								}
							>
								<Trash2 className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
					<div className="break-words whitespace-pre-wrap">{comment.text}</div>
				</div>
			))}
			<Textarea
				data-testid="trace-activity-comment-input"
				placeholder={t("tracing.review.commentPlaceholder")}
				value={text}
				onChange={(event) => setText(event.target.value)}
			/>
			<Button data-testid="trace-activity-comment-add-button" disabled={isLoading || !text.trim()} onClick={submit}>
				{t("tracing.review.addComment")}
			</Button>
		</div>
	);
}

/** Extra tabs of the span detail pane: Review (scores) and Activity (comments). */
export function renderTraceSpanExtras(
	detail: TraceDetail,
	span: TraceSpan,
): { tabs: { id: string; label: string; content: React.ReactNode }[] } {
	return {
		tabs: [
			{ id: "review", label: i18n.t("tracing.review.tab", { ns: "observability" }), content: <ReviewTab detail={detail} span={span} /> },
			{
				id: "activity",
				label: i18n.t("tracing.review.activityTab", { ns: "observability" }),
				content: <ActivityTab detail={detail} span={span} />,
			},
		],
	};
}