import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import GradientHeader from "@/components/ui/gradientHeader";
import { BookOpen, Code, ExternalLink, FileText, GitBranch, Play, Shield, Users, Zap } from "lucide-react";
import Link from "next/link";

const docSections = [
	{
		title: "Quick Start",
		description: "Get Bifrost running in under 30 seconds",
		icon: Play,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/quickstart",
		badge: "Popular",
		items: ["HTTP Transport Setup", "Go Package Usage", "Docker Guide"],
	},
	{
		title: "Architecture",
		description: "Deep dive into Bifrost's design and performance",
		icon: GitBranch,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/architecture",
		items: ["System Overview", "Request Flow", "Concurrency Model", "Design Decisions"],
	},
	{
		title: "Usage Guides",
		description: "Complete API reference and configuration guides",
		icon: BookOpen,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/usage",
		badge: "Comprehensive",
		items: ["Providers Setup", "Key Management", "Error Handling", "Memory & Networking"],
	},
	{
		title: "Contributing",
		description: "Help improve Bifrost for everyone",
		icon: Users,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/contributing",
		items: ["Contributing Guide", "Adding Providers", "Plugin Development", "Code Conventions"],
	},
	{
		title: "Integration Examples",
		description: "Practical examples and testing code",
		icon: Code,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/usage/http-transport/integrations",
		items: ["OpenAI Integration", "Anthropic Integration", "GenAI Integration", "Migration Guides"],
	},
	{
		title: "Benchmarks",
		description: "Performance metrics and guides",
		icon: Zap,
		url: "https://github.com/maximhq/bifrost/blob/main/docs/benchmarks.md",
		items: ["5K RPS Test Results", "Performance Metrics", "Configuration Tuning", "Hardware Comparisons"],
	},
];

const featuredDocs = [
	{
		title: "MCP Documentation",
		description: "Comprehensive guide to Model Context Protocol integration",
		content: "Learn how to build sophisticated AI agents with MCP support, tool calling, and external integrations.",
		href: "https://github.com/maximhq/bifrost/blob/main/docs/mcp.md",
		icon: FileText,
		buttonText: "View MCP Guide",
		borderColor: "border-primary/20",
		backgroundColor: "bg-primary/5",
		iconColor: "text-primary",
	},
	{
		title: "Governance Plugin",
		description: "Complete access control, budgets, and rate limiting guide",
		content: "Master Virtual Keys, hierarchical budgets, rate limiting, and usage tracking for secure AI infrastructure.",
		href: "https://github.com/maximhq/bifrost/blob/main/docs/governance.md",
		icon: Shield,
		buttonText: "View Governance Guide",
		borderColor: "border-green-200 dark:border-green-800",
		backgroundColor: "bg-green-50 dark:bg-green-950/20",
		iconColor: "text-green-600",
	},
];

export default function DocsPage() {
	return (
		<div className="dark:bg-card bg-white">
			<div className="mx-auto max-w-7xl">
				<div className="space-y-8">
					{/* Header */}
					<div className="space-y-4 text-center">
						<div className="bg-primary/10 text-primary inline-flex items-center gap-2 rounded-full px-4 py-2 text-sm">
							<BookOpen className="h-4 w-4" />
							<span className="font-semibold">Documentation</span>
						</div>
						<GradientHeader title="Power Up Your Bifrost Stack" />
						<p className="text-muted-foreground mx-auto max-w-2xl text-lg">
							Everything you need to know about building production AI applications with Bifrost
						</p>
						<div className="flex justify-center gap-4">
							<Button asChild>
								<Link href="https://github.com/maximhq/bifrost/tree/main/docs" target="_blank">
									<ExternalLink className="mr-2 h-4 w-4" />
									View Full Documentation
								</Link>
							</Button>
							<Button variant="outline" asChild>
								<Link href="https://github.com/maximhq/bifrost/tree/main/docs/quickstart" target="_blank">
									<Play className="mr-2 h-4 w-4" />
									Quick Start Guide
								</Link>
							</Button>
						</div>
					</div>

					{/* Documentation Sections */}
					<div className="grid gap-6 md:grid-cols-2 lg:grid-cols-3">
						{docSections.map((section) => {
							const Icon = section.icon;
							return (
								<Card key={section.title} className="group transition-all duration-200 hover:shadow-lg">
									<CardHeader>
										<div className="flex items-center justify-between">
											<div className="bg-primary/10 group-hover:bg-primary/20 mb-4 flex h-12 w-12 items-center justify-center rounded-lg transition-colors">
												<Icon className="text-primary h-6 w-6" />
											</div>
											{section.badge && (
												<Badge variant="secondary" className="text-xs">
													{section.badge}
												</Badge>
											)}
										</div>
										<CardTitle className="text-xl">{section.title}</CardTitle>
										<CardDescription className="leading-relaxed">{section.description}</CardDescription>
									</CardHeader>
									<CardContent className="flex h-full flex-col justify-between gap-8">
										<div className="space-y-4">
											<ul className="space-y-2">
												{section.items.map((item, index) => (
													<li key={index} className="text-muted-foreground flex items-center gap-2 text-sm">
														<div className="bg-primary h-1.5 w-1.5 rounded-full" />
														{item}
													</li>
												))}
											</ul>
										</div>
										<Button asChild variant="outline" className="w-full">
											<Link href={section.url} target="_blank" className="flex items-center justify-center gap-2">
												Read More
												<ExternalLink className="h-4 w-4" />
											</Link>
										</Button>
									</CardContent>
								</Card>
							);
						})}
					</div>

					{/* Featured Documentation */}
					<div className="grid gap-6 pt-8 md:grid-cols-2">
						{featuredDocs.map((doc, index) => (
							<Card className={`${doc.borderColor} ${doc.backgroundColor}`} key={index}>
								<CardHeader>
									<CardTitle className="flex items-center gap-2">
										<doc.icon className={`h-5 w-5 ${doc.iconColor}`} />
										{doc.title}
									</CardTitle>
									<CardDescription>{doc.description}</CardDescription>
								</CardHeader>
								<CardContent>
									<p className="text-muted-foreground mb-4 text-sm">{doc.content}</p>
									<Button asChild className="w-full">
										<Link href={doc.href} target="_blank">
											<doc.icon className="mr-2 h-4 w-4" />
											{doc.buttonText}
										</Link>
									</Button>
								</CardContent>
							</Card>
						))}
					</div>
				</div>
			</div>
		</div>
	);
}
