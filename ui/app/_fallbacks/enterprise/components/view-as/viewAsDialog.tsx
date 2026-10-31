// Non-enterprise builds have no view-as mode.
export const useCanViewAs = () => false;

export default function ViewAsDialog(_props: { open: boolean; onOpenChange: (open: boolean) => void }) {
	return null;
}