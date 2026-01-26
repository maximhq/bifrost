"use client";

import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import React, { useEffect, useState } from "react";
import { Card, CardContent, CardHeader } from "./card";

interface PromoCardItem {
	id: string;
	title: string | React.ReactElement;
	description: string | React.ReactElement;
	dismissible?: boolean;
	variant?: "default" | "warning";
}

interface PromoCardStackProps {
	cards: PromoCardItem[];
	className?: string;
	onCardsEmpty?: () => void;
	onDismiss?: (cardId: string) => void;
}

export function PromoCardStack({ cards, className = "", onCardsEmpty, onDismiss }: PromoCardStackProps) {
	const [items, setItems] = useState(() => {
		// Sort so non-dismissible cards appear at the top
		return [...cards].sort((a, b) => {
			const aDismissible = a.dismissible !== false;
			const bDismissible = b.dismissible !== false;
			if (aDismissible === bDismissible) return 0;
			return aDismissible ? 1 : -1; // Non-dismissible first
		});
	});
	const [removingId, setRemovingId] = useState<string | null>(null);
	const [isAnimating, setIsAnimating] = useState(false);
	const prevLenRef = React.useRef(items.length);
	// Track dismissed card IDs to prevent them from reappearing during animation
	const dismissedIdsRef = React.useRef<Set<string>>(new Set());

	useEffect(() => {
		// Skip syncing while animating to prevent interrupting dismiss animation
		if (isAnimating) return;
		
		// Sort so non-dismissible cards appear at the top, excluding dismissed cards
		const sortedCards = [...cards]
			.filter((card) => !dismissedIdsRef.current.has(card.id))
			.sort((a, b) => {
				const aDismissible = a.dismissible !== false;
				const bDismissible = b.dismissible !== false;
				if (aDismissible === bDismissible) return 0;
				return aDismissible ? 1 : -1; // Non-dismissible first
			});
		setItems(sortedCards);
	}, [cards, isAnimating]);

	// Call once when the stack transitions from non-empty to empty
	useEffect(() => {
		if (prevLenRef.current > 0 && items.length === 0) {
			onCardsEmpty?.();
		}
		prevLenRef.current = items.length;
	}, [items.length]);

	const handleDismiss = (cardId: string) => {
		if (isAnimating) return;
		setIsAnimating(true);
		setRemovingId(cardId);
		// Track this card as dismissed to prevent it from reappearing
		dismissedIdsRef.current.add(cardId);
		onDismiss?.(cardId);

		setTimeout(() => {
			setItems((prev) => prev.filter((it) => it.id !== cardId));
			setRemovingId(null);
			setIsAnimating(false);
		}, 400);
	};

	const MAX_VISIBLE_CARDS = 10;
	const visibleCards = items.slice(0, MAX_VISIBLE_CARDS);

	if (!cards || cards.length === 0 || visibleCards.length === 0) {
		return null;
	}

	return (
		<div className={`relative ${className}`} style={{ marginBottom: "60px", height: "130px" }}>
			{visibleCards.map((card, index) => {
				const isTopCard = index === 0;
				const isRemoving = removingId === card.id;
				const scale = 1 - index * 0.05;
				const yOffset = index * 10;
				const opacity = 1 - index * 0.2;

				return (
					<div
						key={card.id}
						className="absolute right-0 left-0 transition-all duration-400 ease-out"
						style={{
							top: isRemoving ? 0 : `${yOffset}px`,
							transform: isRemoving ? "translateX(-120%) rotate(-8deg)" : `scale(${scale})`,
							opacity: isRemoving ? 0 : opacity,
							zIndex: visibleCards.length - index,
							transformOrigin: "center center",
							pointerEvents: isTopCard && !isAnimating ? "auto" : "none",
							height: "190px",
						}}
					>
						<Card
							className={cn(
								"flex h-full w-full flex-col gap-0 rounded-lg px-2.5 py-2",
								visibleCards.length < 2 ? "shadow-none" : "shadow-md",
								card.variant === "warning" && "border-amber-500/50 bg-amber-50 dark:border-amber-500/70 dark:bg-amber-950/20",
							)}
						>
							<CardHeader className={cn(
								"flex-shrink-0 p-1 text-sm font-medium",
								card.variant === "warning" ? "text-amber-800 dark:text-amber-400" : "text-muted-foreground",
							)}>
								<div className="flex items-start justify-between">
									<div className="min-w-0 flex-1">{typeof card.title === "string" ? card.title : card.title}</div>
									{card.dismissible !== false && isTopCard && (
										<button
											aria-label="Dismiss"
											type="button"
											onClick={() => handleDismiss(card.id)}
											disabled={isAnimating}
											className="hover:text-foreground text-muted-foreground -m-1 flex-shrink-0 rounded p-1 disabled:opacity-50"
										>
											<X className="h-3.5 w-3.5" />
										</button>
									)}
								</div>
							</CardHeader>
							<CardContent className="text-muted-foreground mt-0 flex-1 overflow-y-auto px-1 pt-0 pb-1 text-xs">
								{typeof card.description === "string" ? card.description : card.description}
							</CardContent>
						</Card>
					</div>
				);
			})}
		</div>
	);
}
