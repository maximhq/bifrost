import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { MCP_AWS_SIGV4_SERVICES } from "@/lib/constants/mcp";
import type { MCPAWSSigV4Config, MCPAWSSigV4Service, SecretVar } from "@/lib/types/mcp";
import { isSecretVarSet } from "@/lib/types/schemas";
import { type Control, useFormContext, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";

const setOrUndefined = (v?: SecretVar) => (isSecretVarSet(v) ? v : undefined);

/**
 * awsSigV4Payload returns the aws_sigv4 block to send. The external ID and
 * session name only apply to an assumed role, so they are dropped without one.
 * The backend replaces the block whole, so the edit sheet sends it in full
 * whenever any field changed.
 */
export function awsSigV4Payload(cfg?: Partial<MCPAWSSigV4Config>): MCPAWSSigV4Config {
	const roleArn = setOrUndefined(cfg?.role_arn);
	return {
		region: cfg?.region ?? { value: "", ref: "" },
		service: cfg?.service || MCP_AWS_SIGV4_SERVICES[0],
		role_arn: roleArn,
		external_id: roleArn ? setOrUndefined(cfg?.external_id) : undefined,
		session_name: roleArn ? setOrUndefined(cfg?.session_name) : undefined,
	};
}

/** awsSigV4FormValues seeds the edit sheet from a stored block. */
export function awsSigV4FormValues(cfg?: MCPAWSSigV4Config): Partial<MCPAWSSigV4Config> {
	return {
		region: cfg?.region,
		service: cfg?.service || MCP_AWS_SIGV4_SERVICES[0],
		role_arn: cfg?.role_arn,
		external_id: cfg?.external_id,
		session_name: cfg?.session_name,
	};
}

interface AWSSigV4FieldsProps {
	// Shared by the create form and the edit sheet, whose form types differ but
	// both store the block under aws_sigv4.*.
	control: Control<any>;
	disabled?: boolean;
}

/** Region, signing service and optional role of an aws_sigv4 server. */
export function AWSSigV4Fields({ control, disabled }: AWSSigV4FieldsProps) {
	const { t } = useTranslation("mcp");
	const { setValue, getValues, clearErrors } = useFormContext();
	const hasRole = isSecretVarSet(useWatch({ control, name: "aws_sigv4.role_arn" }));
	// The external ID and session name belong to the role; hidden values left behind would fail validation.
	// Cleared to an empty value, not undefined, which a remounted field would replace with the stored default.
	const clearRoleSettings = () => {
		for (const name of ["aws_sigv4.external_id", "aws_sigv4.session_name"] as const) {
			if (isSecretVarSet(getValues(name))) {
				setValue(name, { value: "", ref: "" }, { shouldDirty: true });
			}
		}
		clearErrors(["aws_sigv4.external_id", "aws_sigv4.session_name"]);
	};
	return (
		<>
			<FormField
				control={control}
				name="aws_sigv4.region"
				render={({ field }) => (
					<FormItem className="flex flex-col gap-2">
						<FormLabel>
							{t("registry.awsSigV4.region")} <span className="text-destructive">*</span>
						</FormLabel>
						<FormControl>
							<SecretVarInput
								data-testid="aws-sigv4-region-input"
								hideValueWhenEnv
								aria-required
								placeholder={t("registry.awsSigV4.regionPlaceholder")}
								disabled={disabled}
								value={field.value}
								onChange={field.onChange}
							/>
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
			<FormField
				control={control}
				name="aws_sigv4.service"
				render={({ field }) => (
					<FormItem className="flex flex-col gap-2">
						<FormLabel>{t("registry.awsSigV4.service")}</FormLabel>
						<Select
							value={field.value || MCP_AWS_SIGV4_SERVICES[0]}
							onValueChange={(v: MCPAWSSigV4Service) => field.onChange(v)}
							disabled={disabled}
						>
							<FormControl>
								<SelectTrigger className="w-full" data-testid="aws-sigv4-service-select">
									<SelectValue />
								</SelectTrigger>
							</FormControl>
							<SelectContent>
								{MCP_AWS_SIGV4_SERVICES.map((service) => (
									<SelectItem key={service} value={service} data-testid={`aws-sigv4-service-${service}`}>
										{service}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<p className="text-muted-foreground text-xs">{t("registry.awsSigV4.serviceHelp")}</p>
						<FormMessage />
					</FormItem>
				)}
			/>
			<FormField
				control={control}
				name="aws_sigv4.role_arn"
				render={({ field }) => (
					<FormItem className="flex flex-col gap-2">
						<FormLabel>{t("registry.awsSigV4.roleArn")}</FormLabel>
						<FormControl>
							<SecretVarInput
								data-testid="aws-sigv4-role-arn-input"
								hideValueWhenEnv
								placeholder={t("registry.awsSigV4.roleArnPlaceholder")}
								disabled={disabled}
								value={field.value}
								onChange={(value) => {
									field.onChange(value);
									if (!isSecretVarSet(value)) {
										clearRoleSettings();
									}
								}}
							/>
						</FormControl>
						<p className="text-muted-foreground text-xs">{t("registry.awsSigV4.roleArnHelp")}</p>
						<FormMessage />
					</FormItem>
				)}
			/>
			{hasRole && (
				<>
					<FormField
						control={control}
						name="aws_sigv4.external_id"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>{t("registry.awsSigV4.externalId")}</FormLabel>
								<FormControl>
									<SecretVarInput
										data-testid="aws-sigv4-external-id-input"
										hideValueWhenEnv
										placeholder={t("registry.awsSigV4.externalIdPlaceholder")}
										disabled={disabled}
										value={field.value}
										onChange={field.onChange}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="aws_sigv4.session_name"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>{t("registry.awsSigV4.sessionName")}</FormLabel>
								<FormControl>
									<SecretVarInput
										data-testid="aws-sigv4-session-name-input"
										hideValueWhenEnv
										placeholder={t("registry.awsSigV4.sessionNamePlaceholder")}
										disabled={disabled}
										value={field.value}
										onChange={field.onChange}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</>
			)}
		</>
	);
}