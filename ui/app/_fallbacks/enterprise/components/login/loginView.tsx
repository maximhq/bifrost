"use client";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { getErrorMessage, useIsAuthEnabledQuery, useLoginMutation } from "@/lib/store/apis";
import { BooksIcon, DiscordLogoIcon, GithubLogoIcon } from "@phosphor-icons/react";
import { Eye, EyeOff } from "lucide-react";
import { useTheme } from "next-themes";
import Image from "next/image";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";

const GoogleIcon = () => (
	<svg viewBox="0 0 24 24" className="mr-2 h-4 w-4">
		<path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z" fill="#4285F4"/>
		<path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853"/>
		<path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" fill="#FBBC05"/>
		<path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#EA4335"/>
	</svg>
);

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
	const { resolvedTheme } = useTheme();
	const [mounted, setMounted] = useState(false);
	const [username, setUsername] = useState("");
	const [password, setPassword] = useState("");
	const [showPassword, setShowPassword] = useState(false);
	const [errorMessage, setErrorMessage] = useState("");
	const [isCheckingAuth, setIsCheckingAuth] = useState(true);
	const router = useRouter();
	const [isLoading, setIsLoading] = useState(false);
	const { data: isAuthEnabledData, isLoading: isLoadingIsAuthEnabled, error: isAuthEnabledError } = useIsAuthEnabledQuery();
	const isAuthEnabled = isAuthEnabledData?.is_auth_enabled || false;
	const hasValidToken = isAuthEnabledData?.has_valid_token || false;
	const [login, { isLoading: isLoggingIn }] = useLoginMutation();

	const showPasswordForm = !isAuthEnabledData?.enabled_methods || isAuthEnabledData.enabled_methods.includes("password");
	const hasSSO = !!(isAuthEnabledData?.google_sso || isAuthEnabledData?.saml);

	useEffect(() => {
		setMounted(true);
		const params = new URLSearchParams(window.location.search);
		if (params.get("error")) {
			setErrorMessage(params.get("message") || "SSO login failed. Please try again.");
		}
	}, []);

	// Check auth status on component mount
	useEffect(() => {
		if (isLoadingIsAuthEnabled) {
			return;
		}
		if (isAuthEnabledError) {
			setErrorMessage("Unable to verify authentication status. Please retry.");
			return;
		}
		if (!isAuthEnabled || hasValidToken) {
			router.push("/workspace");
			return;
		}
		// Auth is enabled but user is not logged in, show login form
		setIsCheckingAuth(false);
	}, [isLoadingIsAuthEnabled]);

	const handleSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
		setIsLoading(true);
		e.preventDefault();
		setErrorMessage("");
		try {
			await login({ username, password }).unwrap();
			// Cookie is set automatically by the server response — just navigate
			router.push("/workspace");
		} catch (error) {
			const message = getErrorMessage(error);
			setErrorMessage(message);
		} finally {
			setIsLoading(false);
		}
	};

	// Use light logo for SSR to avoid hydration mismatch
	const logoSrc = mounted && resolvedTheme === "dark" ? "/bifrost-logo-dark.png" : "/bifrost-logo.png";

	// Show loading state while checking auth
	if (isCheckingAuth || isLoadingIsAuthEnabled) {
		return (
			<div className="flex min-h-screen items-center justify-center p-4">
				<div className="w-full max-w-md">
					<div className="border-border bg-card w-full space-y-6 rounded-sm border p-8 ">
						<div className="flex items-center justify-center">
							<Image src={logoSrc} alt="Bifrost" width={160} height={26} priority className="" />
						</div>
						<div className="flex items-center justify-center py-8">
							<div className="text-muted-foreground text-sm">Checking authentication...</div>
						</div>
					</div>
				</div>
			</div>
		);
	}

	return (
		<div className="flex min-h-screen items-center justify-center p-4">
			<div className="w-full max-w-md">
				<div className="border-border bg-card w-full space-y-6 rounded-sm border p-8 ">
					{/* Logo */}
					<div className="flex items-center justify-center">
						<Image src={logoSrc} alt="Bifrost" width={160} height={26} priority className="" />
					</div>

					<div className="space-y-2 text-center">
						<h1 className="text-foreground text-lg font-semibold">Welcome back</h1>
						<p className="text-muted-foreground text-sm">Sign in to your account to continue</p>
					</div>

					{errorMessage && <div className="bg-destructive/10 text-destructive rounded-sm p-3 text-sm">{errorMessage}</div>}

					{hasSSO && (
						<div className="space-y-3">
							{isAuthEnabledData?.google_sso && (
								<Button
									variant="outline"
									className="h-9 w-full text-sm"
									onClick={() => window.location.href = isAuthEnabledData.google_sso!.login_url}
								>
									<GoogleIcon />
									Sign in with Google
								</Button>
							)}
							{isAuthEnabledData?.saml && (
								<Button
									variant="outline"
									className="h-9 w-full text-sm"
									onClick={() => window.location.href = isAuthEnabledData.saml!.login_url}
								>
									Sign in with SSO
								</Button>
							)}
						</div>
					)}

					{hasSSO && showPasswordForm && (
						<div className="relative my-4">
							<div className="absolute inset-0 flex items-center">
								<span className="w-full border-t" />
							</div>
							<div className="relative flex justify-center text-xs uppercase">
								<span className="bg-card px-2 text-muted-foreground">or</span>
							</div>
						</div>
					)}

					{showPasswordForm && (
						<form onSubmit={handleSubmit} className="space-y-5">
							<div className="space-y-2">
								<Label htmlFor="username" className="text-sm font-medium">
									Username
								</Label>
								<Input
									id="username"
									type="text"
									placeholder="Enter your username"
									value={username}
									onChange={(e) => setUsername(e.target.value)}
									required
									className="text-sm"
									autoComplete="username"
								/>
							</div>

							<div className="space-y-2">
								<Label htmlFor="password" className="text-sm font-medium">
									Password
								</Label>
								<div className="relative">
									<Input
										id="password"
										type={showPassword ? "text" : "password"}
										placeholder="Enter your password"
										value={password}
										onChange={(e) => setPassword(e.target.value)}
										required
										className="text-sm pr-10"
										autoComplete="current-password"
									/>
									<button
										type="button"
										onClick={() => setShowPassword(!showPassword)}
										className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
										aria-label={showPassword ? "Hide password" : "Show password"}
									>
										{showPassword ? (
											<EyeOff className="h-4 w-4" />
										) : (
											<Eye className="h-4 w-4" />
										)}
									</button>
								</div>
							</div>

							<Button type="submit" className="h-9 w-full text-sm" isLoading={isLoading} disabled={isLoading}>
								{isLoading || isLoggingIn ? "Signing in..." : "Sign in"}
							</Button>
						</form>
					)}

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
