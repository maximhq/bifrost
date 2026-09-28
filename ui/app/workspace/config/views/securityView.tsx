import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { getErrorMessage, useGetCoreConfigQuery, useIsAuthEnabledQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { AuthConfig, CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { authProofOfControlSchema, SecretVar } from "@/lib/types/schemas";
import { parseArrayFromText } from "@/lib/utils/array";
import { formatCooldown } from "@/lib/utils/duration";
import { getApiBaseUrl } from "@/lib/utils/port";
import { isSecretVarSet } from "@/lib/utils/secretVarForm";
import { getPasswordPolicyFailures, type PasswordRequirementKey, validateOrigins } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useGetAuthTypeQuery } from "@enterprise/lib/store/apis/scimApi";
import { AlertTriangle, Loader2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";

// Go duration string: one or more <number><unit> segments, e.g. "5m", "1h30m".
const COOLDOWN_PATTERN = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;

const secretVarEquals = (a?: SecretVar, b?: SecretVar) =>
	(a?.value ?? "") === (b?.value ?? "") && (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");

export default function SecurityView() {
	const { t } = useTranslation("config");
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const { data: authType, isLoading: authTypeLoading, error: authTypeError } = useGetAuthTypeQuery(undefined, { skip: !IS_ENTERPRISE });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const showPasswordSection = !IS_ENTERPRISE || (!authTypeLoading && !authTypeError && authType?.type !== "sso");
	const passwordInputRef = useRef<HTMLInputElement | HTMLTextAreaElement>(null);
	const passwordUnchangedRef = useRef(true);
	const inferenceAuthTouchedRef = useRef(false);

	const [localValues, setLocalValues] = useState<{
		allowed_origins: string;
		allowed_headers: string;
		required_headers: string;
		whitelisted_routes: string;
		vk_rotation_cooldown: string;
	}>({
		allowed_origins: "",
		allowed_headers: "",
		required_headers: "",
		whitelisted_routes: "",
		vk_rotation_cooldown: "",
	});

	const [authConfig, setAuthConfig] = useState<AuthConfig>({
		admin_username: { value: "", ref: "" },
		admin_password: { value: "", ref: "" },
		is_enabled: false,
	});
	const [passwordError, setPasswordError] = useState("");
	const [setupToken, setSetupToken] = useState("");
	const [setupTokenErrorMessage, setSetupTokenErrorMessage] = useState<string | null>(null);
	const [currentPassword, setCurrentPassword] = useState("");
	const [proofError, setProofError] = useState("");
	const [setupTokenOpen, setSetupTokenOpen] = useState(false);
	const currentPasswordInputRef = useRef<HTMLInputElement>(null);
	// No admin account has ever been created on this instance yet. The very first
	// PUT /api/config that creates one must include the setup token the operator
	// configured via setup_token in config.json (or BIFROST_SETUP_TOKEN), so this
	// field only needs to show up that once.
	const isFirstTimeSetup = !bifrostConfig?.auth_config;
	// OSS setup lock: while it is active, this page only loads because the browser holds
	// the HttpOnly setup session cookie from the login setup view. Every request (the
	// save below included) carries it, and the server accepts it as first-admin proof,
	// so the token is never asked for again here.
	const { data: authState } = useIsAuthEnabledQuery(undefined, { skip: IS_ENTERPRISE });
	const authorizedBySetupToken = isFirstTimeSetup && !IS_ENTERPRISE && !!authState?.setup_required;
	// The config layout gates this query on loading, not on error, so a failed
	// GET /api/config?from_db=true still renders this form with no config (the
	// dashboard shell's own config request can succeed). isFirstTimeSetup then
	// reads true for every instance, and a later successful refetch would overwrite
	// any toggle made in the meantime, leaving dashboard auth on with inference
	// auth reset to the stored value.
	const isConfigLoading = !bifrostConfig;
	// An admin account exists but dashboard protection is switched off, so this browser
	// was let in without any credential check. Turning protection back on (or replacing
	// the stored credentials) from here has to prove control of the instance: PUT
	// /api/config answers 403 unless the request carries the current admin password or
	// the operator's setup token. Neither applies while auth is on (the session is signed
	// in) or before the first admin exists (the setup token flow above covers that).
	const isAuthDisabledWithStoredAccount = !isFirstTimeSetup && bifrostConfig?.auth_config?.is_enabled === false;

	useEffect(() => {
		if (bifrostConfig && config) {
			inferenceAuthTouchedRef.current = false;
			setLocalConfig(config);
			setLocalValues({
				allowed_origins: config?.allowed_origins?.join(", ") || "",
				allowed_headers: config?.allowed_headers?.join(", ") || "",
				required_headers: config?.required_headers?.join(", ") || "",
				whitelisted_routes: config?.whitelisted_routes?.join(", ") || "",
				vk_rotation_cooldown: formatCooldown(config?.vk_rotation_cooldown),
			});
		}
		if (bifrostConfig?.auth_config) {
			passwordUnchangedRef.current = true;
			setAuthConfig(bifrostConfig.auth_config);
		}
	}, [config, bifrostConfig]);

	const credentialsChanged = useMemo(() => {
		const usernameChanged =
			authConfig.admin_username?.value !== bifrostConfig?.auth_config?.admin_username?.value ||
			authConfig.admin_username?.ref !== bifrostConfig?.auth_config?.admin_username?.ref ||
			authConfig.admin_username?.type !== bifrostConfig?.auth_config?.admin_username?.type;
		const passwordChanged =
			authConfig.admin_password?.value !== bifrostConfig?.auth_config?.admin_password?.value ||
			authConfig.admin_password?.ref !== bifrostConfig?.auth_config?.admin_password?.ref ||
			authConfig.admin_password?.type !== bifrostConfig?.auth_config?.admin_password?.type;
		return usernameChanged || passwordChanged;
	}, [authConfig, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		const localOrigins = localConfig.allowed_origins?.slice().sort().join(",");
		const serverOrigins = config.allowed_origins?.slice().sort().join(",");
		const originsChanged = localOrigins !== serverOrigins;

		const localHeaders = localConfig.allowed_headers?.slice().sort().join(",");
		const serverHeaders = config.allowed_headers?.slice().sort().join(",");
		const headersChanged = localHeaders !== serverHeaders;

		const authChanged = showPasswordSection
			? authConfig.is_enabled !== bifrostConfig?.auth_config?.is_enabled || credentialsChanged
			: false;

		const localRequired = localConfig.required_headers?.slice().sort().join(",");
		const serverRequired = config.required_headers?.slice().sort().join(",");
		const requiredChanged = localRequired !== serverRequired;

		const localWhitelistedRoutes = localConfig.whitelisted_routes?.slice().sort().join(",");
		const serverWhitelistedRoutes = config.whitelisted_routes?.slice().sort().join(",");
		const whitelistedRoutesChanged = localWhitelistedRoutes !== serverWhitelistedRoutes;

		const enforceAuthOnInferenceChanged = localConfig.enforce_auth_on_inference !== config.enforce_auth_on_inference;
		const allowDirectKeysChanged = localConfig.allow_direct_keys !== config.allow_direct_keys;
		const deleteExpiredVirtualKeysChanged = localConfig.delete_expired_virtual_keys !== config.delete_expired_virtual_keys;
		const enforceVKObjectOwnershipChanged =
			(localConfig.enforce_vk_object_ownership ?? true) !== (config.enforce_vk_object_ownership ?? true);
		const skipAccessProfileUserVirtualKeyCreationChanged =
			IS_ENTERPRISE &&
			(localConfig.skip_access_profile_user_virtual_key_creation ?? false) !==
				(config.skip_access_profile_user_virtual_key_creation ?? false);
		const skipAccessProfileUserVirtualKeyDeletionChanged =
			IS_ENTERPRISE &&
			(localConfig.skip_access_profile_user_virtual_key_deletion ?? false) !==
				(config.skip_access_profile_user_virtual_key_deletion ?? false);
		const dualCredentialConflictBehaviorChanged =
			(localConfig.dual_credential_conflict_behavior || "prefer_idp") !== (config.dual_credential_conflict_behavior || "prefer_idp");
		const vkRotationCooldownChanged = formatCooldown(localConfig.vk_rotation_cooldown) !== formatCooldown(config.vk_rotation_cooldown);
		const issuerURLChanged = !secretVarEquals(localConfig.oauth2_server_config?.issuer_url, config.oauth2_server_config?.issuer_url);

		return (
			originsChanged ||
			headersChanged ||
			requiredChanged ||
			whitelistedRoutesChanged ||
			authChanged ||
			enforceAuthOnInferenceChanged ||
			allowDirectKeysChanged ||
			deleteExpiredVirtualKeysChanged ||
			enforceVKObjectOwnershipChanged ||
			skipAccessProfileUserVirtualKeyCreationChanged ||
			skipAccessProfileUserVirtualKeyDeletionChanged ||
			dualCredentialConflictBehaviorChanged ||
			vkRotationCooldownChanged ||
			issuerURLChanged
		);
	}, [config, localConfig, authConfig, bifrostConfig, showPasswordSection, credentialsChanged]);

	const requiresProofOfControl = isAuthDisabledWithStoredAccount && (authConfig.is_enabled || credentialsChanged);

	const needsRestart = useMemo(() => {
		if (!config) return false;

		const localOrigins = localConfig.allowed_origins?.slice().sort().join(",");
		const serverOrigins = config.allowed_origins?.slice().sort().join(",");
		const originsChanged = localOrigins !== serverOrigins;

		const localHeaders = localConfig.allowed_headers?.slice().sort().join(",");
		const serverHeaders = config.allowed_headers?.slice().sort().join(",");
		const headersChanged = localHeaders !== serverHeaders;

		const enforceAuthOnInferenceChanged = localConfig.enforce_auth_on_inference !== config.enforce_auth_on_inference && IS_ENTERPRISE;

		return originsChanged || headersChanged || enforceAuthOnInferenceChanged;
	}, [config, localConfig]);

	const handleAllowedOriginsChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, allowed_origins: value }));
		setLocalConfig((prev) => ({ ...prev, allowed_origins: parseArrayFromText(value) }));
	}, []);

	const handleAllowedHeadersChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, allowed_headers: value }));
		setLocalConfig((prev) => ({ ...prev, allowed_headers: parseArrayFromText(value) }));
	}, []);

	const handleRequiredHeadersChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, required_headers: value }));
		setLocalConfig((prev) => ({ ...prev, required_headers: parseArrayFromText(value) }));
	}, []);

	const handleWhitelistedRoutesChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, whitelisted_routes: value }));
		setLocalConfig((prev) => ({ ...prev, whitelisted_routes: parseArrayFromText(value) }));
	}, []);

	const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean) => {
		if (field === "enforce_auth_on_inference") inferenceAuthTouchedRef.current = true;
		setLocalConfig((prev) => ({ ...prev, [field]: value }));
	}, []);

	const handleVkRotationCooldownChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, vk_rotation_cooldown: value }));
		// The backend accepts Go duration strings; empty input means 0 (disabled).
		setLocalConfig((prev) => ({ ...prev, vk_rotation_cooldown: value.trim() === "" ? 0 : value.trim() }));
	}, []);

	const handleAuthToggle = useCallback(
		(checked: boolean) => {
			setAuthConfig((prev) => ({ ...prev, is_enabled: checked }));
			setProofError("");
			if (!isFirstTimeSetup || inferenceAuthTouchedRef.current) return;
			// Untouched preselection follows the dashboard toggle both ways, so canceling setup restores the stored value.
			setLocalConfig((prev) => ({ ...prev, enforce_auth_on_inference: checked || (config?.enforce_auth_on_inference ?? false) }));
		},
		[isFirstTimeSetup, config?.enforce_auth_on_inference],
	);

	const handleCurrentPasswordChange = useCallback((value: string) => {
		setCurrentPassword(value);
		setProofError("");
	}, []);

	const handleSetupTokenChange = useCallback((value: string) => {
		setSetupToken(value);
		setProofError("");
	}, []);

	const formatPasswordPolicyError = useCallback(
		(failures: PasswordRequirementKey[]) => {
			if (failures.length === 0) return "";
			return t("security.passwordMustInclude", {
				requirements: failures.map((key) => t(`security.passwordRequirement.${key}`)).join(t("security.requirementSeparator")),
			});
		},
		[t],
	);

	const handleAuthFieldChange = useCallback(
		(field: "admin_username" | "admin_password", value: SecretVar) => {
			if (field === "admin_password") {
				passwordUnchangedRef.current = false;
				const passwordPolicyFailures = !value.ref && value.value ? getPasswordPolicyFailures(value.value, false) : [];
				setPasswordError(formatPasswordPolicyError(passwordPolicyFailures));
			}
			setAuthConfig((prev) => ({ ...prev, [field]: value }));
		},
		[formatPasswordPolicyError],
	);

	const handleSave = useCallback(async () => {
		try {
			const validation = validateOrigins(localConfig.allowed_origins);

			if (!validation.isValid && localConfig.allowed_origins.length > 0) {
				toast.error(t("security.toastInvalidOrigins", { origins: validation.invalidOrigins.join(", ") }));
				return;
			}
			const oauthModeActive = localConfig.mcp_server_auth_mode === "both" || localConfig.mcp_server_auth_mode === "oauth";
			if (oauthModeActive && !isSecretVarSet(localConfig.oauth2_server_config?.issuer_url)) {
				toast.error(t("security.toastIssuerUrlRequired"));
				return;
			}
			const cooldownInput = localValues.vk_rotation_cooldown.trim();
			if (cooldownInput !== "" && cooldownInput !== "0" && !COOLDOWN_PATTERN.test(cooldownInput)) {
				toast.error(t("security.toastInvalidCooldown"));
				return;
			}
			const hasUsername = authConfig.admin_username?.value || authConfig.admin_username?.ref;
			const hasPassword = authConfig.admin_password?.value || authConfig.admin_password?.ref;
			const passwordPolicyFailures =
				showPasswordSection && authConfig.is_enabled && !authConfig.admin_password?.ref && authConfig.admin_password?.value
					? getPasswordPolicyFailures(authConfig.admin_password.value, passwordUnchangedRef.current)
					: [];

			if (passwordPolicyFailures.length > 0) {
				setPasswordError(formatPasswordPolicyError(passwordPolicyFailures));
				passwordInputRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
				passwordInputRef.current?.focus({ preventScroll: true });
				return;
			}
			if (isFirstTimeSetup && authConfig.is_enabled && !authorizedBySetupToken && !setupToken.trim()) {
				setSetupTokenErrorMessage(t("security.setupTokenHelp"));
				return;
			}
			const proof = requiresProofOfControl
				? authProofOfControlSchema.safeParse({ current_password: currentPassword, setup_token: setupToken })
				: null;
			if (proof && !proof.success) {
				setProofError(t("security.proofRequired"));
				currentPasswordInputRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
				currentPasswordInputRef.current?.focus({ preventScroll: true });
				return;
			}
			setPasswordError("");
			setProofError("");

			await updateCoreConfig({
				...bifrostConfig!,
				client_config: localConfig,
				...(showPasswordSection
					? {
							auth_config: {
								...(authConfig.is_enabled && hasUsername && hasPassword ? authConfig : { ...authConfig, is_enabled: false }),
								...(isFirstTimeSetup && !authorizedBySetupToken ? { setup_token: setupToken.trim() } : {}),
								...(proof?.success && proof.data.current_password ? { current_password: proof.data.current_password } : {}),
								...(proof?.success && proof.data.setup_token ? { setup_token: proof.data.setup_token } : {}),
							},
						}
					: {}),
			}).unwrap();
			setSetupToken("");
			setCurrentPassword("");
			setSetupTokenOpen(false);
			toast.success(t("security.toastSaved"));
			// Dashboard auth is now on, so the OSS setup lock is lifted. Expire the setup
			// session cookie (logout clears it) and sign in with the new admin account.
			if (showPasswordSection && authConfig.is_enabled && hasUsername && hasPassword && authorizedBySetupToken) {
				await fetch(`${getApiBaseUrl()}/session/logout`, { method: "POST", credentials: "include" }).catch(() => undefined);
				window.location.href = "/login";
			}
		} catch (error) {
			const message = getErrorMessage(error);
			const status = typeof error === "object" && error !== null && "status" in error ? (error as { status?: unknown }).status : undefined;
			if (requiresProofOfControl && status === 403) {
				// The server rejected the proof itself (wrong password or token), so the
				// answer belongs on the confirmation field rather than in a toast.
				setProofError(message);
				currentPasswordInputRef.current?.focus({ preventScroll: true });
			} else if (isFirstTimeSetup && message.toLowerCase().includes("setup token")) {
				setSetupTokenErrorMessage(message);
			} else {
				toast.error(message);
			}
		}
	}, [
		bifrostConfig,
		localConfig,
		localValues.vk_rotation_cooldown,
		authConfig,
		showPasswordSection,
		updateCoreConfig,
		isFirstTimeSetup,
		setupToken,
		currentPassword,
		requiresProofOfControl,
		authorizedBySetupToken,
	]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<PageTitle title={t("security.title")}>{t("security.description")}</PageTitle>

			<div className="space-y-4">
				{/* Password Protect the Dashboard */}
				{IS_ENTERPRISE && authTypeLoading ? (
					<div className="flex items-center justify-center rounded-sm border p-8" data-testid="security-auth-type-loading">
						<Loader2 className="text-muted-foreground h-5 w-5 animate-spin" aria-hidden />
						<span className="sr-only">{t("security.loadingAuth")}</span>
					</div>
				) : null}
				{IS_ENTERPRISE && !authTypeLoading && authTypeError ? (
					<Alert variant="destructive" data-testid="security-auth-type-error">
						<AlertTriangle className="h-4 w-4" />
						<AlertDescription>
							{t("security.authTypeUnavailable")} {getErrorMessage(authTypeError)}
						</AlertDescription>
					</Alert>
				) : null}
				{showPasswordSection && (
					<div>
						<div className="space-y-4 rounded-sm border p-4">
							<div className="flex items-center justify-between">
								<div className="space-y-0.5">
									<Label htmlFor="auth-enabled" className="text-sm font-medium">
										{t("security.passwordProtect")} <Badge variant="secondary">BETA</Badge>
									</Label>
									<p className="text-muted-foreground text-sm">{t("security.passwordProtectHelp")}</p>
								</div>
								<Switch id="auth-enabled" checked={authConfig.is_enabled} disabled={isConfigLoading} onCheckedChange={handleAuthToggle} />
							</div>
							<div className="space-y-4">
								<div className="space-y-2">
									<Label htmlFor="admin-username">{t("security.username")}</Label>
									<SecretVarInput
										id="admin-username"
										autoComplete="username"
										type="text"
										placeholder={t("security.usernamePlaceholder")}
										value={authConfig.admin_username}
										disabled={!authConfig.is_enabled}
										onChange={(value) => handleAuthFieldChange("admin_username", value)}
									/>
								</div>
								<div className="space-y-2">
									<Label htmlFor="admin-password">{t("security.password")}</Label>
									<SecretVarInput
										ref={passwordInputRef}
										id="admin-password"
										// new-password: password managers offer to save or generate here instead of
										// filling a password saved for this host, which would silently replace what
										// the operator typed.
										autoComplete="new-password"
										aria-invalid={!!passwordError}
										aria-describedby={passwordError ? "admin-password-error" : undefined}
										type="password"
										placeholder={t("security.passwordPlaceholder")}
										value={authConfig.admin_password}
										disabled={!authConfig.is_enabled}
										onChange={(value) => handleAuthFieldChange("admin_password", value)}
									/>
									<p className="text-muted-foreground text-xs">{t("security.passwordPolicyHelp")}</p>
									{passwordError ? (
										<p id="admin-password-error" className="text-destructive text-xs" role="alert">
											{passwordError}
										</p>
									) : null}
								</div>
								{requiresProofOfControl ? (
									<div className="space-y-2 rounded-sm border border-dashed p-3" data-testid="security-proof-of-control">
										<Label htmlFor="current-password">{t("security.confirmWithPassword")}</Label>
										<Input
											ref={currentPasswordInputRef}
											id="current-password"
											data-testid="security-current-password-input"
											type="password"
											autoComplete="current-password"
											aria-invalid={!!proofError}
											aria-describedby={proofError ? "current-password-error" : undefined}
											placeholder={t("security.currentPasswordPlaceholder")}
											value={currentPassword}
											onChange={(e) => handleCurrentPasswordChange(e.target.value)}
										/>
										<p className="text-muted-foreground text-xs">{t("security.proofHelp")}</p>
										<Collapsible open={setupTokenOpen} onOpenChange={setSetupTokenOpen}>
											<CollapsibleTrigger asChild>
												<Button
													type="button"
													variant="link"
													size="sm"
													className="h-auto p-0 text-xs"
													data-testid="security-setup-token-toggle"
												>
													{setupTokenOpen ? t("security.hideSetupToken") : t("security.useSetupTokenInstead")}
												</Button>
											</CollapsibleTrigger>
											<CollapsibleContent className="space-y-2 pt-2">
												<Label htmlFor="setup-token">{t("security.setupToken")}</Label>
												<Input
													id="setup-token"
													data-testid="security-setup-token-input"
													type="password"
													autoComplete="off"
													placeholder={t("security.setupTokenPlaceholder")}
													value={setupToken}
													onChange={(e) => handleSetupTokenChange(e.target.value)}
												/>
												<p className="text-muted-foreground text-xs">
													<Trans
														t={t}
														i18nKey="security.setupTokenAlternativeHelp"
														components={{ code0: <code />, code1: <code />, code2: <code /> }}
													/>
												</p>
											</CollapsibleContent>
										</Collapsible>
										{proofError ? (
											<p
												id="current-password-error"
												data-testid="security-current-password-error"
												className="text-destructive text-xs"
												role="alert"
											>
												{proofError}
											</p>
										) : null}
									</div>
								) : null}
								{isFirstTimeSetup && authConfig.is_enabled && authorizedBySetupToken ? (
									<p className="text-muted-foreground text-xs" data-testid="security-setup-token-authorized">
										{t("security.authorizedBySetupToken")}
									</p>
								) : isFirstTimeSetup && authConfig.is_enabled ? (
									<div className="space-y-2">
										<Label htmlFor="setup-token">{t("security.setupToken")}</Label>
										<Input
											id="setup-token"
											data-testid="security-setup-token-input"
											type="password"
											autoComplete="off"
											placeholder={t("security.setupTokenPlaceholder")}
											value={setupToken}
											onChange={(e) => handleSetupTokenChange(e.target.value)}
										/>
										<p className="text-muted-foreground text-xs">
											<Trans t={t} i18nKey="security.firstSetupHelp" components={{ code0: <code />, code1: <code />, code2: <code /> }} />
										</p>
									</div>
								) : null}
							</div>
						</div>
					</div>
				)}
				{/* Enable Auth on Inference */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="enforce-auth-on-inference" className="text-sm font-medium">
							{IS_ENTERPRISE ? t("security.enableAuthOnInference") : t("security.enforceVirtualKeys")}
						</label>
						<p className="text-muted-foreground text-sm">
							{IS_ENTERPRISE ? t("security.enableAuthHelp") : t("security.enforceVkHelp")} {t("security.see")}{" "}
							<a
								href="https://docs.getbifrost.ai/features/governance/virtual-keys"
								target="_blank"
								rel="noopener noreferrer"
								className="text-primary underline"
								data-testid="security-virtual-keys-docs-link"
							>
								{t("security.documentation")}
							</a>{" "}
							{t("security.setupVirtualKeyHelp")}
						</p>
					</div>
					<Switch
						id="enforce-auth-on-inference"
						data-testid="enforce-auth-on-inference-switch"
						checked={localConfig.enforce_auth_on_inference}
						disabled={isConfigLoading}
						onCheckedChange={(checked) => handleConfigChange("enforce_auth_on_inference", checked)}
					/>
				</div>
				{(authConfig.is_enabled || authType?.type === "sso") && !config?.enforce_auth_on_inference && (
					<Alert variant="destructive" data-testid="inference-auth-off-warning">
						<AlertTriangle className="h-4 w-4" />
						<AlertDescription>{t("security.inferenceAuthOffWarning")}</AlertDescription>
					</Alert>
				)}
				{/* Dual Credential Conflict Behavior */}
				{IS_ENTERPRISE && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="dual-credential-conflict-behavior" className="text-sm font-medium">
								{t("security.dualCredential")}
							</label>
							<p className="text-muted-foreground text-sm">
								<Trans t={t} i18nKey="security.dualCredentialHelp" components={{ b0: <b />, b1: <b />, b2: <b />, b3: <b />, b4: <b /> }} />
							</p>
						</div>
						<Select
							value={localConfig.dual_credential_conflict_behavior || "prefer_idp"}
							onValueChange={(value) =>
								setLocalConfig((prev) => ({
									...prev,
									dual_credential_conflict_behavior: value as CoreConfig["dual_credential_conflict_behavior"],
								}))
							}
						>
							<SelectTrigger
								id="dual-credential-conflict-behavior"
								data-testid="dual-credential-conflict-behavior-select"
								className="w-full sm:w-[180px]"
							>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="prefer_idp">{t("security.preferIdp")}</SelectItem>
								<SelectItem value="prefer_vk">{t("security.preferVk")}</SelectItem>
								<SelectItem value="error">{t("security.rejectRequest")}</SelectItem>
							</SelectContent>
						</Select>
					</div>
				)}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="allow-direct-keys" className="text-sm font-medium">
							{t("security.allowDirectKeys")}
						</label>
						<p className="text-muted-foreground text-sm">
							<Trans t={t} i18nKey="security.directKeysHelp" components={{ b0: <b />, b1: <b />, b2: <b />, b3: <b /> }} />
						</p>
					</div>
					<Switch
						id="allow-direct-keys"
						data-testid="security-allow-direct-keys-switch"
						checked={localConfig.allow_direct_keys}
						onCheckedChange={(checked) => handleConfigChange("allow_direct_keys", checked)}
					/>
				</div>
				{/* Cooldown After Virtual Key Rotation */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="vk-rotation-cooldown" className="text-sm font-medium">
							{t("security.vkRotationCooldown")}
						</label>
						<p className="text-muted-foreground text-sm">{t("security.vkRotationCooldownDesc")}</p>
					</div>
					<Input
						id="vk-rotation-cooldown"
						data-testid="security-vk-rotation-cooldown-input"
						className="w-[180px]"
						placeholder="5m"
						value={localValues.vk_rotation_cooldown}
						onChange={(e) => handleVkRotationCooldownChange(e.target.value)}
					/>
				</div>
				{/* Delete Expired Virtual Keys */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="delete-expired-virtual-keys" className="text-sm font-medium">
							{t("security.deleteExpiredVirtualKeys")}
						</label>
						<p className="text-muted-foreground text-sm">
							<Trans t={t} i18nKey="security.deleteExpiredVirtualKeysHelp" components={{ b0: <b /> }} />
						</p>
					</div>
					<Switch
						id="delete-expired-virtual-keys"
						data-testid="security-delete-expired-virtual-keys-switch"
						checked={localConfig.delete_expired_virtual_keys}
						onCheckedChange={(checked) => handleConfigChange("delete_expired_virtual_keys", checked)}
					/>
				</div>
				{/* Restrict Provider Objects to the Creating Virtual Key */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="enforce-vk-object-ownership" className="text-sm font-medium">
							{t("security.enforceVkObjectOwnership")}
						</label>
						<p className="text-muted-foreground text-sm">{t("security.enforceVkObjectOwnershipHelp")}</p>
					</div>
					<Switch
						id="enforce-vk-object-ownership"
						data-testid="security-enforce-vk-object-ownership-switch"
						checked={localConfig.enforce_vk_object_ownership ?? true}
						onCheckedChange={(checked) => handleConfigChange("enforce_vk_object_ownership", checked)}
					/>
				</div>
				{/* Access profiles creating and deleting users' virtual keys, each switched separately */}
				{IS_ENTERPRISE && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="skip-access-profile-user-virtual-key-creation" className="text-sm font-medium">
								Don&apos;t Create User Virtual Keys for Access Profiles
							</label>
							<p className="text-muted-foreground text-sm">
								When enabled, a user&apos;s first access profile doesn&apos;t create a virtual key for them. Keys the user already owns, or
								that you add for them, are still governed by the profile while they hold one.
							</p>
						</div>
						<Switch
							id="skip-access-profile-user-virtual-key-creation"
							data-testid="security-skip-access-profile-user-virtual-key-creation-switch"
							checked={localConfig.skip_access_profile_user_virtual_key_creation ?? false}
							onCheckedChange={(checked) => handleConfigChange("skip_access_profile_user_virtual_key_creation", checked)}
						/>
					</div>
				)}
				{IS_ENTERPRISE && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="skip-access-profile-user-virtual-key-deletion" className="text-sm font-medium">
								Don&apos;t Delete User Virtual Keys When Access Profiles Are Removed
							</label>
							<p className="text-muted-foreground text-sm">
								When enabled, removing a user&apos;s last access profile keeps their virtual keys as ordinary keys, with none of the
								profile&apos;s providers or access. Deleting the user still deletes their keys.
							</p>
						</div>
						<Switch
							id="skip-access-profile-user-virtual-key-deletion"
							data-testid="security-skip-access-profile-user-virtual-key-deletion-switch"
							checked={localConfig.skip_access_profile_user_virtual_key_deletion ?? false}
							onCheckedChange={(checked) => handleConfigChange("skip_access_profile_user_virtual_key_deletion", checked)}
						/>
					</div>
				)}
				{/* OAuth issuer URL: shared by MCP OAuth and Cross App Access */}
				<div className="space-y-2 rounded-sm border p-4" data-testid="oauth2-issuer-url-section">
					<div className="space-y-0.5">
						<label htmlFor="oauth2-issuer-url" className="text-sm font-medium">
							{t("security.oauthIssuerUrl")}
						</label>
						<p className="text-muted-foreground text-sm">
							<Trans
								t={t}
								i18nKey="security.oauthIssuerUrlHelp"
								components={{
									code0: <code className="text-xs" />,
									code1: <code className="text-xs" />,
									code2: <code className="text-xs" />,
								}}
							/>
						</p>
					</div>
					<SecretVarInput
						id="oauth2-issuer-url"
						data-testid="oauth2-issuer-url-input"
						placeholder={t("mcpSettings.issuerUrlPlaceholder")}
						value={localConfig.oauth2_server_config?.issuer_url}
						onChange={(value: SecretVar) =>
							setLocalConfig((prev) => ({ ...prev, oauth2_server_config: { ...prev.oauth2_server_config, issuer_url: value } }))
						}
						disabled={!hasSettingsUpdateAccess}
					/>
					{isSecretVarSet(config?.oauth2_server_config?.issuer_url) &&
						!secretVarEquals(localConfig.oauth2_server_config?.issuer_url, config?.oauth2_server_config?.issuer_url) && (
							<Alert variant="warning" data-testid="oauth2-issuer-url-change-warning">
								<AlertTriangle className="size-4" />
								<AlertDescription>
									{t("security.issuerChangeWarning")}
								</AlertDescription>
							</Alert>
						)}
				</div>
				{/* Allowed Origins */}
				{needsRestart && <RestartWarning />}
				<div>
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="allowed-origins" className="text-sm font-medium">
								{t("security.allowedOrigins")}
							</label>
							<p className="text-muted-foreground text-sm">{t("security.originsHelp")}</p>
						</div>
						<Textarea
							id="allowed-origins"
							className="h-24"
							placeholder={t("security.originsPlaceholder")}
							value={localValues.allowed_origins}
							onChange={(e) => handleAllowedOriginsChange(e.target.value)}
						/>
					</div>
				</div>
				<div>
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="allowed-headers" className="text-sm font-medium">
								{t("security.allowedHeaders")}
							</label>
							<p className="text-muted-foreground text-sm">{t("security.allowedHeadersHelp")}</p>
						</div>
						<Textarea
							id="allowed-headers"
							className="h-24"
							placeholder={t("security.headersPlaceholder")}
							value={localValues.allowed_headers}
							onChange={(e) => handleAllowedHeadersChange(e.target.value)}
						/>
					</div>
				</div>
				<div>
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="required-headers" className="text-sm font-medium">
								{t("security.requiredHeaders")}
							</label>
							<p className="text-muted-foreground text-sm">{t("security.requiredHeadersHelp")}</p>
						</div>
						<Textarea
							id="required-headers"
							data-testid="required-headers-textarea"
							className="h-24"
							placeholder={t("security.requiredHeadersPlaceholder")}
							value={localValues.required_headers}
							onChange={(e) => handleRequiredHeadersChange(e.target.value)}
						/>
					</div>
				</div>
				<div>
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="whitelisted-routes" className="text-sm font-medium">
								{t("security.whitelistedRoutes")}
							</label>
							<p className="text-muted-foreground text-sm">
								<Trans t={t} i18nKey="security.routesHelp" components={{ b0: <b />, b1: <b />, b2: <b /> }} />
							</p>
						</div>
						<Textarea
							id="whitelisted-routes"
							data-testid="whitelisted-routes-textarea"
							className="h-24"
							placeholder={t("security.routesPlaceholder")}
							value={localValues.whitelisted_routes}
							onChange={(e) => handleWhitelistedRoutesChange(e.target.value)}
						/>
					</div>
				</div>
			</div>
			<div className="bg-card sticky bottom-0 flex justify-end py-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess} data-testid="security-save-btn">
					{isLoading ? t("saving") : t("saveChanges")}
				</Button>
			</div>
			<Dialog open={!!setupTokenErrorMessage} onOpenChange={(open) => !open && setSetupTokenErrorMessage(null)}>
				<DialogContent data-testid="setup-token-error-dialog">
					<DialogHeader>
						<DialogTitle>{t("security.setupTokenRequired")}</DialogTitle>
						<DialogDescription>{setupTokenErrorMessage}</DialogDescription>
					</DialogHeader>
					<DialogFooter>
						<Button variant="outline" onClick={() => setSetupTokenErrorMessage(null)} data-testid="setup-token-error-close">
							{t("security.close")}
						</Button>
						<Button asChild data-testid="setup-token-error-view-docs">
							<a href="https://docs.getbifrost.ai/quickstart/gateway/setting-up-auth" target="_blank" rel="noopener noreferrer">
								{t("security.viewDocs")}
							</a>
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</div>
	);
}

const RestartWarning = () => {
	const { t } = useTranslation("config");
	return (
		<Alert variant="destructive" className="mt-2">
			<AlertTriangle className="h-4 w-4" />
			<AlertDescription>{t("restartWarning")}</AlertDescription>
		</Alert>
	);
};