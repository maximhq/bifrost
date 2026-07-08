"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { useListSkillVersionsQuery } from "@/lib/store/apis/skillsApi";
import { SkillVersionSummary } from "@/lib/types/skills";
import { ChevronDown } from "lucide-react";
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
			<PopoverContent align="end" className="w-72 p-0">
				<SkillVersionsList
					skillId={skillId}
					servingVersion={servingVersion}
					open={open}
					onSelectVersion={(v) => {
						onSelectVersion(v);
						setOpen(false);
					}}
				/>
			</PopoverContent>
		</Popover>
	);
}

/**
 * The searchable, infinitely-scrolling list of skill versions. Extracted so it can
 * be rendered either inside the standalone popover or inside a SplitButton dropdown.
 * `open` gates the query so versions are only fetched while the container is open.
 */
export function SkillVersionsList({
	skillId,
	servingVersion,
	open,
	onSelectVersion,
}: {
	skillId: string;
	servingVersion: string;
	open: boolean;
	onSelectVersion: (version: SkillVersionSummary) => void;
}) {
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
		<Command shouldFilter={false}>
			<CommandInput
				data-testid="skill-versions-search-input"
				placeholder="Search versions..."
				value={search}
				onValueChange={setSearch}
				isLoading={isFetching}
			/>
			<CommandList>
				{!isFetching && accumulated.length === 0 && (
					<CommandEmpty>{debouncedSearch ? "No versions match your search" : "No versions yet"}</CommandEmpty>
				)}
				<CommandGroup>
					{accumulated.map((v) => {
						const isServing = v.version === servingVersion;
						return (
							<CommandItem
								key={v.id}
								value={`${v.id}-${v.version}`}
								data-testid="skill-version-option"
								onSelect={() => onSelectVersion(v)}
								className="flex cursor-pointer items-center justify-between gap-2"
							>
								<span className="flex items-center gap-2">
									<span className="text-sm font-medium">{v.version}</span>
									{isServing && (
										<Badge variant="secondary" className="h-auto bg-emerald-100 px-1.5 py-0 text-xs">
											Serving
										</Badge>
									)}
								</span>
								<span className="text-muted-foreground shrink-0 text-xs">{formatDate(v.created_at)}</span>
							</CommandItem>
						);
					})}

					{isError && !isFetching && accumulated.length === 0 && (
						<div className="text-muted-foreground py-6 text-center text-xs">Failed to load versions</div>
					)}

					{/* Sentinel observed to trigger the next page fetch */}
					{hasMore && <div ref={sentinelRef} className="h-px" />}
				</CommandGroup>
			</CommandList>
		</Command>
	);
}