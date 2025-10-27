import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";

interface Props {
	show: boolean;
	onContinue: () => void;
	onCancel: () => void;
}

export default function ConfirmRedirectionDialog({ show, onContinue, onCancel }: Props) {
	return (
		<AlertDialog open={show}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Confirm Redirection</AlertDialogTitle>
					<AlertDialogDescription>You have unsaved data. Are you sure you want to continue?</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter className="mt-4">
					<AlertDialogCancel onClick={onCancel}>Cancel</AlertDialogCancel>
					<AlertDialogAction
						onClick={() => {
							onContinue();
						}}
					>
						Continue
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
