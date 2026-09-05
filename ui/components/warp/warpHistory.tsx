import { formatWarpUsage } from "@/components/warp/warpStream.utils";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import {
  useDeleteWarpConversationMutation,
  useListWarpConversationsQuery,
} from "@/lib/store/apis/warpApi";
import type { WarpConversation } from "@/lib/types/warp";
import { cn } from "@/lib/utils";
import { formatDistanceToNow } from "date-fns";
import { Loader2, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

interface WarpHistoryProps {
  /** The thread currently open in the transcript, if any, so it can be marked. */
  activeConversationId?: string;
  /** Called with the thread to reopen. The parent loads it and swaps the transcript. */
  onOpen: (conversation: WarpConversation) => Promise<void> | void;
}

/**
 * Saved threads, most recent first.
 *
 * Whose threads depends on the deployment: with authentication each person sees
 * their own, without it there is no identity to scope by and the history is
 * common to everyone. That decision is made server-side from the caller's
 * identity, so this list simply shows whatever the API returns.
 *
 * The list is refetched every time it mounts. Threads are created by the chat
 * stream, which RTK never sees, so a cached list would be missing the
 * conversation that was just had.
 */
export default function WarpHistory({
  activeConversationId,
  onOpen,
}: WarpHistoryProps) {
  const {
    data: conversations,
    isLoading,
    isError,
  } = useListWarpConversationsQuery(
    { limit: 50 },
    { refetchOnMountOrArgChange: true },
  );
  const [deleteConversation] = useDeleteWarpConversationMutation();
  const [openingId, setOpeningId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const open = async (conversation: WarpConversation) => {
    setOpeningId(conversation.id);
    try {
      await onOpen(conversation);
    } catch {
      toast.error("Could not open that conversation.");
    } finally {
      setOpeningId(null);
    }
  };

  const remove = async (conversation: WarpConversation) => {
    setDeletingId(conversation.id);
    try {
      await deleteConversation(conversation.id).unwrap();
    } catch {
      toast.error("Could not delete that conversation.");
    } finally {
      setDeletingId(null);
    }
  };

  if (isLoading) {
    return (
      <div
        className="text-muted-foreground flex items-center justify-center gap-2 p-6 text-xs"
        data-testid="warp-history-loading"
      >
        <Loader2 className="size-3.5 animate-spin" /> Loading history
      </div>
    );
  }
  if (isError) {
    return (
      <p
        className="text-destructive p-6 text-center text-xs"
        data-testid="warp-history-error"
      >
        Could not load history.
      </p>
    );
  }
  if (!conversations || conversations.length === 0) {
    return (
      <p
        className="text-muted-foreground p-6 text-center text-xs"
        data-testid="warp-history-empty"
      >
        No saved conversations yet.
      </p>
    );
  }

  return (
    <ScrollArea className="h-full">
      <ul className="space-y-1 p-2" data-testid="warp-history">
        {conversations.map((conversation) => {
          const cost = formatWarpUsage({
            total_tokens: conversation.total_tokens,
            cost: { total_cost: conversation.total_cost },
          });
          const isActive = conversation.id === activeConversationId;
          return (
            <li key={conversation.id} className="group relative">
              <button
                type="button"
                onClick={() => void open(conversation)}
                disabled={openingId !== null}
                data-testid={`warp-history-item-${conversation.id}`}
                className={cn(
                  "hover:bg-accent w-full cursor-pointer rounded-md px-3 py-2 pr-9 text-left transition-colors disabled:cursor-default",
                  isActive && "bg-accent/60",
                )}
              >
                <p className="truncate text-sm">
                  {conversation.title || "Untitled"}
                </p>
                {/* Cost sits beside the time so the spend of a thread is visible
								    without opening it. It is the only place a whole conversation's
								    cost is summed anywhere in the dashboard. */}
                <p className="text-muted-foreground flex items-center gap-1.5 text-[11px] tabular-nums">
                  <span>
                    {formatDistanceToNow(new Date(conversation.updated_at), {
                      addSuffix: true,
                    })}
                  </span>
                  <span aria-hidden>·</span>
                  <span>{conversation.message_count} messages</span>
                  {cost && (
                    <>
                      <span aria-hidden>·</span>
                      <span data-testid="warp-history-cost">{cost}</span>
                    </>
                  )}
                  {openingId === conversation.id && (
                    <Loader2 className="ml-1 size-3 animate-spin" />
                  )}
                </p>
              </button>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label="Delete conversation"
                data-testid={`warp-history-delete-${conversation.id}`}
                disabled={deletingId === conversation.id}
                onClick={() => void remove(conversation)}
                className="text-muted-foreground hover:text-destructive absolute top-1/2 right-1 size-7 -translate-y-1/2 opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
              >
                {deletingId === conversation.id ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <Trash2 className="size-3.5" />
                )}
              </Button>
            </li>
          );
        })}
      </ul>
    </ScrollArea>
  );
}
