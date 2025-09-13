"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import GradientHeader from "@/components/ui/gradientHeader";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { GithubLogoIcon } from "@phosphor-icons/react";
import { AlertTriangle, ChevronRight, Code, Container, Database, Info, Monitor, Puzzle, Shield, Terminal, Zap } from "lucide-react";
import { useTheme } from "next-themes";
import Image from "next/image";
import Link from "next/link";

const featuredPlugins = [
	{
		name: "maxim",
		displayName: "Maxim Logger",
		description: "Advanced LLM observability, tracing, and analytics platform integration",
		category: "Observability",
		status: "production",
		httpSupport: true,
		capabilities: ["Real-time LLM tracing", "Performance analytics", "Cost tracking", "Error monitoring", "Custom session tracking"],
		icon: Monitor,
		color: "bg-blue-500",
		url: "https://github.com/maximhq/bifrost/tree/main/plugins/maxim",
		quickStart: {
			http: "bifrost-http --plugins maxim",
			docker: "docker run -e APP_PLUGINS=maxim bifrost-transport",
		},
	},
	{
		name: "mocker",
		displayName: "Response Mocker",
		description: "Mock AI responses for testing, development, and cost-effective prototyping",
		category: "Development",
		status: "production",
		httpSupport: false,
		capabilities: [
			"Configurable mock responses",
			"Request pattern matching",
			"Development environment support",
			"Cost-free testing",
			"Latency simulation",
		],
		icon: Code,
		color: "bg-blue-500",
		url: "https://github.com/maximhq/bifrost/tree/main/plugins/mocker",
		quickStart: {
			http: "HTTP support coming soon",
			docker: "HTTP support coming soon",
		},
	},
	{
		name: "circuit-breaker",
		displayName: "Circuit Breaker",
		description: "Resilience patterns for handling provider failures and preventing cascade errors",
		category: "Reliability",
		status: "enterprise",
		httpSupport: false,
		capabilities: ["Automatic failure detection", "Fallback mechanisms", "Rate limiting", "Health monitoring", "Recovery strategies"],
		icon: Shield,
		color: "bg-orange-500",
		url: "https://github.com/maximhq/bifrost/tree/main/plugins/circuitbreaker",
		quickStart: {
			http: "HTTP support coming soon",
			docker: "HTTP support coming soon",
		},
	},
];

const upcomingPlugins = [
	{
		name: "Redis Cache",
		description: "High-performance caching layer with Redis backend",
		icon: Database,
		status: "coming-soon",
	},
	{
		name: "Auth Guard",
		description: "Enterprise authentication and authorization middleware",
		icon: Shield,
		status: "coming-soon",
	},
	{
		name: "Rate Limiter",
		description: "Advanced rate limiting with multiple strategies",
		icon: Zap,
		status: "coming-soon",
	},
];

