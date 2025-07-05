import Header from "@/components/header";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Puzzle, ExternalLink, Code, TestTube, User, Star, Download, Plus } from "lucide-react";
import Link from "next/link";

const availablePlugins = [
	{
		name: "maxim",
		description: "Core plugin for Maxim-specific functionality and integrations",
		author: "Maxim Team",
		version: "1.0.0",
		status: "stable",
		language: "Go",
		url: "https://github.com/maximhq/bifrost/tree/main/plugins/maxim",
	},
	{
		name: "mocker",
		description: "Mock responses for testing and development environments",
		author: "Maxim Team",
		version: "1.0.0",
		status: "stable",
		language: "Go",
		url: "https://github.com/maximhq/bifrost/tree/main/plugins/mocker",
	},
];

const pluginCategories = [
	{
		title: "Authentication & Security",
		description: "Plugins for enhanced security, authentication, and access control",
		icon: User,
		count: 0,
		upcoming: true,
	},
	{
		title: "Monitoring & Analytics",
		description: "Advanced monitoring, metrics collection, and analytics plugins",
		icon: TestTube,
		count: 0,
		upcoming: true,
	},
	{
		title: "Request Processing",
		description: "Custom request/response transformation and processing plugins",
		icon: Code,
		count: 2,
		upcoming: false,
	},
];

export default function PluginsPage() {
	return (
		<div className="bg-background">
			<Header title="Plugins" />
			<div className="mx-auto max-w-7xl p-8">
				<div className="space-y-8">
					{/* Header */}
					<div className="space-y-4 text-center">
						<div className="bg-primary/10 text-primary inline-flex items-center gap-2 rounded-full px-3 py-1 text-sm font-medium">
							<Puzzle className="h-4 w-4" />
							Plugin System
							<Badge variant="secondary" className="ml-2 text-xs">
								Beta
							</Badge>
						</div>
						<h1 className="text-4xl font-bold">Bifrost Plugins</h1>
						<p className="text-muted-foreground mx-auto max-w-2xl text-lg">
							Extend Bifrost&apos;s functionality with powerful plugins. Build custom middleware, add new providers, or integrate with
							external systems.
						</p>
					</div>

					{/* Plugin Categories */}
					<div className="grid gap-6 md:grid-cols-3">
						{pluginCategories.map((category) => {
							const Icon = category.icon;
							return (
								<Card key={category.title} className="group transition-all duration-200 hover:shadow-lg">
									<CardHeader>
										<div className="flex items-center justify-between">
											<div className="bg-primary/10 group-hover:bg-primary/20 mb-4 flex h-12 w-12 items-center justify-center rounded-lg transition-colors">
												<Icon className="text-primary h-6 w-6" />
											</div>
											<Badge variant={category.upcoming ? "secondary" : "default"} className="text-xs">
												{category.upcoming ? "Coming Soon" : `${category.count} Available`}
											</Badge>
										</div>
										<CardTitle className="text-xl">{category.title}</CardTitle>
										<CardDescription className="leading-relaxed">{category.description}</CardDescription>
									</CardHeader>
								</Card>
							);
						})}
					</div>

					{/* Available Plugins */}
					<div className="space-y-6">
						<div className="flex items-center justify-between">
							<div>
								<h2 className="text-2xl font-bold">Available Plugins</h2>
								<p className="text-muted-foreground">Plugins currently available in the Bifrost repository</p>
							</div>
							<Button variant="outline" asChild>
								<Link href="https://github.com/maximhq/bifrost/tree/main/docs/contributing/plugin.md" target="_blank">
									<Plus className="mr-2 h-4 w-4" />
									Create Plugin
								</Link>
							</Button>
						</div>

						<div className="grid gap-6 md:grid-cols-2">
							{availablePlugins.map((plugin) => (
								<Card key={plugin.name} className="group transition-all duration-200 hover:shadow-lg">
									<CardHeader>
										<div className="flex items-center justify-between">
											<div className="flex items-center gap-3">
												<div className="bg-primary/10 flex h-10 w-10 items-center justify-center rounded-lg">
													<Puzzle className="text-primary h-5 w-5" />
												</div>
												<div>
													<CardTitle className="text-xl">{plugin.name}</CardTitle>
													<div className="mt-1 flex items-center gap-2">
														<Badge variant="outline" className="text-xs">
															v{plugin.version}
														</Badge>
														<Badge variant="secondary" className="text-xs">
															{plugin.language}
														</Badge>
														<Badge variant={plugin.status === "stable" ? "default" : "secondary"} className="text-xs">
															{plugin.status}
														</Badge>
													</div>
												</div>
											</div>
										</div>
										<CardDescription className="leading-relaxed">{plugin.description}</CardDescription>
									</CardHeader>
									<CardContent className="space-y-4">
										<div className="flex items-center justify-between text-sm">
											<span className="text-muted-foreground">Author: {plugin.author}</span>
											<div className="text-muted-foreground flex items-center gap-1">
												<Star className="h-3 w-3" />
												<span>Official</span>
											</div>
										</div>
										<div className="flex gap-2">
											<Button asChild variant="outline" className="flex-1">
												<Link href={plugin.url} target="_blank" className="flex items-center justify-center gap-2">
													<Code className="h-4 w-4" />
													View Source
												</Link>
											</Button>
											<Button asChild size="sm" variant="ghost">
												<Link href={plugin.url} target="_blank">
													<ExternalLink className="h-4 w-4" />
												</Link>
											</Button>
										</div>
									</CardContent>
								</Card>
							))}
						</div>
					</div>

					{/* Plugin Development */}
					<div className="grid gap-6 pt-8 md:grid-cols-2">
						<Card className="border-primary/20 bg-primary/5">
							<CardHeader>
								<CardTitle className="flex items-center gap-2">
									<Code className="text-primary h-5 w-5" />
									Plugin Development
								</CardTitle>
								<CardDescription>Learn how to create custom plugins for Bifrost</CardDescription>
							</CardHeader>
							<CardContent>
								<p className="text-muted-foreground mb-4 text-sm">
									Build custom middleware, add new providers, or create specialized request processors with the Bifrost plugin system.
								</p>
								<Button asChild className="w-full">
									<Link href="https://github.com/maximhq/bifrost/tree/main/docs/contributing/plugin.md" target="_blank">
										<Code className="mr-2 h-4 w-4" />
										Plugin Guide
									</Link>
								</Button>
							</CardContent>
						</Card>

						<Card className="border-green-200 bg-green-50 dark:border-green-800 dark:bg-green-950/20">
							<CardHeader>
								<CardTitle className="flex items-center gap-2">
									<Download className="h-5 w-5 text-green-600" />
									Plugin Architecture
								</CardTitle>
								<CardDescription>Understand how plugins work in Bifrost</CardDescription>
							</CardHeader>
							<CardContent>
								<p className="text-muted-foreground mb-4 text-sm">
									Explore the plugin architecture, lifecycle hooks, and best practices for building robust plugins.
								</p>
								<Button asChild variant="outline" className="w-full">
									<Link href="https://github.com/maximhq/bifrost/tree/main/docs/architecture/plugins.md" target="_blank">
										<Puzzle className="mr-2 h-4 w-4" />
										Architecture Docs
									</Link>
								</Button>
							</CardContent>
						</Card>
					</div>
				</div>
			</div>
		</div>
	);
}
