import { Badge } from "@/components/ui/badge";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hoverCard";
import { useDescriptionSlot, useSetTopbarTitle } from "@/lib/contexts/topbarContext";
import type { Breadcrumb } from "@/lib/contexts/topbarContext.utils";
import { cn } from "@/lib/utils";
import { Info } from "lucide-react";
import { createPortal } from "react-dom";

/** Colour family of a topbar tag. Tones are semantic so pages never pass raw classes. */
export type PageTagTone = "neutral" | "info" | "success" | "warning" | "danger";

/**
 * A status pill shown beside the page title in the topbar, e.g. a live
 * "Cluster is updating" or "Healthy" marker. Data rather than JSX, so the
 * topbar owns the styling and every page's tags look the same.
 */
export interface PageTag {
	label: string;
	tone?: PageTagTone;
	/** Pulses the leading dot, for state that is changing right now. */
	pulse?: boolean;
	/** Native tooltip with the longer explanation. */
	title?: string;
	"data-testid"?: string;
}

const tagToneClass: Record<PageTagTone, { badge: string; dot: string }> = {
	neutral: { badge: "border-border bg-muted/50 text-muted-foreground", dot: "bg-gray-400" },
	info: {
		badge: "border-blue-200 bg-blue-50/50 text-blue-700 dark:border-blue-800 dark:bg-blue-950/30 dark:text-blue-400",
		dot: "bg-blue-500",
	},
	success: {
		badge: "border-emerald-200 bg-emerald-50/50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-400",
		dot: "bg-emerald-500",
	},
	warning: {
		badge: "border-amber-200 bg-amber-50/50 text-amber-700 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-400",
		dot: "bg-amber-500",
	},
	danger: {
		badge: "border-red-200 bg-red-50/50 text-red-700 dark:border-red-800 dark:bg-red-950/30 dark:text-red-400",
		dot: "bg-red-500",
	},
};

/** PageTagBadge renders one PageTag; exported so a page can preview the same pill inline. */
export function PageTagBadge({ tag }: { tag: PageTag }) {
	const tone = tagToneClass[tag.tone ?? "neutral"];
	return (
		<Badge variant="outline" className={cn("shrink-0", tone.badge)} title={tag.title} data-testid={tag["data-testid"]}>
			<span className={cn("h-1.5 w-1.5 rounded-full", tone.dot, tag.pulse && "animate-pulse")} />
			{tag.label}
		</Badge>
	);
}

function withTestIdSuffix(tag: PageTag, suffix: string): PageTag {
	return tag["data-testid"] ? { ...tag, "data-testid": `${tag["data-testid"]}${suffix}` } : tag;
}

/**
 * Declares the page's title and, optionally, its description. Renders nothing
 * inline on desktop — the title is hoisted into the <Topbar>, and the
 * description becomes an info icon beside it that opens on hover. Below md the
 * topbar slot is hidden, so `tags` alone also render inline where this sits.
 *
 * Drop it where the old heading lived so the page still reads as its own
 * source of truth:
 *
 *   <PageTitle title="Webhooks">
 *     Register endpoints to receive signed notifications. Pass the endpoint&apos;s
 *     name in the <code>x-bf-async-webhook</code> header.
 *   </PageTitle>
 *
 * The description is portalled rather than lifted through context state, so it
 * can be arbitrary JSX — links, <code>, conditional spans — without its
 * unstable identity re-rendering the whole shell on every parent render.
 *
 * `title` is only needed when the route slug wouldn't produce the right label
 * (tab views, "&"-joined names). Without it the topbar falls back to the last
 * path segment.
 *
 * `beta` marks the page as in beta, rendering a badge beside the title:
 *
 *   <PageTitle title="Alert Rules" beta>Create rules that…</PageTitle>
 *
 * `breadcrumbs` replaces the title with a trail for a page nested under
 * another. Ancestor crumbs are muted and clickable; the last one is the
 * current page and is never a link:
 *
 *   <PageTitle breadcrumbs={[{ label: "Webhooks", to: "/workspace/webhooks" }, { label: "Deliveries" }]} />
 *
 * It takes precedence over `title` when both are given.
 *
 * `tags` adds status pills after the title, for live page state rather than a
 * page name. Pass an empty array (not a conditional element) when there is
 * nothing to show:
 *
 *   <PageTitle title="Cluster Overview" tags={[{ label: "Healthy", tone: "success" }]} />
 */
export default function PageTitle({
	title,
	beta,
	breadcrumbs,
	tags,
	children,
}: {
	title?: string;
	beta?: boolean;
	breadcrumbs?: Breadcrumb[];
	tags?: PageTag[];
	children?: React.ReactNode;
}) {
	useSetTopbarTitle(breadcrumbs?.length ? breadcrumbs : title);
	const slot = useDescriptionSlot();
	// The badge's label needs a name; with a trail that is the current page.
	const label = title ?? breadcrumbs?.at(-1)?.label;

	// The topbar slot is hidden below md, so live status tags also render
	// inline, mobile only; each breakpoint shows exactly one copy.
	const mobileTags = tags?.length ? (
		<div className="mb-3 flex flex-wrap items-center gap-2 md:hidden" data-testid="page-tags-mobile">
			{tags.map((tag) => (
				<PageTagBadge key={tag.label} tag={withTestIdSuffix(tag, "-mobile")} />
			))}
		</div>
	) : null;

	if (!slot || (!children && !beta && !tags?.length)) return mobileTags;

	return (
		<>
			{mobileTags}
			{createPortal(
				<>
					{beta && (
						<Badge
							className="shrink-0 dark:border-green-950 dark:bg-green-900"
							aria-label={label ? `${label} is in beta` : "This page is in beta"}
						>
							Beta
						</Badge>
					)}
					{tags?.map((tag) => (
						<PageTagBadge key={tag.label} tag={tag} />
					))}
					{children && (
						<HoverCard openDelay={100} closeDelay={100}>
							<HoverCardTrigger asChild>
								<button
									type="button"
									data-testid="page-description-trigger"
									aria-label="About this page"
									className="text-muted-foreground hover:text-foreground flex size-5 shrink-0 cursor-help items-center justify-center rounded-sm transition-colors"
								>
									<Info className="size-4" strokeWidth={2} />
								</button>
							</HoverCardTrigger>
							<HoverCardContent
								align="start"
								side="bottom"
								className="text-muted-foreground w-80 rounded-sm text-sm leading-relaxed font-normal shadow-none"
							>
								{children}
							</HoverCardContent>
						</HoverCard>
					)}
				</>,
				slot,
			)}
		</>
	);
}