import { isNotificationsUnavailable, shouldHideNotificationTrigger } from "@/components/notificationCenter.utils";
import { TOPBAR_MENU_SIDE_OFFSET } from "@/components/topbar.utils";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scrollArea";
import {
	clearAllNotifications,
	markAllNotificationsRead,
	markNotificationRead,
	removeNotification,
	selectNotificationPreferences,
	selectUnreadNotificationsCount,
	selectVisibleNotifications,
	useAppDispatch,
	useAppSelector,
	useGetNotificationsQuery,
} from "@/lib/store";
import type { NotificationSeverity } from "@/lib/types/notifications";
import { cn } from "@/lib/utils";
import { useNavigate } from "@tanstack/react-router";
import { formatDistanceToNow } from "date-fns";
import { Check, CheckCircle2, CircleAlert, Inbox, Info, RefreshCw, TriangleAlert, X } from "lucide-react";
import { useState } from "react";

const severityStyles: Record<NotificationSeverity, string> = {
	info: "bg-blue-500/10 text-blue-600 dark:text-blue-400",
	success: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
	warning: "bg-amber-500/10 text-amber-600 dark:text-amber-400",
	error: "bg-destructive/10 text-destructive",
};

const severityIcons = {
	info: Info,
	success: CheckCircle2,
	warning: TriangleAlert,
	error: CircleAlert,
};

