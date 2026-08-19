import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hoverCard";
import { useDescriptionSlot, useSetTopbarTitle } from "@/lib/contexts/topbarContext";
import { Info } from "lucide-react";
import { createPortal } from "react-dom";

/**
 * Declares the page's title and, optionally, its description. Renders nothing
 * inline — the title is hoisted into the <Topbar>, and the description becomes
 * an info icon beside it that opens on hover.
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
 */
export default function PageTitle({ title, children }: { title?: string; children?: React.ReactNode }) {
	useSetTopbarTitle(title);
	const slot = useDescriptionSlot();

	if (!children || !slot) return null;

	return createPortal(
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
		</HoverCard>,
		slot,
	);
}