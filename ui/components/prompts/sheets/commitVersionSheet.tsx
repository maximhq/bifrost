import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scrollArea'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Message, MessageType } from '@/lib/message'
import { Markdown } from '@/components/ui/markdown'
import { getErrorMessage } from '@/lib/store'
import { useCommitSessionMutation } from '@/lib/store/apis/promptsApi'
import { PromptSession, PromptSessionMessage } from '@/lib/types/prompts'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'

interface CommitVersionFormData {
  commitMessage: string
}

interface CommitVersionSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  session: PromptSession
  onCommitted: (versionId: number) => void
}

function MessagePreview({ sessionMessage, selected, onToggle }: {
  sessionMessage: PromptSessionMessage
  selected: boolean
  onToggle: () => void
}) {
  const msg = useMemo(() => Message.deserialize(sessionMessage.message), [sessionMessage.message])
  const role = msg.role
  const content = msg.content
  const hasToolCalls = msg.type === MessageType.CompletionResult && msg.toolCalls && msg.toolCalls.length > 0

  return (
    <label
      className={cn(
        "group flex items-start gap-3 rounded-md border px-3 py-2.5 cursor-pointer transition-colors",
        selected ? "border-border" : "border-transparent",
      )}
    >
      <Checkbox
        checked={selected}
        onCheckedChange={onToggle}
        className="mt-1 shrink-0"
      />
      <div className="min-w-0 flex-1">
        <span className="text-xs font-medium uppercase">
          {role}
        </span>
        <div className="mt-1 line-clamp-3 text-sm text-muted-foreground">
          {hasToolCalls && !content ? (
            <span className="italic">Tool call: {msg.toolCalls!.map(tc => tc.function.name).join(', ')}</span>
          ) : content ? (
            <Markdown content={content} className="text-muted-foreground [&_*]:text-sm" />
          ) : (
            <span className="italic">Empty message</span>
          )}
        </div>
      </div>
    </label>
  )
}

export function CommitVersionSheet({ open, onOpenChange, session, onCommitted }: CommitVersionSheetProps) {
  const [commitSession, { isLoading }] = useCommitSessionMutation()
  const [selectedIndices, setSelectedIndices] = useState<Set<number>>(new Set())

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<CommitVersionFormData>({
    defaultValues: { commitMessage: '' },
  })

  // Reset form and select only the first message when sheet opens
  useEffect(() => {
    if (open) {
      reset({ commitMessage: '' })
      setSelectedIndices(new Set(session.messages.length > 0 ? [0] : []))
    }
  }, [open, reset, session?.messages?.length])

  const toggleMessage = useCallback((index: number) => {
    setSelectedIndices(prev => {
      const next = new Set(prev)
      if (next.has(index)) {
        next.delete(index)
      } else {
        next.add(index)
      }
      return next
    })
  }, [])

  const allSelected = selectedIndices.size === session.messages.length

  const toggleAll = useCallback(() => {
    if (allSelected) {
      setSelectedIndices(new Set())
    } else {
      setSelectedIndices(new Set(session.messages.map((_, i) => i)))
    }
  }, [allSelected, session.messages])

  async function onSubmit(data: CommitVersionFormData) {
    if (selectedIndices.size === 0) {
      toast.error('Please select at least one message to commit')
      return
    }
    try {
      const sortedIndices = Array.from(selectedIndices).sort((a, b) => a - b)
      const commitData: { commit_message: string; message_indices?: number[] } = {
        commit_message: data.commitMessage.trim(),
      }
      // Only send message_indices if not all messages are selected
      if (!allSelected) {
        commitData.message_indices = sortedIndices
      }
      const result = await commitSession({
        id: session.id,
        promptId: session.prompt_id,
        data: commitData,
      }).unwrap()
      toast.success('Version committed')
      reset()
      onCommitted(result.version.id)
      onOpenChange(false)
    } catch (err) {
      toast.error('Failed to commit version', {
        description: getErrorMessage(err),
      })
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className='flex h-full flex-col p-8' onOpenAutoFocus={(e) => { e.preventDefault(); document.getElementById("commitMessage")?.focus(); }}>
        <form onSubmit={handleSubmit(onSubmit)} className="flex flex-1 flex-col overflow-hidden">
          <SheetHeader className='flex flex-col items-start'>
            <SheetTitle>Commit as Version</SheetTitle>
            <SheetDescription>
              Select the messages to include in this version. Uncheck any messages you want to exclude.
            </SheetDescription>
          </SheetHeader>

          {/* Messages selection - scrollable */}
          <div className="mt-4 flex flex-1 flex-col overflow-hidden">
            <div className="mb-2 flex items-center justify-between">
              <Label className="text-sm">Messages ({selectedIndices.size}/{session.messages.length})</Label>
              <button
                type="button"
                onClick={toggleAll}
                className="text-xs text-muted-foreground hover:text-foreground transition-colors"
              >
                {allSelected ? 'Deselect all' : 'Select all'}
              </button>
            </div>
            <ScrollArea className="flex-1 rounded-md border overflow-y-auto">
              <div className="space-y-1 p-2">
                {session.messages.map((sessionMsg, index) => (
                  <MessagePreview
                    key={sessionMsg.id}
                    sessionMessage={sessionMsg}
                    selected={selectedIndices.has(index)}
                    onToggle={() => toggleMessage(index)}
                  />
                ))}
              </div>
            </ScrollArea>
          </div>

          {/* Commit message + CTAs - always visible at bottom */}
          <div className="mt-4 shrink-0 space-y-4">
            <div className="space-y-2">
              <Label htmlFor="commitMessage">Commit Message</Label>
              <Input
                id="commitMessage"
                data-testid="commit-version-message"
                placeholder="Added system message for better context..."
                {...register('commitMessage', {
                  required: 'Commit message is required',
                  validate: (v) => v.trim().length > 0 || 'Commit message cannot be blank',
                })}
                autoFocus
              />
              {errors.commitMessage ? (
                <p className="text-destructive text-xs">{errors.commitMessage.message}</p>
              ) : (
                <p className="text-muted-foreground text-xs">
                  Describe what changed in this version (e.g., &quot;Added error handling instructions&quot;)
                </p>
              )}
            </div>

            <SheetFooter className="p-0 flex flex-row items-center justify-end gap-2">
              <Button type="button" variant="outline" data-testid="commit-version-cancel" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" data-testid="commit-version-submit" disabled={isLoading || selectedIndices.size === 0} className={selectedIndices.size === 0 ? 'opacity-50' : ''}>
                {isLoading ? 'Committing...' : 'Commit Version'}
              </Button>
            </SheetFooter>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  )
}
