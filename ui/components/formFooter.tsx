import { Button } from "@/components/ui/button";
import { DialogFooter } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Validator } from "@/lib/utils/validation";
import { Save } from "lucide-react";

interface FormFooterProps {
	validator: Validator;
	label: string;
	onCancel: () => void;
	isLoading: boolean;
	isEditing: boolean;
	hasPermission?: boolean;
}

export default function FormFooter({ validator, label, onCancel, isLoading, isEditing, hasPermission = true }: FormFooterProps) {
	const isDisabled = isLoading || !validator.isValid() || !hasPermission;

	const getTooltipMessage = () => {
		if (!hasPermission) return "您没有权限执行此操作";
		if (isLoading) return "Saving...";
		return validator.getFirstError() || "Please fix validation errors";
	};

	return (
		<DialogFooter className="mt-4">
			<Button type="button" variant="outline" onClick={onCancel} disabled={isLoading}>取消</Button>
			<TooltipProvider>
				<Tooltip>
					<TooltipTrigger asChild>
						<span>
							<Button type="submit" disabled={isDisabled}>
								<Save className="h-4 w-4" />
								{isLoading ? "Saving..." : isEditing ? `Update ${label}` : `Create ${label}`}
							</Button>
						</span>
					</TooltipTrigger>
					{isDisabled && (
						<TooltipContent>
							<p>{getTooltipMessage()}</p>
						</TooltipContent>
					)}
				</Tooltip>
			</TooltipProvider>
		</DialogFooter>
	);
}