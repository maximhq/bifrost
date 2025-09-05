import { Button } from "@/components/ui/button";
import { DialogFooter } from "@/components/ui/dialog";
import { Save } from "lucide-react";
import { Validator } from "@/lib/utils/validation";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

interface FormFooterProps {
	validator: Validator;
	label: string;
	onCancel: () => void;
	isLoading: boolean;
	isEditing: boolean;
}

export default function FormFooter({ validator, label, onCancel, isLoading, isEditing }: FormFooterProps) {
	return (
		<DialogFooter className="mt-4">
			<Button variant="outline" onClick={onCancel} disabled={isLoading}>
				Cancel
			</Button>
			<TooltipProvider>
				<Tooltip>
					<TooltipTrigger asChild>
						<span>
							<Button type="submit" disabled={isLoading || !validator.isValid()}>
								<Save className="h-4 w-4" />
								{isLoading ? "Saving..." : isEditing ? `Update ${label}` : `Create ${label}`}
							</Button>
						</span>
					</TooltipTrigger>
					{(!validator.isValid() || isLoading) && (
						<TooltipContent>
							<p>{isLoading ? "Saving..." : validator.getFirstError() || "Please fix validation errors"}</p>
						</TooltipContent>
					)}
				</Tooltip>
			</TooltipProvider>
		</DialogFooter>
	);
}
