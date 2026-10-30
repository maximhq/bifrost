import RSSFeedIcon from "@/components/rssFeedIcon";
import { readRSSFeedLogoFile, RSS_FEED_LOGO_TYPES, rssFeedLogoFileError } from "@/components/rssFeedIcon.utils";
import { rssFeedFormErrorField, rssNewsSwitchState } from "@/components/rssSourcesSheet.utils";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import {
	getErrorMessage,
	useCreateRSSFeedMutation,
	useDeleteRSSFeedMutation,
	useGetRSSSettingsQuery,
	useUpdateRSSSettingsMutation,
	useGetRSSFeedsQuery,
	useUpdateRSSFeedMutation,
} from "@/lib/store";
import type { RSSFeed } from "@/lib/types/rssFeeds";
import { rssFeedFormSchema, type RSSFeedFormSchema } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ImagePlus, Loader2, Plus, RefreshCw, Trash2, Upload, X } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

interface RSSSourcesSheetProps {
	open: boolean;
	onClose: () => void;
}

/**
 * Gateway-wide news source configuration, opened from the notification tray.
 * Builtin feeds can only be toggled; custom feeds can be added and removed.
 */
export default function RSSSourcesSheet({ open, onClose }: RSSSourcesSheetProps) {
	const { t } = useTranslation("shell");
	const canEdit = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data, isLoading, isError, refetch } = useGetRSSFeedsQuery(undefined, { skip: !open });
	const { data: settings, isError: isSettingsError, refetch: refetchSettings } = useGetRSSSettingsQuery(undefined, { skip: !open });
	const [updateSettings] = useUpdateRSSSettingsMutation();
	const newsSwitch = rssNewsSwitchState(settings, isSettingsError);

	const setNewsEnabled = async (enabled: boolean) => {
		try {
			await updateSettings({ enabled }).unwrap();
			toast.success(enabled ? t("rssSources.newsOn") : t("rssSources.newsOff"));
		} catch (error) {
			toast.error(t("rssSources.updateSettingsFailed"), { description: getErrorMessage(error) });
		}
	};
	const [updateFeed] = useUpdateRSSFeedMutation();
	const [deleteFeed] = useDeleteRSSFeedMutation();
	// Every in-flight delete, not just the latest call, so a second delete does not re-enable the first.
	const [deletingIds, setDeletingIds] = useState<ReadonlySet<number>>(() => new Set());

	const { builtin, custom } = useMemo(() => {
		const feeds = data?.feeds ?? [];
		return { builtin: feeds.filter((f) => f.source === "builtin"), custom: feeds.filter((f) => f.source === "custom") };
	}, [data]);

	const toggle = async (feed: RSSFeed, enabled: boolean) => {
		try {
			await updateFeed({ id: feed.id, enabled }).unwrap();
		} catch (error) {
			toast.error(t("rssSources.updateFeedFailed", { name: feed.name }), { description: getErrorMessage(error) });
		}
	};

	const setLogo = async (feed: RSSFeed, iconUrl: string) => {
		try {
			await updateFeed({ id: feed.id, icon_url: iconUrl }).unwrap();
			toast.success(iconUrl ? t("rssSources.updatedLogo", { name: feed.name }) : t("rssSources.removedLogo", { name: feed.name }));
		} catch (error) {
			toast.error(t("rssSources.updateLogoFailed", { name: feed.name }), { description: getErrorMessage(error) });
		}
	};

	const remove = async (feed: RSSFeed) => {
		setDeletingIds((ids) => new Set(ids).add(feed.id));
		try {
			await deleteFeed(feed.id).unwrap();
			toast.success(t("rssSources.removed", { name: feed.name }));
		} catch (error) {
			toast.error(t("rssSources.removeFailed", { name: feed.name }), { description: getErrorMessage(error) });
		} finally {
			setDeletingIds((ids) => {
				const next = new Set(ids);
				next.delete(feed.id);
				return next;
			});
		}
	};

	return (
		<Sheet open={open} onOpenChange={(next) => !next && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden px-0" data-testid="rss-sources-sheet">
				<SheetHeader className="flex flex-col items-start pt-8" headerClassName="px-4 md:px-6">
					<SheetTitle>{t("rssSources.title")}</SheetTitle>
					<SheetDescription>{t("rssSources.description")}</SheetDescription>
				</SheetHeader>

				<div className="flex-1 space-y-6 overflow-y-auto px-4 pb-8 md:px-6">
					<section className="flex items-start gap-3 rounded-sm border p-4" data-testid="rss-news-switch">
						<div className="min-w-0 flex-1 space-y-0.5">
							<Label htmlFor="rss-news-enabled" className="text-sm font-semibold">
								{t("rssSources.showNews")}
							</Label>
							<p className="text-muted-foreground text-xs">{t("rssSources.showNewsHelp")}</p>
							{newsSwitch.failed && (
								<p className="text-destructive pt-1 text-xs" data-testid="rss-news-settings-error">
									{t("rssSources.settingLoadFailed")}{" "}
									<button type="button" onClick={() => refetchSettings()} className="cursor-pointer font-medium underline">
										{t("rssSources.tryAgain")}
									</button>
								</p>
							)}
						</div>
						<Switch
							id="rss-news-enabled"
							checked={newsSwitch.checked}
							disabled={!canEdit || !newsSwitch.ready}
							onAsyncCheckedChange={setNewsEnabled}
							data-testid="rss-news-enabled-toggle"
						/>
					</section>

					{isLoading ? (
						<div className="text-muted-foreground flex h-32 items-center justify-center gap-2 text-sm">
							<Loader2 className="size-4 animate-spin" /> {t("rssSources.loading")}
						</div>
					) : isError ? (
						<div className="flex h-32 flex-col items-center justify-center gap-3 text-center">
							<p className="text-muted-foreground text-sm">{t("rssSources.loadFailed")}</p>
							<Button variant="outline" size="sm" onClick={() => refetch()}>
								<RefreshCw className="size-3.5" /> {t("rssSources.tryAgain")}
							</Button>
						</div>
					) : (
						<div className={cn("space-y-6", newsSwitch.ready && !newsSwitch.checked && "opacity-60")}>
							<section className="space-y-2">
								<h3 className="text-sm font-semibold">{t("rssSources.builtinFeeds")}</h3>
								<div className="divide-y rounded-sm border">
									{builtin.map((feed) => (
										<FeedRow key={feed.id} feed={feed} canEdit={canEdit} onToggle={toggle} />
									))}
								</div>
							</section>

							<section className="space-y-2">
								<h3 className="text-sm font-semibold">{t("rssSources.customFeeds")}</h3>
								{custom.length > 0 ? (
									<div className="divide-y rounded-sm border">
										{custom.map((feed) => (
											<FeedRow
												key={feed.id}
												feed={feed}
												canEdit={canEdit}
												onToggle={toggle}
												onDelete={remove}
												onSetLogo={setLogo}
												deleting={deletingIds.has(feed.id)}
											/>
										))}
									</div>
								) : (
									<p className="text-muted-foreground text-sm" data-testid="rss-custom-feeds-empty">
										{t("rssSources.noCustomFeeds")}
									</p>
								)}
							</section>

							{canEdit && <AddFeedForm />}
						</div>
					)}
				</div>
			</SheetContent>
		</Sheet>
	);
}

