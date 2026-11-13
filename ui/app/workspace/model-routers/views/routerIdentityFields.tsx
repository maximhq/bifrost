import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { msg } from "@/lib/i18n/validationMessage";
import type { Control } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

export const routerIdentitySchema = z.object({
	name: z.string().trim().min(1, msg("validation.nameIsRequired")),
	description: z.string().trim(),
});
export type RouterIdentityValues = z.infer<typeof routerIdentitySchema>;

// RouterIdentityFields are a router's name and description, as the creation
// wizard's first step and the edit-details dialog both ask for them.
export function RouterIdentityFields({ control }: { control: Control<RouterIdentityValues> }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	return (
		<>
			<FormField
				control={control}
				name="name"
				render={({ field }) => (
					<FormItem>
						<FormLabel>{t("columnName")}</FormLabel>
						<FormControl>
							<Input {...field} placeholder={t("namePlaceholder")} autoFocus data-testid="model-router-name-input" />
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
			<FormField
				control={control}
				name="description"
				render={({ field }) => (
					<FormItem>
						<FormLabel>{t("descriptionLabel")}</FormLabel>
						<FormControl>
							<Textarea {...field} rows={3} placeholder={t("descriptionPlaceholder")} data-testid="model-router-description-input" />
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
		</>
	);
}