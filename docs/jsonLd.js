const jsonLd = {
	"@context": "https://schema.org",
	"@type": "WebPage",
	url: "https://www.getmaxim.ai/bifrost/docs",
	name: "Bifrost Documentation",
	description:
		"Comprehensive documentation for Maxim's end-to-end platform for AI simulation, evaluation, and observability. Learn how to build, evaluate, and monitor GenAI workflows at scale.",
	publisher: {
		"@type": "Organization",
		name: "Bifrost",
		url: "https://www.getmaxim.ai/bifrost",
		logo: {
			"@type": "ImageObject",
			url: "https://bifrost.getmaxim.ai/logo-full.svg",
			width: 300,
			height: 60,
		},
		sameAs: ["https://twitter.com/getmaximai", "https://www.linkedin.com/company/maxim-ai", "https://www.youtube.com/@getmaximai"],
	},
	mainEntity: {
		"@type": "TechArticle",
		name: "Bifrost Documentation",
		url: "https://www.getmaxim.ai/bifrost",
		headline: "Bifrost Docs",
		description:
			"Bifrost is the fastest LLM gateway in the market, 90x faster than LiteLLM (P99 latency).",
		inLanguage: "en",
	},
};

function injectJsonLd() {
	const script = document.createElement("script");
	script.type = "application/ld+json";
	script.text = JSON.stringify(jsonLd);

	if (document.readyState === "loading") {
		document.addEventListener("DOMContentLoaded", () => {
			document.head.appendChild(script);
		});
	} else {
		document.head.appendChild(script);
	}

	return () => {
		if (script.parentNode) {
			script.parentNode.removeChild(script);
		}
	};
}

// Call the function to inject JSON-LD
const cleanup = injectJsonLd();

// Cleanup when needed
// cleanup()