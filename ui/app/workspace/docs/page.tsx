import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import GradientHeader from "@/components/ui/gradientHeader";
import { BookOpen, Code, ExternalLink, FileText, GitBranch, Play, Shield, Users, Zap } from "lucide-react";
import { useTranslation } from "react-i18next";

type DocSectionKey = "quickStart" | "architecture" | "usageGuides" | "contributing" | "integrationExamples" | "benchmarks";

const docSections: {
	key: DocSectionKey;
	testSlug: string; // keeps data-testid values stable across locales
	icon: typeof Play;
	url: string;
	badge?: "popular" | "comprehensive";
}[] = [
	{
		key: "quickStart",
		testSlug: "quick-start",
		icon: Play,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/quickstart",
		badge: "popular",
	},
	{
		key: "architecture",
		testSlug: "architecture",
		icon: GitBranch,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/architecture",
	},
	{
		key: "usageGuides",
		testSlug: "usage-guides",
		icon: BookOpen,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/usage",
		badge: "comprehensive",
	},
	{
		key: "contributing",
		testSlug: "contributing",
		icon: Users,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/contributing",
	},
	{
		key: "integrationExamples",
		testSlug: "integration-examples",
		icon: Code,
		url: "https://github.com/maximhq/bifrost/tree/main/docs/usage/http-transport/integrations",
	},
	{
		key: "benchmarks",
		testSlug: "benchmarks",
		icon: Zap,
		url: "https://github.com/maximhq/bifrost/blob/main/docs/benchmarks.md",
	},
];

const featuredDocs = [
	{
		key: "mcpDocs",
		testSlug: "mcp-documentation",
		href: "https://github.com/maximhq/bifrost/blob/main/docs/mcp.md",
		icon: FileText,
		borderColor: "border-primary/20",
		backgroundColor: "bg-primary/5",
		iconColor: "text-primary",
	},
	{
		key: "governancePlugin",
		testSlug: "governance-plugin",
		href: "https://github.com/maximhq/bifrost/blob/main/docs/governance.md",
		icon: Shield,
		borderColor: "border-green-200 dark:border-green-800",
		backgroundColor: "bg-green-50 dark:bg-green-950/20",
		iconColor: "text-green-600",
	},
] as const;

// Item lists are string arrays in config.json; the typed catalog treats arrays as
// leaves (see lib/i18n/i18next.d.ts), so check the shape here rather than cast it.
function docItems(value: unknown): string[] {
	return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

export default function DocsPage() {
	const { t } = useTranslation("config");
	return (
		<div className="dark:bg-card bg-white">
			<div className="mx-auto max-w-7xl">
				<div className="space-y-8">
					{/* Header */}
					<div className="space-y-4 text-center">
						<div className="bg-primary/10 text-primary inline-flex items-center gap-2 rounded-full px-4 py-2 text-sm">
							<BookOpen className="h-4 w-4" />
							<span className="font-semibold">{t("docs.title")}</span>
						</div>
						<GradientHeader title={t("docs.heading")} />
						<p className="text-muted-foreground mx-auto max-w-2xl text-lg">{t("docs.subtitle")}</p>
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
								<Card key={section.key} className="group transition-all duration-200 hover:shadow-lg">
									<CardHeader>
										<div className="flex items-center justify-between">
											<div className="bg-primary/10 group-hover:bg-primary/20 mb-4 flex h-12 w-12 items-center justify-center rounded-lg transition-colors">
												<Icon className="text-primary h-6 w-6" />
											</div>
											{section.badge && (
												<Badge variant="secondary" className="text-xs">
													{t(`docs.${section.badge}`)}
												</Badge>
											)}
										</div>
										<CardTitle className="text-xl">{t(`docs.${section.key}`)}</CardTitle>
										<CardDescription className="leading-relaxed">{t(`docs.${section.key}Desc`)}</CardDescription>
									</CardHeader>
									<CardContent className="flex h-full flex-col justify-between gap-8">
										<div className="space-y-4">
											<ul className="space-y-2">
												{docItems(t(`docs.items.${section.key}`, { returnObjects: true })).map((item, index) => (
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
												data-testid={`docs-read-more-${section.testSlug}`}
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
						{featuredDocs.map((doc) => (
							<Card className={`${doc.borderColor} ${doc.backgroundColor}`} key={doc.key}>
								<CardHeader>
									<CardTitle className="flex items-center gap-2">
										<doc.icon className={`h-5 w-5 ${doc.iconColor}`} />
										{t(`docs.${doc.key}`)}
									</CardTitle>
									<CardDescription>{t(`docs.${doc.key}Desc`)}</CardDescription>
								</CardHeader>
								<CardContent>
									<p className="text-muted-foreground mb-4 text-sm">{t(`docs.${doc.key}Content`)}</p>
									<Button asChild className="w-full">
										<a href={doc.href} target="_blank" rel="noopener noreferrer" data-testid={`docs-featured-${doc.testSlug}`}>
											<doc.icon className="mr-2 h-4 w-4" />
											{t(doc.key === "mcpDocs" ? "docs.viewMcpGuide" : "docs.viewGovernanceGuide")}
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