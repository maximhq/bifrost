import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import GradientHeader from "@/components/ui/gradientHeader";
import { BookOpen, Code, ExternalLink, FileText, GitBranch, Play, Shield, Users, Zap } from "lucide-react";

const docSections = [
	{
		title: "快速开始",
		description: "30 秒内运行 Bifrost",
		icon: Play,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/quickstart",
		badge: "Popular",
		items: ["HTTP Transport Setup", "Go Package Usage", "Docker Guide"],
	},
	{
		title: "架构",
		description: "深入了解 Bifrost 的设计和性能",
		icon: GitBranch,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/architecture",
		items: ["System Overview", "Request Flow", "Concurrency Model", "Design Decisions"],
	},
	{
		title: "使用指南",
		description: "完整的 API 参考和配置指南",
		icon: BookOpen,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/usage",
		badge: "Comprehensive",
		items: ["Providers Setup", "Key Management", "Error Handling", "Memory & Networking"],
	},
	{
		title: "参与贡献",
		description: "帮助改进 Bifrost，造福所有人",
		icon: Users,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/contributing",
		items: ["Contributing Guide", "Adding Providers", "Plugin Development", "Code Conventions"],
	},
	{
		title: "集成示例",
		description: "实用示例和测试代码",
		icon: Code,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/usage/http-transport/integrations",
		items: ["OpenAI Integration", "Anthropic Integration", "GenAI Integration", "Migration Guides"],
	},
	{
		title: "基准测试",
		description: "性能指标和指南",
		icon: Zap,
		url: "https://github.com/maximhq/bifrost/blob/main/docs/benchmarks.md",
		items: ["5K RPS Test Results", "Performance Metrics", "Configuration Tuning", "Hardware Comparisons"],
	},
];

const featuredDocs = [
	{
		title: "MCP 文档",
		description: "模型上下文协议集成综合指南",
		content: "Learn how to build sophisticated AI agents with MCP support, tool calling, and external integrations.",
		href: "https://github.com/maximhq/bifrost/blob/main/docs/mcp.md",
		icon: FileText,
		buttonText: "View MCP Guide",
		borderColor: "border-primary/20",
		backgroundColor: "bg-primary/5",
		iconColor: "text-primary",
	},
	{
		title: "治理插件",
		description: "完整的访问控制、预算和速率限制指南",
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
							<span className="font-semibold">文档</span>
						</div>
						<GradientHeader title="为您的 Bifrost 技术栈赋能" />
						<p className="text-muted-foreground mx-auto max-w-2xl text-lg">使用 Bifrost 构建生产级 AI 应用所需的一切</p>
						<div className="flex justify-center gap-4">
							<Button asChild>
								<a
									href="https://github.com/maximhq/bifrost/tree/main/docs"
									target="_blank"
									rel="noopener noreferrer"
									data-testid="docs-view-full-documentation-link"
								>
									<ExternalLink className="mr-2 h-4 w-4" />查看完整文档</a>
							</Button>
							<Button variant="outline" asChild>
								<a
									href="https://github.com/maximhq/bifrost/tree/main/docs/quickstart"
									target="_blank"
									rel="noopener noreferrer"
									data-testid="docs-quick-start-guide-link"
								>
									<Play className="mr-2 h-4 w-4" />快速入门指南</a>
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
											<a
												href={section.url}
												target="_blank"
												rel="noopener noreferrer"
												className="flex items-center justify-center gap-2"
												data-testid={`docs-read-more-${section.title.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`}
											>阅读更多<ExternalLink className="h-4 w-4" />
											</a>
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
										<a
											href={doc.href}
											target="_blank"
											rel="noopener noreferrer"
											data-testid={`docs-featured-${doc.title.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`}
										>
											<doc.icon className="mr-2 h-4 w-4" />
											{doc.buttonText}
										</a>
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