import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { getErrorMessage } from '@/lib/store'
import { useCommitSessionMutation } from '@/lib/store/apis/promptsApi'
import { PromptSession } from '@/lib/types/prompts'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { toast } from 'sonner'

interface CommitVersionFormData {
  commitMessage: string
}

interface CommitVersionSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  session: PromptSession
  onCommitted: (versionId: number) => void
}

export function CommitVersionSheet({ open, onOpenChange, session, onCommitted }: CommitVersionSheetProps) {
  const [commitSession, { isLoading }] = useCommitSessionMutation()

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<CommitVersionFormData>({
    defaultValues: { commitMessage: '' },
  })

  useEffect(() => {
    if (open) {
      reset({ commitMessage: '' })
    }
  }, [open, reset])

  async function onSubmit(data: CommitVersionFormData) {
    try {
      const result = await commitSession({
        id: session.id,
        promptId: session.prompt_id,
        data: { commit_message: data.commitMessage.trim() },
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
      <SheetContent className='p-8' onOpenAutoFocus={(e) => { e.preventDefault(); document.getElementById("commitMessage")?.focus(); }}>
        <form onSubmit={handleSubmit(onSubmit)}>
          <SheetHeader className='flex flex-col items-start'>
            <SheetTitle>Commit as Version</SheetTitle>
            <SheetDescription>
              Create a new immutable version from the current session. Versions cannot be modified after creation.
            </SheetDescription>
          </SheetHeader>

          <div className="mt-6 space-y-4">
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
                  Describe what changed in this version (e.g., "Added error handling instructions")
                </p>
              )}
            </div>
          </div>

          <SheetFooter className="mt-6 p-0 flex flex-row items-center justify-end gap-2">
            <Button type="button" variant="outline" data-testid="commit-version-cancel" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" data-testid="commit-version-submit" disabled={isLoading}>
              {isLoading ? 'Committing...' : 'Commit Version'}
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  )
}
