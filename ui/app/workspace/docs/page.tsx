import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import GradientHeader from "@/components/ui/gradientHeader";
import { useTranslation } from "react-i18next";
import { BookOpen, Code, ExternalLink, FileText, GitBranch, Play, Shield, Users, Zap } from "lucide-react";

const docSections = [
	{
		titleKey: "docs.quickStart",
		descriptionKey: "docs.quickStartDesc",
		icon: Play,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/quickstart",
		badgeKey: "docs.popular",
		items: ["docs.httpTransportSetup", "docs.goPackageUsage", "docs.dockerGuide"],
	},
	{
		titleKey: "docs.architecture",
		descriptionKey: "docs.architectureDesc",
		icon: GitBranch,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/architecture",
		items: ["docs.systemOverview", "docs.requestFlow", "docs.concurrencyModel", "docs.designDecisions"],
	},
	{
		titleKey: "docs.usageGuides",
		descriptionKey: "docs.usageGuidesDesc",
		icon: BookOpen,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/usage",
		badgeKey: "docs.comprehensive",
		items: ["docs.providersSetup", "docs.keyManagement", "docs.errorHandling", "docs.memoryNetworking"],
	},
	{
		titleKey: "docs.contributing",
		descriptionKey: "docs.contributingDesc",
		icon: Users,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/contributing",
		items: ["docs.contributingGuide", "docs.addingProviders", "docs.pluginDevelopment", "docs.codeConventions"],
	},
	{
		titleKey: "docs.integrationExamples",
		descriptionKey: "docs.integrationExamplesDesc",
		icon: Code,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/usage/http-transport/integrations",
		items: ["docs.openAiIntegration", "docs.anthropicIntegration", "docs.genAiIntegration", "docs.migrationGuides"],
	},
	{
		titleKey: "docs.benchmarks",
		descriptionKey: "docs.benchmarksDesc",
		icon: Zap,
		url: "https://github.com/maximhq/bifrost/blob/main/docs/benchmarks.md",
		items: ["docs.fiveKRpsTestResults", "docs.performanceMetrics", "docs.configurationTuning", "docs.hardwareComparisons"],
	},
];

const featuredDocs = [
	{
		titleKey: "docs.mcpDocumentation",
		descriptionKey: "docs.mcpDocumentationDesc",
		contentKey: "docs.mcpDocumentationContent",
		href: "https://github.com/maximhq/bifrost/blob/main/docs/mcp.md",
		icon: FileText,
		buttonTextKey: "docs.viewMcpGuide",
		borderColor: "border-primary/20",
		backgroundColor: "bg-primary/5",
		iconColor: "text-primary",
	},
	{
		titleKey: "docs.governancePlugin",
		descriptionKey: "docs.governancePluginDesc",
		contentKey: "docs.governancePluginContent",
		href: "https://github.com/maximhq/bifrost/blob/main/docs/governance.md",
		icon: Shield,
		buttonTextKey: "docs.viewGovernanceGuide",
		borderColor: "border-green-200 dark:border-green-800",
		backgroundColor: "bg-green-50 dark:bg-green-950/20",
		iconColor: "text-green-600",
	},
];

export default function DocsPage() {
	const { t } = useTranslation();

	return (
		<div className="dark:bg-card bg-white">
			<div className="mx-auto max-w-7xl">
				<div className="space-y-8">
					{/* Header */}
					<div className="space-y-4 text-center">
						<div className="bg-primary/10 text-primary inline-flex items-center gap-2 rounded-full px-4 py-2 text-sm">
							<BookOpen className="h-4 w-4" />
							<span className="font-semibold">{t("docs.documentation")}</span>
						</div>
						<GradientHeader title={t("docs.powerUpBifrostStack")} />
						<p className="text-muted-foreground mx-auto max-w-2xl text-lg">{t("docs.everythingNeeded")}</p>
						<div className="flex justify-center gap-4">
							<Button asChild>
								<a
									href="https://github.com/maximhq/bifrost/tree/main/docs"
									target="_blank"
									rel="noopener noreferrer"
									data-testid="docs-view-full-documentation-link"
								>
									<ExternalLink className="mr-2 h-4 w-4" />
									{t("docs.viewFullDocumentation")}
								</a>
							</Button>
							<Button variant="outline" asChild>
								<a
									href="https://github.com/maximhq/bifrost/tree/main/docs/quickstart"
									target="_blank"
									rel="noopener noreferrer"
									data-testid="docs-quick-start-guide-link"
								>
									<Play className="mr-2 h-4 w-4" />
									{t("docs.quickStartGuide")}
								</a>
							</Button>
						</div>
					</div>

					{/* Documentation Sections */}
					<div className="grid gap-6 md:grid-cols-2 lg:grid-cols-3">
						{docSections.map((section) => {
							const Icon = section.icon;
							return (
								<Card key={section.titleKey} className="group transition-all duration-200 hover:shadow-lg">
									<CardHeader>
										<div className="flex items-center justify-between">
											<div className="bg-primary/10 group-hover:bg-primary/20 mb-4 flex h-12 w-12 items-center justify-center rounded-lg transition-colors">
												<Icon className="text-primary h-6 w-6" />
											</div>
											{section.badgeKey && (
												<Badge variant="secondary" className="text-xs">
													{t(section.badgeKey)}
												</Badge>
											)}
										</div>
										<CardTitle className="text-xl">{t(section.titleKey)}</CardTitle>
										<CardDescription className="leading-relaxed">{t(section.descriptionKey)}</CardDescription>
									</CardHeader>
									<CardContent className="flex h-full flex-col justify-between gap-8">
										<div className="space-y-4">
											<ul className="space-y-2">
												{section.items.map((itemKey, index) => (
													<li key={index} className="text-muted-foreground flex items-center gap-2 text-sm">
														<div className="bg-primary h-1.5 w-1.5 rounded-full" />
														{t(itemKey)}
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
												data-testid={`docs-read-more-${section.titleKey.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`}
											>
												{t("docs.readMore")}
												<ExternalLink className="h-4 w-4" />
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
										{t(doc.titleKey)}
									</CardTitle>
									<CardDescription>{t(doc.descriptionKey)}</CardDescription>
								</CardHeader>
								<CardContent>
									<p className="text-muted-foreground mb-4 text-sm">{t(doc.contentKey)}</p>
									<Button asChild className="w-full">
										<a
											href={doc.href}
											target="_blank"
											rel="noopener noreferrer"
											data-testid={`docs-featured-${doc.titleKey.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`}
										>
											<doc.icon className="mr-2 h-4 w-4" />
											{t(doc.buttonTextKey)}
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