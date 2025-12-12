import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
} from "@/components/ui/alertDialog"
import { getErrorMessage, useDeleteProviderMutation } from "@/lib/store"
import { ModelProvider } from "@/lib/types/config"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { AlertDialogTitle } from "@radix-ui/react-alert-dialog"
import { toast } from "sonner"

interface Props {
  show: boolean
  onCancel: () => void
  onDelete: () => void
  provider: ModelProvider
}

export default function ConfirmDeleteProviderDialog({ show, onCancel, onDelete, provider }: Props) {
  const [deleteProvider, { isLoading: isDeletingProvider }] = useDeleteProviderMutation()
  const hasDeleteAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete)

  const onDeleteHandler = () => {
    deleteProvider(provider.name)
      .unwrap()
      .then(() => {
        onDelete()
      })
      .catch((err) => {
        toast.error("Failed to delete provider", {
          description: getErrorMessage(err),
        })
      })
  }

  return (
    <AlertDialog open={show}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete Provider</AlertDialogTitle>
          <AlertDialogDescription>Are you sure you want to delete this provider? This action cannot be undone.</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onCancel}>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={onDeleteHandler} disabled={isDeletingProvider || !hasDeleteAccess}>
            {isDeletingProvider ? "Deleting..." : "Delete"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
