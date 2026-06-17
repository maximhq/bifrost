"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useListSkillVersionsQuery } from "@/lib/store/apis/skillsApi";
import { SkillVersionSummary } from "@/lib/types/skills";
import { ChevronDown, Loader2, Search } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { formatDate, useDebouncedValue } from "../components/helpers";

const PAGE_SIZE = 20;

export function SkillVersionsPopover({
	skillId,
	servingVersion,
	onSelectVersion,
}: {
	skillId: string;
	servingVersion: string;
	onSelectVersion: (version: SkillVersionSummary) => void;
}) {
	const [open, setOpen] = useState(false);
	const [search, setSearch] = useState("");
	const debouncedSearch = useDebouncedValue(search, 300);
	const [offset, setOffset] = useState(0);
	const [accumulated, setAccumulated] = useState<SkillVersionSummary[]>([]);

	const { data, isFetching, isError } = useListSkillVersionsQuery(
		{
			id: skillId,
			limit: PAGE_SIZE,
			offset,
			search: debouncedSearch || undefined,
		},
		{ skip: !open },
	);

	// Reset the accumulator whenever the search term changes so a new query
	// starts from page 0 instead of appending onto stale results.
useEffect(() => {
    setOffset(0);
    setAccumulated([]);
}, [debouncedSearch, skillId]);

	// Append each fetched page. RTK Query replaces `data` per arg combo, so we
	// accumulate here; dedupe by id guards against effect re-runs on the same page.
	useEffect(() => {
		if (!data) return;
		setAccumulated((prev) => {
			if (offset === 0) return data.versions;
			const seen = new Set(prev.map((v) => v.id));
			return [...prev, ...data.versions.filter((v) => !seen.has(v.id))];
		});
	}, [data, offset]);

	const total = data?.total ?? 0;
	const hasMore = accumulated.length < total;

	// Infinite scroll: bump the offset when the bottom sentinel scrolls into view.
	const sentinelRef = useRef<HTMLDivElement | null>(null);
	useEffect(() => {
		const node = sentinelRef.current;
		if (!node || !open || !hasMore || isFetching) return;
		const observer = new IntersectionObserver(
			(entries) => {
				if (entries[0]?.isIntersecting) {
					setOffset((o) => o + PAGE_SIZE);
				}
			},
			{ threshold: 1 },
		);
		observer.observe(node);
		return () => observer.disconnect();
	}, [open, hasMore, isFetching]);

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger asChild>
				<Button variant="outline" size="sm" data-testid="skill-versions-popover-trigger" className="h-8 gap-1.5">
					Versions
					<ChevronDown className="h-3.5 w-3.5" />
				</Button>
			</PopoverTrigger>
			<PopoverContent align="end" className="w-80 p-0">
				<div className="border-b p-2">
					<div className="relative">
						<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
						<Input
							data-testid="skill-versions-search-input"
							aria-label="Search versions"
							placeholder="Search versions..."
							value={search}
							onChange={(e) => setSearch(e.target.value)}
							className="h-8 pl-8 text-sm"
						/>
					</div>
				</div>
				<ScrollArea className="h-72">
					<div className="p-1">
						{accumulated.map((v) => {
							const isServing = v.version === servingVersion;
							return (
								<button
									key={v.id}
									type="button"
									data-testid="skill-version-option"
									onClick={() => {
										onSelectVersion(v);
										setOpen(false);
									}}
									className="hover:bg-muted/60 flex w-full items-center justify-between gap-2 rounded-sm px-2 py-1.5 text-left transition-colors"
								>
									<span className="flex items-center gap-2">
										<span className="font-mono text-sm font-medium">{v.version}</span>
										{isServing && (
											<Badge
												variant="secondary"
												className="bg-emerald-100 text-xs text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400"
											>
												Serving
											</Badge>
										)}
									</span>
									<span className="text-muted-foreground shrink-0 text-xs">{formatDate(v.created_at)}</span>
								</button>
							);
						})}

						{isFetching && (
							<div className="text-muted-foreground flex items-center justify-center gap-2 py-3 text-xs">
								<Loader2 className="h-3.5 w-3.5 animate-spin" />
								Loading...
							</div>
						)}

						{isError && !isFetching && accumulated.length === 0 && (
							<div className="text-muted-foreground py-6 text-center text-xs">Failed to load versions</div>
						)}

						{!isError && !isFetching && accumulated.length === 0 && (
							<div className="text-muted-foreground py-6 text-center text-xs">
								{debouncedSearch ? "No versions match your search" : "No versions yet"}
							</div>
						)}

						{/* Sentinel observed to trigger the next page fetch */}
						{hasMore && <div ref={sentinelRef} className="h-px" />}
					</div>
				</ScrollArea>
			</PopoverContent>
		</Popover>
	);
}