export default function NotificationCenter() {
	const dispatch = useAppDispatch();
	const navigate = useNavigate();
	const [open, setOpen] = useState(false);
	const notifications = useAppSelector(selectVisibleNotifications);
	const unreadCount = useAppSelector(selectUnreadNotificationsCount);
	const { readIds } = useAppSelector(selectNotificationPreferences);
	const { isLoading, isFetching, isError, error, refetch } = useGetNotificationsQuery({ limit: 50 });

	const openNotification = (id: string, actionPath?: string) => {
		dispatch(markNotificationRead(id));
		if (actionPath) navigate({ to: actionPath });
	};

	// 503 means no config store is configured — a supported deployment that will
	// never recover, so drop the trigger instead of parking a permanent error in
	// the topbar. Transient failures still get the in-tray retry below.
	if (isNotificationsUnavailable(error)) return null;

	// Hide the trigger until there is something to open. A failed load is the one
	// empty state that stays visible — see shouldHideNotificationTrigger.
	if (shouldHideNotificationTrigger({ open, isLoading, isError, count: notifications.length })) return null;

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger asChild>
				<button
					type="button"
					aria-label={unreadCount ? `Notifications, ${unreadCount} unread` : "Notifications"}
					data-testid="topbar-notifications-btn"
					// size-8 matches the theme toggle and the menu/user-pill trigger. Every topbar trigger has to
					// share one box: Radix anchors sideOffset to the trigger's bounding box, so a shorter trigger
					// opens its surface higher than its neighbours even at an identical offset, and a narrower one
					// breaks the even horizontal rhythm of the icon row.
					className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md transition-colors"
				>
					{/* The badge hangs off the glyph, not the 32px hit area, so widening the button does not fling
					    the count into the corner. */}
					<span className="relative flex items-center justify-center">
						<Inbox className="size-4" strokeWidth={2} />
						{unreadCount > 0 && (
							<span
								data-testid="topbar-notifications-badge"
								className="absolute -top-1.5 -right-2 flex min-w-4 items-center justify-center rounded-full bg-red-600 px-1 text-[9px] leading-4 font-semibold text-white dark:bg-red-700"
							>
								{unreadCount > 99 ? "99+" : unreadCount}
							</span>
						)}
					</span>
				</button>
			</PopoverTrigger>
			<PopoverContent
				align="end"
				// TOPBAR_MENU_SIDE_OFFSET, one pixel tighter than the app-wide default of 4.
				// Every topbar surface uses it so the three menus open on the same line.
				sideOffset={TOPBAR_MENU_SIDE_OFFSET}
				className="w-[min(24rem,calc(100vw-1rem))] overflow-hidden p-0"
				data-testid="notification-tray"
			>
				<div className="flex h-12 items-center justify-between border-b px-4">
					<div className="flex items-center gap-2">
						<h2 className="text-sm font-semibold">Notifications</h2>
						{isFetching && !isLoading && <RefreshCw className="text-muted-foreground size-3 animate-spin" />}
					</div>
					{notifications.length > 0 && (
						<button
							type="button"
							onClick={() => dispatch(markAllNotificationsRead())}
							className="text-muted-foreground hover:text-foreground cursor-pointer text-xs transition-colors"
							data-testid="notifications-mark-all-read"
						>
							Mark all read
						</button>
					)}
				</div>

				{isLoading ? (
					<div className="text-muted-foreground flex h-44 items-center justify-center gap-2 text-sm">
						<RefreshCw className="size-4 animate-spin" /> Loading notifications
					</div>
				) : isError ? (
					<div className="flex h-44 flex-col items-center justify-center gap-3 px-6 text-center">
						<p className="text-muted-foreground text-sm">Notifications could not be loaded.</p>
						<button type="button" onClick={() => refetch()} className="text-primary cursor-pointer text-sm font-medium hover:underline">
							Try again
						</button>
					</div>
				) : notifications.length === 0 ? (
					<div className="flex h-44 flex-col items-center justify-center gap-2 px-6 text-center" data-testid="notifications-empty-state">
						<span className="bg-muted text-muted-foreground flex size-9 items-center justify-center rounded-full">
							<Check className="size-4" />
						</span>
						<p className="text-sm font-medium">You&apos;re all caught up</p>
						<p className="text-muted-foreground text-xs">New notifications will appear here.</p>
					</div>
				) : (
					<ScrollArea className="h-[min(26rem,calc(100vh-8rem))]">
						<div className="divide-y">
							{notifications.map((notification) => {
								const Icon = severityIcons[notification.severity];
								const isRead = readIds.includes(notification.id);
								return (
									// Open and dismiss are sibling native buttons, never nested. A
									// <button> may not contain interactive content, and role="button"
									// makes the same promise to assistive tech, so a dismiss control
									// inside the row control leaves screen readers announcing one
									// widget where there are two. Siblings also mean Enter and Space
									// come from the platform instead of a hand-rolled key handler.
									<div
										key={notification.id}
										data-testid={`notification-item-${notification.id}`}
										className={cn(
											"group relative flex transition-colors",
											isRead ? "hover:bg-accent/60" : "bg-accent/25 hover:bg-accent/60",
										)}
									>
										<button
											type="button"
											onClick={() => openNotification(notification.id, notification.action_path)}
											data-testid={`notification-open-${notification.id}`}
											className="focus-visible:ring-ring flex min-w-0 flex-1 cursor-pointer gap-3 px-4 py-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset"
										>
											<span
												className={cn(
													"mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full",
													severityStyles[notification.severity],
												)}
											>
												<Icon className="size-3.5" />
											</span>
											<span className="min-w-0 flex-1 pr-5">
												<span className="flex items-start gap-2">
													<span className={cn("min-w-0 flex-1 text-sm", !isRead && "font-semibold")}>{notification.title}</span>
													{!isRead && <span className="bg-primary mt-1.5 size-1.5 shrink-0 rounded-full" aria-label="Unread" />}
												</span>
												<span className="text-muted-foreground mt-0.5 line-clamp-3 block text-xs leading-relaxed">
													{notification.message}
												</span>
												<span className="text-muted-foreground mt-1.5 flex items-center gap-2 text-[11px]">
													<span>{formatDistanceToNow(new Date(notification.created_at), { addSuffix: true })}</span>
													{notification.action_label && <span className="text-primary font-medium">{notification.action_label}</span>}
												</span>
											</span>
										</button>
										<button
											type="button"
											aria-label={`Dismiss ${notification.title}`}
											onClick={() => dispatch(removeNotification(notification.id))}
											data-testid={`notification-dismiss-${notification.id}`}
											className="text-muted-foreground hover:bg-muted hover:text-foreground absolute top-2.5 right-2.5 cursor-pointer rounded p-1 opacity-0 transition-opacity group-hover:opacity-100 focus:opacity-100"
										>
											<X className="size-3" />
										</button>
									</div>
								);
							})}
						</div>
					</ScrollArea>
				)}

				{notifications.length > 0 && (
					<div className="flex h-10 items-center justify-end border-t px-4">
						<button
							type="button"
							onClick={() => dispatch(clearAllNotifications())}
							className="text-muted-foreground hover:text-destructive cursor-pointer text-xs transition-colors"
							data-testid="notifications-clear-all"
						>
							Clear all
						</button>
					</div>
				)}
			</PopoverContent>
		</Popover>
	);
}