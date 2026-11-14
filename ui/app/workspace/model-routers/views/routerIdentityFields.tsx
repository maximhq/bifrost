import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import type { ModelRouterIdentityFormSchema } from "@/lib/types/schemas";
import type { Control, FieldValues, Path } from "react-hook-form";
import { useTranslation } from "react-i18next";

// RouterIdentityFields are a router's name and description, as the creation
// wizard's first step and the router sheet both ask for them. They take any
// form whose values include the two.
export function RouterIdentityFields<T extends FieldValues & ModelRouterIdentityFormSchema>({ control }: { control: Control<T> }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	return (
		<>
			<FormField
				control={control}
				name={"name" as Path<T>}
				render={({ field }) => (
					<FormItem>
						<FormLabel>{t("columnName")}</FormLabel>
						<FormControl>
							<Input
								{...field}
								value={field.value ?? ""}
								placeholder={t("namePlaceholder")}
								autoFocus
								data-testid="model-router-name-input"
							/>
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
			<FormField
				control={control}
				name={"description" as Path<T>}
				render={({ field }) => (
					<FormItem>
						<FormLabel>{t("descriptionLabel")}</FormLabel>
						<FormControl>
							<Textarea
								{...field}
								value={field.value ?? ""}
								rows={3}
								placeholder={t("descriptionPlaceholder")}
								data-testid="model-router-description-input"
							/>
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
		</>
	);
}