interface FeedRowProps {
	feed: RSSFeed;
	canEdit: boolean;
	onToggle: (feed: RSSFeed, enabled: boolean) => Promise<void>;
	onDelete?: (feed: RSSFeed) => Promise<void>;
	onSetLogo?: (feed: RSSFeed, iconUrl: string) => Promise<void>;
	deleting?: boolean;
}

function FeedRow({ feed, canEdit, onToggle, onDelete, onSetLogo, deleting }: FeedRowProps) {
	const { t } = useTranslation("shell");
	return (
		<div className="flex items-center gap-3 px-3 py-2.5" data-testid={`rss-feed-row-${feed.slug}`}>
			<RSSFeedIcon icon={feed.icon} iconUrl={feed.icon_url} feedUrl={feed.url} name={feed.name} size={20} />
			<div className="min-w-0 flex-1">
				<p className="truncate text-sm font-medium">{feed.name}</p>
				<p className="text-muted-foreground truncate text-xs" title={feed.url}>
					{feed.url}
				</p>
			</div>
			{onSetLogo && canEdit && <FeedLogoMenu feed={feed} onSetLogo={onSetLogo} />}
			{onDelete && canEdit && (
				<Button
					variant="ghost"
					size="icon"
					className="text-muted-foreground hover:text-destructive size-7"
					aria-label={t("rssSources.removeAria", { name: feed.name })}
					disabled={deleting}
					onClick={() => onDelete(feed)}
					data-testid={`rss-feed-delete-${feed.id}`}
				>
					{deleting ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
				</Button>
			)}
			<Switch
				checked={feed.enabled}
				disabled={!canEdit}
				aria-label={t(feed.enabled ? "rssSources.disableAria" : "rssSources.enableAria", { name: feed.name })}
				onAsyncCheckedChange={(checked) => onToggle(feed, checked)}
				data-testid={`rss-feed-toggle-${feed.slug}`}
			/>
		</div>
	);
}

/** Upload or remove the logo of an existing custom feed. */
function FeedLogoMenu({ feed, onSetLogo }: { feed: RSSFeed; onSetLogo: (feed: RSSFeed, iconUrl: string) => Promise<void> }) {
	const { t } = useTranslation("shell");
	const fileInput = useRef<HTMLInputElement>(null);

	const onFile = async (file: File | undefined) => {
		if (!file) return;
		const error = rssFeedLogoFileError(file);
		if (error) {
			toast.error(error);
			return;
		}
		try {
			await onSetLogo(feed, await readRSSFeedLogoFile(file));
		} catch (readError) {
			toast.error(getErrorMessage(readError));
		}
	};

	return (
		<>
			<input
				ref={fileInput}
				type="file"
				accept={RSS_FEED_LOGO_TYPES.join(",")}
				className="hidden"
				data-testid={`rss-feed-logo-file-${feed.id}`}
				onChange={(event) => {
					void onFile(event.target.files?.[0]);
					event.target.value = "";
				}}
			/>
			<DropdownMenu>
				<DropdownMenuTrigger asChild>
					<Button
						variant="ghost"
						size="icon"
						className="text-muted-foreground size-7"
						aria-label={t("rssSources.changeLogoAria", { name: feed.name })}
						data-testid={`rss-feed-logo-${feed.id}`}
					>
						<ImagePlus className="size-3.5" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end">
					<DropdownMenuItem onSelect={() => fileInput.current?.click()} data-testid={`rss-feed-logo-upload-${feed.id}`}>
						<Upload className="size-3.5" /> {t("rssSources.uploadLogo")}
					</DropdownMenuItem>
					{feed.icon_url && (
						<DropdownMenuItem onSelect={() => void onSetLogo(feed, "")} data-testid={`rss-feed-logo-remove-${feed.id}`}>
							<X className="size-3.5" /> {t("rssSources.removeLogo")}
						</DropdownMenuItem>
					)}
				</DropdownMenuContent>
			</DropdownMenu>
		</>
	);
}

function AddFeedForm() {
	const { t } = useTranslation("shell");
	const [createFeed, { isLoading }] = useCreateRSSFeedMutation();
	const fileInput = useRef<HTMLInputElement>(null);
	const {
		register,
		handleSubmit,
		reset,
		setError,
		setValue,
		clearErrors,
		watch,
		formState: { errors },
	} = useForm<RSSFeedFormSchema>({
		resolver: zodResolver(rssFeedFormSchema),
		defaultValues: { name: "", url: "", icon_url: "" },
	});
	const [feedUrl, iconUrl] = watch(["url", "icon_url"]);
	const isUploadedLogo = iconUrl.startsWith("data:");

	const onLogoFile = async (file: File | undefined) => {
		if (!file) return;
		const error = rssFeedLogoFileError(file);
		if (error) {
			setError("icon_url", { message: error });
			return;
		}
		try {
			setValue("icon_url", await readRSSFeedLogoFile(file), { shouldDirty: true });
			clearErrors("icon_url");
		} catch (readError) {
			setError("icon_url", { message: getErrorMessage(readError) });
		}
	};

	const onSubmit = async (values: RSSFeedFormSchema) => {
		try {
			const feed = await createFeed({ name: values.name, url: values.url, icon_url: values.icon_url || undefined }).unwrap();
			toast.success(t("rssSources.addedFeed", { name: feed.name }));
			reset();
		} catch (error) {
			const message = getErrorMessage(error);
			setError(rssFeedFormErrorField(message), { message });
		}
	};

	return (
		<form onSubmit={handleSubmit(onSubmit)} className="space-y-3 rounded-sm border p-4" data-testid="rss-add-feed-form">
			<h3 className="text-sm font-semibold">{t("rssSources.addFeedTitle")}</h3>
			<div className="space-y-1.5">
				<Label htmlFor="rss-add-feed-name">{t("rssSources.name")}</Label>
				<Input
					id="rss-add-feed-name"
					placeholder={t("rssSources.namePlaceholder")}
					data-testid="rss-add-feed-name"
					aria-invalid={!!errors.name}
					className={errors.name ? "border-destructive" : ""}
					{...register("name")}
				/>
				{errors.name && (
					<p className="text-destructive text-xs" data-testid="rss-add-feed-name-error">
						{errors.name.message}
					</p>
				)}
			</div>
			<div className="space-y-1.5">
				<Label htmlFor="rss-add-feed-url">{t("rssSources.feedUrl")}</Label>
				<Input
					id="rss-add-feed-url"
					placeholder="https://github.com/maximhq/bifrost/releases.atom"
					data-testid="rss-add-feed-url"
					aria-invalid={!!errors.url}
					className={errors.url ? "border-destructive" : ""}
					{...register("url")}
				/>
				{errors.url && (
					<p className="text-destructive text-xs" data-testid="rss-add-feed-error">
						{errors.url.message}
					</p>
				)}
			</div>
			<div className="space-y-1.5">
				<Label htmlFor="rss-add-feed-logo">
					{t("rssSources.logo")} <span className="text-muted-foreground font-normal">{t("rssSources.optional")}</span>
				</Label>
				<div className="flex items-center gap-2">
					<span className="flex size-9 shrink-0 items-center justify-center rounded-md border" data-testid="rss-add-feed-logo-preview">
						<RSSFeedIcon iconUrl={iconUrl} feedUrl={feedUrl} name={t("rssSources.logoPreviewName")} size={20} />
					</span>
					{isUploadedLogo ? (
						<div className="text-muted-foreground flex h-9 min-w-0 flex-1 items-center rounded-md border px-3 text-sm">
							{t("rssSources.uploadedImage")}
						</div>
					) : (
						<Input
							id="rss-add-feed-logo"
							placeholder="https://example.com/logo.png"
							data-testid="rss-add-feed-logo-url"
							aria-invalid={!!errors.icon_url}
							className={errors.icon_url ? "border-destructive" : ""}
							{...register("icon_url")}
						/>
					)}
					<input
						ref={fileInput}
						type="file"
						accept={RSS_FEED_LOGO_TYPES.join(",")}
						className="hidden"
						data-testid="rss-add-feed-logo-file"
						onChange={(event) => {
							void onLogoFile(event.target.files?.[0]);
							event.target.value = "";
						}}
					/>
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => fileInput.current?.click()}
						data-testid="rss-add-feed-logo-upload"
					>
						<Upload className="size-3.5" /> {t("rssSources.upload")}
					</Button>
					{iconUrl && (
						<Button
							type="button"
							variant="ghost"
							size="icon"
							className="text-muted-foreground size-8"
							aria-label={t("rssSources.removeLogo")}
							onClick={() => setValue("icon_url", "", { shouldDirty: true })}
							data-testid="rss-add-feed-logo-clear"
						>
							<X className="size-3.5" />
						</Button>
					)}
				</div>
				{errors.icon_url ? (
					<p className="text-destructive text-xs" data-testid="rss-add-feed-logo-error">
						{errors.icon_url.message}
					</p>
				) : (
					<p className="text-muted-foreground text-xs">{t("rssSources.logoHelp")}</p>
				)}
			</div>
			<div className="flex justify-end">
				<Button type="submit" size="sm" disabled={isLoading} data-testid="rss-add-feed-submit">
					{isLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
					{t("rssSources.addFeed")}
				</Button>
			</div>
		</form>
	);
}