import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
} from '@/components/ui/alertDialog'
import { getErrorMessage, useDeletePluginMutation } from '@/lib/store'
import { Plugin } from '@/lib/types/plugins'
import { AlertDialogTitle } from '@radix-ui/react-alert-dialog'
import { toast } from 'sonner'

interface Props {
  show: boolean
  onCancel: () => void
  onDelete: () => void
  plugin: Plugin
}

export default function ConfirmDeletePluginDialog ({ show, onCancel, onDelete, plugin }: Props) {
  const [deletePlugin, { isLoading: isDeletingPlugin }] = useDeletePluginMutation()

  const onDeleteHandler = () => {
    deletePlugin(plugin.name)
      .unwrap()
      .then(() => {
        onDelete()
      })
      .catch((err) => {
        toast.error('Failed to delete plugin', {
          description: getErrorMessage(err),
        })
      })
  }

  return (
    <AlertDialog open={show}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete Plugin</AlertDialogTitle>
          <AlertDialogDescription>
            Are you sure you want to delete the plugin "{plugin.name}"? This action cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onCancel}>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={onDeleteHandler} disabled={isDeletingPlugin}>
            {isDeletingPlugin ? 'Deleting...' : 'Delete'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

