import { LanguageToggle } from "@/components/languageToggle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useT } from "@/lib/i18n";
import { useBranding } from "@/lib/hooks/useBranding";
import { getErrorMessage, useLoginMutation } from "@/lib/store/apis";
import { BooksIcon, DiscordLogoIcon, GithubLogoIcon } from "@phosphor-icons/react";
import { useNavigate } from "@tanstack/react-router";
import { Eye, EyeOff } from "lucide-react";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";

const externalLinks = [
	{
		title: "Discord Server",
		url: "https://discord.gg/exN5KAydbU",
		icon: DiscordLogoIcon,
	},
	{
		title: "GitHub Repository",
		url: "https://github.com/maximhq/bifrost",
		icon: GithubLogoIcon,
	},
	{
		title: "Full Documentation",
		url: "https://docs.getbifrost.ai",
		icon: BooksIcon,
		strokeWidth: 1,
	},
];

export default function LoginView() {
	const t = useT();
	const { resolvedTheme } = useTheme();
	const [mounted, setMounted] = useState(false);
	const [username, setUsername] = useState("");
	const [password, setPassword] = useState("");
	const [showPassword, setShowPassword] = useState(false);
	const [errorMessage, setErrorMessage] = useState("");
	const navigate = useNavigate();
	const [isLoading, setIsLoading] = useState(false);
	const [login, { isLoading: isLoggingIn }] = useLoginMutation();

	useEffect(() => {
		setMounted(true);
	}, []);

	const handleSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
		setIsLoading(true);
		e.preventDefault();
		setErrorMessage("");
		try {
			await login({ username, password }).unwrap();
			navigate({ to: "/workspace" });
		} catch (error) {
			const message = getErrorMessage(error);
			setErrorMessage(message);
		} finally {
			setIsLoading(false);
		}
	};

	const { logoSrc, logoAlt } = useBranding(mounted && resolvedTheme === "dark");

	return (
		<div className="flex min-h-screen items-center justify-center p-4">
			<div className="w-full max-w-md">
				<div className="border-border bg-card w-full space-y-6 rounded-sm border p-8">
					{/* Logo */}
					<div className="flex items-center justify-center">
						<img src={logoSrc} alt={logoAlt} width={160} height={26} className="max-h-[40px] w-auto max-w-[220px] object-contain" />
					</div>

					<div className="space-y-2 text-center">
						<h1 className="text-foreground text-lg font-semibold">{t("login.welcomeBack")}</h1>
						<p className="text-muted-foreground text-sm">{t("login.signInToContinue")}</p>
					</div>

					<form onSubmit={handleSubmit} className="space-y-5">
						{errorMessage && <div className="bg-destructive/10 text-destructive rounded-sm p-3 text-sm">{errorMessage}</div>}

						<div className="space-y-2">
							<Label htmlFor="username" className="text-sm font-medium">
								{t("login.username")}
							</Label>
							<Input
								id="username"
								type="text"
								placeholder={t("login.usernamePlaceholder")}
								value={username}
								onChange={(e) => setUsername(e.target.value)}
								required
								className="text-sm"
								autoComplete="username"
							/>
						</div>

						<div className="space-y-2">
							<Label htmlFor="password" className="text-sm font-medium">
								{t("login.password")}
							</Label>
							<div className="relative">
								<Input
									id="password"
									type={showPassword ? "text" : "password"}
									placeholder={t("login.passwordPlaceholder")}
									value={password}
									onChange={(e) => setPassword(e.target.value)}
									required
									className="pr-10 text-sm"
									autoComplete="current-password"
								/>
								<button
									type="button"
									onClick={() => setShowPassword(!showPassword)}
									className="text-muted-foreground hover:text-foreground absolute top-1/2 right-3 -translate-y-1/2 transition-colors"
									aria-label={showPassword ? t("login.hidePassword") : t("login.showPassword")}
								>
									{showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
								</button>
							</div>
						</div>

						<Button type="submit" className="h-9 w-full text-sm" isLoading={isLoading} disabled={isLoading}>
							{isLoading || isLoggingIn ? t("login.signingIn") : t("login.signIn")}
						</Button>
					</form>

					<div className="flex justify-center">
						<LanguageToggle />
					</div>

					{/* Social Links */}
					<div className="flex items-center justify-center gap-4 pt-4">
						{externalLinks.map((item, index) => (
							<a
								key={index}
								href={item.url}
								target="_blank"
								rel="noopener noreferrer"
								className="text-muted-foreground hover:text-primary transition-colors"
								title={item.title}
							>
								<item.icon className="h-5 w-5" size={20} weight="regular" strokeWidth={item.strokeWidth} />
							</a>
						))}
					</div>
				</div>
			</div>
		</div>
	);
}