export default function PluginsPage() {
	const { resolvedTheme } = useTheme();
	return (
		<div className="dark:bg-card min-h-screen bg-white">
			<div className="mx-auto max-w-7xl">
				<div className="space-y-12">
					{/* Hero Section */}
					<div className="space-y-4 text-center">
						<div className="bg-primary/10 text-primary inline-flex items-center gap-2 rounded-full px-4 py-2 text-sm">
							<Puzzle className="h-4 w-4" />
							<span className="font-semibold">Plugin Ecosystem</span>
							<Badge variant="default" className="ml-1 text-xs">
								Beta
							</Badge>
						</div>

						<GradientHeader title="Supercharge Bifrost" />

						<p className="text-muted-foreground mx-auto max-w-3xl text-lg leading-relaxed">
							Extend Bifrost with powerful plugins for observability, testing, security, and custom business logic. Full support in Go SDK,
							with HTTP transport integration in active development.
						</p>

						<div className="flex flex-col items-center justify-center gap-4 sm:flex-row">
							<Button size="lg" asChild>
								<Link href="https://github.com/maximhq/bifrost/tree/main/plugins" target="_blank">
									<GithubLogoIcon className="mr-2 h-5 w-5" weight="bold" />
									Browse All Plugins
								</Link>
							</Button>
						</div>
					</div>

					{/* HTTP Transport Status */}
					<Alert className="border-amber-200 bg-amber-50 dark:border-amber-800 dark:bg-amber-950/20">
						<AlertTriangle className="h-4 w-4 text-amber-600" />
						<AlertDescription className="text-amber-800 dark:text-amber-200">
							HTTP transport support for custom and third party plugins is currently in active development and will be available soon.
						</AlertDescription>
					</Alert>

					{/* Featured Plugins */}
					<section className="space-y-8">
						<div className="text-center">
							<h2 className="mb-4 text-3xl font-bold">Featured Plugins</h2>
							<p className="text-muted-foreground text-lg">Production-ready plugins with varying levels of HTTP transport support</p>
						</div>

						<div className="grid gap-8 lg:grid-cols-3">
							{featuredPlugins.map((plugin) => {
								const Icon = plugin.icon;
								return (
									<Card
										key={plugin.name}
										className="hover:border-primary/50 group border-1 shadow-none transition-all duration-300 hover:shadow-xl"
									>
										<CardHeader>
											<div className="flex items-start justify-between">
												{plugin.name == "maxim" ? (
													<Image
														src={`/maxim-logo${resolvedTheme === "dark" ? "-dark" : ""}.png`}
														alt="Maxim"
														width={32}
														height={32}
														className="h-14 w-auto"
													/>
												) : (
													<div className={`rounded-xl p-3 ${plugin.color} bg-opacity-10`}>
														<Icon className={`h-8 w-8 ${plugin.color.replace("bg-", "text-")}`} />
													</div>
												)}

												<Badge variant={plugin.status === "production" ? "default" : "secondary"} className="text-xs capitalize">
													{plugin.status}
												</Badge>
											</div>

											<div className="space-y-2">
												<CardTitle className="group-hover:text-primary text-xl transition-colors">{plugin.displayName}</CardTitle>
												<Badge variant="outline" className="w-fit text-xs">
													{plugin.category}
												</Badge>
											</div>

											<CardDescription className="text-base leading-relaxed">{plugin.description}</CardDescription>
										</CardHeader>

										<CardContent className="flex h-full flex-col justify-between gap-6">
											<div className="space-y-6">
												<div className="space-y-3">
													<h4 className="text-muted-foreground text-sm font-semibold tracking-wide uppercase">Key Features</h4>
													<div className="grid gap-2">
														{plugin.capabilities.slice(0, 3).map((capability) => (
															<div key={capability} className="flex items-center gap-2 text-sm">
																<ChevronRight className="text-primary h-3 w-3" />
																{capability}
															</div>
														))}
													</div>
												</div>

												{plugin.httpSupport ? (
													<Tabs defaultValue="http" className="w-full">
														<TabsList className="grid w-full grid-cols-2">
															<TabsTrigger value="http" className="text-xs">
																HTTP
															</TabsTrigger>
															<TabsTrigger value="docker" className="text-xs">
																Docker
															</TabsTrigger>
														</TabsList>

														<TabsContent value="http" className="mt-3">
															<div className="bg-muted rounded-sm p-3">
																<div className="mb-2 flex items-center gap-2">
																	<Terminal className="h-3 w-3" />
																	<span className="text-xs font-semibold">Command Line</span>
																</div>
																<code className="block font-mono text-xs">{plugin.quickStart.http}</code>
															</div>
														</TabsContent>

														<TabsContent value="docker" className="mt-3">
															<div className="bg-muted rounded-sm p-3">
																<div className="mb-2 flex items-center gap-2">
																	<Container className="h-3 w-3" />
																	<span className="text-xs font-semibold">Docker Environment</span>
																</div>
																<code className="block font-mono text-xs">{plugin.quickStart.docker}</code>
															</div>
														</TabsContent>
													</Tabs>
												) : (
													<div className="mt-3 rounded-sm border border-amber-200 bg-amber-50 p-3 dark:border-amber-800 dark:bg-amber-950/20">
														<div className="flex items-center gap-2 text-amber-700 dark:text-amber-300">
															<Info className="h-3 w-3" />
															<span className="text-xs font-semibold">HTTP transport support coming soon</span>
														</div>
													</div>
												)}
											</div>

											<div className="space-y-2">
												<Button asChild variant="outline" className="w-full">
													<Link href={plugin.url} target="_blank">
														<Code className="mr-1 h-4 w-4" />
														Source Code
													</Link>
												</Button>
												<Button asChild variant="outline" className="w-full">
													<Link href={plugin.url + "/README.md"} target="_blank">
														<Info className="mr-1 h-4 w-4" />
														Plugin Documentation
													</Link>
												</Button>
											</div>
										</CardContent>
									</Card>
								);
							})}
						</div>
					</section>

					{/* Usage Patterns */}
					<section className="space-y-8">
						<div className="text-center">
							<h2 className="mb-4 text-3xl font-bold">Usage Patterns</h2>
							<p className="text-muted-foreground text-lg">Multiple ways to integrate plugins into your workflow</p>
						</div>

						<div className="grid gap-6 md:grid-cols-3">
							<Card className="border-blue-200 bg-blue-50 dark:border-blue-800 dark:bg-blue-950/20">
								<CardHeader>
									<div className="flex items-center gap-3">
										<Terminal className="h-8 w-8 text-blue-600" />
										<div>
											<CardTitle className="text-blue-800 dark:text-blue-200">HTTP Transport</CardTitle>
											<CardDescription className="text-blue-700 dark:text-blue-300">Maxim plugin only (for now)</CardDescription>
										</div>
									</div>
								</CardHeader>
								<CardContent className="space-y-3">
									<div className="rounded-sm bg-blue-100 p-3 dark:bg-blue-900">
										<code className="font-mono text-sm text-blue-800 dark:text-blue-200">bifrost-http --plugins maxim</code>
									</div>
									<p className="text-sm text-blue-700 dark:text-blue-300">Additional plugins coming soon</p>
								</CardContent>
							</Card>

							<Card className="border-purple-200 bg-purple-50 dark:border-purple-800 dark:bg-purple-950/20">
								<CardHeader>
									<div className="flex items-center gap-3">
										<Container className="h-8 w-8 text-purple-600" />
										<div>
											<CardTitle className="text-purple-800 dark:text-purple-200">Docker Deployment</CardTitle>
											<CardDescription className="text-purple-700 dark:text-purple-300">Environment variables</CardDescription>
										</div>
									</div>
								</CardHeader>
								<CardContent className="space-y-3">
									<div className="rounded-sm bg-purple-100 p-3 dark:bg-purple-900">
										<code className="font-mono text-sm text-purple-800 dark:text-purple-200">docker run -e APP_PLUGINS=maxim</code>
									</div>
									<p className="text-sm text-purple-700 dark:text-purple-300">Additional plugins coming soon</p>
								</CardContent>
							</Card>

							<Card className="border-green-200 bg-green-50 dark:border-green-800 dark:bg-green-950/20">
								<CardHeader>
									<div className="flex items-center gap-3">
										<Code className="h-8 w-8 text-green-600" />
										<div>
											<CardTitle className="text-green-800 dark:text-green-200">Go SDK</CardTitle>
											<CardDescription className="text-green-700 dark:text-green-300">Full plugin ecosystem</CardDescription>
										</div>
									</div>
								</CardHeader>
								<CardContent className="space-y-3">
									<div className="rounded-sm bg-green-100 p-3 dark:bg-green-900">
										<code className="font-mono text-sm text-green-800 dark:text-green-200">Plugins: []schemas.Plugin{`{...}`}</code>
									</div>
									<p className="text-sm text-green-700 dark:text-green-300">All plugins available</p>
								</CardContent>
							</Card>
						</div>
					</section>

					{/* Coming Soon */}
					<section className="space-y-8">
						<div className="text-center">
							<h2 className="mb-4 text-3xl font-bold">Coming Soon</h2>
							<p className="text-muted-foreground text-lg">Exciting plugins currently in development</p>
						</div>

						<div className="grid gap-6 md:grid-cols-3">
							{upcomingPlugins.map((plugin) => {
								const Icon = plugin.icon;
								return (
									<Card key={plugin.name} className="border-muted-foreground/30 border-2 border-dashed">
										<CardHeader>
											<div className="flex items-center justify-between">
												<div className="flex items-center gap-3">
													<div className="bg-muted rounded-lg p-2">
														<Icon className="text-muted-foreground h-6 w-6" />
													</div>
													<div>
														<CardTitle className="text-muted-foreground text-lg">{plugin.name}</CardTitle>
														<Badge variant="secondary" className="mt-1 text-xs">
															Coming Soon
														</Badge>
													</div>
												</div>
											</div>
											<CardDescription className="text-muted-foreground">{plugin.description}</CardDescription>
										</CardHeader>
									</Card>
								);
							})}
						</div>
					</section>

					{/* Community & Resources */}
					<section className="from-primary/5 rounded-2xl bg-gradient-to-r to-green-600/5 p-8">
						<div className="space-y-6 text-center">
							<h2 className="text-3xl font-bold">Join the Plugin Ecosystem</h2>
							<p className="text-muted-foreground mx-auto max-w-2xl text-lg">
								Contribute to the growing collection of Bifrost plugins or build your own custom solutions
							</p>

							<div className="flex flex-col justify-center gap-4 sm:flex-row">
								<Button size="lg" asChild>
									<Link href="https://github.com/maximhq/bifrost/tree/main/plugins" target="_blank">
										<GithubLogoIcon className="mr-2 h-5 w-5" weight="bold" />
										Plugin Repository
									</Link>
								</Button>
								<Button size="lg" variant="outline" asChild>
									<Link href="https://docs.getbifrost.ai/architecture/core/concurrency" target="_blank">
										<Puzzle className="mr-2 h-5 w-5" />
										Architecture Docs
									</Link>
								</Button>
							</div>
						</div>
					</section>
				</div>
			</div>
		</div>
	);
}
