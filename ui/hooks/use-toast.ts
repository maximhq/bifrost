import { toast as sonnerToast } from "sonner";

export interface Toast {
	title: string;
	description?: string;
	variant?: "default" | "destructive";
}

export function useToast() {
	const toast = ({ title, description, variant }: Toast) => {
		const message = description ? `${title}: ${description}` : title;

		if (variant === "destructive") {
			sonnerToast.error(message);
		} else {
			sonnerToast.success(message);
		}
	};

	return { toast };
}
