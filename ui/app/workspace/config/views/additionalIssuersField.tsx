import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { SecretVar } from "@/lib/types/schemas";
import { emptySecretVar, isSecretVarSet } from "@/lib/utils/secretVarForm";
import { AlertTriangle, Plus, X } from "lucide-react";
import { useRef } from "react";
import { Trans, useTranslation } from "react-i18next";
import { sameIssuer } from "./additionalIssuersField.utils";

// Matches MaxAdditionalIssuerURLs on the server.
export const MAX_ADDITIONAL_ISSUERS = 10;

interface AdditionalIssuersFieldProps {
	value: SecretVar[];
	saved: SecretVar[];
	onChange: (value: SecretVar[]) => void;
	disabled?: boolean;
}

/** Editable list of oauth2_server_config.additional_issuer_urls. */
export function AdditionalIssuersField({ value, saved, onChange, disabled }: AdditionalIssuersFieldProps) {
	const { t } = useTranslation("config");
	// Stable row keys, so removing a row does not hand its input state to the next one.
	const nextKey = useRef(0);
	const keys = useRef<number[]>([]);
	while (keys.current.length < value.length) keys.current.push(nextKey.current++);
	keys.current.length = value.length;

	const remove = (index: number) => {
		keys.current.splice(index, 1);
		onChange(value.filter((_, i) => i !== index));
	};
	const removedSaved = saved.some((s) => isSecretVarSet(s) && !value.some((v) => sameIssuer(v, s)));

	return (
		<div className="space-y-2 border-t pt-4" data-testid="oauth2-additional-issuers-section">
			<div className="space-y-0.5">
				<span className="text-sm font-medium">{t("security.oauthAdditionalIssuers")}</span>
				<p className="text-muted-foreground text-sm">
					<Trans t={t} i18nKey="security.oauthAdditionalIssuersHelp" components={{ code0: <code className="text-xs" /> }} />
				</p>
			</div>
			{value.map((issuer, index) => (
				<div key={keys.current[index]} className="flex items-start gap-2">
					<SecretVarInput
						className="flex-1"
						data-testid="oauth2-additional-issuer-input"
						aria-label={t("security.oauthAdditionalIssuerLabel", { number: index + 1 })}
						placeholder={t("security.oauthAdditionalIssuerPlaceholder")}
						value={issuer}
						onChange={(next: SecretVar) => onChange(value.map((v, i) => (i === index ? next : v)))}
						disabled={disabled}
					/>
					<Button
						type="button"
						variant="ghost"
						size="icon"
						data-testid="oauth2-additional-issuer-remove-btn"
						aria-label={t("security.removeAdditionalIssuer", { number: index + 1 })}
						onClick={() => remove(index)}
						className="text-muted-foreground hover:text-destructive"
						disabled={disabled}
					>
						<X className="h-4 w-4" />
					</Button>
				</div>
			))}
			<Button
				type="button"
				variant="outline"
				size="sm"
				data-testid="oauth2-additional-issuer-add-btn"
				onClick={() => onChange([...value, emptySecretVar()])}
				disabled={disabled || value.length >= MAX_ADDITIONAL_ISSUERS}
			>
				<Plus className="mr-2 h-4 w-4" />
				{t("security.addAdditionalIssuer")}
			</Button>
			{removedSaved && (
				<Alert variant="warning" data-testid="oauth2-additional-issuer-removed-warning">
					<AlertTriangle className="size-4" />
					<AlertDescription>{t("security.additionalIssuerRemovedWarning")}</AlertDescription>
				</Alert>
			)}
		</div>
	);
}